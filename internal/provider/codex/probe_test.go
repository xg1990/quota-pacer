package codex

import (
	"context"
	"net/http"
	"testing"
	"time"

	"quota-pacer/internal/host"
)

type fixedProbeClock struct{ now time.Time }

func (c fixedProbeClock) Now() time.Time { return c.now }

type scriptedHTTPDoer struct {
	responses map[string]host.HTTPResponse
	calls     []string
}

func (d *scriptedHTTPDoer) HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	d.calls = append(d.calls, req.URL)
	return d.responses[req.URL], nil
}

const usagePayload5hOnly = `{
	"plan_type": "plus",
	"rate_limit": {
		"primary_window": {
			"limit_window_seconds": 18000,
			"reset_at": "2026-08-20T14:00:00Z",
			"remaining": 40,
			"limit": 50
		}
	}
}`

func TestProber_Probe_AttachesResetCreditsOnReadyUsage(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	doer := &scriptedHTTPDoer{responses: map[string]host.HTTPResponse{
		WhamUsageURL: {StatusCode: http.StatusOK, Body: []byte(usagePayload5hOnly)},
		WhamResetCreditsURL: {StatusCode: http.StatusOK, Body: []byte(`{
			"available_count": 1,
			"credits": [{"id": "c1", "expires_at": "2026-09-01T00:00:00Z", "status": "available"}]
		}`)},
	}}
	prober := NewProber(doer, fixedProbeClock{now: now})

	result := prober.Probe(context.Background(), ProbeRequest{Provider: "codex", AuthIndex: "auth-1", AccessToken: "tok"})

	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.AvailableResetCredits != 1 {
		t.Errorf("expected AvailableResetCredits 1, got %d", result.AvailableResetCredits)
	}
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if result.NearestResetCreditExpiresAt == nil || !result.NearestResetCreditExpiresAt.Equal(want) {
		t.Errorf("expected NearestResetCreditExpiresAt %v, got %v", want, result.NearestResetCreditExpiresAt)
	}
	if len(doer.calls) != 2 || doer.calls[0] != WhamUsageURL || doer.calls[1] != WhamResetCreditsURL {
		t.Errorf("expected usage probe followed by reset-credits probe, got calls %v", doer.calls)
	}
}

func TestProber_Probe_ResetCreditsFailureDoesNotFailUsageProbe(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	doer := &scriptedHTTPDoer{responses: map[string]host.HTTPResponse{
		WhamUsageURL:        {StatusCode: http.StatusOK, Body: []byte(usagePayload5hOnly)},
		WhamResetCreditsURL: {StatusCode: http.StatusInternalServerError},
	}}
	prober := NewProber(doer, fixedProbeClock{now: now})

	result := prober.Probe(context.Background(), ProbeRequest{Provider: "codex", AuthIndex: "auth-1", AccessToken: "tok"})

	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady despite reset-credits failure, got %v", result.Status)
	}
	if result.AvailableResetCredits != 0 || result.NearestResetCreditExpiresAt != nil {
		t.Errorf("expected no reset-credit signal on sister-endpoint failure, got count=%d expiresAt=%v", result.AvailableResetCredits, result.NearestResetCreditExpiresAt)
	}
}

func TestProber_Probe_SkipsResetCreditsWhenUsageProbeFails(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	doer := &scriptedHTTPDoer{responses: map[string]host.HTTPResponse{
		WhamUsageURL: {StatusCode: http.StatusUnauthorized},
	}}
	prober := NewProber(doer, fixedProbeClock{now: now})

	result := prober.Probe(context.Background(), ProbeRequest{Provider: "codex", AuthIndex: "auth-1", AccessToken: "tok"})

	if result.Status != StatusProbeFailed {
		t.Fatalf("expected StatusProbeFailed, got %v", result.Status)
	}
	if len(doer.calls) != 1 {
		t.Errorf("expected reset-credits probe to be skipped when usage probe fails, got calls %v", doer.calls)
	}
}
