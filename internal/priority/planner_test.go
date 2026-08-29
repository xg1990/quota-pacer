package priority

import (
	"testing"
	"time"

	"quota-pacer/internal/core"
)

func TestPlanFreshOnly_Claude_PositiveRemaining(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(30 * time.Hour) // not near reset (> 24h)
	rem1 := int64(45)
	rem2 := int64(30)

	credentials := []core.Credential{
		{
			Name:      "claude-1",
			AuthIndex: "auth-c1",
			Provider:  core.ProviderClaude,
			Type:      core.CredentialTypeClaude,
			Priority:  0,
		},
		{
			Name:      "claude-2",
			AuthIndex: "auth-c2",
			Provider:  core.ProviderClaude,
			Type:      core.CredentialTypeClaude,
			Priority:  0,
		},
	}

	evidence := []ProbeEvidence{
		{
			Provider:      core.ProviderClaude,
			AuthIndex:     "auth-c1",
			ObservedAt:    now,
			ResetAt:       &resetAt,
			Remaining:     &rem1,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
		{
			Provider:      core.ProviderClaude,
			AuthIndex:     "auth-c2",
			ObservedAt:    now,
			ResetAt:       &resetAt,
			Remaining:     &rem2,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 2 {
		t.Fatalf("expected 2 plan items, got %d", len(plan.Items))
	}

	// Higher remaining (45) should have higher priority (100), then (30) gets (99)
	var item1, item2 *PlanItem
	for i := range plan.Items {
		if plan.Items[i].Credential.AuthIndex == "auth-c1" {
			item1 = &plan.Items[i]
		} else if plan.Items[i].Credential.AuthIndex == "auth-c2" {
			item2 = &plan.Items[i]
		}
	}

	if item1 == nil || item2 == nil {
		t.Fatalf("missing items in plan")
	}
	if item1.Priority != 100 {
		t.Errorf("expected item1 priority 100, got %d", item1.Priority)
	}
	if item2.Priority != 99 {
		t.Errorf("expected item2 priority 99, got %d", item2.Priority)
	}
	if item1.Disabled || item2.Disabled {
		t.Errorf("expected active credentials not disabled")
	}
}

func TestPlanFreshOnly_Claude_FreeDepleted(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(2 * time.Hour)
	zeroRem := int64(0)

	credentials := []core.Credential{
		{
			Name:      "claude-free",
			AuthIndex: "auth-cf",
			Provider:  core.ProviderClaude,
			Type:      core.CredentialTypeClaude,
			Disabled:  false,
		},
	}

	evidence := []ProbeEvidence{
		{
			Provider:      core.ProviderClaude,
			AuthIndex:     "auth-cf",
			ObservedAt:    now,
			ResetAt:       &resetAt,
			Remaining:     &zeroRem,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypeFree,
			EvidenceFresh: true,
		},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	if plan.Items[0].Priority != 0 {
		t.Errorf("expected priority 0 for depleted, got %d", plan.Items[0].Priority)
	}
	if plan.Items[0].Reason != "fresh remaining depleted" {
		t.Errorf("expected reason 'fresh remaining depleted', got %q", plan.Items[0].Reason)
	}
}

func TestPlanFreshOnly_Claude_PaidDepleted(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(2 * time.Hour)
	zeroRem := int64(0)

	credentials := []core.Credential{
		{
			Name:      "claude-pro",
			AuthIndex: "auth-cp",
			Provider:  core.ProviderClaude,
			Type:      core.CredentialTypeClaude,
			Disabled:  false,
		},
	}

	evidence := []ProbeEvidence{
		{
			Provider:      core.ProviderClaude,
			AuthIndex:     "auth-cp",
			ObservedAt:    now,
			ResetAt:       &resetAt,
			Remaining:     &zeroRem,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	if plan.Items[0].Priority != 0 {
		t.Errorf("expected priority 0 for depleted, got %d", plan.Items[0].Priority)
	}
	if plan.Items[0].Reason != "fresh remaining depleted" {
		t.Errorf("expected reason 'fresh remaining depleted', got %q", plan.Items[0].Reason)
	}
}

func TestPlanFreshOnly_MultiProvider(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(30 * time.Hour)
	rem := int64(50)

	credentials := []core.Credential{
		{Name: "c1", AuthIndex: "auth-claude", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
		{Name: "c2", AuthIndex: "auth-codex", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
		{Name: "c3", AuthIndex: "auth-ag", Provider: core.ProviderAntigravity, Type: core.CredentialTypeAntigravity},
	}

	evidence := []ProbeEvidence{
		{Provider: core.ProviderClaude, AuthIndex: "auth-claude", ObservedAt: now, ResetAt: &resetAt, Remaining: &rem, Freshness: core.FreshnessFresh, ProbeStatus: core.ProbeStatusReady, Status: EvidenceStatusReady, PlanType: core.PlanTypePro, EvidenceFresh: true},
		{Provider: core.ProviderCodex, AuthIndex: "auth-codex", ObservedAt: now, ResetAt: &resetAt, Remaining: &rem, Freshness: core.FreshnessFresh, ProbeStatus: core.ProbeStatusReady, Status: EvidenceStatusReady, PlanType: core.PlanTypePro, EvidenceFresh: true},
		{Provider: core.ProviderAntigravity, AuthIndex: "auth-ag", ObservedAt: now, ResetAt: &resetAt, Remaining: &rem, Freshness: core.FreshnessFresh, ProbeStatus: core.ProbeStatusReady, Status: EvidenceStatusReady, PlanType: core.PlanTypePro, EvidenceFresh: true},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 3 {
		t.Fatalf("expected 3 plan items, got %d", len(plan.Items))
	}

	// 全局唯一优先级：跨 Provider 分配 100, 99, 98
	priorities := make(map[int]bool)
	for _, item := range plan.Items {
		if item.Priority < 98 || item.Priority > 100 {
			t.Errorf("expected priority in [98, 100] for %s, got %d", item.Credential.Name, item.Priority)
		}
		if priorities[item.Priority] {
			t.Errorf("duplicate priority %d found", item.Priority)
		}
		priorities[item.Priority] = true
	}
}

func TestPlanFreshOnly_CrossProvider_PacingRanking(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)

	// Claude: reset in 43h (~1.79d), 51% remaining -> Pacing score = 0.51 / (43/168) = 1.99
	resetClaude := now.Add(43 * time.Hour)
	remClaude := int64(51)

	// Codex: reset in 120h (5d), 80% remaining -> Pacing score = 0.80 / (120/168) = 1.12
	resetCodex := now.Add(120 * time.Hour)
	remCodex := int64(80)

	// Antigravity: reset in 140h (5.83d), 65% remaining -> Pacing score = 0.65 / (140/168) = 0.78
	resetAG := now.Add(140 * time.Hour)
	remAG := int64(65)

	credentials := []core.Credential{
		{Name: "claude-acc", AuthIndex: "auth-claude-urgent", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
		{Name: "codex-acc", AuthIndex: "auth-codex-mid", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
		{Name: "ag-acc", AuthIndex: "auth-ag-slow", Provider: core.ProviderAntigravity, Type: core.CredentialTypeAntigravity},
	}

	evidence := []ProbeEvidence{
		{
			Provider:          core.ProviderClaude,
			AuthIndex:         "auth-claude-urgent",
			ObservedAt:        now,
			ResetAt:           &resetClaude,
			LongWindowResetAt: &resetClaude,
			Remaining:         &remClaude,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
		{
			Provider:          core.ProviderCodex,
			AuthIndex:         "auth-codex-mid",
			ObservedAt:        now,
			ResetAt:           &resetCodex,
			LongWindowResetAt: &resetCodex,
			Remaining:         &remCodex,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
		{
			Provider:          core.ProviderAntigravity,
			AuthIndex:         "auth-ag-slow",
			ObservedAt:        now,
			ResetAt:           &resetAG,
			LongWindowResetAt: &resetAG,
			Remaining:         &remAG,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 3 {
		t.Fatalf("expected 3 plan items, got %d", len(plan.Items))
	}

	priorityByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
	}

	// 预期排序：Claude (score 1.99) -> 100, Codex (score 1.12) -> 99, Antigravity (score 0.78) -> 98
	if p := priorityByAuth["auth-claude-urgent"]; p != 100 {
		t.Errorf("expected claude priority 100, got %d", p)
	}
	if p := priorityByAuth["auth-codex-mid"]; p != 99 {
		t.Errorf("expected codex priority 99, got %d", p)
	}
	if p := priorityByAuth["auth-ag-slow"]; p != 98 {
		t.Errorf("expected antigravity priority 98, got %d", p)
	}
}

func TestPlanFreshOnly_PacingScore_WeeklyWindow(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	// Account 1: reset in 2 days (48h), 80% remaining -> score = 0.80 / (48/168) = 2.80
	reset2Days := now.Add(48 * time.Hour)
	rem80 := int64(80)

	// Account 2: reset in 4 days (96h), 80% remaining -> score = 0.80 / (96/168) = 1.40
	reset4Days := now.Add(96 * time.Hour)

	// Account 3: reset in 2 days (48h), 10% remaining -> score = 0.10 / (48/168) = 0.35
	rem10 := int64(10)

	credentials := []core.Credential{
		{Name: "c-fast-burn", AuthIndex: "auth-fast", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
		{Name: "c-mid-pace", AuthIndex: "auth-mid", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
		{Name: "c-slow-burn", AuthIndex: "auth-slow", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
	}

	evidence := []ProbeEvidence{
		{
			Provider:          core.ProviderClaude,
			AuthIndex:         "auth-fast", // reset in 2d, 10% remaining (score 0.35)
			ObservedAt:        now,
			ResetAt:           &reset2Days,
			LongWindowResetAt: &reset2Days,
			Remaining:         &rem10,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
		{
			Provider:          core.ProviderClaude,
			AuthIndex:         "auth-mid", // reset in 4d, 80% remaining (score 1.40)
			ObservedAt:        now,
			ResetAt:           &reset4Days,
			LongWindowResetAt: &reset4Days,
			Remaining:         &rem80,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
		{
			Provider:          core.ProviderClaude,
			AuthIndex:         "auth-slow", // reset in 2d, 80% remaining (score 2.80) -> should rank #1
			ObservedAt:        now,
			ResetAt:           &reset2Days,
			LongWindowResetAt: &reset2Days,
			Remaining:         &rem80,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 3 {
		t.Fatalf("expected 3 plan items, got %d", len(plan.Items))
	}

	priorityByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
	}

	// 预期排序：auth-slow (score 2.80) = 100, auth-mid (score 1.40) = 99, auth-fast (score 0.35) = 98
	if p := priorityByAuth["auth-slow"]; p != 100 {
		t.Errorf("expected auth-slow priority 100, got %d", p)
	}
	if p := priorityByAuth["auth-mid"]; p != 99 {
		t.Errorf("expected auth-mid priority 99, got %d", p)
	}
	if p := priorityByAuth["auth-fast"]; p != 98 {
		t.Errorf("expected auth-fast priority 98, got %d", p)
	}
}

func TestPlanFreshOnly_PacingScore_CodexDualWindowMismatch(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)

	// Codex 双窗口场景：Remaining/ResetAt 来自 5 小时窗口（3 小时后重置），
	// LongWindowResetAt 是不相关的 weekly 窗口（5 天后重置）。必须用 ResetAt
	// 的启发式分档（3h <= 6h -> 5h 基准）而不是借用 LongWindowResetAt 的 7 天。
	resetShort := now.Add(3 * time.Hour)
	resetLong := now.Add(5 * 24 * time.Hour)
	rem := int64(50)

	credentials := []core.Credential{
		{Name: "codex-dual", AuthIndex: "auth-codex-dual", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:          core.ProviderCodex,
			AuthIndex:         "auth-codex-dual",
			ObservedAt:        now,
			ResetAt:           &resetShort,
			LongWindowResetAt: &resetLong,
			Remaining:         &rem,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
	}

	options := Options{Now: now, MaxPriority: 100}
	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}

	// 期望：score = 0.50 / (3h/5h) ≈ 0.8333。若退化回旧逻辑（无条件 7 天基准）
	// 会得到 0.50 / (3h/168h) ≈ 28.0，两者差距悬殊，足以捕捉回归。
	got := plan.Items[0].PacingScore
	want := 0.5 / (3.0 / 5.0)
	if diff := got - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected pacing score %.6f, got %.6f", want, got)
	}
}

func TestPlanFreshOnly_PacingScore_FullRemainingWins(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)

	// 刚重置的满额账号：周期还有整整 7 天，按旧公式 score = 1.0/1.0 = 1.0（全局最低档之一）。
	resetFull := now.Add(168 * time.Hour)
	remFull := int64(100)

	// 即将过期但仍有较多剩余额度的账号：按旧公式 score = 0.9/(2/168) ≈ 75.6，旧逻辑下排名最高。
	resetUrgent := now.Add(2 * time.Hour)
	remUrgent := int64(90)

	credentials := []core.Credential{
		{Name: "claude-full", AuthIndex: "auth-full", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
		{Name: "codex-urgent", AuthIndex: "auth-urgent", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}

	evidence := []ProbeEvidence{
		{
			Provider:          core.ProviderClaude,
			AuthIndex:         "auth-full",
			ObservedAt:        now,
			ResetAt:           &resetFull,
			LongWindowResetAt: &resetFull,
			Remaining:         &remFull,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
		{
			Provider:          core.ProviderCodex,
			AuthIndex:         "auth-urgent",
			ObservedAt:        now,
			ResetAt:           &resetUrgent,
			LongWindowResetAt: &resetUrgent,
			Remaining:         &remUrgent,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePro,
			EvidenceFresh:     true,
		},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 2 {
		t.Fatalf("expected 2 plan items, got %d", len(plan.Items))
	}

	priorityByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
	}

	// 满额账号必须排在第一位，避免"周期未激活"的账号长期闲置。
	if p := priorityByAuth["auth-full"]; p != 100 {
		t.Errorf("expected auth-full priority 100, got %d", p)
	}
	if p := priorityByAuth["auth-urgent"]; p != 99 {
		t.Errorf("expected auth-urgent priority 99, got %d", p)
	}
}

func TestPlanFreshOnly_PacingScore_FreeVersusPaid(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	resetWeekly := now.Add(168 * time.Hour)
	resetPlus := now.Add(139 * time.Hour)

	rem100 := int64(100)
	rem33 := int64(33)

	credentials := []core.Credential{
		{Name: "antigravity-free", AuthIndex: "auth-ag-free", Provider: core.ProviderAntigravity, Type: core.CredentialTypeAntigravity},
		{Name: "codex-plus", AuthIndex: "auth-codex-plus", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}

	evidence := []ProbeEvidence{
		{
			Provider:          core.ProviderAntigravity,
			AuthIndex:         "auth-ag-free",
			ObservedAt:        now,
			ResetAt:           &resetWeekly,
			LongWindowResetAt: &resetWeekly,
			Remaining:         &rem100,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypeFree,
			EvidenceFresh:     true,
		},
		{
			Provider:          core.ProviderCodex,
			AuthIndex:         "auth-codex-plus",
			ObservedAt:        now,
			ResetAt:           &resetPlus,
			LongWindowResetAt: &resetPlus,
			Remaining:         &rem33,
			Freshness:         core.FreshnessFresh,
			ProbeStatus:       core.ProbeStatusReady,
			Status:            EvidenceStatusReady,
			PlanType:          core.PlanTypePlus,
			EvidenceFresh:     true,
		},
	}

	options := Options{
		Now:         now,
		MaxPriority: 100,
	}

	plan := PlanFreshOnly(credentials, evidence, options)
	if len(plan.Items) != 2 {
		t.Fatalf("expected 2 plan items, got %d", len(plan.Items))
	}

	priorityByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
	}

	// 移除 paid-first 规则后，纯按 PacingScore 排序：
	// auth-ag-free (score = 1.000 / 1.000 = 1.0) 应排在 auth-codex-plus (score = 0.33 / (139/168) ≈ 0.399) 前面
	if p := priorityByAuth["auth-ag-free"]; p != 100 {
		t.Errorf("expected antigravity-free priority 100, got %d", p)
	}
	if p := priorityByAuth["auth-codex-plus"]; p != 99 {
		t.Errorf("expected codex-plus priority 99, got %d", p)
	}
}

func TestPlanFreshOnly_PacingScore_MultiWindow_ShortWindowTighter(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)

	// 短窗口：10% 剩余，1h 后重置 -> score = 0.10 / (1/5) = 0.50（更紧张）
	shortReset := now.Add(1 * time.Hour)
	shortRem := int64(10)
	// 长窗口：80% 剩余，84h 后重置 -> score = 0.80 / (84/168) = 1.60
	longReset := now.Add(84 * time.Hour)
	longRem := int64(80)
	primaryRem := int64(50) // 主字段仅用于通过顶部短路，不驱动分档

	credentials := []core.Credential{
		{Name: "codex-multi", AuthIndex: "auth-multi-short", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:             core.ProviderCodex,
			AuthIndex:            "auth-multi-short",
			ObservedAt:           now,
			Remaining:            &primaryRem,
			ShortWindowRemaining: &shortRem,
			ShortWindowResetAt:   &shortReset,
			LongWindowRemaining:  &longRem,
			LongWindowResetAt:    &longReset,
			Freshness:            core.FreshnessFresh,
			ProbeStatus:          core.ProbeStatusReady,
			Status:               EvidenceStatusReady,
			PlanType:             core.PlanTypePro,
			EvidenceFresh:        true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}

	got := plan.Items[0].PacingScore
	want := 0.10 / (1.0 / 5.0)
	if diff := got - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected pacing score %.6f (short window wins), got %.6f", want, got)
	}
}

func TestPlanFreshOnly_PacingScore_MultiWindow_LongWindowTighter(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)

	// 短窗口：80% 剩余，1h 后重置 -> score = 0.80 / (1/5) = 4.00
	shortReset := now.Add(1 * time.Hour)
	shortRem := int64(80)
	// 长窗口：10% 剩余，84h 后重置 -> score = 0.10 / (84/168) = 0.20（更紧张）
	longReset := now.Add(84 * time.Hour)
	longRem := int64(10)
	primaryRem := int64(50)

	credentials := []core.Credential{
		{Name: "codex-multi", AuthIndex: "auth-multi-long", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:             core.ProviderCodex,
			AuthIndex:            "auth-multi-long",
			ObservedAt:           now,
			Remaining:            &primaryRem,
			ShortWindowRemaining: &shortRem,
			ShortWindowResetAt:   &shortReset,
			LongWindowRemaining:  &longRem,
			LongWindowResetAt:    &longReset,
			Freshness:            core.FreshnessFresh,
			ProbeStatus:          core.ProbeStatusReady,
			Status:               EvidenceStatusReady,
			PlanType:             core.PlanTypePro,
			EvidenceFresh:        true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}

	got := plan.Items[0].PacingScore
	want := 0.10 / (84.0 / 168.0)
	if diff := got - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected pacing score %.6f (long window wins), got %.6f", want, got)
	}
}

func TestPlanFreshOnly_PacingScore_MultiWindow_LongOnlyFallback(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)

	// 模拟只上报长窗口数据的场景（如 Codex weekly-only 付费计划）：
	// ShortWindow* 全为 nil，应走"只有一个窗口分数"分支，不报错。
	longReset := now.Add(42 * time.Hour)
	longRem := int64(64)
	primaryRem := int64(64)

	credentials := []core.Credential{
		{Name: "codex-weekly-only", AuthIndex: "auth-weekly-only", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:            core.ProviderCodex,
			AuthIndex:           "auth-weekly-only",
			ObservedAt:          now,
			Remaining:           &primaryRem,
			LongWindowRemaining: &longRem,
			LongWindowResetAt:   &longReset,
			Freshness:           core.FreshnessFresh,
			ProbeStatus:         core.ProbeStatusReady,
			Status:              EvidenceStatusReady,
			PlanType:            core.PlanTypePro,
			EvidenceFresh:       true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}

	got := plan.Items[0].PacingScore
	want := 0.64 / (42.0 / 168.0)
	if diff := got - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected pacing score %.6f (long-only), got %.6f", want, got)
	}
}

func TestPlanFreshOnly_PacingScore_MultiQuotaWindows_Bottleneck(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	primaryRem := int64(100)

	// 账号定义了 4 个窗口：5h, 24h, 7d (weekly), 30d (monthly)
	// Window 1: 5h 窗口, 剩余 80%, 4h 后重置 -> score = 0.80 / (4/5) = 1.00
	// Window 2: 24h 窗口, 剩余 50%, 12h 后重置 -> score = 0.50 / (12/24) = 1.00
	// Window 3: 7d 窗口, 剩余 20%, 84h 后重置 -> score = 0.20 / (84/168) = 0.40 (最紧张瓶颈窗口)
	// Window 4: 30d 窗口, 剩余 90%, 15d 后重置 -> score = 0.90 / (15/30) = 1.80
	windows := []core.QuotaWindow{
		{Name: "5h", Duration: 5 * time.Hour, Remaining: 80, ResetAt: now.Add(4 * time.Hour)},
		{Name: "24h", Duration: 24 * time.Hour, Remaining: 50, ResetAt: now.Add(12 * time.Hour)},
		{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: 20, ResetAt: now.Add(84 * time.Hour)},
		{Name: "monthly", Duration: 30 * 24 * time.Hour, Remaining: 90, ResetAt: now.Add(15 * 24 * time.Hour)},
	}

	credentials := []core.Credential{
		{Name: "codex-4-windows", AuthIndex: "auth-4-windows", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:      core.ProviderCodex,
			AuthIndex:     "auth-4-windows",
			ObservedAt:    now,
			Remaining:     &primaryRem,
			Windows:       windows,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}

	got := plan.Items[0].PacingScore
	want := 0.20 / (84.0 / 168.0) // 0.40 (weekly is bottleneck)
	if diff := got - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected pacing score %.6f (weekly window bottleneck), got %.6f", want, got)
	}
}

func TestPlanFreshOnly_PacingScore_MultiQuotaWindows_AnyWindowDepletedIsZero(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	primaryRem := int64(100)

	// 5h 窗口还有 90%，但 7d 窗口剩余 0
	windows := []core.QuotaWindow{
		{Name: "5h", Duration: 5 * time.Hour, Remaining: 90, ResetAt: now.Add(4 * time.Hour)},
		{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: 0, ResetAt: now.Add(48 * time.Hour)},
	}

	credentials := []core.Credential{
		{Name: "codex-window-depleted", AuthIndex: "auth-window-depleted", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:      core.ProviderCodex,
			AuthIndex:     "auth-window-depleted",
			ObservedAt:    now,
			Remaining:     &primaryRem,
			Windows:       windows,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	if got := plan.Items[0].PacingScore; got != 0 {
		t.Errorf("expected pacing score 0 when any window is depleted, got %.6f", got)
	}
}


func TestPlanFreshOnly_CachedEvidencePopulatesPacingScoreAndZeroChanges(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	shortReset := now.Add(4 * time.Hour)
	longReset := now.Add(5 * 24 * time.Hour)
	rem := int64(80)
	shortRem := int64(80)
	longRem := int64(90)

	credentials := []core.Credential{
		{
			Name:      "codex-cached",
			AuthIndex: "auth-cached-1",
			Provider:  core.ProviderCodex,
			Type:      core.CredentialTypeCodex,
			Priority:  42,
			Disabled:  false,
			PlanType:  core.PlanTypePlus,
		},
	}
	cachedEvidence := []ProbeEvidence{
		{
			Provider:             core.ProviderCodex,
			AuthIndex:            "auth-cached-1",
			ObservedAt:           now.Add(-10 * time.Minute),
			ResetAt:              &shortReset,
			Remaining:            &rem,
			ShortWindowRemaining: &shortRem,
			ShortWindowResetAt:   &shortReset,
			LongWindowRemaining:  &longRem,
			LongWindowResetAt:    &longReset,
			Freshness:            core.FreshnessStale,
			ProbeStatus:          core.ProbeStatusReady,
			Status:               EvidenceStatusReady,
			PlanType:             core.PlanTypePlus,
			EvidenceFresh:        false,
		},
	}

	plan := PlanFreshOnly(credentials, cachedEvidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(plan.Items))
	}
	item := plan.Items[0]
	if item.EvidenceFresh {
		t.Errorf("expected EvidenceFresh=false for cached item")
	}
	if item.Remaining == nil || *item.Remaining != 80 {
		t.Errorf("expected Remaining=80, got %v", item.Remaining)
	}
	if item.ShortWindowRemaining == nil || *item.ShortWindowRemaining != 80 {
		t.Errorf("expected ShortWindowRemaining=80, got %v", item.ShortWindowRemaining)
	}
	if item.LongWindowRemaining == nil || *item.LongWindowRemaining != 90 {
		t.Errorf("expected LongWindowRemaining=90, got %v", item.LongWindowRemaining)
	}
	if item.PlanType != core.PlanTypePlus {
		t.Errorf("expected PlanType plus, got %s", item.PlanType)
	}
	if item.PacingScore <= 0 {
		t.Errorf("expected PacingScore > 0, got %.6f", item.PacingScore)
	}
	if item.Priority != 42 {
		t.Errorf("expected Priority preserved at 42, got %d", item.Priority)
	}
	if item.Disabled {
		t.Errorf("expected Disabled preserved as false")
	}
	if len(plan.Changes) != 0 {
		t.Errorf("expected 0 changes for cached evidence, got %d changes: %+v", len(plan.Changes), plan.Changes)
	}
}

func TestPlanFreshOnly_MixedFreshAndCached(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	freshRem := int64(95)
	cachedRem := int64(70)

	credentials := []core.Credential{
		{Name: "c-fresh", AuthIndex: "auth-fresh", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex, Priority: 10, Disabled: false},
		{Name: "c-cached", AuthIndex: "auth-cached", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex, Priority: 50, Disabled: false},
	}
	evidence := []ProbeEvidence{
		{
			Provider:      core.ProviderCodex,
			AuthIndex:     "auth-fresh",
			ObservedAt:    now,
			ResetAt:       &reset,
			Remaining:     &freshRem,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
		{
			Provider:      core.ProviderCodex,
			AuthIndex:     "auth-cached",
			ObservedAt:    now.Add(-5 * time.Minute),
			ResetAt:       &reset,
			Remaining:     &cachedRem,
			Freshness:     core.FreshnessStale,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePlus,
			EvidenceFresh: false,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(plan.Items))
	}

	// Changes should ONLY contain the fresh item
	if len(plan.Changes) != 1 {
		t.Fatalf("expected exactly 1 change for fresh item, got %d: %+v", len(plan.Changes), plan.Changes)
	}
	if plan.Changes[0].Credential.AuthIndex != "auth-fresh" {
		t.Errorf("expected change on auth-fresh, got %s", plan.Changes[0].Credential.AuthIndex)
	}
	if plan.Changes[0].Priority != 100 {
		t.Errorf("expected fresh item assigned top priority 100, got %d", plan.Changes[0].Priority)
	}

	// Cached item retains priority 50
	var cachedItem *PlanItem
	for i := range plan.Items {
		if plan.Items[i].Credential.AuthIndex == "auth-cached" {
			cachedItem = &plan.Items[i]
		}
	}
	if cachedItem == nil {
		t.Fatalf("cached item not found in items")
	}
	if cachedItem.Priority != 50 {
		t.Errorf("expected cached item to keep priority 50, got %d", cachedItem.Priority)
	}
	if cachedItem.PacingScore <= 0 {
		t.Errorf("expected cached item to have valid PacingScore > 0, got %.6f", cachedItem.PacingScore)
	}
}
