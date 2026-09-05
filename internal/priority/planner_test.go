package priority

import (
	"testing"
	"time"

	"quota-pacer/internal/core"
)

func TestPlanFreshOnly_Claude_PositiveRemaining(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(1 * time.Hour) // <=6h -> legacy 5h 分档基准，timeRemainingRatio=0.2，不会被 clamp 抹平差异
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
	if item2.Priority != 100 {
		t.Errorf("expected item2 priority 100 (shared fresh-positive tier), got %d", item2.Priority)
	}
	if want := weightFromHeadroom(remainingHeadroom(*item1, now)); item1.Weight != want {
		t.Errorf("expected item1 weight %d, got %d", want, item1.Weight)
	}
	if want := weightFromHeadroom(remainingHeadroom(*item2, now)); item2.Weight != want {
		t.Errorf("expected item2 weight %d, got %d", want, item2.Weight)
	}
	if item1.Weight <= item2.Weight {
		t.Errorf("expected higher-remaining item1 weight > item2 weight, got item1=%d item2=%d", item1.Weight, item2.Weight)
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

	// 共享健康 tier：跨 Provider 相同证据 -> 相同 priority(100) + 相同 weight
	for _, item := range plan.Items {
		if item.Priority != 100 {
			t.Errorf("expected shared tier priority 100 for %s, got %d", item.Credential.Name, item.Priority)
		}
		if want := weightFromHeadroom(remainingHeadroom(item, now)); item.Weight != want {
			t.Errorf("expected weight %d for %s, got %d", want, item.Credential.Name, item.Weight)
		}
	}
	if plan.Items[0].Weight != plan.Items[1].Weight || plan.Items[1].Weight != plan.Items[2].Weight {
		t.Errorf("expected identical evidence across providers to produce equal weights, got %d/%d/%d",
			plan.Items[0].Weight, plan.Items[1].Weight, plan.Items[2].Weight)
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
	weightByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
		weightByAuth[item.Credential.AuthIndex] = item.Weight
	}

	// 共享健康 tier：全部凭证 priority=100，只靠 weight 按"距离配速目标还能多用多少"区分流量占比。
	for _, item := range plan.Items {
		if item.Priority != 100 {
			t.Errorf("expected shared tier priority 100 for %s, got %d", item.Credential.Name, item.Priority)
		}
		if want := weightFromHeadroom(remainingHeadroom(item, now)); item.Weight != want {
			t.Errorf("expected weight %d for %s, got %d", want, item.Credential.Name, item.Weight)
		}
	}
	// 预期 weight 排序（headroom = remainingRatio - timeRemainingRatio，均走 7d 长窗口基准）：
	// Claude 0.51-43/168≈0.254 > Codex 0.80-120/168≈0.086 > Antigravity 0.65-140/168<0（floor 0）
	if weightByAuth["auth-claude-urgent"] <= weightByAuth["auth-codex-mid"] {
		t.Errorf("expected claude weight > codex weight, got claude=%d codex=%d",
			weightByAuth["auth-claude-urgent"], weightByAuth["auth-codex-mid"])
	}
	if weightByAuth["auth-codex-mid"] <= weightByAuth["auth-ag-slow"] {
		t.Errorf("expected codex weight > antigravity weight, got codex=%d antigravity=%d",
			weightByAuth["auth-codex-mid"], weightByAuth["auth-ag-slow"])
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
	weightByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
		weightByAuth[item.Credential.AuthIndex] = item.Weight
	}

	// 共享健康 tier：全部凭证 priority=100，只靠 weight 按"距离配速目标还能多用多少"区分流量占比。
	for _, item := range plan.Items {
		if item.Priority != 100 {
			t.Errorf("expected shared tier priority 100 for %s, got %d", item.Credential.Name, item.Priority)
		}
		if want := weightFromHeadroom(remainingHeadroom(item, now)); item.Weight != want {
			t.Errorf("expected weight %d for %s, got %d", want, item.Credential.Name, item.Weight)
		}
	}
	// headroom（均走 7d 长窗口基准）：auth-slow 0.80-48/168≈0.514 > auth-mid 0.80-96/168≈0.229
	// > auth-fast 0.10-48/168<0（floor 0）
	if weightByAuth["auth-slow"] <= weightByAuth["auth-mid"] {
		t.Errorf("expected auth-slow weight > auth-mid weight, got slow=%d mid=%d", weightByAuth["auth-slow"], weightByAuth["auth-mid"])
	}
	if weightByAuth["auth-mid"] <= weightByAuth["auth-fast"] {
		t.Errorf("expected auth-mid weight > auth-fast weight, got mid=%d fast=%d", weightByAuth["auth-mid"], weightByAuth["auth-fast"])
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
	weightByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
		weightByAuth[item.Credential.AuthIndex] = item.Weight
	}

	// 共享健康 tier：满额账号（remaining>=100 特判直接给满 headroom=1.0）
	// 与即将过期账号 priority 都是 100，只靠 weight 体现悬殊差距。
	for _, item := range plan.Items {
		if item.Priority != 100 {
			t.Errorf("expected shared tier priority 100 for %s, got %d", item.Credential.Name, item.Priority)
		}
		if want := weightFromHeadroom(remainingHeadroom(item, now)); item.Weight != want {
			t.Errorf("expected weight %d for %s, got %d", want, item.Credential.Name, item.Weight)
		}
	}
	if weightByAuth["auth-full"] <= weightByAuth["auth-urgent"] {
		t.Errorf("expected auth-full weight > auth-urgent weight, got full=%d urgent=%d",
			weightByAuth["auth-full"], weightByAuth["auth-urgent"])
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
	weightByAuth := make(map[string]int)
	for _, item := range plan.Items {
		priorityByAuth[item.Credential.AuthIndex] = item.Priority
		weightByAuth[item.Credential.AuthIndex] = item.Weight
	}

	// 移除 paid-first 规则后，纯按"距离配速目标还能多用多少"排序：
	// auth-ag-free 满额 -> headroom=1.0 满值；auth-codex-plus 0.33-139/168<0 -> floor 0。
	// 两者共享同一个 priority tier（100），不再靠 priority 差异体现。
	for _, item := range plan.Items {
		if item.Priority != 100 {
			t.Errorf("expected shared tier priority 100 for %s, got %d", item.Credential.Name, item.Priority)
		}
		if want := weightFromHeadroom(remainingHeadroom(item, now)); item.Weight != want {
			t.Errorf("expected weight %d for %s, got %d", want, item.Credential.Name, item.Weight)
		}
	}
	if weightByAuth["auth-ag-free"] <= weightByAuth["auth-codex-plus"] {
		t.Errorf("expected antigravity-free weight > codex-plus weight, got free=%d plus=%d",
			weightByAuth["auth-ag-free"], weightByAuth["auth-codex-plus"])
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
	// 单一 tier 成员：weight 由自身 remaining-pace headroom 决定，不再是"唯一成员就必然满值"。
	var freshItem *PlanItem
	for i := range plan.Items {
		if plan.Items[i].Credential.AuthIndex == "auth-fresh" {
			freshItem = &plan.Items[i]
		}
	}
	if freshItem == nil {
		t.Fatalf("fresh item not found in items")
	}
	if want := weightFromHeadroom(remainingHeadroom(*freshItem, now)); plan.Changes[0].Weight != want {
		t.Errorf("expected sole tier member weight %d, got %d", want, plan.Changes[0].Weight)
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

// --- Codex banked reset-credit "即将过期" pacing boost ---
// 用户需求：Codex 若存在一条 available 状态的银行化重置额度（可手动兑换的一次性重置），
// 且其 expiresAt 落在未来 14 天内（即将作废），应更激进地消耗额度而非保守 pacing，避免
// 额度白白过期浪费。最终确认的实现：只作用于驱动 weight 的 remainingHeadroom（不再作用于
// pacingScore，因为 pacingScore 已经不再驱动 priority/tier 归属），在正常算出的 headroom
// 基础上直接 +1.0、不做任何上限 clamp——不封顶是为了让这类账号的 weight 能明显超过普通
// 满额账号的上限（1000），调度器才会真的倾斜更多流量过去。详见 planner.go 中
// codexResetCreditBoostActive/remainingHeadroom 的注释说明。

func codexItemWithWindow(remaining int64, resetIn time.Duration, duration time.Duration) PlanItem {
	rem := remaining
	return PlanItem{
		Credential: core.Credential{AuthIndex: "auth-codex-credit", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
		Remaining:  &rem,
		Windows:    []core.QuotaWindow{{Name: "weekly", Duration: duration, Remaining: remaining, ResetAt: time.Time{}.Add(resetIn)}},
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_ExpiringWithin14Days(t *testing.T) {
	now := time.Time{}
	item := codexItemWithWindow(30, 84*time.Hour, 168*time.Hour)
	baseline := remainingHeadroom(item, now) // 0.30-0.5=-0.2 -> floor 0

	expiresAt := now.Add(5 * 24 * time.Hour) // 5 天后过期，落在 14 天窗口内
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	boosted := remainingHeadroom(item, now)
	want := baseline + 1.0
	if diff := boosted - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected boosted headroom %.6f (baseline %.6f + 1.0), got %.6f", want, baseline, boosted)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_ExceedsFullWhenUnderlyingHeadroomPositive(t *testing.T) {
	// 专门覆盖"不封顶"这个关键性质：底层 headroom 本身就是正数时，+1.0 后应明显超过 1.0，
	// 而不是被压回 1.0——否则这个即将浪费额度的账号跟普通满额账号（headroom=1.0）就没区别了。
	now := time.Time{}
	item := codexItemWithWindow(80, 10*time.Hour, 168*time.Hour) // ratio=0.8, timeRatio=10/168≈0.0595, headroom≈0.7405
	baseline := remainingHeadroom(item, now)
	if baseline <= 0 {
		t.Fatalf("expected positive unboosted headroom as test precondition, got %.6f", baseline)
	}

	expiresAt := now.Add(5 * 24 * time.Hour)
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	boosted := remainingHeadroom(item, now)
	want := baseline + 1.0
	if diff := boosted - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected boosted headroom %.6f (baseline %.6f + 1.0, not clamped), got %.6f", want, baseline, boosted)
	}
	if boosted <= 1.0 {
		t.Errorf("expected boosted headroom to exceed 1.0 (not clamped to full), got %.6f", boosted)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_ExpiringBeyond14Days(t *testing.T) {
	now := time.Time{}
	item := codexItemWithWindow(30, 84*time.Hour, 168*time.Hour)
	baseline := remainingHeadroom(item, now)

	expiresAt := now.Add(20 * 24 * time.Hour) // 20 天后过期，超出 14 天窗口
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	got := remainingHeadroom(item, now)
	if diff := got - baseline; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected unboosted headroom %.6f (beyond 14d window), got %.6f", baseline, got)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_NoAvailableCredit(t *testing.T) {
	now := time.Time{}
	item := codexItemWithWindow(30, 84*time.Hour, 168*time.Hour)
	baseline := remainingHeadroom(item, now)

	expiresAt := now.Add(5 * 24 * time.Hour)
	item.AvailableResetCredits = 0 // 无可用额度
	item.NearestResetCreditExpiresAt = &expiresAt

	got := remainingHeadroom(item, now)
	if diff := got - baseline; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected unboosted headroom %.6f when AvailableResetCredits=0, got %.6f", baseline, got)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_NonCodexProviderUnaffected(t *testing.T) {
	now := time.Time{}
	rem := int64(30)
	item := PlanItem{
		Credential: core.Credential{AuthIndex: "auth-claude-credit", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
		Remaining:  &rem,
		Windows:    []core.QuotaWindow{{Name: "weekly", Duration: 168 * time.Hour, Remaining: 30, ResetAt: now.Add(84 * time.Hour)}},
	}
	baseline := remainingHeadroom(item, now)

	expiresAt := now.Add(5 * 24 * time.Hour)
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	got := remainingHeadroom(item, now)
	if diff := got - baseline; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected non-Codex provider unaffected by reset-credit boost, baseline=%.6f got=%.6f", baseline, got)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_DepletedRemainingStaysZero(t *testing.T) {
	now := time.Time{}
	item := codexItemWithWindow(0, 84*time.Hour, 168*time.Hour)
	expiresAt := now.Add(5 * 24 * time.Hour)
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	if got := remainingHeadroom(item, now); got != 0 {
		t.Errorf("expected headroom 0 for depleted primary remaining even with imminent-expiry credit, got %.6f", got)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_AlreadyExpiredCreditNotBoosted(t *testing.T) {
	now := time.Time{}.Add(30 * 24 * time.Hour)
	item := codexItemWithWindow(30, 84*time.Hour, 168*time.Hour)
	item.Windows[0].ResetAt = now.Add(84 * time.Hour)
	baseline := remainingHeadroom(item, now)

	expiresAt := now.Add(-time.Hour) // 已过期
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	got := remainingHeadroom(item, now)
	if diff := got - baseline; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected unboosted headroom %.6f for already-expired credit, got %.6f", baseline, got)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_BoundaryExactly14Days(t *testing.T) {
	now := time.Time{}
	item := codexItemWithWindow(30, 84*time.Hour, 168*time.Hour)
	baseline := remainingHeadroom(item, now)

	expiresAt := now.Add(14 * 24 * time.Hour) // 精确 14 天：应触发提升
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	got := remainingHeadroom(item, now)
	want := baseline + 1.0
	if diff := got - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected boosted headroom %.6f at exactly-14-day boundary, got %.6f", want, got)
	}
}

func TestRemainingHeadroom_CodexResetCreditBoost_BoundaryJustOver14Days(t *testing.T) {
	now := time.Time{}
	item := codexItemWithWindow(30, 84*time.Hour, 168*time.Hour)
	baseline := remainingHeadroom(item, now)

	expiresAt := now.Add(14*24*time.Hour + time.Minute) // 超出 14 天一分钟：不应触发
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	got := remainingHeadroom(item, now)
	if diff := got - baseline; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected unboosted headroom %.6f just beyond 14-day boundary, got %.6f", baseline, got)
	}
}

// TestPacingScore_NotAffectedByCodexResetCreditBoost 锁定这次拍板的范围收窄：pacingScore
// 已经不再驱动 priority/tier 归属（只用于陈旧凭证 tie-break 与审计展示），Codex reset-credit
// 提升只作用于 remainingHeadroom/weight 这一条链路，不应该再影响 pacingScore 的取值。
func TestPacingScore_NotAffectedByCodexResetCreditBoost(t *testing.T) {
	now := time.Time{}
	item := codexItemWithWindow(30, 84*time.Hour, 168*time.Hour)
	baseline := pacingScore(item, now)

	expiresAt := now.Add(5 * 24 * time.Hour) // 落在 14 天窗口内，会让 remainingHeadroom 被提升
	item.AvailableResetCredits = 1
	item.NearestResetCreditExpiresAt = &expiresAt

	got := pacingScore(item, now)
	if diff := got - baseline; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected pacingScore unaffected by reset-credit boost, baseline=%.6f got=%.6f", baseline, got)
	}
}

// TestPlanFreshOnly_CodexResetCreditBoost_ThreadsThroughEvidence 验证 AvailableResetCredits/
// NearestResetCreditExpiresAt 能从 ProbeEvidence 正确贯穿到 PlanItem，PacingScore 保持不受
// 提升影响，而 Weight（经由 remainingHeadroom）确实反映了提升后的 headroom。
func TestPlanFreshOnly_CodexResetCreditBoost_ThreadsThroughEvidence(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	rem := int64(30)
	resetAt := now.Add(84 * time.Hour)
	expiresAt := now.Add(5 * 24 * time.Hour)

	credentials := []core.Credential{
		{Name: "codex-credit", AuthIndex: "auth-codex-credit", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:                    core.ProviderCodex,
			AuthIndex:                   "auth-codex-credit",
			ObservedAt:                  now,
			Remaining:                   &rem,
			Windows:                     []core.QuotaWindow{{Name: "weekly", Duration: 168 * time.Hour, Remaining: 30, ResetAt: resetAt}},
			AvailableResetCredits:       1,
			NearestResetCreditExpiresAt: &expiresAt,
			Freshness:                   core.FreshnessFresh,
			ProbeStatus:                 core.ProbeStatusReady,
			Status:                      EvidenceStatusReady,
			PlanType:                    core.PlanTypePlus,
			EvidenceFresh:               true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	item := plan.Items[0]
	if item.AvailableResetCredits != 1 {
		t.Errorf("expected AvailableResetCredits threaded through to PlanItem, got %d", item.AvailableResetCredits)
	}
	if item.NearestResetCreditExpiresAt == nil || !item.NearestResetCreditExpiresAt.Equal(expiresAt) {
		t.Errorf("expected NearestResetCreditExpiresAt threaded through, got %v", item.NearestResetCreditExpiresAt)
	}
	// PacingScore 不受提升影响：0.30/(84/168)=0.6。
	wantScore := 0.30 / (84.0 / 168.0)
	if diff := item.PacingScore - wantScore; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("expected unboosted PacingScore %.6f, got %.6f", wantScore, item.PacingScore)
	}
	// Weight 反映提升后的 headroom：unboosted headroom=0.30-0.5=-0.2->floor 0，+1.0=1.0，
	// weight=weightFromHeadroom(1.0)=1000（跟一个普通满额账号打平，因为这个用例底层 headroom
	// 本身已经是 0；"能超过 1000"的场景见 TestPlanFreshOnly_CodexResetCreditBoost_WeightExceedsNormalCap）。
	if item.Weight != weightScaleReference {
		t.Errorf("expected Weight %d (boosted headroom reaches exactly full), got %d", weightScaleReference, item.Weight)
	}
}

// TestPlanFreshOnly_CodexResetCreditBoost_WeightExceedsNormalCap 端到端验证"不封顶"这个关键
// 性质在完整 PlanFreshOnly 流程里确实生效：底层 headroom 为正时，提升后的 weight 应明显超过
// weightScaleReference（1000）这个普通满额账号的上限，调度器才会真的倾斜更多流量过去。
func TestPlanFreshOnly_CodexResetCreditBoost_WeightExceedsNormalCap(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	rem := int64(80)
	resetAt := now.Add(10 * time.Hour)
	expiresAt := now.Add(5 * 24 * time.Hour)

	credentials := []core.Credential{
		{Name: "codex-credit", AuthIndex: "auth-codex-credit", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex},
	}
	evidence := []ProbeEvidence{
		{
			Provider:                    core.ProviderCodex,
			AuthIndex:                   "auth-codex-credit",
			ObservedAt:                  now,
			Remaining:                   &rem,
			Windows:                     []core.QuotaWindow{{Name: "weekly", Duration: 168 * time.Hour, Remaining: 80, ResetAt: resetAt}},
			AvailableResetCredits:       1,
			NearestResetCreditExpiresAt: &expiresAt,
			Freshness:                   core.FreshnessFresh,
			ProbeStatus:                 core.ProbeStatusReady,
			Status:                      EvidenceStatusReady,
			PlanType:                    core.PlanTypePlus,
			EvidenceFresh:               true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	item := plan.Items[0]
	if item.Weight <= weightScaleReference {
		t.Errorf("expected boosted Weight to exceed the normal full-quota cap %d, got %d", weightScaleReference, item.Weight)
	}
}

// TestPlanFreshOnly_OverPaceAccountStillGetsFloorWeightInSharedTier 覆盖用户明确要求的场景：
// 共享 tier 内一个已经落后于配速目标（remainingHeadroom floor 到 0）的账号，仍必须以
// weight=weightFloor（1，不是 0）出现在最终 Plan 里，并进入 Changes（不能被跳过/排除），
// 才能继续参与 CPA WeightedRoundRobinSelector 的轮转。
func TestPlanFreshOnly_OverPaceAccountStillGetsFloorWeightInSharedTier(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	healthyReset := now.Add(1 * time.Hour) // <=6h -> 5h 分档基准
	healthyRem := int64(90)

	overPaceReset := now.Add(90 * time.Hour)
	overPaceRem := int64(5)

	credentials := []core.Credential{
		{Name: "healthy", AuthIndex: "auth-healthy", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
		{Name: "over-pace", AuthIndex: "auth-over-pace", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude},
	}
	evidence := []ProbeEvidence{
		{
			Provider:      core.ProviderClaude,
			AuthIndex:     "auth-healthy",
			ObservedAt:    now,
			ResetAt:       &healthyReset,
			Remaining:     &healthyRem,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
		{
			Provider:      core.ProviderClaude,
			AuthIndex:     "auth-over-pace",
			ObservedAt:    now,
			ResetAt:       &overPaceReset,
			Remaining:     &overPaceRem,
			Windows:       []core.QuotaWindow{{Name: "custom", Duration: 100 * time.Hour, Remaining: 5, ResetAt: overPaceReset}},
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
	}

	plan := PlanFreshOnly(credentials, evidence, Options{Now: now, MaxPriority: 100})
	if len(plan.Items) != 2 {
		t.Fatalf("expected 2 plan items, got %d", len(plan.Items))
	}

	var overPaceItem *PlanItem
	for i := range plan.Items {
		if plan.Items[i].Credential.AuthIndex == "auth-over-pace" {
			overPaceItem = &plan.Items[i]
		}
		if plan.Items[i].Priority != 100 {
			t.Errorf("expected shared tier priority 100 for %s, got %d", plan.Items[i].Credential.Name, plan.Items[i].Priority)
		}
	}
	if overPaceItem == nil {
		t.Fatalf("over-pace item not found in plan.Items")
	}
	if overPaceItem.Weight != weightFloor {
		t.Errorf("expected over-pace account weight floored at %d, got %d", weightFloor, overPaceItem.Weight)
	}

	var overPaceChange *Change
	for i := range plan.Changes {
		if plan.Changes[i].Credential.AuthIndex == "auth-over-pace" {
			overPaceChange = &plan.Changes[i]
		}
	}
	if overPaceChange == nil {
		t.Fatalf("expected over-pace account to appear in plan.Changes (not be excluded), but it was missing")
	}
	if overPaceChange.Weight != weightFloor {
		t.Errorf("expected over-pace account's Change.Weight floored at %d, got %d", weightFloor, overPaceChange.Weight)
	}
}

// --- weightFromHeadroom / remainingHeadroom ---

func TestWeightFromHeadroom_FullHeadroomGetsFullWeight(t *testing.T) {
	if got := weightFromHeadroom(1.0); got != weightScaleReference {
		t.Errorf("expected weight %d for headroom=1.0, got %d", weightScaleReference, got)
	}
}

func TestWeightFromHeadroom_ProportionalMidpoint(t *testing.T) {
	if got := weightFromHeadroom(0.5); got != 500 {
		t.Errorf("expected weight 500 for headroom=0.5, got %d", got)
	}
}

func TestWeightFromHeadroom_FlooredAtOneForNearZeroHeadroom(t *testing.T) {
	if got := weightFromHeadroom(0.0001); got != weightFloor {
		t.Errorf("expected weight floored at %d for near-zero headroom, got %d", weightFloor, got)
	}
}

// TestWeightFromHeadroom_FlooredAtOneWhenOverPace 覆盖用户明确要求的场景：已经落后于配速目标
// （headroom 算出来是 0，即"超标使用"）的账号，weight 不能归零，必须保留 weightFloor（1）继续
// 参与 CPA WeightedRoundRobinSelector 的轮转——weight<=0 会被该 selector 的
// positiveWeightAuths 过滤器直接踢出轮转，0 不是"一点点流量"，是"完全没有"。
func TestWeightFromHeadroom_FlooredAtOneWhenOverPace(t *testing.T) {
	if got := weightFromHeadroom(0); got != weightFloor {
		t.Errorf("expected over-pace account (headroom=0) to floor at weight %d, got %d", weightFloor, got)
	}
}

func TestRemainingHeadroom_FullRemainingSpecialCase(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(168 * time.Hour)
	rem := int64(100)
	item := PlanItem{
		Credential: core.Credential{Provider: core.ProviderClaude},
		Remaining:  &rem,
		ResetAt:    &resetAt,
	}
	if got := remainingHeadroom(item, now); got != 1.0 {
		t.Errorf("expected full remaining (未激活周期) to short-circuit to headroom 1.0, got %.6f", got)
	}
}

func TestRemainingHeadroom_StaleResetAtAlreadyPassed(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(-time.Hour) // reset 时间已过但 remaining 尚未刷新
	rem := int64(60)
	item := PlanItem{
		Credential: core.Credential{Provider: core.ProviderClaude},
		Remaining:  &rem,
		ResetAt:    &resetAt,
	}
	if got := remainingHeadroom(item, now); got != 1.0 {
		t.Errorf("expected stale already-passed resetAt to short-circuit to headroom 1.0, got %.6f", got)
	}
}

func TestRemainingHeadroom_OverPaceFloorsToZero(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	// 5% 剩余，但只过了 10% 的窗口时间——远落后于配速目标，headroom 应 floor 到 0（不是负数）。
	resetAt := now.Add(90 * time.Hour) // 10% 时间流逝对应 100h 窗口中的 10h 已过，还剩 90h
	rem := int64(5)
	item := PlanItem{
		Credential: core.Credential{Provider: core.ProviderClaude},
		Remaining:  &rem,
		ResetAt:    &resetAt,
		Windows:    []core.QuotaWindow{{Name: "custom", Duration: 100 * time.Hour, Remaining: 5, ResetAt: resetAt}},
	}
	if got := remainingHeadroom(item, now); got != 0 {
		t.Errorf("expected over-pace account to floor headroom at 0, got %.6f", got)
	}
}

func TestRemainingHeadroom_MultiWindowBottleneckIsMinHeadroom(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	rem := int64(80)
	item := PlanItem{
		Credential: core.Credential{Provider: core.ProviderCodex},
		Remaining:  &rem,
		Windows: []core.QuotaWindow{
			// 5h 窗口：80% 剩余，4h 后重置 -> timeRemainingRatio=4/5=0.8 -> headroom=0.8-0.8=0
			{Name: "5h", Duration: 5 * time.Hour, Remaining: 80, ResetAt: now.Add(4 * time.Hour)},
			// 30d 窗口：80% 剩余，15d 后重置 -> timeRemainingRatio=15/30=0.5 -> headroom=0.8-0.5=0.3
			{Name: "monthly", Duration: 30 * 24 * time.Hour, Remaining: 80, ResetAt: now.Add(15 * 24 * time.Hour)},
		},
	}
	// 瓶颈窗口应取 headroom 更小的那个（5h 窗口，headroom=0），而不是 30d 窗口的 0.3。
	if got := remainingHeadroom(item, now); got != 0 {
		t.Errorf("expected bottleneck window (min headroom) to dominate, got %.6f", got)
	}
}

// --- ensureUniquePriorities：区分"设计内共享 tier priority"与"真正意外冲突" ---

func TestEnsureUniquePriorities_PreservesSharedTierPriority(t *testing.T) {
	remA := int64(50)
	remB := int64(30)
	items := []PlanItem{
		{
			Credential:    core.Credential{AuthIndex: "auth-a"},
			Priority:      100,
			EvidenceFresh: true,
			Remaining:     &remA,
			Reason:        "fresh remaining positive",
		},
		{
			Credential:    core.Credential{AuthIndex: "auth-b"},
			Priority:      100,
			EvidenceFresh: true,
			Remaining:     &remB,
			Reason:        "fresh remaining positive",
		},
	}
	options := Options{Now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), MaxPriority: 100}

	ensureUniquePriorities(items, options)

	if items[0].Priority != 100 || items[1].Priority != 100 {
		t.Errorf("expected both fresh-positive tier members to keep shared priority 100, got %d and %d",
			items[0].Priority, items[1].Priority)
	}
	if items[0].ForceWrite || items[1].ForceWrite {
		t.Errorf("expected tier members untouched by uniqueness pass (no ForceWrite)")
	}
	if items[0].Reason != "fresh remaining positive" || items[1].Reason != "fresh remaining positive" {
		t.Errorf("expected tier members' Reason left unchanged, got %q and %q", items[0].Reason, items[1].Reason)
	}
}

func TestEnsureUniquePriorities_BumpsStaleItemCollidingWithTierSlot(t *testing.T) {
	remTier := int64(50)
	items := []PlanItem{
		{
			Credential:    core.Credential{AuthIndex: "auth-tier"},
			Priority:      100,
			EvidenceFresh: true,
			Remaining:     &remTier,
			Reason:        "fresh remaining positive",
		},
		{
			// 陈旧遗留凭证：本轮无 fresh 证据，但仍占用着与 tier 相同的 priority 槽位。
			Credential: core.Credential{AuthIndex: "auth-stale", Priority: 100},
			Priority:   100,
		},
	}
	options := Options{Now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), MaxPriority: 100}

	ensureUniquePriorities(items, options)

	if items[0].Priority != 100 {
		t.Errorf("expected tier member to keep priority 100, got %d", items[0].Priority)
	}
	if items[1].Priority == 100 {
		t.Errorf("expected stale item colliding with tier slot to be bumped off priority 100, still at %d", items[1].Priority)
	}
	if !items[1].ForceWrite {
		t.Errorf("expected stale item bump to set ForceWrite=true so it gets written back to host")
	}
	if items[1].Reason != "priority uniqueness" {
		t.Errorf("expected stale item Reason 'priority uniqueness', got %q", items[1].Reason)
	}
}

func TestEnsureUniquePriorities_StillCorrectsGenuineNonTierCollision(t *testing.T) {
	remTier := int64(50)
	items := []PlanItem{
		{
			Credential:    core.Credential{AuthIndex: "auth-tier"},
			Priority:      100,
			EvidenceFresh: true,
			Remaining:     &remTier,
			Reason:        "fresh remaining positive",
		},
		{
			Credential: core.Credential{AuthIndex: "auth-p", Priority: 50},
			Priority:   50,
		},
		{
			Credential: core.Credential{AuthIndex: "auth-q", Priority: 50},
			Priority:   50,
		},
	}
	options := Options{Now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), MaxPriority: 100}

	ensureUniquePriorities(items, options)

	if items[0].Priority != 100 {
		t.Errorf("expected tier member unaffected by non-tier collision fix, got %d", items[0].Priority)
	}
	if items[1].Priority == items[2].Priority {
		t.Errorf("expected genuine collision between non-tier stale items to be resolved, both still at %d", items[1].Priority)
	}
	if items[1].Priority >= 100 || items[2].Priority >= 100 {
		t.Errorf("expected reassigned non-tier priorities to stay below the reserved tier slot 100, got %d and %d",
			items[1].Priority, items[2].Priority)
	}
}
