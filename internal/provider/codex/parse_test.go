package codex

import (
	"testing"
	"time"

	"quota-pacer/internal/core"
)

func TestParseWhamUsage_Paid5hAndWeekly(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	fiveHourReset := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 18000,
				"reset_at": "2026-08-20T14:00:00Z",
				"remaining": 40,
				"limit": 50
			},
			"secondary_window": {
				"limit_window_seconds": 604800,
				"reset_at": "2026-08-27T00:00:00Z",
				"remaining": 450,
				"limit": 500
			}
		}
	}`

	result := ParseWhamUsage([]byte(payload), observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.PlanType != core.PlanTypePlus {
		t.Errorf("expected PlanTypePlus, got %v", result.PlanType)
	}
	if result.Window != WindowFiveHour {
		t.Errorf("expected WindowFiveHour, got %v", result.Window)
	}
	if result.Remaining == nil || *result.Remaining != 40 {
		t.Errorf("expected remaining 40, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(fiveHourReset) {
		t.Errorf("expected resetAt %v, got %v", fiveHourReset, result.ResetAt)
	}
	if result.LongWindowResetAt == nil || !result.LongWindowResetAt.Equal(weeklyReset) {
		t.Errorf("expected LongWindowResetAt %v, got %v", weeklyReset, result.LongWindowResetAt)
	}
}

func TestParseWhamUsage_FreeMonthly(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	monthlyReset := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "free",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 2592000,
				"reset_at": "2026-09-01T00:00:00Z",
				"remaining": 10,
				"limit": 10
			}
		}
	}`

	result := ParseWhamUsage([]byte(payload), observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.PlanType != core.PlanTypeFree {
		t.Errorf("expected PlanTypeFree, got %v", result.PlanType)
	}
	if result.Window != WindowMonthly {
		t.Errorf("expected WindowMonthly, got %v", result.Window)
	}
	if result.Remaining == nil || *result.Remaining != 10 {
		t.Errorf("expected remaining 10, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(monthlyReset) {
		t.Errorf("expected resetAt %v, got %v", monthlyReset, result.ResetAt)
	}
}
