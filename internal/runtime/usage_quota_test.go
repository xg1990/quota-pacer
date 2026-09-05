package runtime

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/state"
)

func TestMergeQuotaWindows_PartialUpdatePreservesUntouchedWindow(t *testing.T) {
	prev := []core.QuotaWindow{
		{Name: "5h", Duration: 5 * time.Hour, Remaining: 80, ResetAt: time.Unix(1000, 0)},
		{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: 60, ResetAt: time.Unix(2000, 0)},
	}
	next := []core.QuotaWindow{
		{Name: "5h", Duration: 5 * time.Hour, Remaining: 30, ResetAt: time.Unix(1500, 0)},
	}

	merged := mergeQuotaWindows(prev, next)
	if len(merged) != 2 {
		t.Fatalf("expected 2 windows after partial merge, got %d: %+v", len(merged), merged)
	}
	var got5h, gotWeekly bool
	for _, w := range merged {
		switch w.Name {
		case "5h":
			got5h = true
			if w.Remaining != 30 {
				t.Errorf("expected 5h window updated to remaining=30, got %d", w.Remaining)
			}
		case "weekly":
			gotWeekly = true
			if w.Remaining != 60 {
				t.Errorf("expected weekly window preserved at remaining=60, got %d", w.Remaining)
			}
		}
	}
	if !got5h || !gotWeekly {
		t.Fatalf("expected both 5h and weekly present after merge, got %+v", merged)
	}
}

func TestMergeQuotaWindows_EmptyPrevReturnsNext(t *testing.T) {
	next := []core.QuotaWindow{{Name: "primary", Duration: 5 * time.Hour, Remaining: 50}}
	merged := mergeQuotaWindows(nil, next)
	if len(merged) != 1 || merged[0].Name != "primary" {
		t.Errorf("expected next returned as-is when prev is empty, got %+v", merged)
	}
}

func TestMergeQuotaWindows_EmptyNextReturnsPrev(t *testing.T) {
	prev := []core.QuotaWindow{{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: 50}}
	merged := mergeQuotaWindows(prev, nil)
	if len(merged) != 1 || merged[0].Name != "weekly" {
		t.Errorf("expected prev returned as-is when next is empty, got %+v", merged)
	}
}

func TestMergeQuotaWindows_MatchesByDurationWhenNameDiffers(t *testing.T) {
	// Codex 被动 header 用 "primary"/"secondary" 泛化命名，主动 wham 探测用
	// "5h"/"weekly"——按 Duration 精确匹配应能正确识别为同一个窗口并覆盖，而不是
	// 误当成第三个新窗口叠加进去。
	prev := []core.QuotaWindow{{Name: "5h", Duration: 5 * time.Hour, Remaining: 80}}
	next := []core.QuotaWindow{{Name: "primary", Duration: 5 * time.Hour, Remaining: 20}}

	merged := mergeQuotaWindows(prev, next)
	if len(merged) != 1 {
		t.Fatalf("expected duration-match to replace (not append), got %d windows: %+v", len(merged), merged)
	}
	if merged[0].Remaining != 20 {
		t.Errorf("expected merged window to carry the new remaining=20, got %d", merged[0].Remaining)
	}
}

func TestWorstQuotaWindow_PicksLowestRemaining(t *testing.T) {
	windows := []core.QuotaWindow{
		{Name: "a", Remaining: 80, ResetAt: time.Unix(100, 0)},
		{Name: "b", Remaining: 20, ResetAt: time.Unix(200, 0)},
		{Name: "c", Remaining: 50, ResetAt: time.Unix(300, 0)},
	}
	worst, ok := worstQuotaWindow(windows)
	if !ok || worst.Name != "b" {
		t.Errorf("expected window 'b' (remaining=20) to be worst, got %+v (ok=%v)", worst, ok)
	}
}

func TestWorstQuotaWindow_EmptyReturnsFalse(t *testing.T) {
	if _, ok := worstQuotaWindow(nil); ok {
		t.Errorf("expected ok=false for empty windows")
	}
}

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	ctx := context.Background()
	store, err := state.Load(ctx, filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatalf("state.Load() error = %v", err)
	}
	return store
}

func TestApplyClaudePassiveUsage_SuccessUpdatesCacheFromHeaders(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	rec := usageRecord{
		AuthIndex: "auth-claude-1",
		Provider:  "claude",
		Failed:    false,
		ResponseHeaders: map[string][]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.2"},
			"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Reset":       {"1780000000"},
		},
	}

	applied, err := applyClaudePassiveUsage(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyClaudePassiveUsage error = %v", err)
	}
	if !applied {
		t.Fatalf("expected passive usage to be applied")
	}

	entry, ok := store.GetEntry("auth-claude-1", "")
	if !ok {
		t.Fatalf("expected cache entry to exist after passive usage update")
	}
	if entry.Source != state.SourcePassiveUsage {
		t.Errorf("expected entry Source=%q, got %q", state.SourcePassiveUsage, entry.Source)
	}
	if entry.Remaining != 80 {
		t.Errorf("expected Remaining=80 (100-20%% used), got %d", entry.Remaining)
	}
}

func TestApplyClaudePassiveUsage_PartialUpdatePreservesOtherWindow(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	// 先种入一条既有的 weekly 窗口数据（模拟此前主动探测或上一轮被动观测留下的状态）。
	if err := store.MarkProbeSuccess(ctx, state.ProbeSuccess{
		AuthIndex:  "auth-claude-2",
		Provider:   core.ProviderClaude,
		ObservedAt: now.Add(-time.Hour),
		ResetAt:    now.Add(6 * 24 * time.Hour),
		Remaining:  70,
		Source:     state.SourceFreshProbe,
		Windows: []core.QuotaWindow{
			{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: 70, ResetAt: now.Add(6 * 24 * time.Hour)},
		},
	}); err != nil {
		t.Fatalf("seed MarkProbeSuccess error = %v", err)
	}

	// 这次被动响应只带 5h 数据，不应清空已有的 weekly 窗口。
	rec := usageRecord{
		AuthIndex: "auth-claude-2",
		Provider:  "claude",
		Failed:    false,
		ResponseHeaders: map[string][]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.9"},
			"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Reset":       {"1780000000"},
		},
	}
	applied, err := applyClaudePassiveUsage(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyClaudePassiveUsage error = %v", err)
	}
	if !applied {
		t.Fatalf("expected passive usage to be applied")
	}

	entry, ok := store.GetEntry("auth-claude-2", "")
	if !ok {
		t.Fatalf("expected cache entry to exist")
	}
	var saw5h, sawWeekly bool
	for _, w := range entry.Windows {
		if w.Name == "5h" {
			saw5h = true
			if w.Remaining != 10 {
				t.Errorf("expected 5h window remaining=10 (100-90%%), got %d", w.Remaining)
			}
		}
		if w.Name == "weekly" {
			sawWeekly = true
			if w.Remaining != 70 {
				t.Errorf("expected weekly window preserved at remaining=70, got %d", w.Remaining)
			}
		}
	}
	if !saw5h || !sawWeekly {
		t.Fatalf("expected both 5h (updated) and weekly (preserved) windows present, got %+v", entry.Windows)
	}
}

func TestApplyClaudePassiveUsage_NonQuotaFailureSkipped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	rec := usageRecord{
		AuthIndex:  "auth-claude-3",
		Provider:   "claude",
		Failed:     true,
		StatusCode: http.StatusUnauthorized, // 鉴权失败，与配额无关
	}
	applied, err := applyClaudePassiveUsage(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyClaudePassiveUsage error = %v", err)
	}
	if applied {
		t.Errorf("expected non-quota failure (401) to be skipped, not applied")
	}
	if store.HasEntry("auth-claude-3", "") {
		t.Errorf("expected no cache entry to be created for a skipped non-quota failure")
	}
}

func TestApplyClaudePassiveUsage_429UpdatesResetAndZeroesRemaining(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	rec := usageRecord{
		AuthIndex:  "auth-claude-4",
		Provider:   "claude",
		Failed:     true,
		StatusCode: http.StatusTooManyRequests,
		ResponseHeaders: map[string][]string{
			"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"},
			"Anthropic-Ratelimit-Unified-5h-Reset":  {"1780000000"},
		},
	}
	applied, err := applyClaudePassiveUsage(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyClaudePassiveUsage error = %v", err)
	}
	if !applied {
		t.Fatalf("expected 429 with rate-limit headers to be applied")
	}
	entry, ok := store.GetEntry("auth-claude-4", "")
	if !ok {
		t.Fatalf("expected cache entry to exist")
	}
	if entry.Remaining != 0 {
		t.Errorf("expected Remaining=0 for a rejected/429 window, got %d", entry.Remaining)
	}
}

func TestApplyClaudePassiveUsage_NoHeadersSkipped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	rec := usageRecord{AuthIndex: "auth-claude-5", Provider: "claude", Failed: false}
	applied, err := applyClaudePassiveUsage(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyClaudePassiveUsage error = %v", err)
	}
	if applied {
		t.Errorf("expected no-headers record to be skipped")
	}
}

func TestApplyCodexPassiveUsage_SuccessUpdatesCache(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	rec := usageRecord{
		AuthIndex: "auth-codex-1",
		Provider:  "codex",
		Failed:    false,
		ResponseHeaders: map[string][]string{
			"X-Codex-Primary-Used-Percent":        {"30"},
			"X-Codex-Primary-Window-Minutes":      {"300"},
			"X-Codex-Primary-Reset-After-Seconds": {"1800"},
		},
	}
	applied, err := applyCodexPassiveUsage(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyCodexPassiveUsage error = %v", err)
	}
	if !applied {
		t.Fatalf("expected passive codex usage to be applied")
	}
	entry, ok := store.GetEntry("auth-codex-1", "")
	if !ok {
		t.Fatalf("expected cache entry to exist")
	}
	if entry.Source != state.SourcePassiveUsage {
		t.Errorf("expected Source=%q, got %q", state.SourcePassiveUsage, entry.Source)
	}
	if entry.Remaining != 70 {
		t.Errorf("expected Remaining=70 (100-30%% used), got %d", entry.Remaining)
	}
}

func TestApplyCodexPassiveUsage_NoHeadersSkipped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	rec := usageRecord{AuthIndex: "auth-codex-2", Provider: "codex", Failed: false}
	applied, err := applyCodexPassiveUsage(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyCodexPassiveUsage error = %v", err)
	}
	if applied {
		t.Errorf("expected no-headers record to be skipped")
	}
}

func TestApplyPassiveUsageEvidence_AntigravityIsNoOp(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	rec := usageRecord{
		AuthIndex: "auth-ag-1",
		Provider:  "antigravity",
		Failed:    true,
		RawBody:   `{"error":{"status":"RESOURCE_EXHAUSTED"}}`,
	}
	applied, err := applyPassiveUsageEvidence(ctx, store, rec, now)
	if err != nil {
		t.Fatalf("applyPassiveUsageEvidence error = %v", err)
	}
	// 拍板不做 fast-path：Antigravity 目前没有 header 级信号，也不做提前写回，
	// 被动路径对它是纯 no-op（精确数值仍由既有主动 loadCodeAssist 探测覆盖）。
	if applied {
		t.Errorf("expected Antigravity passive usage to be a no-op, got applied=true")
	}
}
