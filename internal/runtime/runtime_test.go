package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"quota-pacer/internal/config"
	"quota-pacer/internal/host"
)

type mockHostCallbacks struct {
	files []host.AuthFile
}

func (m mockHostCallbacks) ListAuthFiles(ctx context.Context) ([]host.AuthFile, error) {
	return m.files, nil
}

func (m mockHostCallbacks) GetAuth(ctx context.Context, authIndex string) (host.AuthDocument, error) {
	return host.AuthDocument{
		AuthIndex: authIndex,
		JSON:      []byte(`{"access_token":"token-123","organization_uuid":"org-123"}`),
	}, nil
}

func (m mockHostCallbacks) GetRuntime(ctx context.Context, authIndex string) (host.RuntimeAuth, error) {
	return host.RuntimeAuth{AuthIndex: authIndex}, nil
}

func (m mockHostCallbacks) SaveAuth(ctx context.Context, name string, doc json.RawMessage) error {
	return nil
}

func (m mockHostCallbacks) HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	return host.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"plan_type":"pro","rate_limits":{"session_limit":{"resets_at":"2026-08-20T18:00:00Z","remaining":45,"limit":50}}}`),
	}, nil
}

func TestRuntime_RegisterAndConfig(t *testing.T) {
	mockHost := mockHostCallbacks{}
	rt := New(Options{
		Host:  mockHost,
		Clock: fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})

	cfgYAML := `
enabled: true
auto_apply: true
provider_scope: "claude"
`
	res, err := rt.Register(context.Background(), RegisterRequest{ConfigYAML: cfgYAML})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if res.SchemaVersion != 1 {
		t.Errorf("expected SchemaVersion 1, got %d", res.SchemaVersion)
	}

	cfg, err := rt.Config()
	if err != nil {
		t.Fatalf("Config failed: %v", err)
	}
	if cfg.ProviderScope != config.ProviderScopeSelected {
		t.Errorf("expected ProviderScopeSelected, got %v", cfg.ProviderScope)
	}

	if err := rt.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}
}

func TestRuntime_HandleJSON(t *testing.T) {
	mockHost := mockHostCallbacks{}
	rt := New(Options{
		Host:  mockHost,
		Clock: fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})

	req := []byte(`{"config_yaml":"enabled: true\nprovider_scope: all"}`)
	resp := rt.Handle(context.Background(), "plugin.register", req)
	if len(resp) == 0 {
		t.Fatalf("expected non-empty response")
	}

	shutdownResp := rt.Handle(context.Background(), "plugin.shutdown", nil)
	if len(shutdownResp) == 0 {
		t.Fatalf("expected non-empty shutdown response")
	}
}
