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

// TestStore_PassiveUsageDoesNotBlockActiveReprobe 回归覆盖 v4→v5 修复的根因 bug：
// 被动流量观测（SourcePassiveUsage，如 Claude/Codex 真实代理响应）会持续刷新
// ObservedAt，但绝不能推进 LastActiveProbeAt——否则高频真实流量会让 isTTLExpired
// 永远基于"最新一次观测"判断未过期，主动 probe 被无限期饿死，fresh-only 的
// priority 计算永远拿不到本轮证据（生产现象：全部凭证 reason=keep current state，
// evidence_fresh=false，attempted=0）。
// 本测试模拟：t0 做一次主动 probe；t0+10min 到来一次被动观测刷新 ObservedAt；
// 在 TTL=15min 的策略下，t0+16min（距离上一次*主动* probe 已超过 TTL，但距离被动
// 观测仅 6 分钟）时 NeedsProbe 必须返回 true——若退化回旧逻辑（以 ObservedAt 判断
// TTL），这里会错误地返回 false。
func TestStore_PassiveUsageDoesNotBlockActiveReprobe(t *testing.T) {
	store, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := t0.Add(5 * time.Hour)
	policy := ProbePolicy{TTL: 15 * time.Minute}

	// t0：一次真正的主动 probe 成功。
	if err := store.MarkProbeSuccess(context.Background(), ProbeSuccess{
		AuthIndex:   "auth-claude-passive",
		Provider:    core.ProviderClaude,
		ObservedAt:  t0,
		ResetAt:     resetAt,
		Remaining:   50,
		Source:      SourceFreshProbe,
		NextProbeAt: t0.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed active MarkProbeSuccess failed: %v", err)
	}

	// t0+10min：被动流量观测刷新 ObservedAt（模拟真实代理请求命中同一凭证）。
	passiveAt := t0.Add(10 * time.Minute)
	if err := store.MarkProbeSuccess(context.Background(), ProbeSuccess{
		AuthIndex:   "auth-claude-passive",
		Provider:    core.ProviderClaude,
		ObservedAt:  passiveAt,
		ResetAt:     resetAt,
		Remaining:   45,
		Source:      SourcePassiveUsage,
		NextProbeAt: passiveAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("passive MarkProbeSuccess failed: %v", err)
	}

	entry, ok := store.GetEntry("auth-claude-passive", "")
	if !ok {
		t.Fatalf("expected entry to exist after passive update")
	}
	if !entry.ObservedAt.Equal(passiveAt) {
		t.Errorf("expected ObservedAt refreshed to passive time %v, got %v", passiveAt, entry.ObservedAt)
	}
	if !entry.LastActiveProbeAt.Equal(t0) {
		t.Errorf("expected LastActiveProbeAt to remain pinned at active probe time %v, got %v", t0, entry.LastActiveProbeAt)
	}

	// t0+16min：距离上一次主动 probe 已超过 15min TTL，即使被动观测才过去 6 分钟，
	// 也必须判定需要重新主动 probe。
	needs, err := store.NeedsProbe(context.Background(), ProbeCheck{
		AuthIndex: "auth-claude-passive",
		Provider:  core.ProviderClaude,
		Now:       t0.Add(16 * time.Minute),
		Policy:    policy,
	})
	if err != nil {
		t.Fatalf("NeedsProbe error: %v", err)
	}
	if !needs {
		t.Errorf("expected NeedsProbe=true once TTL elapses since last ACTIVE probe, despite recent passive refresh (regression: passive traffic must not starve active probing)")
	}
}

// TestStore_PassiveOnlyEntryAlwaysDueForActiveProbe 覆盖对抗性 review 指出的一个更隐蔽的
// 竞态场景：升级到 v5 之后，某个 auth_index 的*第一笔*写入恰好是被动观测（而不是插件
// 自己排到的主动 probe）——例如刚迁移完、旧版 v4 缓存条目还没轮到主动重新探测，就先被一次
// 真实代理流量的被动观测命中。MarkProbeSuccess 对被动来源仍会无条件把整条 Entry 的
// SchemaVersion 刷新为最新版本，这意味着"SchemaVersion 不匹配强制重新 probe"这道迁移保险
// 会被这次被动写入提前"消费掉"，而 LastActiveProbeAt 却因为来源是被动而保持零值不变。
// 如果 isTTLExpired 把"从未做过主动 probe"（LastActiveProbeAt 为零）误判为"未过期"，
// 且被动流量又持续把 NextProbeAt 刷新到未来（mergePassiveWindows 每次写入都是
// now+1h），就会重现和迁移前一模一样的永久饿死症状，只是触发路径从 ObservedAt-TTL
// 换成了 zero-LastActiveProbeAt + NextProbeAt 抢跑。本测试确保 isTTLExpired 把
// LastActiveProbeAt 为零值的条目直接视为已过期，不依赖 NextProbeAt 是否被持续推后。
func TestStore_PassiveOnlyEntryAlwaysDueForActiveProbe(t *testing.T) {
	store, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := t0.Add(5 * time.Hour)
	policy := ProbePolicy{TTL: 15 * time.Minute}

	// 模拟：这个 auth_index 从未被主动 probe 过（无论是全新条目，还是刚迁移的旧
	// v4 条目），第一笔、也是仅有的几笔写入全部来自被动观测，且每次都把 NextProbeAt
	// 刷新到 1 小时之后——刻意比 TTL 更长、比真实流量到达间隔更短，模拟"被动流量
	// 快于主动 probe 节奏"的生产场景。
	for i, minutes := range []int{0, 5, 10} {
		observedAt := t0.Add(time.Duration(minutes) * time.Minute)
		if err := store.MarkProbeSuccess(context.Background(), ProbeSuccess{
			AuthIndex:   "auth-claude-passive-only",
			Provider:    core.ProviderClaude,
			ObservedAt:  observedAt,
			ResetAt:     resetAt,
			Remaining:   50 - i,
			Source:      SourcePassiveUsage,
			NextProbeAt: observedAt.Add(time.Hour),
		}); err != nil {
			t.Fatalf("passive MarkProbeSuccess[%d] failed: %v", i, err)
		}
	}

	entry, ok := store.GetEntry("auth-claude-passive-only", "")
	if !ok {
		t.Fatalf("expected entry to exist after passive-only writes")
	}
	if !entry.LastActiveProbeAt.IsZero() {
		t.Fatalf("expected LastActiveProbeAt to remain zero (no active probe ever happened), got %v", entry.LastActiveProbeAt)
	}
	if !entry.NextProbeAt.Equal(t0.Add(10*time.Minute + time.Hour)) {
		t.Fatalf("expected NextProbeAt pushed out by the latest passive write, got %v", entry.NextProbeAt)
	}

	// 即使 NextProbeAt 还远在未来（被被动流量持续推后），只要从未做过主动 probe，
	// NeedsProbe 也必须返回 true——不能让"被动流量足够频繁"变成永久免检的理由。
	needs, err := store.NeedsProbe(context.Background(), ProbeCheck{
		AuthIndex: "auth-claude-passive-only",
		Provider:  core.ProviderClaude,
		Now:       t0.Add(11 * time.Minute),
		Policy:    policy,
	})
	if err != nil {
		t.Fatalf("NeedsProbe error: %v", err)
	}
	if !needs {
		t.Errorf("expected NeedsProbe=true for an entry that has never had a genuine active probe, regardless of NextProbeAt being pushed into the future by passive writes")
	}
}

// TestStore_ScheduledOnlyEntryRespectsNextProbeAt 覆盖第二轮对抗性 review 指出的回归：
// isTTLExpired 把"LastActiveProbeAt 为零值"一律视为已过期，会误伤大机队分批/错峰探测
// （credentials 数超过 ImmediateProbeLimit 时，MarkProbeScheduled 会为尚未轮到的凭证写入
// 一条只有 NextProbeAt、完全没有任何观测（ObservedAt 也是零值）的占位条目）——如果这类
// 占位条目也被当成"已过期"抢跑，错峰调度就会在下一轮直接崩成一次性探测风暴。
// 本测试验证：只写过 MarkProbeScheduled、从未有过任何观测的条目，在 NextProbeAt 还没到
// 之前，NeedsProbe 必须遵守分批调度返回 false，不能被 zero-LastActiveProbeAt 的修复误伤。
func TestStore_ScheduledOnlyEntryRespectsNextProbeAt(t *testing.T) {
	store, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	futureProbeAt := t0.Add(2 * time.Hour)

	if err := store.MarkProbeScheduled(context.Background(), ProbeSchedule{
		AuthIndex:   "auth-claude-scheduled-only",
		Provider:    core.ProviderClaude,
		NextProbeAt: futureProbeAt,
	}); err != nil {
		t.Fatalf("MarkProbeScheduled failed: %v", err)
	}

	entry, ok := store.GetEntry("auth-claude-scheduled-only", "")
	if !ok {
		t.Fatalf("expected scheduled-only entry to exist")
	}
	if !entry.ObservedAt.IsZero() || !entry.LastActiveProbeAt.IsZero() {
		t.Fatalf("expected a pure scheduling stub with zero ObservedAt/LastActiveProbeAt, got %+v", entry)
	}

	needs, err := store.NeedsProbe(context.Background(), ProbeCheck{
		AuthIndex: "auth-claude-scheduled-only",
		Provider:  core.ProviderClaude,
		Now:       t0.Add(5 * time.Minute),
		Policy:    ProbePolicy{TTL: 15 * time.Minute},
	})
	if err != nil {
		t.Fatalf("NeedsProbe error: %v", err)
	}
	if needs {
		t.Errorf("expected NeedsProbe=false for a never-observed scheduling stub still ahead of its staggered NextProbeAt; the zero-LastActiveProbeAt starvation fix must not defeat batch staggering")
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
