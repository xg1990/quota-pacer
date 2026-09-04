package codex

import (
	"testing"
	"time"
)

func TestParseWhamResetCredits_AvailableWithExpiry(t *testing.T) {
	payload := `{
		"available_count": 1,
		"credits": [
			{"id": "cred-1", "granted_at": "2026-08-01T00:00:00Z", "expires_at": "2026-09-10T00:00:00Z", "reset_type": "codexRateLimits", "status": "available"}
		]
	}`

	summary, ok := parseWhamResetCredits([]byte(payload))
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if summary.availableCount != 1 {
		t.Errorf("expected availableCount 1, got %d", summary.availableCount)
	}
	want := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	if summary.nearestExpiresAt == nil || !summary.nearestExpiresAt.Equal(want) {
		t.Errorf("expected nearestExpiresAt %v, got %v", want, summary.nearestExpiresAt)
	}
}

func TestParseWhamResetCredits_PicksNearestAmongMultiple(t *testing.T) {
	payload := `{
		"available_count": 2,
		"credits": [
			{"id": "cred-far", "expires_at": "2026-12-01T00:00:00Z", "status": "available"},
			{"id": "cred-near", "expires_at": "2026-09-05T00:00:00Z", "status": "available"}
		]
	}`

	summary, ok := parseWhamResetCredits([]byte(payload))
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	if summary.nearestExpiresAt == nil || !summary.nearestExpiresAt.Equal(want) {
		t.Errorf("expected nearest to be the soonest expiry %v, got %v", want, summary.nearestExpiresAt)
	}
}

func TestParseWhamResetCredits_IgnoresRedeemedAndExpiredStatus(t *testing.T) {
	payload := `{
		"available_count": 0,
		"credits": [
			{"id": "cred-redeemed", "expires_at": "2026-09-05T00:00:00Z", "status": "redeemed"},
			{"id": "cred-expired", "expires_at": "2026-08-01T00:00:00Z", "status": "expired"}
		]
	}`

	summary, ok := parseWhamResetCredits([]byte(payload))
	if ok {
		t.Fatalf("expected ok=false when no status=available entries and available_count=0, got %+v", summary)
	}
}

func TestParseWhamResetCredits_CamelCaseFieldNames(t *testing.T) {
	payload := `{
		"availableCount": 1,
		"credits": [
			{"id": "cred-1", "grantedAt": "2026-08-01T00:00:00Z", "expiresAt": "2026-09-10T00:00:00Z", "resetType": "codexRateLimits", "status": "available"}
		]
	}`

	summary, ok := parseWhamResetCredits([]byte(payload))
	if !ok {
		t.Fatalf("expected ok=true for camelCase field variant")
	}
	if summary.availableCount != 1 {
		t.Errorf("expected availableCount 1, got %d", summary.availableCount)
	}
	want := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	if summary.nearestExpiresAt == nil || !summary.nearestExpiresAt.Equal(want) {
		t.Errorf("expected nearestExpiresAt %v, got %v", want, summary.nearestExpiresAt)
	}
}

func TestParseWhamResetCredits_EmptyOrGarbageBody(t *testing.T) {
	if _, ok := parseWhamResetCredits(nil); ok {
		t.Errorf("expected ok=false for nil body")
	}
	if _, ok := parseWhamResetCredits([]byte("")); ok {
		t.Errorf("expected ok=false for empty body")
	}
	if _, ok := parseWhamResetCredits([]byte("not json")); ok {
		t.Errorf("expected ok=false for unparseable body")
	}
}

func TestParseWhamResetCredits_NoAvailableCreditsButCountZero(t *testing.T) {
	payload := `{"available_count": 0, "credits": []}`
	if _, ok := parseWhamResetCredits([]byte(payload)); ok {
		t.Errorf("expected ok=false when no signal present")
	}
}
