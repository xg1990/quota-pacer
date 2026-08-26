package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quota-pacer/internal/core"
)

func TestStore_MarkAndNeedsProbe(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-store-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "refresh-cache.json")
	store, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)

	// NeedsProbe on new entry -> true
	check := ProbeCheck{
		AuthIndex: "auth-claude-1",
		Provider:  core.ProviderClaude,
		Now:       now,
		Policy:    ProbePolicy{TTL: 15 * time.Minute},
	}
	needs, err := store.NeedsProbe(context.Background(), check)
	if err != nil || !needs {
		t.Errorf("expected NeedsProbe=true for new entry, got %v, err=%v", needs, err)
	}

	// Mark success
	success := ProbeSuccess{
		AuthIndex:   "auth-claude-1",
		Provider:    core.ProviderClaude,
		ObservedAt:  now,
		ResetAt:     resetAt,
		Remaining:   45,
		Source:      SourceFreshProbe,
		NextProbeAt: now.Add(time.Hour),
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	// NeedsProbe immediately after -> false
	needs, err = store.NeedsProbe(context.Background(), check)
	if err != nil || needs {
		t.Errorf("expected NeedsProbe=false after success, got %v, err=%v", needs, err)
	}

	// Save and Reload
	if err := store.SaveAtomic(context.Background()); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	reloaded, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	entry, ok := reloaded.GetEntry("auth-claude-1", "")
	if !ok {
		t.Fatalf("expected entry found in reloaded store")
	}
	if entry.Remaining != 45 {
		t.Errorf("expected remaining 45, got %d", entry.Remaining)
	}
}
