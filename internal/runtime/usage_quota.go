package runtime

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
	"quota-pacer/internal/provider/claude"
	"quota-pacer/internal/provider/codex"
	"quota-pacer/internal/state"
)

// applyPassiveUsageEvidence 是 Claude/Codex 被动配额信号的分发入口：从真实流量的
// usage.handle 记录里提取配额窗口数据，合并写入既有 state.Store 缓存（与主动 probe
// 共用同一份缓存条目，按最新观测时间统一覆盖，不区分来源优先级）。
//
// Antigravity：CPA 侧没有 header 级配额信号（配额耗尽只体现在响应 body 的 gRPC 错误
// 结构里），精确剩余数值已经由现有独立的 loadCodeAssist 主动探测覆盖；本次拍板不做
// "提前写回"fast-path，所以被动感知在这里没有可做的事——不产生任何新逻辑，直接归入
// default 分支的 no-op。
// xAI：走既有 applyXAIUsageToStore 路径（成功/失败策略），不经过这里。
func applyPassiveUsageEvidence(ctx context.Context, store *state.Store, rec usageRecord, now time.Time) (bool, error) {
	switch normalizeUsageProvider(rec.Provider) {
	case core.ProviderClaude:
		return applyClaudePassiveUsage(ctx, store, rec, now)
	case core.ProviderCodex:
		return applyCodexPassiveUsage(ctx, store, rec, now)
	default:
		return false, nil
	}
}

func normalizeUsageProvider(raw string) core.Provider {
	p := core.Provider(strings.ToLower(strings.TrimSpace(raw)))
	switch p {
	case core.ProviderClaude, core.ProviderCodex, core.ProviderAntigravity, core.ProviderXAI:
		return p
	default:
		return core.ProviderUnknown
	}
}

func applyClaudePassiveUsage(ctx context.Context, store *state.Store, rec usageRecord, now time.Time) (bool, error) {
	headers := toHostHeader(rec.ResponseHeaders)
	if headers == nil {
		return false, nil
	}
	var result claude.ProbeResult
	switch {
	case rec.Failed && rec.StatusCode == http.StatusTooManyRequests:
		result = claude.ParseClaudeRateLimitError(nil, headers, now)
	case !rec.Failed:
		result = claude.ParseClaudeUsage(nil, headers, now)
	default:
		// 非 429 的失败（网络超时、鉴权错误、5xx 等）与配额无关，不驱动任何写入。
		return false, nil
	}
	if result.Status != claude.StatusReady || result.ResetAt == nil || result.Remaining == nil {
		return false, nil
	}
	return mergePassiveWindows(ctx, store, rec.AuthIndex, core.ProviderClaude, result.Windows, result.PlanType, now)
}

func applyCodexPassiveUsage(ctx context.Context, store *state.Store, rec usageRecord, now time.Time) (bool, error) {
	headers := toHostHeader(rec.ResponseHeaders)
	if headers == nil {
		return false, nil
	}
	result := codex.ParseUsageHeaders(headers, now)
	if result.Status != codex.StatusReady || result.ResetAt == nil || result.Remaining == nil {
		return false, nil
	}
	return mergePassiveWindows(ctx, store, rec.AuthIndex, core.ProviderCodex, result.Windows, result.PlanType, now)
}

func toHostHeader(raw map[string][]string) host.Header {
	if len(raw) == 0 {
		return nil
	}
	return host.Header(raw)
}

// mergePassiveWindows 把被动观测到的窗口数据与既有缓存条目的 Windows 合并写回：
// 只覆盖这次响应里实际出现的窗口（按 Duration，其次按 Name 匹配），其余窗口原样保留
// ——避免一次只带 5h 数据的响应把缓存里已有的 weekly 数据抹掉。顶层 Remaining/ResetAt
// 取合并后剩余比例最紧张的窗口（仅用于 eligibility gate；实际打分仍由 pacingScore
// 对 Windows 取 min 完成）。
func mergePassiveWindows(ctx context.Context, store *state.Store, authIndex string, provider core.Provider, newWindows []core.QuotaWindow, planType core.PlanType, now time.Time) (bool, error) {
	if len(newWindows) == 0 {
		return false, nil
	}
	prevEntry, _ := store.GetEntry(authIndex, "")
	merged := mergeQuotaWindows(prevEntry.Windows, newWindows)
	worst, ok := worstQuotaWindow(merged)
	if !ok {
		return false, nil
	}
	if planType == core.PlanTypeUnknown || planType == "" {
		planType = prevEntry.PlanType
	}
	err := store.MarkProbeSuccess(ctx, state.ProbeSuccess{
		AuthIndex:   authIndex,
		Provider:    provider,
		ObservedAt:  now,
		ResetAt:     worst.ResetAt,
		Remaining:   int(worst.Remaining),
		Source:      state.SourcePassiveUsage,
		NextProbeAt: now.Add(time.Hour),
		PlanType:    planType,
		Windows:     merged,
	})
	if err != nil {
		return false, fmt.Errorf("merge passive usage evidence: %w", err)
	}
	return true, nil
}

// mergeQuotaWindows 按 Duration（同一时长视为同一个窗口，其次按 Name 兜底）合并两份
// core.QuotaWindow 列表：next 覆盖 prev 里时长/名称匹配的条目，prev 里 next 未提到的
// 窗口原样保留。
func mergeQuotaWindows(prev, next []core.QuotaWindow) []core.QuotaWindow {
	if len(next) == 0 {
		return prev
	}
	if len(prev) == 0 {
		return next
	}
	matched := make([]bool, len(prev))
	merged := make([]core.QuotaWindow, 0, len(prev)+len(next))
	merged = append(merged, next...)
	for _, nw := range next {
		for i, pw := range prev {
			if matched[i] {
				continue
			}
			if pw.Duration == nw.Duration || (pw.Name != "" && pw.Name == nw.Name) {
				matched[i] = true
			}
		}
	}
	for i, pw := range prev {
		if !matched[i] {
			merged = append(merged, pw)
		}
	}
	return merged
}

// worstQuotaWindow 返回剩余比例最低（最紧张）的窗口，用作顶层 Remaining/ResetAt。
func worstQuotaWindow(windows []core.QuotaWindow) (core.QuotaWindow, bool) {
	if len(windows) == 0 {
		return core.QuotaWindow{}, false
	}
	worst := windows[0]
	for _, w := range windows[1:] {
		if w.Remaining < worst.Remaining {
			worst = w
		}
	}
	return worst, true
}
