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
	// 对齐 Claude/Antigravity 宏观口径：主窗口为 Weekly
	if result.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly, got %v", result.Window)
	}
	// Weekly remaining 百分比: (450/500)*100 = 90%
	if result.Remaining == nil || *result.Remaining != 90 {
		t.Errorf("expected remaining 90, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(weeklyReset) {
		t.Errorf("expected resetAt %v, got %v", weeklyReset, result.ResetAt)
	}
	if result.LongWindowResetAt == nil || !result.LongWindowResetAt.Equal(weeklyReset) {
		t.Errorf("expected LongWindowResetAt %v, got %v", weeklyReset, result.LongWindowResetAt)
	}
	// 5h remaining 百分比: (40/50)*100 = 80%
	if result.ShortWindowRemaining == nil || *result.ShortWindowRemaining != 80 {
		t.Errorf("expected ShortWindowRemaining 80, got %v", result.ShortWindowRemaining)
	}
	if result.ShortWindowResetAt == nil || !result.ShortWindowResetAt.Equal(fiveHourReset) {
		t.Errorf("expected ShortWindowResetAt %v, got %v", fiveHourReset, result.ShortWindowResetAt)
	}
	if result.LongWindowRemaining == nil || *result.LongWindowRemaining != 90 {
		t.Errorf("expected LongWindowRemaining 90, got %v", result.LongWindowRemaining)
	}
	if len(result.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(result.Windows))
	}
	if result.Windows[0].Name != "5h" || result.Windows[0].Remaining != 80 || !result.Windows[0].ResetAt.Equal(fiveHourReset) {
		t.Errorf("unexpected window[0]: %+v", result.Windows[0])
	}
	if result.Windows[1].Name != "weekly" || result.Windows[1].Remaining != 90 || !result.Windows[1].ResetAt.Equal(weeklyReset) {
		t.Errorf("unexpected window[1]: %+v", result.Windows[1])
	}
}

func TestParseWhamUsage_PaidWeeklyOnly(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window": {
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
	if result.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly, got %v", result.Window)
	}
	if result.Remaining == nil || *result.Remaining != 90 {
		t.Errorf("expected remaining 90, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(weeklyReset) {
		t.Errorf("expected resetAt %v, got %v", weeklyReset, result.ResetAt)
	}
	if result.LongWindowResetAt == nil || !result.LongWindowResetAt.Equal(weeklyReset) {
		t.Errorf("expected LongWindowResetAt %v, got %v", weeklyReset, result.LongWindowResetAt)
	}
	if result.ShortWindowRemaining != nil {
		t.Errorf("expected ShortWindowRemaining nil, got %v", *result.ShortWindowRemaining)
	}
	if result.ShortWindowResetAt != nil {
		t.Errorf("expected ShortWindowResetAt nil, got %v", *result.ShortWindowResetAt)
	}
	if result.LongWindowRemaining == nil || *result.LongWindowRemaining != 90 {
		t.Errorf("expected LongWindowRemaining 90, got %v", result.LongWindowRemaining)
	}
}

func TestParseWhamUsage_Paid5hOnly(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	fiveHourReset := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "pro",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 18000,
				"reset_at": "2026-08-20T14:00:00Z",
				"remaining": 40,
				"limit": 50
			}
		}
	}`

	result := ParseWhamUsage([]byte(payload), observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.Window != WindowFiveHour {
		t.Errorf("expected WindowFiveHour, got %v", result.Window)
	}
	if result.Remaining == nil || *result.Remaining != 80 {
		t.Errorf("expected remaining 80, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(fiveHourReset) {
		t.Errorf("expected resetAt %v, got %v", fiveHourReset, result.ResetAt)
	}
	if result.ShortWindowRemaining == nil || *result.ShortWindowRemaining != 80 {
		t.Errorf("expected ShortWindowRemaining 80, got %v", result.ShortWindowRemaining)
	}
	if result.ShortWindowResetAt == nil || !result.ShortWindowResetAt.Equal(fiveHourReset) {
		t.Errorf("expected ShortWindowResetAt %v, got %v", fiveHourReset, result.ShortWindowResetAt)
	}
	if result.LongWindowRemaining != nil {
		t.Errorf("expected LongWindowRemaining nil, got %v", result.LongWindowRemaining)
	}
	if result.LongWindowResetAt != nil {
		t.Errorf("expected LongWindowResetAt nil, got %v", result.LongWindowResetAt)
	}
}

func TestParseWhamUsage_UsedPercentFormat(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 604800,
				"reset_at": "2026-08-27T00:00:00Z",
				"used_percent": 25.4
			}
		}
	}`

	result := ParseWhamUsage([]byte(payload), observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	// 100 - 25.4 = 74.6 -> ceil -> 75%
	if result.Remaining == nil || *result.Remaining != 75 {
		t.Errorf("expected remaining 75, got %v", result.Remaining)
	}
}

func TestParseWhamUsage_UsedWithLimit(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 604800,
				"reset_at": "2026-08-27T00:00:00Z",
				"used": 15,
				"limit": 100
			}
		}
	}`

	result := ParseWhamUsage([]byte(payload), observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	// (100 - 15) / 100 * 100 = 85%
	if result.Remaining == nil || *result.Remaining != 85 {
		t.Errorf("expected remaining 85, got %v", result.Remaining)
	}
}

func TestParseWhamUsage_FractionRemaining(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 604800,
				"reset_at": "2026-08-27T00:00:00Z",
				"remaining": 0.473
			}
		}
	}`

	result := ParseWhamUsage([]byte(payload), observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	// 0.473 * 100 = 47.3 -> ceil -> 48%
	if result.Remaining == nil || *result.Remaining != 48 {
		t.Errorf("expected remaining 48, got %v", result.Remaining)
	}
}

func TestParseWhamUsage_LimitReached(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 604800,
				"reset_at": "2026-08-27T00:00:00Z",
				"limit_reached": true,
				"remaining": 50,
				"limit": 500
			}
		}
	}`

	result := ParseWhamUsage([]byte(payload), observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
}

func TestParseWhamUsage_DepletedWindows(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	fiveHourReset := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	// 1. 5h 耗尽，weekly 未耗尽
	fiveDepletedPayload := `{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window": {
				"limit_window_seconds": 18000,
				"reset_at": "2026-08-20T14:00:00Z",
				"remaining": 0,
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

	r1 := ParseWhamUsage([]byte(fiveDepletedPayload), observedAt)
	if r1.Window != WindowFiveHour {
		t.Errorf("expected WindowFiveHour on 5h depletion, got %v", r1.Window)
	}
	if r1.Remaining == nil || *r1.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", r1.Remaining)
	}
	if r1.ResetAt == nil || !r1.ResetAt.Equal(fiveHourReset) {
		t.Errorf("expected resetAt fiveHourReset, got %v", r1.ResetAt)
	}
	if r1.LongWindowResetAt == nil || !r1.LongWindowResetAt.Equal(weeklyReset) {
		t.Errorf("expected LongWindowResetAt weeklyReset, got %v", r1.LongWindowResetAt)
	}

	// 2. weekly 耗尽
	weeklyDepletedPayload := `{
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
				"remaining": 0,
				"limit": 500
			}
		}
	}`

	r2 := ParseWhamUsage([]byte(weeklyDepletedPayload), observedAt)
	if r2.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly on weekly depletion, got %v", r2.Window)
	}
	if r2.Remaining == nil || *r2.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", r2.Remaining)
	}
	if r2.ResetAt == nil || !r2.ResetAt.Equal(weeklyReset) {
		t.Errorf("expected resetAt weeklyReset, got %v", r2.ResetAt)
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
	// 10 / 10 * 100 = 100%
	if result.Remaining == nil || *result.Remaining != 100 {
		t.Errorf("expected remaining 100, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(monthlyReset) {
		t.Errorf("expected resetAt %v, got %v", monthlyReset, result.ResetAt)
	}
	if result.ShortWindowRemaining != nil {
		t.Errorf("expected ShortWindowRemaining nil, got %v", *result.ShortWindowRemaining)
	}
	if result.ShortWindowResetAt != nil {
		t.Errorf("expected ShortWindowResetAt nil, got %v", *result.ShortWindowResetAt)
	}
	if result.LongWindowRemaining == nil || *result.LongWindowRemaining != 100 {
		t.Errorf("expected LongWindowRemaining 100, got %v", result.LongWindowRemaining)
	}
}

