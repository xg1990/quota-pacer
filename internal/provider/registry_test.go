package provider

import (
	"testing"

	"quota-pacer/internal/core"
)

func TestRegistry_Evaluate_Claude(t *testing.T) {
	tests := []struct {
		name             string
		credential       core.Credential
		expectedProvider core.Provider
		expectedStrategy core.StrategyName
	}{
		{
			name: "provider claude",
			credential: core.Credential{
				Name:     "claude-1",
				Provider: core.ProviderClaude,
			},
			expectedProvider: core.ProviderClaude,
			expectedStrategy: core.StrategyClaude,
		},
		{
			name: "type claude",
			credential: core.Credential{
				Name: "claude-2",
				Type: core.CredentialTypeClaude,
			},
			expectedProvider: core.ProviderClaude,
			expectedStrategy: core.StrategyClaude,
		},
		{
			name: "type claude-oauth",
			credential: core.Credential{
				Name: "claude-3",
				Type: core.CredentialType("claude-oauth"),
			},
			expectedProvider: core.ProviderClaude,
			expectedStrategy: core.StrategyClaude,
		},
		{
			name: "type anthropic",
			credential: core.Credential{
				Name: "claude-4",
				Type: core.CredentialType("anthropic"),
			},
			expectedProvider: core.ProviderClaude,
			expectedStrategy: core.StrategyClaude,
		},
		{
			name: "provider codex",
			credential: core.Credential{
				Name:     "codex-1",
				Provider: core.ProviderCodex,
			},
			expectedProvider: core.ProviderCodex,
			expectedStrategy: core.StrategyCodex,
		},
		{
			name: "provider antigravity",
			credential: core.Credential{
				Name:     "ag-1",
				Provider: core.ProviderAntigravity,
			},
			expectedProvider: core.ProviderAntigravity,
			expectedStrategy: core.StrategyAntigravity,
		},
		{
			name: "provider xai",
			credential: core.Credential{
				Name:     "xai-1",
				Provider: core.ProviderXAI,
			},
			expectedProvider: core.ProviderXAI,
			expectedStrategy: core.StrategyXAI,
		},
	}

	reg := NewRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := reg.Evaluate(tt.credential)
			if res.Provider != tt.expectedProvider {
				t.Errorf("expected provider %v, got %v", tt.expectedProvider, res.Provider)
			}
			if res.StrategyName != tt.expectedStrategy {
				t.Errorf("expected strategy %v, got %v", tt.expectedStrategy, res.StrategyName)
			}
		})
	}
}
