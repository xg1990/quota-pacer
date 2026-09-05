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
