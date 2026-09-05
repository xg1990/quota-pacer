package codex

import (
	"testing"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
)

func TestParseUsageHeaders_PrimaryAndSecondary(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	headers := host.Header{
		"X-Codex-Primary-Used-Percent":          []string{"60"},
		"X-Codex-Primary-Window-Minutes":        []string{"300"}, // 5h
		"X-Codex-Primary-Reset-After-Seconds":   []string{"3600"},
		"X-Codex-Secondary-Used-Percent":        []string{"10"},
		"X-Codex-Secondary-Window-Minutes":      []string{"10080"}, // 7d
		"X-Codex-Secondary-Reset-After-Seconds": []string{"86400"},
		"X-Codex-Plan-Type":                     []string{"plus"},
	}

	result := ParseUsageHeaders(headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if result.PlanType != core.PlanTypePlus {
		t.Errorf("expected PlanTypePlus, got %v", result.PlanType)
	}
	if len(result.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(result.Windows))
	}
	// primary: used 60% -> remaining 40%; secondary: used 10% -> remaining 90%.
	// primary 更紧张(40<90) -> 顶层主窗口应为 primary。
	if result.Remaining == nil || *result.Remaining != 40 {
		t.Errorf("expected top-level Remaining=40 (primary, tighter), got %v", result.Remaining)
	}
	var sawPrimary, sawSecondary bool
	for _, w := range result.Windows {
		switch w.Name {
		case "primary":
			sawPrimary = true
			if w.Remaining != 40 {
				t.Errorf("expected primary window remaining 40, got %d", w.Remaining)
			}
			if w.Duration != 5*time.Hour {
				t.Errorf("expected primary window duration 5h, got %v", w.Duration)
			}
		case "secondary":
			sawSecondary = true
			if w.Remaining != 90 {
				t.Errorf("expected secondary window remaining 90, got %d", w.Remaining)
			}
			if w.Duration != 7*24*time.Hour {
				t.Errorf("expected secondary window duration 7d, got %v", w.Duration)
			}
		}
	}
	if !sawPrimary || !sawSecondary {
		t.Errorf("expected both primary and secondary windows present, got %+v", result.Windows)
	}
}

func TestParseUsageHeaders_PrimaryOnly(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	headers := host.Header{
		"X-Codex-Primary-Used-Percent":        []string{"25"},
		"X-Codex-Primary-Window-Minutes":      []string{"300"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"1800"},
	}

	result := ParseUsageHeaders(headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v (err: %s)", result.Status, result.Error)
	}
	if len(result.Windows) != 1 || result.Windows[0].Name != "primary" {
		t.Fatalf("expected exactly 1 primary window, got %+v", result.Windows)
	}
	if result.Remaining == nil || *result.Remaining != 75 {
		t.Errorf("expected Remaining=75, got %v", result.Remaining)
	}
}

func TestParseUsageHeaders_LimitReachedOverridesUsedPercent(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	headers := host.Header{
		"X-Codex-Primary-Used-Percent":        []string{"50"}, // 若忽略 limit-reached 会误判还有余量
		"X-Codex-Primary-Window-Minutes":      []string{"300"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"1800"},
		"X-Codex-Primary-Limit-Reached":       []string{"true"},
	}

	result := ParseUsageHeaders(headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Errorf("expected Remaining=0 when limit-reached=true, got %v", result.Remaining)
	}
}

func TestParseUsageHeaders_NoQuotaHeadersFails(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	headers := host.Header{
		"Content-Type": []string{"application/json"},
	}

	result := ParseUsageHeaders(headers, observedAt)
	if result.Status != StatusProbeFailed {
		t.Errorf("expected StatusProbeFailed when no quota headers present, got %v", result.Status)
	}
}

func TestParseUsageHeaders_MissingResetInfoIgnoresWindow(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	headers := host.Header{
		"X-Codex-Primary-Used-Percent":   []string{"50"},
		"X-Codex-Primary-Window-Minutes": []string{"300"},
		// 缺 Reset-After-Seconds 和 Reset-At：无法确定重置时间，该窗口应被忽略。
	}

	result := ParseUsageHeaders(headers, observedAt)
	if result.Status != StatusProbeFailed {
		t.Errorf("expected StatusProbeFailed when reset info missing, got %v", result.Status)
	}
}

func TestParseUsageHeaders_CaseInsensitiveHeaderNames(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	headers := host.Header{
		"x-codex-primary-used-percent":        []string{"20"},
		"x-codex-primary-window-minutes":      []string{"300"},
		"x-codex-primary-reset-after-seconds": []string{"1800"},
	}

	result := ParseUsageHeaders(headers, observedAt)
	if result.Status != StatusReady {
		t.Fatalf("expected StatusReady with lowercase header names, got %v", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 80 {
		t.Errorf("expected Remaining=80, got %v", result.Remaining)
	}
}
