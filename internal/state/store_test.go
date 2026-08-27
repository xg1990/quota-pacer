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

func TestStore_MultiWindowPersistence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cpa-store-multi-test-*")
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
	reset5h := now.Add(5 * time.Hour)
	reset7d := now.Add(7 * 24 * time.Hour)
	shortRem := int64(90)
	longRem := int64(75)

	success := ProbeSuccess{
		AuthIndex:            "auth-codex-1",
		Provider:             core.ProviderCodex,
		ObservedAt:           now,
		ResetAt:              reset5h,
		Remaining:            90,
		Source:               SourceFreshProbe,
		NextProbeAt:          now.Add(time.Hour),
		PlanType:             core.PlanTypePlus,
		ShortWindowRemaining: &shortRem,
		ShortWindowResetAt:   reset5h,
		LongWindowRemaining:  &longRem,
		LongWindowResetAt:    reset7d,
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	if err := store.SaveAtomic(context.Background()); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	reloaded, err := Load(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	entry, ok := reloaded.GetEntry("auth-codex-1", "")
	if !ok {
		t.Fatalf("expected entry found in reloaded store")
	}
	if entry.PlanType != core.PlanTypePlus {
		t.Errorf("expected plan_type plus, got %s", entry.PlanType)
	}
	if entry.ShortWindowRemaining == nil || *entry.ShortWindowRemaining != 90 {
		t.Errorf("expected short_window_remaining 90, got %v", entry.ShortWindowRemaining)
	}
	if !entry.ShortWindowResetAt.Equal(reset5h) {
		t.Errorf("expected short_window_reset_at %v, got %v", reset5h, entry.ShortWindowResetAt)
	}
	if entry.LongWindowRemaining == nil || *entry.LongWindowRemaining != 75 {
		t.Errorf("expected long_window_remaining 75, got %v", entry.LongWindowRemaining)
	}
	if !entry.LongWindowResetAt.Equal(reset7d) {
		t.Errorf("expected long_window_reset_at %v, got %v", reset7d, entry.LongWindowResetAt)
	}
}

func TestStore_ValidEntry(t *testing.T) {
	store, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(2 * time.Hour)

	// Entry does not exist
	if _, ok := store.ValidEntry("auth-1", "", now, ProbePolicy{TTL: 15 * time.Minute}); ok {
		t.Errorf("expected ValidEntry=false for non-existent entry")
	}

	// Add entry
	success := ProbeSuccess{
		AuthIndex:   "auth-1",
		Provider:    core.ProviderClaude,
		ObservedAt:  now,
		ResetAt:     resetAt,
		Remaining:   50,
		Source:      SourceFreshProbe,
		NextProbeAt: now.Add(time.Hour),
	}
	if err := store.MarkProbeSuccess(context.Background(), success); err != nil {
		t.Fatalf("MarkProbeSuccess failed: %v", err)
	}

	// Valid within TTL and before reset
	entry, ok := store.ValidEntry("auth-1", "", now.Add(5*time.Minute), ProbePolicy{TTL: 15 * time.Minute})
	if !ok || entry.Remaining != 50 {
		t.Errorf("expected ValidEntry=true, got %v, entry=%+v", ok, entry)
	}

	// Past resetAt -> false
	if _, ok := store.ValidEntry("auth-1", "", resetAt.Add(time.Minute), ProbePolicy{TTL: 15 * time.Minute}); ok {
		t.Errorf("expected ValidEntry=false past resetAt")
	}
}
