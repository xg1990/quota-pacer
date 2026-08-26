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
