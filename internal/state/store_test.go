package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quota-pacer/internal/core"
)

func TestStore_MarkAndNeedsProbe(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-store-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "refresh-cache.json")
	store, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)

	// NeedsProbe on new entry -> true
	check := ProbeCheck{
		AuthIndex: "auth-claude-1",
		Provider:  core.ProviderClaude,
		Now:       now,
		Policy:    ProbePolicy{TTL: 15 * time.Minute},
	}
	needs, err := store.NeedsProbe(context.Background(), check)
	if err != nil || !needs {
		t.Errorf("expected NeedsProbe=true for new entry, got %v, err=%v", needs, err)
	}

	// Mark success
	success := ProbeSuccess{
		AuthIndex:   "auth-claude-1",
		Provider:    core.ProviderClaude,
		ObservedAt:  now,
		ResetAt:     resetAt,
		Remaining:   45,
		Source:      SourceFreshProbe,
		NextProbeAt: now.Add(time.Hour),
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	// NeedsProbe immediately after -> false
	needs, err = store.NeedsProbe(context.Background(), check)
	if err != nil || needs {
		t.Errorf("expected NeedsProbe=false after success, got %v, err=%v", needs, err)
	}

	// Save and Reload
	if err := store.SaveAtomic(context.Background()); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	reloaded, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	entry, ok := reloaded.GetEntry("auth-claude-1", "")
	if !ok {
		t.Fatalf("expected entry found in reloaded store")
	}
	if entry.Remaining != 45 {
		t.Errorf("expected remaining 45, got %d", entry.Remaining)
	}
}

func TestStore_MultiWindowPersistence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-store-multi-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "refresh-cache.json")
	store, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	reset5h := now.Add(5 * time.Hour)
	reset7d := now.Add(7 * 24 * time.Hour)
	shortRem := int64(90)
	longRem := int64(75)

	success := ProbeSuccess{
		AuthIndex:            "auth-codex-1",
		Provider:             core.ProviderCodex,
		ObservedAt:           now,
		ResetAt:              reset5h,
		Remaining:            90,
		Source:               SourceFreshProbe,
		NextProbeAt:          now.Add(time.Hour),
		PlanType:             core.PlanTypePlus,
		ShortWindowRemaining: &shortRem,
		ShortWindowResetAt:   reset5h,
		LongWindowRemaining:  &longRem,
		LongWindowResetAt:    reset7d,
		Windows: []core.QuotaWindow{
			{Name: "5h", Duration: 5 * time.Hour, Remaining: 90, ResetAt: reset5h},
			{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: 75, ResetAt: reset7d},
		},
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	if err := store.SaveAtomic(context.Background()); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	reloaded, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	entry, ok := reloaded.GetEntry("auth-codex-1", "")
	if !ok {
		t.Fatalf("expected entry found in reloaded store")
	}
	if entry.PlanType != core.PlanTypePlus {
		t.Errorf("expected plan_type plus, got %s", entry.PlanType)
	}
	if entry.ShortWindowRemaining == nil || *entry.ShortWindowRemaining != 90 {
		t.Errorf("expected short_window_remaining 90, got %v", entry.ShortWindowRemaining)
	}
	if !entry.ShortWindowResetAt.Equal(reset5h) {
		t.Errorf("expected short_window_reset_at %v, got %v", reset5h, entry.ShortWindowResetAt)
	}
	if entry.LongWindowRemaining == nil || *entry.LongWindowRemaining != 75 {
		t.Errorf("expected long_window_remaining 75, got %v", entry.LongWindowRemaining)
	}
	if !entry.LongWindowResetAt.Equal(reset7d) {
		t.Errorf("expected long_window_reset_at %v, got %v", reset7d, entry.LongWindowResetAt)
	}
	if len(entry.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(entry.Windows))
	}
	if entry.Windows[0].Name != "5h" || entry.Windows[0].Remaining != 90 {
		t.Errorf("unexpected window 0: %+v", entry.Windows[0])
	}
	if entry.Windows[1].Name != "weekly" || entry.Windows[1].Remaining != 75 {
		t.Errorf("unexpected window 1: %+v", entry.Windows[1])
	}
}

// TestStore_ResetCreditsPersistence 验证 Codex 银行化重置额度信号
// （AvailableResetCredits/NearestResetCreditExpiresAt）随 MarkProbeSuccess 落盘并可跨
// 进程重新加载，与既有多窗口字段的持久化契约一致。
func TestStore_ResetCreditsPersistence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-store-reset-credits-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "refresh-cache.json")
	store, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(84 * time.Hour)
	expiresAt := now.Add(5 * 24 * time.Hour)

	success := ProbeSuccess{
		AuthIndex:                   "auth-codex-credit",
		Provider:                    core.ProviderCodex,
		ObservedAt:                  now,
		ResetAt:                     resetAt,
		Remaining:                   30,
		Source:                      SourceFreshProbe,
		NextProbeAt:                 now.Add(time.Hour),
		PlanType:                    core.PlanTypePlus,
		AvailableResetCredits:       1,
		NearestResetCreditExpiresAt: expiresAt,
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	entry, ok := store.GetEntry("auth-codex-credit", "")
	if !ok {
		t.Fatalf("expected entry to exist before save")
	}
	if entry.AvailableResetCredits != 1 {
		t.Errorf("expected AvailableResetCredits 1 before save, got %d", entry.AvailableResetCredits)
	}
	if !entry.NearestResetCreditExpiresAt.Equal(expiresAt) {
		t.Errorf("expected NearestResetCreditExpiresAt %v before save, got %v", expiresAt, entry.NearestResetCreditExpiresAt)
	}

	if err := store.SaveAtomic(context.Background()); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	reloaded, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	reloadedEntry, ok := reloaded.GetEntry("auth-codex-credit", "")
	if !ok {
		t.Fatalf("expected entry found in reloaded store")
	}
	if reloadedEntry.AvailableResetCredits != 1 {
		t.Errorf("expected AvailableResetCredits 1 after reload, got %d", reloadedEntry.AvailableResetCredits)
	}
	if !reloadedEntry.NearestResetCreditExpiresAt.Equal(expiresAt) {
		t.Errorf("expected NearestResetCreditExpiresAt %v after reload, got %v", expiresAt, reloadedEntry.NearestResetCreditExpiresAt)
	}
}

// TestStore_SchemaVersionMismatchForcesReprobe 回归覆盖：v1.0.5 曾在 Entry/ProbeSuccess 增补
// 多窗口字段（ShortWindowRemaining/LongWindowRemaining）却未递增 SchemaVersion，导致升级前
// 写入的旧缓存条目被当作”完整有效”继续回放，remainingHeadroom 因缺字段静默退化为 legacy 单窗口口径，
// 与后续基于 fresh evidence 冻结的 priority 产生视觉上的不一致。此测试确保版本不匹配的旧条目
// 会被 NeedsProbe 判定为需要重新探测、被 ValidEntry 判定为无效（不会被当作缓存证据回放）。
func TestStore_SchemaVersionMismatchForcesReprobe(t *testing.T) {
	store, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(2 * time.Hour)

	success := ProbeSuccess{
		AuthIndex:   "auth-legacy",
		Provider:    core.ProviderCodex,
		ObservedAt:  now,
		ResetAt:     resetAt,
		Remaining:   88,
		Source:      SourceFreshProbe,
		NextProbeAt: now.Add(time.Hour),
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	// 模拟升级前写入的旧 schema 版本缓存条目（缺失本版本新增的多窗口字段）。
	entry, ok := store.GetEntry("auth-legacy", "")
	if !ok {
		t.Fatalf("expected entry to exist")
	}
	entry.SchemaVersion = SchemaVersion - 1
	store.entries[entryKey("auth-legacy", "")] = entry

	if _, ok := store.ValidEntry("auth-legacy", "", now.Add(5*time.Minute), ProbePolicy{TTL: 15 * time.Minute}); ok {
		t.Errorf("expected ValidEntry=false for stale schema version entry")
	}

	needs, err := store.NeedsProbe(context.Background(), ProbeCheck{
		AuthIndex: "auth-legacy",
		Provider:  core.ProviderCodex,
		Now:       now.Add(5 * time.Minute),
		Policy:    ProbePolicy{TTL: 15 * time.Minute},
	})
	if err != nil {
		t.Fatalf("NeedsProbe error: %v", err)
	}
	if !needs {
		t.Errorf("expected NeedsProbe=true for stale schema version entry")
	}
}

func TestStore_ValidEntry(t *testing.T) {
	store, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(2 * time.Hour)

	// Entry does not exist
	if _, ok := store.ValidEntry("auth-1", "", now, ProbePolicy{TTL: 15 * time.Minute}); ok {
		t.Errorf("expected ValidEntry=false for non-existent entry")
	}

	// Add entry
	success := ProbeSuccess{
		AuthIndex:   "auth-1",
		Provider:    core.ProviderClaude,
		ObservedAt:  now,
		ResetAt:     resetAt,
		Remaining:   50,
		Source:      SourceFreshProbe,
		NextProbeAt: now.Add(time.Hour),
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	// Valid within TTL and before reset
	entry, ok := store.ValidEntry("auth-1", "", now.Add(5*time.Minute), ProbePolicy{TTL: 15 * time.Minute})
	if !ok || entry.Remaining != 50 {
		t.Errorf("expected ValidEntry=true, got %v, entry=%+v", ok, entry)
	}

	// Past resetAt -> false
	if _, ok := store.ValidEntry("auth-1", "", resetAt.Add(time.Minute), ProbePolicy{TTL: 15 * time.Minute}); ok {
		t.Errorf("expected ValidEntry=false past resetAt")
	}
}
