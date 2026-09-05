package apply

import (
	"context"
	"testing"

	"quota-pacer/internal/core"
	"quota-pacer/internal/priority"
)

type mockHost struct {
	patchedPriority map[string]int
	patchedWeight   map[string]int
	patchedDisabled map[string]bool
}

func (m *mockHost) PatchPriority(ctx context.Context, authIndex string, p int) error {
	m.patchedPriority[authIndex] = p
	return nil
}

func (m *mockHost) PatchWeight(ctx context.Context, authIndex string, weight int) error {
	m.patchedWeight[authIndex] = weight
	return nil
}

func (m *mockHost) PatchDisabled(ctx context.Context, name string, disabled bool) error {
	m.patchedDisabled[name] = disabled
	return nil
}

type mockAuditor struct{}

func (mockAuditor) SaveSnapshot(ctx context.Context, snapshot PlanSnapshot) error {
	return nil
}

func (mockAuditor) RecordEvent(ctx context.Context, event AuditEvent) error {
	return nil
}

func TestApply_Success(t *testing.T) {
	h := &mockHost{
		patchedPriority: map[string]int{},
		patchedWeight:   map[string]int{},
		patchedDisabled: map[string]bool{},
	}
	aud := mockAuditor{}

	plan := priority.Plan{
		Items: []priority.PlanItem{
			{
				Credential: core.Credential{
					Name:      "c1",
					AuthIndex: "auth-1",
					Provider:  core.ProviderClaude,
					Priority:  10,
					Disabled:  false,
				},
				Priority:      100,
				Disabled:      false,
				EvidenceFresh: true,
			},
		},
		Changes: []priority.Change{
			{
				Credential: core.Credential{
					Name:      "c1",
					AuthIndex: "auth-1",
					Provider:  core.ProviderClaude,
					Priority:  10,
					Disabled:  false,
				},
				Priority:      100,
				Disabled:      false,
				EvidenceFresh: true,
				Reason:        "fresh remaining positive",
			},
		},
	}

	result, err := Apply(context.Background(), Request{
		Host:              h,
		Auditor:           aud,
		Plan:              plan,
		ReportSkippedPlan: true,
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if result.Succeeded != 1 {
		t.Errorf("expected 1 succeeded, got %d", result.Succeeded)
	}
	if h.patchedPriority["auth-1"] != 100 {
		t.Errorf("expected priority patched to 100, got %d", h.patchedPriority["auth-1"])
	}
}

// TestApply_OverPaceAccountStillPatchesFloorWeight 覆盖用户明确要求的场景：一个已经落后于配速
// 目标（remainingHeadroom floor 到 0，weight floor 到 1）的共享 tier 成员，priority/disabled
// 均无变化，仍必须触发 PatchWeight(weight=1) 写回，而不能因为"没有其它字段变化"被整体跳过——
// weight=1 不是 0，CPA WeightedRoundRobinSelector 的 positiveWeightAuths 过滤器只排除
// weight<=0 的凭证，weight=1 必须继续参与轮转。
func TestApply_OverPaceAccountStillPatchesFloorWeight(t *testing.T) {
	h := &mockHost{
		patchedPriority: map[string]int{},
		patchedWeight:   map[string]int{},
		patchedDisabled: map[string]bool{},
	}
	aud := mockAuditor{}

	credential := core.Credential{
		Name:      "c-over-pace",
		AuthIndex: "auth-over-pace",
		Provider:  core.ProviderClaude,
		Priority:  100,
		Disabled:  false,
	}
	plan := priority.Plan{
		Items: []priority.PlanItem{
			{
				Credential:    credential,
				Priority:      100,
				Weight:        1,
				Disabled:      false,
				EvidenceFresh: true,
				Reason:        "fresh remaining positive",
			},
		},
		Changes: []priority.Change{
			{
				Credential:    credential,
				Priority:      100,
				Weight:        1,
				Disabled:      false,
				EvidenceFresh: true,
				Reason:        "fresh remaining positive",
			},
		},
	}

	result, err := Apply(context.Background(), Request{
		Host:    h,
		Auditor: aud,
		Plan:    plan,
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if result.Succeeded != 1 {
		t.Errorf("expected 1 succeeded, got %d (result=%+v)", result.Succeeded, result)
	}
	if !result.Changes[0].WeightAttempted {
		t.Errorf("expected WeightAttempted=true for a tier member with weight=1 (floor); it must not be treated as unchanged/skipped")
	}
	weight, patched := h.patchedWeight["auth-over-pace"]
	if !patched {
		t.Fatalf("expected PatchWeight to be called for the over-pace account, but it was never invoked")
	}
	if weight != 1 {
		t.Errorf("expected patched weight 1 (floor, not 0 — weight<=0 would be excluded from CPA's weighted rotation), got %d", weight)
	}
}
