package claude

import (
	"testing"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
)

func TestParseClaudeUsage_StandardSessionLimit(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAtExpected := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "pro",
		"rate_limits": {
			"session_limit": {
				"resets_at": "2026-08-20T15:00:00Z",
				"remaining": 45,
				"limit": 50,
				"used": 5
			}
		}
	}`

	result := ParseClaudeUsage([]byte(payload), nil, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.Provider != core.ProviderClaude {
		t.Errorf("expected ProviderClaude, got %v", result.Provider)
	}
	if result.PlanType != core.PlanTypePro {
		t.Errorf("expected PlanTypePro, got %v", result.PlanType)
	}
	if result.Window != WindowFiveHour {
		t.Errorf("expected WindowFiveHour, got %v", result.Window)
	}
	if result.Remaining == nil || *result.Remaining != 45 {
		t.Errorf("expected remaining 45, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(resetAtExpected) {
		t.Errorf("expected resetAt %v, got %v", resetAtExpected, result.ResetAt)
	}
	if result.ShortWindowRemaining != nil {
		t.Errorf("expected nil ShortWindowRemaining for single-window data, got %v", *result.ShortWindowRemaining)
	}
	if result.ShortWindowResetAt != nil {
		t.Errorf("expected nil ShortWindowResetAt for single-window data, got %v", *result.ShortWindowResetAt)
	}
	if result.LongWindowRemaining != nil {
		t.Errorf("expected nil LongWindowRemaining for single-window data, got %v", *result.LongWindowRemaining)
	}
}

func TestParseClaudeUsage_FiveHourAndWeeklyWindows(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "claude_pro",
		"rate_limits": {
			"five_hour": {
				"resets_at": "2026-08-20T14:00:00Z",
				"remaining": 30,
				"limit": 50
			},
			"weekly": {
				"resets_at": "2026-08-27T00:00:00Z",
				"remaining": 400,
				"limit": 500
			}
		}
	}`

	result := ParseClaudeUsage([]byte(payload), nil, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly, got %v", result.Window)
	}
	if result.Remaining == nil || *result.Remaining != 400 {
		t.Errorf("expected remaining 400, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(weeklyReset) {
		t.Errorf("expected resetAt %v, got %v", weeklyReset, result.ResetAt)
	}
	if result.LongWindowResetAt == nil || !result.LongWindowResetAt.Equal(weeklyReset) {
		t.Errorf("expected LongWindowResetAt %v, got %v", weeklyReset, result.LongWindowResetAt)
	}
	if result.ShortWindowRemaining == nil || *result.ShortWindowRemaining != 30 {
		t.Errorf("expected ShortWindowRemaining 30, got %v", result.ShortWindowRemaining)
	}
	fiveHourReset := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	if result.ShortWindowResetAt == nil || !result.ShortWindowResetAt.Equal(fiveHourReset) {
		t.Errorf("expected ShortWindowResetAt %v, got %v", fiveHourReset, result.ShortWindowResetAt)
	}
	if result.LongWindowRemaining == nil || *result.LongWindowRemaining != 400 {
		t.Errorf("expected LongWindowRemaining 400, got %v", result.LongWindowRemaining)
	}
}

func TestParseClaudeUsage_FiveHourDepleted(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	fiveHourReset := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "pro",
		"rate_limits": {
			"five_hour": {
				"resets_at": "2026-08-20T14:00:00Z",
				"remaining": 0,
				"limit": 50
			},
			"weekly": {
				"resets_at": "2026-08-27T00:00:00Z",
				"remaining": 400,
				"limit": 500
			}
		}
	}`

	result := ParseClaudeUsage([]byte(payload), nil, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(fiveHourReset) {
		t.Errorf("expected resetAt %v, got %v", fiveHourReset, result.ResetAt)
	}
	if result.LongWindowResetAt == nil || !result.LongWindowResetAt.Equal(weeklyReset) {
		t.Errorf("expected LongWindowResetAt %v, got %v", weeklyReset, result.LongWindowResetAt)
	}
}

func TestParseClaudeUsage_WeeklyDepleted(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "pro",
		"rate_limits": {
			"five_hour": {
				"resets_at": "2026-08-20T14:00:00Z",
				"remaining": 20,
				"limit": 50
			},
			"weekly": {
				"resets_at": "2026-08-27T00:00:00Z",
				"remaining": 0,
				"limit": 500
			}
		}
	}`

	result := ParseClaudeUsage([]byte(payload), nil, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly when weekly depleted, got %v", result.Window)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(weeklyReset) {
		t.Errorf("expected resetAt %v, got %v", weeklyReset, result.ResetAt)
	}
}

func TestParseClaudeUsage_V1ModelsResponse(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	payload := `{"data":[{"type":"model","id":"claude-opus-5"},{"type":"model","id":"claude-sonnet-5"}],"has_more":false}`
	headers := host.Header{
		"anthropic-organization-id": []string{"org-model-uuid-999"},
	}

	result := ParseClaudeUsage([]byte(payload), headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.OrganizationUUID != "org-model-uuid-999" {
		t.Errorf("expected org uuid org-model-uuid-999, got %s", result.OrganizationUUID)
	}
	if result.Remaining == nil || *result.Remaining <= 0 {
		t.Errorf("expected positive remaining, got %v", result.Remaining)
	}
}

func TestParseClaudeUsage_OrganizationsArray(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expectedReset := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

	payload := `[
		{
			"uuid": "org-uuid-12345",
			"name": "Personal Org",
			"capabilities": ["claude_pro", "chat"],
			"rate_limits": {
				"session_limit": {
					"resets_at": "2026-08-20T15:00:00Z",
					"remaining": 25,
					"limit": 50
				}
			}
		}
	]`

	result := ParseClaudeUsage([]byte(payload), nil, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.PlanType != core.PlanTypePro {
		t.Errorf("expected PlanTypePro, got %v", result.PlanType)
	}
	if result.OrganizationUUID != "org-uuid-12345" {
		t.Errorf("expected org uuid org-uuid-12345, got %s", result.OrganizationUUID)
	}
	if result.Remaining == nil || *result.Remaining != 25 {
		t.Errorf("expected remaining 25, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expectedReset) {
		t.Errorf("expected resetAt %v, got %v", expectedReset, result.ResetAt)
	}
}

func TestParseClaudeUsage_TeamPlan(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	payload := `{
		"plan_type": "claude_team",
		"session_limit": {
			"resets_at": "2026-08-20T15:00:00Z",
			"remaining_queries": 80,
			"limit": 100
		}
	}`

	result := ParseClaudeUsage([]byte(payload), nil, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.PlanType != core.PlanTypeTeam {
		t.Errorf("expected PlanTypeTeam, got %v", result.PlanType)
	}
	if result.Remaining == nil || *result.Remaining != 80 {
		t.Errorf("expected remaining 80, got %v", result.Remaining)
	}
}

func TestParseClaudeRateLimitError_JSONBody(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expectedReset := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

	payload := `{
		"error": {
			"type": "rate_limit_error",
			"message": "You have reached your 5-hour limit. Please try again later.",
			"resets_at": "2026-08-20T15:00:00Z"
		}
	}`

	result := ParseClaudeRateLimitError([]byte(payload), nil, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expectedReset) {
		t.Errorf("expected resetAt %v, got %v", expectedReset, result.ResetAt)
	}
}

func TestParseClaudeRateLimitError_Headers(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expectedReset := observedAt.Add(120 * time.Second)

	headers := host.Header{
		"Retry-After": []string{"120"},
	}

	result := ParseClaudeRateLimitError(nil, headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expectedReset) {
		t.Errorf("expected resetAt %v, got %v", expectedReset, result.ResetAt)
	}
}

func TestParseClaudeUsage_CorruptOrEmpty(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	resEmpty := ParseClaudeUsage([]byte(""), nil, observedAt)
	if resEmpty.Status != StatusProbeFailed {
		t.Errorf("expected StatusProbeFailed for empty body, got %v", resEmpty.Status)
	}

	resCorrupt := ParseClaudeUsage([]byte("{invalid json"), nil, observedAt)
	if resCorrupt.Status != StatusProbeFailed {
		t.Errorf("expected StatusProbeFailed for corrupt body, got %v", resCorrupt.Status)
	}
}

func TestParseClaudeUsage_UnifiedHeaders_7dAnd5h(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expectedWeeklyReset := time.Unix(1787767200, 0).UTC()

	headers := host.Header{
		"anthropic-ratelimit-unified-5h-status":      []string{"allowed"},
		"anthropic-ratelimit-unified-5h-utilization": []string{"0.0"},
		"anthropic-ratelimit-unified-5h-reset":       []string{"1787627400"},
		"anthropic-ratelimit-unified-7d-status":      []string{"allowed"},
		"anthropic-ratelimit-unified-7d-utilization": []string{"0.49"},
		"anthropic-ratelimit-unified-7d-reset":       []string{"1787767200"},
		"anthropic-ratelimit-unified-status":        []string{"allowed"},
		"anthropic-organization-id":                  []string{"org-test-7d-5h"},
	}

	result := ParseClaudeUsage([]byte(`{"type":"message"}`), headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.Remaining == nil || *result.Remaining != 51 {
		t.Errorf("expected exact remaining 51 (1 - 0.49), got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expectedWeeklyReset) {
		t.Errorf("expected resetAt %v, got %v", expectedWeeklyReset, result.ResetAt)
	}
	if result.LongWindowResetAt == nil || !result.LongWindowResetAt.Equal(expectedWeeklyReset) {
		t.Errorf("expected LongWindowResetAt %v, got %v", expectedWeeklyReset, result.LongWindowResetAt)
	}
	if result.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly, got %v", result.Window)
	}
	if result.OrganizationUUID != "org-test-7d-5h" {
		t.Errorf("expected org-test-7d-5h, got %v", result.OrganizationUUID)
	}
	if result.ShortWindowRemaining == nil || *result.ShortWindowRemaining != 100 {
		t.Errorf("expected ShortWindowRemaining 100, got %v", result.ShortWindowRemaining)
	}
	expected5hReset := time.Unix(1787627400, 0).UTC()
	if result.ShortWindowResetAt == nil || !result.ShortWindowResetAt.Equal(expected5hReset) {
		t.Errorf("expected ShortWindowResetAt %v, got %v", expected5hReset, result.ShortWindowResetAt)
	}
	if result.LongWindowRemaining == nil || *result.LongWindowRemaining != 51 {
		t.Errorf("expected LongWindowRemaining 51, got %v", result.LongWindowRemaining)
	}
}

func TestParseClaudeUsage_UnifiedHeaders_5hDepleted(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expected5hReset := time.Unix(1787627400, 0).UTC()
	expectedWeeklyReset := time.Unix(1787767200, 0).UTC()

	headers := host.Header{
		"anthropic-ratelimit-unified-5h-status":      []string{"rejected"},
		"anthropic-ratelimit-unified-5h-utilization": []string{"1.0"},
		"anthropic-ratelimit-unified-5h-reset":       []string{"1787627400"},
		"anthropic-ratelimit-unified-7d-status":      []string{"allowed"},
		"anthropic-ratelimit-unified-7d-utilization": []string{"0.20"},
		"anthropic-ratelimit-unified-7d-reset":       []string{"1787767200"},
		"anthropic-ratelimit-unified-status":        []string{"rejected"},
	}

	result := ParseClaudeUsage([]byte(`{"type":"error"}`), headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expected5hReset) {
		t.Errorf("expected resetAt %v, got %v", expected5hReset, result.ResetAt)
	}
	if result.Window != WindowFiveHour {
		t.Errorf("expected WindowFiveHour, got %v", result.Window)
	}
	if result.LongWindowResetAt == nil || !result.LongWindowResetAt.Equal(expectedWeeklyReset) {
		t.Errorf("expected LongWindowResetAt %v, got %v", expectedWeeklyReset, result.LongWindowResetAt)
	}
	if result.ShortWindowRemaining != nil {
		t.Errorf("expected nil ShortWindowRemaining for rejected single-window branch, got %v", *result.ShortWindowRemaining)
	}
	if result.LongWindowRemaining != nil {
		t.Errorf("expected nil LongWindowRemaining for rejected single-window branch, got %v", *result.LongWindowRemaining)
	}
}

func TestParseClaudeUsage_UnifiedHeaders_7dDepleted(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expectedWeeklyReset := time.Unix(1787767200, 0).UTC()

	headers := host.Header{
		"anthropic-ratelimit-unified-7d-status":      []string{"rejected"},
		"anthropic-ratelimit-unified-7d-utilization": []string{"1.0"},
		"anthropic-ratelimit-unified-7d-reset":       []string{"1787767200"},
		"anthropic-ratelimit-unified-status":        []string{"rejected"},
	}

	result := ParseClaudeUsage([]byte(`{}`), headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expectedWeeklyReset) {
		t.Errorf("expected resetAt %v, got %v", expectedWeeklyReset, result.ResetAt)
	}
	if result.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly, got %v", result.Window)
	}
}

func TestParseClaudeRateLimitError_UnifiedHeaders(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expected5hReset := time.Unix(1787627400, 0).UTC()

	headers := host.Header{
		"anthropic-ratelimit-unified-5h-status":      []string{"rejected"},
		"anthropic-ratelimit-unified-5h-utilization": []string{"1.0"},
		"anthropic-ratelimit-unified-5h-reset":       []string{"1787627400"},
		"anthropic-ratelimit-unified-status":        []string{"rejected"},
	}

	result := ParseClaudeRateLimitError([]byte(`{"type":"error"}`), headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expected5hReset) {
		t.Errorf("expected resetAt %v, got %v", expected5hReset, result.ResetAt)
	}
	if result.Window != WindowFiveHour {
		t.Errorf("expected WindowFiveHour, got %v", result.Window)
	}
}
