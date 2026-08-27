package config

import (
	"testing"
	"time"
)

func TestLoadBytes_JSON(t *testing.T) {
	configJSON := `{
		"enabled": true,
		"auto_apply": true,
		"provider_scope": "claude",
		"interval": "10m",
		"immediate_probe_limit": 20,
		"max_concurrency": 4,
		"active_group_size": 8
	}`

	cfg, err := LoadBytes([]byte(configJSON))
	if err != nil {
		t.Fatalf("LoadBytes failed: %v", err)
	}

	if !cfg.Enabled {
		t.Errorf("expected Enabled=true")
	}
	if !cfg.AutoApply {
		t.Errorf("expected AutoApply=true")
	}
	if cfg.ProviderScope != ProviderScopeSelected {
		t.Errorf("expected ProviderScopeSelected, got %v", cfg.ProviderScope)
	}
	if len(cfg.SelectedProviders) != 1 || cfg.SelectedProviders[0] != "claude" {
		t.Errorf("expected SelectedProviders=['claude'], got %v", cfg.SelectedProviders)
	}
	if cfg.Interval != 10*time.Minute {
		t.Errorf("expected Interval=10m, got %v", cfg.Interval)
	}
	if cfg.ImmediateProbeLimit != 20 {
		t.Errorf("expected ImmediateProbeLimit=20, got %d", cfg.ImmediateProbeLimit)
	}
	if cfg.MaxConcurrency != 4 {
		t.Errorf("expected MaxConcurrency=4, got %d", cfg.MaxConcurrency)
	}
	if cfg.ActiveGroupSize != 8 {
		t.Errorf("expected ActiveGroupSize=8, got %d", cfg.ActiveGroupSize)
	}
}

func TestLoadBytes_YAML(t *testing.T) {
	configYAML := `
enabled: true
auto_apply: true
provider_scope: "antigravity|codex|claude|xai"
interval: 20m
`

	cfg, err := LoadBytes([]byte(configYAML))
	if err != nil {
		t.Fatalf("LoadBytes failed: %v", err)
	}

	if len(cfg.SelectedProviders) != 4 {
		t.Fatalf("expected 4 selected providers, got %v", cfg.SelectedProviders)
	}
	if cfg.Interval != 20*time.Minute {
		t.Errorf("expected Interval=20m, got %v", cfg.Interval)
	}
}

func TestLoadBytes_Default(t *testing.T) {
	cfg, err := LoadBytes([]byte("{}"))
	if err != nil {
		t.Fatalf("LoadBytes failed: %v", err)
	}

	def := Default()
	if cfg.Interval != def.Interval {
		t.Errorf("expected default interval %v, got %v", def.Interval, cfg.Interval)
	}
	if cfg.ProviderScope != ProviderScopeAll {
		t.Errorf("expected default ProviderScopeAll, got %v", cfg.ProviderScope)
	}
}
