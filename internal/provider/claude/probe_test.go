package claude

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
)

type mockHostDoer struct {
	handler func(req host.HTTPRequest) (host.HTTPResponse, error)
}

func (m mockHostDoer) HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	return m.HTTPDoRaw(ctx, req)
}

func (m mockHostDoer) HTTPDoRaw(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	if m.handler != nil {
		return m.handler(req)
	}
	return host.HTTPResponse{StatusCode: http.StatusOK}, nil
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func TestProber_Probe_V1ModelsDefaultSuccess(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	modelsJSON := `{"data":[{"type":"model","id":"claude-opus-5"},{"type":"model","id":"claude-sonnet-5"}],"has_more":false}`

	mock := mockHostDoer{
		handler: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			if strings.HasSuffix(req.URL, "/v1/models") {
				return host.HTTPResponse{
					StatusCode: http.StatusOK,
					Headers: host.Header{
						"anthropic-organization-id": []string{"org-model-123"},
					},
					Body: []byte(modelsJSON),
				}, nil
			}
			return host.HTTPResponse{StatusCode: http.StatusNotFound}, nil
		},
	}

	prober := NewProber(mock, fixedClock{now: now})
	result := prober.Probe(context.Background(), ProbeRequest{
		Provider:    core.ProviderClaude,
		AuthIndex:   "auth_claude_models",
		AccessToken: "test-token",
	})

	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.OrganizationUUID != "org-model-123" {
		t.Errorf("expected org-model-123, got %v", result.OrganizationUUID)
	}
	if result.Remaining == nil || *result.Remaining <= 0 {
		t.Errorf("expected positive remaining, got %v", result.Remaining)
	}
}

func TestProber_Probe_DirectOrgSuccess(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

	usageJSON := `{
		"plan_type": "pro",
		"rate_limits": {
			"session_limit": {
				"resets_at": "2026-08-20T15:00:00Z",
				"remaining": 40,
				"limit": 50
			}
		}
	}`

	mock := mockHostDoer{
		handler: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			if strings.Contains(req.URL, "/organizations/org-xyz-123/usage") {
				return host.HTTPResponse{
					StatusCode: http.StatusOK,
					Body:       []byte(usageJSON),
				}, nil
			}
			return host.HTTPResponse{StatusCode: http.StatusNotFound}, nil
		},
	}

	prober := NewProber(mock, fixedClock{now: now})
	result := prober.Probe(context.Background(), ProbeRequest{
		Provider:         core.ProviderClaude,
		AuthIndex:        "auth_claude_1",
		AccessToken:      "test-token",
		OrganizationUUID: "org-xyz-123",
		BaseURL:          DefaultWebBaseURL,
	})

	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.Provider != core.ProviderClaude {
		t.Errorf("expected ProviderClaude, got %v", result.Provider)
	}
	if result.AuthIndex != "auth_claude_1" {
		t.Errorf("expected auth_claude_1, got %v", result.AuthIndex)
	}
	if result.OrganizationUUID != "org-xyz-123" {
		t.Errorf("expected org-xyz-123, got %v", result.OrganizationUUID)
	}
	if result.PlanType != core.PlanTypePro {
		t.Errorf("expected PlanTypePro, got %v", result.PlanType)
	}
	if result.Remaining == nil || *result.Remaining != 40 {
		t.Errorf("expected remaining 40, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(resetAt) {
		t.Errorf("expected resetAt %v, got %v", resetAt, result.ResetAt)
	}
}

func TestProber_Probe_DiscoverOrganization(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

	orgsJSON := `[
		{
			"uuid": "discovered-org-uuid",
			"name": "Personal",
			"capabilities": ["claude_pro"]
		}
	]`

	usageJSON := `{
		"plan_type": "pro",
		"rate_limits": {
			"five_hour": {
				"resets_at": "2026-08-20T15:00:00Z",
				"remaining": 25,
				"limit": 50
			}
		}
	}`

	mock := mockHostDoer{
		handler: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			if strings.HasSuffix(req.URL, "/organizations") {
				return host.HTTPResponse{
					StatusCode: http.StatusOK,
					Body:       []byte(orgsJSON),
				}, nil
			}
			if strings.Contains(req.URL, "/organizations/discovered-org-uuid/usage") {
				return host.HTTPResponse{
					StatusCode: http.StatusOK,
					Body:       []byte(usageJSON),
				}, nil
			}
			return host.HTTPResponse{StatusCode: http.StatusNotFound}, nil
		},
	}

	prober := NewProber(mock, fixedClock{now: now})
	result := prober.Probe(context.Background(), ProbeRequest{
		Provider:    core.ProviderClaude,
		AuthIndex:   "auth_claude_2",
		AccessToken: "test-token-2",
		BaseURL:     DefaultWebBaseURL,
	})

	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.OrganizationUUID != "discovered-org-uuid" {
		t.Errorf("expected discovered-org-uuid, got %v", result.OrganizationUUID)
	}
	if result.Remaining == nil || *result.Remaining != 25 {
		t.Errorf("expected remaining 25, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(resetAt) {
		t.Errorf("expected resetAt %v, got %v", resetAt, result.ResetAt)
	}
}

func TestProber_Probe_RateLimit429(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

	rateLimitJSON := `{
		"error": {
			"type": "rate_limit_error",
			"message": "You have reached your 5-hour limit.",
			"resets_at": "2026-08-20T15:00:00Z"
		}
	}`

	mock := mockHostDoer{
		handler: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			return host.HTTPResponse{
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(rateLimitJSON),
			}, nil
		},
	}

	prober := NewProber(mock, fixedClock{now: now})
	result := prober.Probe(context.Background(), ProbeRequest{
		Provider:         core.ProviderClaude,
		AuthIndex:        "auth_claude_3",
		AccessToken:      "test-token-3",
		OrganizationUUID: "org-xyz",
	})

	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected remaining 0, got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(resetAt) {
		t.Errorf("expected resetAt %v, got %v", resetAt, result.ResetAt)
	}
}

func TestProber_Probe_Unauthorized401(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	mock := mockHostDoer{
		handler: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			return host.HTTPResponse{
				StatusCode: http.StatusUnauthorized,
				Body:       []byte(`{"error": {"type": "authentication_error", "message": "invalid token"}}`),
			}, nil
		},
	}

	prober := NewProber(mock, fixedClock{now: now})
	result := prober.Probe(context.Background(), ProbeRequest{
		Provider:         core.ProviderClaude,
		AuthIndex:        "auth_claude_4",
		AccessToken:      "bad-token",
		OrganizationUUID: "org-xyz",
	})

	if result.Status != StatusProbeFailed {
		t.Errorf("expected StatusProbeFailed, got %v", result.Status)
	}
	if !strings.Contains(result.Error, "401") {
		t.Errorf("expected error mentioning 401, got %v", result.Error)
	}
}

func TestProber_Probe_NetworkError(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	mock := mockHostDoer{
		handler: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			return host.HTTPResponse{}, errors.New("connection reset by peer")
		},
	}

	prober := NewProber(mock, fixedClock{now: now})
	result := prober.Probe(context.Background(), ProbeRequest{
		Provider:    core.ProviderClaude,
		AuthIndex:   "auth_claude_5",
		AccessToken: "test-token-5",
	})

	if result.Status != StatusProbeFailed {
		t.Errorf("expected StatusProbeFailed, got %v", result.Status)
	}
}

func TestProber_Probe_MessagesMicroProbeSuccess(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expectedWeeklyReset := time.Unix(1787767200, 0).UTC()

	mock := mockHostDoer{
		handler: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			if strings.HasSuffix(req.URL, "/v1/messages") && req.Method == http.MethodPost {
				return host.HTTPResponse{
					StatusCode: http.StatusOK,
					Headers: host.Header{
						"anthropic-ratelimit-unified-5h-status":      []string{"allowed"},
						"anthropic-ratelimit-unified-5h-utilization": []string{"0.0"},
						"anthropic-ratelimit-unified-5h-reset":       []string{"1787627400"},
						"anthropic-ratelimit-unified-7d-status":      []string{"allowed"},
						"anthropic-ratelimit-unified-7d-utilization": []string{"0.49"},
						"anthropic-ratelimit-unified-7d-reset":       []string{"1787767200"},
						"anthropic-ratelimit-unified-status":        []string{"allowed"},
						"anthropic-organization-id":                  []string{"org-micro-probe-123"},
					},
					Body: []byte(`{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}`),
				}, nil
			}
			return host.HTTPResponse{StatusCode: http.StatusNotFound}, nil
		},
	}

	prober := NewProber(mock, fixedClock{now: now})
	result := prober.Probe(context.Background(), ProbeRequest{
		Provider:    core.ProviderClaude,
		AuthIndex:   "auth_claude_micro",
		AccessToken: "test-token",
	})

	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.Remaining == nil || *result.Remaining != 51 {
		t.Errorf("expected remaining 51 (from 0.49 utilization), got %v", result.Remaining)
	}
	if result.ResetAt == nil || !result.ResetAt.Equal(expectedWeeklyReset) {
		t.Errorf("expected resetAt %v, got %v", expectedWeeklyReset, result.ResetAt)
	}
	if result.OrganizationUUID != "org-micro-probe-123" {
		t.Errorf("expected org-micro-probe-123, got %v", result.OrganizationUUID)
	}
	if result.Window != WindowWeekly {
		t.Errorf("expected WindowWeekly, got %v", result.Window)
	}
}
