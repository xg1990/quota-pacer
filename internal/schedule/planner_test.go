package schedule

import (
	"testing"
	"time"

	"quota-pacer/internal/core"
)

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

type fixedRNG struct{}

func (fixedRNG) Int63n(int64) int64 {
	return 0
}

func TestPlanProbeSchedule_ImmediateOnly(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	credentials := []core.Credential{
		{Name: "c1", AuthIndex: "a1", Provider: core.ProviderClaude, Priority: 100},
		{Name: "c2", AuthIndex: "a2", Provider: core.ProviderClaude, Priority: 90},
	}

	options := Options{
		Clock:                 fixedClock{now: now},
		RNG:                   fixedRNG{},
		ImmediateProbeLimit:   30,
		TopPriorityProbeCount: 10,
		ActiveGroupSize:       10,
		ActiveGroupJitter:     0,
		Interval:              15 * time.Minute,
	}

	plan, err := PlanProbeSchedule(credentials, options)
	if err != nil {
		t.Fatalf("PlanProbeSchedule failed: %v", err)
	}

	if len(plan.Immediate) != 2 {
		t.Errorf("expected 2 immediate probes, got %d", len(plan.Immediate))
	}
	if len(plan.ActiveGroups) != 0 {
		t.Errorf("expected 0 active groups, got %d", len(plan.ActiveGroups))
	}
	if len(plan.DisabledGroups) != 0 {
		t.Errorf("expected 0 disabled groups, got %d", len(plan.DisabledGroups))
	}
}

func TestPlanProbeSchedule_ActiveAndDisabledBatching(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	credentials := make([]core.Credential, 15)
	for i := 0; i < 12; i++ {
		credentials[i] = core.Credential{
			Name:      "active",
			AuthIndex: string(rune('a' + i)),
			Provider:  core.ProviderClaude,
			Priority:  100 - i,
			Disabled:  false,
		}
	}
	for i := 12; i < 15; i++ {
		credentials[i] = core.Credential{
			Name:      "disabled",
			AuthIndex: string(rune('a' + i)),
			Provider:  core.ProviderClaude,
			Priority:  -1,
			Disabled:  true,
		}
	}

	options := Options{
		Clock:                 fixedClock{now: now},
		RNG:                   fixedRNG{},
		ImmediateProbeLimit:   5, // lower than total count (15)
		TopPriorityProbeCount: 5,
		ActiveGroupSize:       5,
		ActiveGroupJitter:     0,
		Interval:              15 * time.Minute,
	}

	plan, err := PlanProbeSchedule(credentials, options)
	if err != nil {
		t.Fatalf("PlanProbeSchedule failed: %v", err)
	}

	if len(plan.Immediate) != 5 {
		t.Errorf("expected 5 immediate probes, got %d", len(plan.Immediate))
	}
	if len(plan.ActiveGroups) == 0 {
		t.Errorf("expected active groups for remaining active credentials")
	}
	if len(plan.DisabledGroups) == 0 {
		t.Errorf("expected disabled groups for disabled credentials")
	}
}
