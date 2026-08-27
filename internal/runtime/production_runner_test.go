package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"quota-pacer/internal/apply"
	"quota-pacer/internal/config"
	"quota-pacer/internal/core"
	"quota-pacer/internal/priority"
	"quota-pacer/internal/schedule"
	"quota-pacer/internal/state"
)

type recordingApplyHost struct {
	priorityWrites int
	disabledWrites int
}

func (h *recordingApplyHost) PatchPriority(_ context.Context, _ string, _ int) error {
	h.priorityWrites++
	return nil
}

func (h *recordingApplyHost) PatchDisabled(_ context.Context, _ string, _ bool) error {
	h.disabledWrites++
	return nil
}

type recordingApplyAuditor struct{}

func (recordingApplyAuditor) SaveSnapshot(context.Context, apply.PlanSnapshot) error { return nil }
func (recordingApplyAuditor) RecordEvent(context.Context, apply.AuditEvent) error     { return nil }

func TestPreserveProbeFailureState(t *testing.T) {
	credential := core.Credential{
		AuthIndex: "claude-auth",
		Provider:  core.ProviderClaude,
		Priority:  90,
		Disabled:  false,
	}
	plan := priority.Plan{
		Items: []priority.PlanItem{{
			Credential:    credential,
			Priority:      999,
			Disabled:      true,
			EvidenceFresh: true,
			ForceWrite:    true,
			Reason:        "provider priority uniqueness",
		}},
		Changes: []priority.Change{{
			Credential:    credential,
			Priority:      999,
			Disabled:      true,
			EvidenceFresh: true,
			Reason:        "provider priority uniqueness",
		}},
	}
	evidence := []priority.ProbeEvidence{{
		Provider:  core.ProviderClaude,
		AuthIndex: credential.AuthIndex,
		Status:    priority.EvidenceStatusProbeFailed,
	}}

	plan = preserveProbeFailureState(plan, evidence)
	item := plan.Items[0]
	if item.Priority != credential.Priority || item.Disabled != credential.Disabled {
		t.Fatalf("state = priority %d disabled %t, want priority %d disabled %t", item.Priority, item.Disabled, credential.Priority, credential.Disabled)
	}
	if item.EvidenceFresh || item.ForceWrite {
		t.Fatal("probe failure must not retain write eligibility")
	}
	if item.Reason != "failedQuotaFetch" {
		t.Fatalf("reason = %q, want failedQuotaFetch", item.Reason)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("changes = %d, want 0", len(plan.Changes))
	}

	host := &recordingApplyHost{}
	result, err := apply.Apply(context.Background(), apply.Request{
		Host:    host,
		Auditor: recordingApplyAuditor{},
		Plan:    plan,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Attempted != 0 || host.priorityWrites != 0 || host.disabledWrites != 0 {
		t.Fatalf("unexpected write: result=%+v priorityWrites=%d disabledWrites=%d", result, host.priorityWrites, host.disabledWrites)
	}
}

func TestPreserveProbeFailureStateLeavesTrustedQuotaChange(t *testing.T) {
	failedCredential := core.Credential{AuthIndex: "claude-failed", Provider: core.ProviderClaude, Priority: 90}
	trustedCredential := core.Credential{AuthIndex: "claude-depleted", Provider: core.ProviderClaude, Priority: 90}
	plan := priority.Plan{
		Items: []priority.PlanItem{
			{Credential: failedCredential, Priority: 999, Disabled: true, EvidenceFresh: true, ForceWrite: true},
			{Credential: trustedCredential, Priority: -1, Disabled: true, EvidenceFresh: true, Reason: "fresh remaining depleted"},
		},
		Changes: []priority.Change{
			{Credential: failedCredential, Priority: 999, Disabled: true, EvidenceFresh: true},
			{Credential: trustedCredential, Priority: -1, Disabled: true, EvidenceFresh: true, Reason: "fresh remaining depleted"},
		},
	}
	evidence := []priority.ProbeEvidence{
		{Provider: core.ProviderClaude, AuthIndex: failedCredential.AuthIndex, Status: priority.EvidenceStatusProbeFailed},
		{Provider: core.ProviderClaude, AuthIndex: trustedCredential.AuthIndex, Status: priority.EvidenceStatusReady, EvidenceFresh: true},
	}

	plan = preserveProbeFailureState(plan, evidence)
	if got := plan.Items[0]; got.Priority != 90 || got.Disabled || got.EvidenceFresh || got.ForceWrite {
		t.Fatalf("failed item = %+v, want preserved state", got)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Credential.AuthIndex != trustedCredential.AuthIndex {
		t.Fatalf("changes = %+v, want only trusted quota change", plan.Changes)
	}
}

// TestDueProbesAppliesProviderPolicyTTL 回归覆盖：dueProbes（自动定时排序路径）必须把
// probePolicyForProvider 算出的真实 TTL/ResetStaleAfter 传给 NeedsProbe，而不是空值 ProbePolicy{}。
// 空值会让 isTTLExpired 恒为 false，退化为只看 NextProbeAt——而成功/失败探测都会把
// NextProbeAt 设为 1 小时后，导致自动路径上的凭证探测一次后进入 1 小时黑窗期，
// evidence 永远无法在 15 分钟 TTL 内刷新为 fresh（历史上曾导致 PacingScore 恒为 0）。
func TestDueProbesAppliesProviderPolicyTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	credential := core.Credential{AuthIndex: "claude-ttl-check", Provider: core.ProviderClaude}

	store, err := state.Load(ctx, filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatalf("state.Load() error = %v", err)
	}
	// 20 分钟前探测成功：超过 15 分钟 TTL，但 NextProbeAt（观测时刻+1h）还没到。
	observedAt := now.Add(-20 * time.Minute)
	if err := store.MarkProbeSuccess(ctx, state.ProbeSuccess{
		AuthIndex:   credential.AuthIndex,
		Provider:    credential.Provider,
		ObservedAt:  observedAt,
		Remaining:   100,
		Source:      state.SourceFreshProbe,
		NextProbeAt: observedAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("MarkProbeSuccess() error = %v", err)
	}

	plan := schedule.Plan{Immediate: []schedule.Probe{{Credential: credential, NextProbeAt: now}}}
	probes, err := dueProbes(ctx, store, plan, now, config.AntigravityModelGroup(""), defaultProbeCacheTTL)
	if err != nil {
		t.Fatalf("dueProbes() error = %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("dueProbes() = %d probes, want 1 (15m TTL must force re-probe even though NextProbeAt has not elapsed)", len(probes))
	}
}

func TestAttachCachedEvidence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	reset5h := now.Add(4 * time.Hour)
	reset7d := now.Add(6 * 24 * time.Hour)
	shortRem := int64(88)
	longRem := int64(95)

	store, err := state.Load(ctx, filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatalf("state.Load() error = %v", err)
	}

	cred1 := core.Credential{AuthIndex: "auth-fresh", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex}
	cred2 := core.Credential{AuthIndex: "auth-cached", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex, PlanType: core.PlanTypePlus}

	// Seed store with cached valid entry for cred2
	if err := store.MarkProbeSuccess(ctx, state.ProbeSuccess{
		AuthIndex:            cred2.AuthIndex,
		Provider:             cred2.Provider,
		ObservedAt:           now.Add(-5 * time.Minute),
		ResetAt:              reset5h,
		Remaining:            88,
		Source:               state.SourceFreshProbe,
		NextProbeAt:          now.Add(55 * time.Minute),
		PlanType:             core.PlanTypePlus,
		ShortWindowRemaining: &shortRem,
		ShortWindowResetAt:   reset5h,
		LongWindowRemaining:  &longRem,
		LongWindowResetAt:    reset7d,
	}); err != nil {
		t.Fatalf("MarkProbeSuccess error: %v", err)
	}

	freshRem := int64(99)
	existingEvidence := []priority.ProbeEvidence{
		{
			Provider:      core.ProviderCodex,
			AuthIndex:     cred1.AuthIndex,
			ObservedAt:    now,
			ResetAt:       &reset5h,
			Remaining:     &freshRem,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        priority.EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
	}

	merged := attachCachedEvidence([]core.Credential{cred1, cred2}, existingEvidence, store, "", now)
	if len(merged) != 2 {
		t.Fatalf("expected 2 evidences, got %d", len(merged))
	}

	// cred1 evidence is unchanged
	if merged[0].AuthIndex != "auth-fresh" || !merged[0].EvidenceFresh {
		t.Errorf("expected fresh evidence preserved, got %+v", merged[0])
	}

	// cred2 evidence is synthesized with EvidenceFresh=false
	cached := merged[1]
	if cached.AuthIndex != "auth-cached" {
		t.Errorf("expected auth-cached, got %s", cached.AuthIndex)
	}
	if cached.EvidenceFresh {
		t.Errorf("expected EvidenceFresh=false for synthesized cached evidence")
	}
	if cached.Remaining == nil || *cached.Remaining != 88 {
		t.Errorf("expected Remaining=88, got %v", cached.Remaining)
	}
	if cached.ShortWindowRemaining == nil || *cached.ShortWindowRemaining != 88 {
		t.Errorf("expected ShortWindowRemaining=88, got %v", cached.ShortWindowRemaining)
	}
	if cached.LongWindowRemaining == nil || *cached.LongWindowRemaining != 95 {
		t.Errorf("expected LongWindowRemaining=95, got %v", cached.LongWindowRemaining)
	}
	if cached.PlanType != core.PlanTypePlus {
		t.Errorf("expected PlanType plus, got %s", cached.PlanType)
	}
}

func TestAttachCachedEvidence_SynthesizesFromStore(t *testing.T) {
	ctx := context.Background()
	store, err := state.Load(ctx, filepath.Join(t.TempDir(), "refresh-cache.json"))
	if err != nil {
		t.Fatalf("state.Load() error = %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	reset5h := now.Add(4 * time.Hour)
	reset7d := now.Add(6 * 24 * time.Hour)
	shortRem := int64(85)
	longRem := int64(70)

	// Populate cached entry for codex-1 in store
	err = store.MarkProbeSuccess(ctx, state.ProbeSuccess{
		AuthIndex:            "codex-1",
		Provider:             core.ProviderCodex,
		ObservedAt:           now.Add(-5 * time.Minute),
		ResetAt:              reset5h,
		Remaining:            85,
		Source:               state.SourceFreshProbe,
		NextProbeAt:          now.Add(55 * time.Minute),
		PlanType:             core.PlanTypePlus,
		ShortWindowRemaining: &shortRem,
		ShortWindowResetAt:   reset5h,
		LongWindowRemaining:  &longRem,
		LongWindowResetAt:    reset7d,
	})
	if err != nil {
		t.Fatalf("MarkProbeSuccess error = %v", err)
	}

	credentials := []core.Credential{
		{AuthIndex: "codex-1", Provider: core.ProviderCodex, Type: core.CredentialTypeCodex, PlanType: core.PlanTypePlus},
		{AuthIndex: "claude-2", Provider: core.ProviderClaude, Type: core.CredentialTypeClaude, PlanType: core.PlanTypePro},
	}

	claudeRem := int64(95)
	claudeReset := now.Add(2 * time.Hour)
	freshEvidence := []priority.ProbeEvidence{
		{
			Provider:      core.ProviderClaude,
			AuthIndex:     "claude-2",
			ObservedAt:    now,
			ResetAt:       &claudeReset,
			Remaining:     &claudeRem,
			Freshness:     core.FreshnessFresh,
			ProbeStatus:   core.ProbeStatusReady,
			Status:        priority.EvidenceStatusReady,
			PlanType:      core.PlanTypePro,
			EvidenceFresh: true,
		},
	}

	merged := attachCachedEvidence(credentials, freshEvidence, store, config.AntigravityModelGroup(""), now)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged evidence items, got %d", len(merged))
	}

	var codexEv, claudeEv priority.ProbeEvidence
	for _, ev := range merged {
		if ev.AuthIndex == "codex-1" {
			codexEv = ev
		} else if ev.AuthIndex == "claude-2" {
			claudeEv = ev
		}
	}

	if !claudeEv.EvidenceFresh {
		t.Errorf("expected claudeEv.EvidenceFresh=true")
	}
	if codexEv.EvidenceFresh {
		t.Errorf("expected codexEv.EvidenceFresh=false")
	}
	if codexEv.Remaining == nil || *codexEv.Remaining != 85 {
		t.Errorf("expected codexEv.Remaining=85, got %v", codexEv.Remaining)
	}
	if codexEv.ShortWindowRemaining == nil || *codexEv.ShortWindowRemaining != 85 {
		t.Errorf("expected codexEv.ShortWindowRemaining=85, got %v", codexEv.ShortWindowRemaining)
	}
	if codexEv.LongWindowRemaining == nil || *codexEv.LongWindowRemaining != 70 {
		t.Errorf("expected codexEv.LongWindowRemaining=70, got %v", codexEv.LongWindowRemaining)
	}
	if codexEv.PlanType != core.PlanTypePlus {
		t.Errorf("expected codexEv.PlanType=plus, got %s", codexEv.PlanType)
	}
}
