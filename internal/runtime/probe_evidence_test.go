package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quota-pacer/internal/config"
	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
	"quota-pacer/internal/priority"
	"quota-pacer/internal/provider/antigravity"
	"quota-pacer/internal/provider/claude"
	"quota-pacer/internal/schedule"
	"quota-pacer/internal/state"
)

func TestRecordClaudeProbeResult_SuccessAndFailure(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-claude-evidence-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "refresh-cache.json")
	store, err := state.Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("state.Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)
	rem := int64(35)

	// Success case
	successResult := claude.ProbeResult{
		Provider:    core.ProviderClaude,
		AuthIndex:   "auth-claude-1",
		ObservedAt:  now,
		ResetAt:     &resetAt,
		Remaining:   &rem,
		Window:      claude.WindowFiveHour,
		Freshness:   core.FreshnessFresh,
		ProbeStatus: core.ProbeStatusReady,
		Status:      claude.StatusReady,
		PlanType:    core.PlanTypePro,
	}

	ev, err := recordClaudeProbeResult(context.Background(), store, successResult, now)
	if err != nil {
		t.Fatalf("recordClaudeProbeResult failed: %v", err)
	}
	if ev.Status != priority.EvidenceStatusReady {
		t.Errorf("expected EvidenceStatusReady, got %v", ev.Status)
	}
	if !ev.EvidenceFresh {
		t.Errorf("expected EvidenceFresh=true")
	}
	if ev.Remaining == nil || *ev.Remaining != 35 {
		t.Errorf("expected remaining 35, got %v", ev.Remaining)
	}

	// Failure case
	failResult := claude.ProbeResult{
		Provider:    core.ProviderClaude,
		AuthIndex:   "auth-claude-2",
		ObservedAt:  now,
		Status:      claude.StatusProbeFailed,
		Error:       "unauthorized",
		Freshness:   core.FreshnessUnknown,
		ProbeStatus: core.ProbeStatusUnknown,
	}

	evFail, err := recordClaudeProbeResult(context.Background(), store, failResult, now)
	if err != nil {
		t.Fatalf("recordClaudeProbeResult failure record error: %v", err)
	}
	if evFail.Status != priority.EvidenceStatusProbeFailed {
		t.Errorf("expected EvidenceStatusProbeFailed, got %v", evFail.Status)
	}
}

func TestRecordAntigravityProbeResult_PersistsLongWindowResetAt(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-antigravity-evidence-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "refresh-cache.json")
	store, err := state.Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("state.Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(3 * time.Hour)
	longWindowResetAt := now.Add(48 * time.Hour)
	rem := int64(40)

	result := antigravity.ProbeResult{
		Provider:          core.ProviderAntigravity,
		AuthIndex:         "auth-antigravity-1",
		ModelGroup:        antigravity.ModelGroupClaudeGPT,
		ObservedAt:        now,
		ResetAt:           &resetAt,
		LongWindowResetAt: &longWindowResetAt,
		Remaining:         &rem,
		Window:            antigravity.WindowFiveHour,
		Freshness:         core.FreshnessFresh,
		ProbeStatus:       core.ProbeStatusReady,
		Status:            antigravity.StatusReady,
		PlanType:          core.PlanTypePro,
	}

	if _, err := recordAntigravityProbeResult(context.Background(), store, result, now); err != nil {
		t.Fatalf("recordAntigravityProbeResult failed: %v", err)
	}

	entry, ok := store.GetEntry("auth-antigravity-1", string(antigravity.ModelGroupClaudeGPT))
	if !ok {
		t.Fatalf("expected persisted entry for auth-antigravity-1")
	}
	if !entry.LongWindowResetAt.Equal(longWindowResetAt) {
		t.Errorf("expected persisted LongWindowResetAt %v, got %v", longWindowResetAt, entry.LongWindowResetAt)
	}
}

func TestCollectFreshEvidence_Claude(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-collect-claude-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "refresh-cache.json")
	store, err := state.Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("state.Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	mockHost := mockHostCallbacks{}
	client := host.NewClient(mockHost)

	probes := []schedule.Probe{
		{
			Credential: core.Credential{
				Name:      "c1",
				AuthIndex: "auth-claude-test",
				Provider:  core.ProviderClaude,
				Type:      core.CredentialTypeClaude,
			},
			NextProbeAt: now,
		},
	}

	input := collectInput{
		client: client,
		store:  store,
		probes: probes,
		authMaterials: map[string]authMaterial{
			"auth-claude-test": {
				accessToken:      "test-token",
				organizationUUID: "org-123",
			},
		},
		now:                   now,
		cacheTTL:              15 * time.Minute,
		forceProbe:            true,
		maxConcurrency:        2,
		antigravityModelGroup: config.AntigravityModelGroupGemini,
	}

	evidence, err := collectFreshEvidence(context.Background(), input)
	if err != nil {
		t.Fatalf("collectFreshEvidence failed: %v", err)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected 1 evidence item, got %d", len(evidence))
	}
	if evidence[0].Provider != core.ProviderClaude {
		t.Errorf("expected ProviderClaude, got %v", evidence[0].Provider)
	}
	if evidence[0].Status != priority.EvidenceStatusReady {
		t.Errorf("expected EvidenceStatusReady, got %v", evidence[0].Status)
	}
}
