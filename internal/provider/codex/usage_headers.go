package codex

import (
	"strconv"
	"strings"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
)

// ParseUsageHeaders 解析真实流量响应里 CPA 已归一化的 X-Codex-Primary-*/
// X-Codex-Secondary-* 被动配额 header（详见 CLIProxyAPI v7.2.151
// internal/runtime/executor/helps/codex_quota.go 的 ParseCodexQuotaEventHeaders：
// 无论配额信号来自 Codex WS codex.rate_limits 事件还是直接的 HTTP 响应，CPA 都会把
// 它们统一归一化成这套同名 header），产出与主动 wham/usage 探测（ParseWhamUsage）
// 同形状的 ProbeResult，供 runtime 层复用既有写回路径。
//
// 只解析 CPA 已经语义化的 X-Codex-Primary-*/X-Codex-Secondary-* 字段；未解析原始
// x-ratelimit-* header——CPA 本身也没有对其做语义归一化（只是原样透传），真实取值
// 格式未经源码验证，不做猜测。
func ParseUsageHeaders(headers host.Header, observedAt time.Time) ProbeResult {
	primary, hasPrimary := parseCodexUsageWindow(headers, "X-Codex-Primary-", observedAt)
	secondary, hasSecondary := parseCodexUsageWindow(headers, "X-Codex-Secondary-", observedAt)
	if !hasPrimary && !hasSecondary {
		return failedResult(observedAt, "no codex quota headers present")
	}

	windows := make([]core.QuotaWindow, 0, 2)
	if hasPrimary {
		windows = append(windows, core.QuotaWindow{Name: "primary", Duration: primary.duration, Remaining: primary.remaining, ResetAt: *primary.resetAt})
	}
	if hasSecondary {
		windows = append(windows, core.QuotaWindow{Name: "secondary", Duration: secondary.duration, Remaining: secondary.remaining, ResetAt: *secondary.resetAt})
	}

	// 顶层主窗口取剩余更紧张（remaining 更小）的一个，与既有"最悲观窗口优先"惯例一致
	// （仅用于 eligibility 判断；实际打分由 pacingScore 对 Windows 取 min 完成）。
	top := primary
	if !hasPrimary || (hasSecondary && secondary.remaining < primary.remaining) {
		top = secondary
	}

	winType := WindowFiveHour
	if top.duration > 24*time.Hour {
		winType = WindowWeekly
	}

	return ProbeResult{
		ObservedAt:  observedAt.UTC(),
		ResetAt:     top.resetAt,
		Remaining:   int64Ptr(top.remaining),
		Window:      winType,
		Windows:     windows,
		Freshness:   core.FreshnessFresh,
		ProbeStatus: core.ProbeStatusReady,
		Status:      StatusReady,
		PlanType:    inferPlanType(getHeaderCaseInsensitive(headers, "X-Codex-Plan-Type")),
	}
}

type codexUsageWindow struct {
	remaining int64
	resetAt   *time.Time
	duration  time.Duration
}

func parseCodexUsageWindow(headers host.Header, prefix string, observedAt time.Time) (codexUsageWindow, bool) {
	usedPercentStr := getHeaderCaseInsensitive(headers, prefix+"Used-Percent")
	minutesStr := getHeaderCaseInsensitive(headers, prefix+"Window-Minutes")
	resetAfterStr := getHeaderCaseInsensitive(headers, prefix+"Reset-After-Seconds")
	resetAtStr := getHeaderCaseInsensitive(headers, prefix+"Reset-At")
	limitReachedStr := getHeaderCaseInsensitive(headers, prefix+"Limit-Reached")

	minutes, minutesOK := parseFloatHeader(minutesStr)
	if !minutesOK || minutes <= 0 {
		return codexUsageWindow{}, false
	}
	duration := time.Duration(minutes * float64(time.Minute))

	var resetAt *time.Time
	if resetAtStr != "" {
		if t, ok := parseCodexTimeString(resetAtStr); ok {
			resetAt = t
		}
	}
	if resetAt == nil && resetAfterStr != "" {
		if secs, ok := parseFloatHeader(resetAfterStr); ok && secs >= 0 {
			t := observedAt.UTC().Add(time.Duration(secs * float64(time.Second)))
			resetAt = &t
		}
	}
	if resetAt == nil {
		return codexUsageWindow{}, false
	}

	var remaining int64
	if reached, ok := parseBoolHeader(limitReachedStr); ok && reached {
		remaining = 0
	} else if usedPercent, ok := parseFloatHeader(usedPercentStr); ok {
		remaining = clampPercent(100 - usedPercent)
	} else {
		return codexUsageWindow{}, false
	}

	return codexUsageWindow{remaining: remaining, resetAt: resetAt, duration: duration}, true
}

func getHeaderCaseInsensitive(h host.Header, target string) string {
	if h == nil {
		return ""
	}
	for k, v := range h {
		if strings.EqualFold(strings.TrimSpace(k), target) {
			for _, val := range v {
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func parseFloatHeader(value string) (float64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func parseBoolHeader(value string) (bool, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false, false
	}
	b, err := strconv.ParseBool(trimmed)
	if err != nil {
		return false, false
	}
	return b, true
}

func parseCodexTimeString(value string) (*time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, false
	}
	if integer, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return parseUnix(integer)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			utc := parsed.UTC()
			return &utc, true
		}
	}
	return nil, false
}
