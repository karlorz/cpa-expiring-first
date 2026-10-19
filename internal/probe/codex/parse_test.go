package codex

import (
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

var observedAt = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestParseWhamUsagePaidWindows(t *testing.T) {
	raw := []byte(`{
	  "plan_type": "plus",
	  "rate_limit": {
	    "primary_window": {"used_percent": 25, "reset_at": "2026-10-09T15:00:00Z", "limit_window_seconds": 18000},
	    "secondary_window": {"used_percent": 80, "reset_at": "2026-10-14T12:00:00Z", "limit_window_seconds": 604800}
	  }
	}`)
	result := ParseWhamUsage(raw, observedAt)
	if result.Status != reading.StatusReady {
		t.Fatalf("status = %q, want ready (%s)", result.Status, result.Error)
	}
	if result.PlanType != reading.PlanTypePlus {
		t.Errorf("plan = %q, want plus", result.PlanType)
	}
	windows := Windows(result)
	if len(windows) != 2 {
		t.Fatalf("windows = %+v, want 5h and weekly", windows)
	}
	byMinutes := map[float64]reading.Window{}
	for _, window := range windows {
		byMinutes[window.Minutes] = window
	}
	if got := byMinutes[300].UsedPercent; got != 25 {
		t.Errorf("5h used = %v, want 25", got)
	}
	if got := byMinutes[10080].UsedPercent; got != 80 {
		t.Errorf("weekly used = %v, want 80", got)
	}
	for _, window := range windows {
		if len(window.Models) != 0 {
			t.Errorf("codex windows must apply to every model, got %v", window.Models)
		}
	}
}

func TestParseWhamUsageFreeMonthly(t *testing.T) {
	raw := []byte(`{
	  "plan_type": "free",
	  "rate_limit": {
	    "primary_window": {"used_percent": 10, "reset_at": "2026-11-01T00:00:00Z", "limit_window_seconds": 2592000}
	  }
	}`)
	result := ParseWhamUsage(raw, observedAt)
	if result.Status != reading.StatusReady {
		t.Fatalf("status = %q, want ready (%s)", result.Status, result.Error)
	}
	windows := Windows(result)
	if len(windows) != 1 || windows[0].Minutes < 28*24*60 {
		t.Fatalf("windows = %+v, want one monthly window", windows)
	}
}

func TestParseWhamUsageExhaustedWeeklyWins(t *testing.T) {
	raw := []byte(`{
	  "plan_type": "pro",
	  "rate_limit": {
	    "primary_window": {"used_percent": 10, "reset_at": "2026-10-09T15:00:00Z", "limit_window_seconds": 18000},
	    "secondary_window": {"used_percent": 100, "reset_at": "2026-10-14T12:00:00Z", "limit_window_seconds": 604800}
	  }
	}`)
	result := ParseWhamUsage(raw, observedAt)
	if result.Status != reading.StatusReady {
		t.Fatalf("status = %q, want ready (%s)", result.Status, result.Error)
	}
	if result.Window != WindowWeekly {
		t.Fatalf("window = %q, want weekly so the reset that unblocks the credential is used", result.Window)
	}
	windows := Windows(result)
	for _, window := range windows {
		if window.Minutes == 10080 && window.UsedPercent != 100 {
			t.Errorf("weekly used = %v, want 100", window.UsedPercent)
		}
	}
}

func TestParseWhamUsageRejectsUnknownPlan(t *testing.T) {
	raw := []byte(`{"plan_type":"mystery","rate_limit":{"primary_window":{"used_percent":10,"reset_at":"2026-10-09T15:00:00Z"}}}`)
	result := ParseWhamUsage(raw, observedAt)
	if result.Status != reading.StatusProbeFailed {
		t.Fatalf("status = %q, want probe_failed", result.Status)
	}
}
