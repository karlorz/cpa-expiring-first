package claude

import (
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

var observedAt = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestParseUnifiedRateLimitHeaders(t *testing.T) {
	headers := host.Header{
		"anthropic-ratelimit-unified-5h-utilization": []string{"0.20"},
		"anthropic-ratelimit-unified-5h-reset":       []string{"2026-10-09T15:00:00Z"},
		"anthropic-ratelimit-unified-7d-utilization": []string{"0.60"},
		"anthropic-ratelimit-unified-7d-reset":       []string{"2026-10-14T12:00:00Z"},
	}
	result := ParseClaudeUsage(nil, headers, observedAt)
	if result.Status != reading.StatusReady {
		t.Fatalf("status = %q, want ready (%s)", result.Status, result.Error)
	}
	windows := Windows(result)
	if len(windows) != 2 {
		t.Fatalf("windows = %+v, want 5h and weekly", windows)
	}
	byMinutes := map[float64]reading.Window{}
	for _, window := range windows {
		byMinutes[window.Minutes] = window
	}
	if got := byMinutes[300].UsedPercent; got != 20 {
		t.Errorf("5h used = %v, want 20", got)
	}
	if got := byMinutes[10080].UsedPercent; got != 60 {
		t.Errorf("weekly used = %v, want 60", got)
	}
	if len(byMinutes[300].Models) != 0 || len(byMinutes[10080].Models) != 0 {
		t.Errorf("claude windows must apply to every model, got %+v", windows)
	}
}

func TestParseClaudeUsageExhaustedWeekly(t *testing.T) {
	headers := host.Header{
		"anthropic-ratelimit-unified-7d-utilization": []string{"1.0"},
		"anthropic-ratelimit-unified-7d-reset":       []string{"2026-10-14T12:00:00Z"},
	}
	result := ParseClaudeUsage(nil, headers, observedAt)
	if result.Status != reading.StatusReady {
		t.Fatalf("status = %q, want ready (%s)", result.Status, result.Error)
	}
	windows := Windows(result)
	if len(windows) != 1 || windows[0].UsedPercent != 100 {
		t.Fatalf("windows = %+v, want one exhausted weekly window", windows)
	}
	if want := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC); !windows[0].ResetsAt.Equal(want) {
		t.Errorf("reset = %v, want %v", windows[0].ResetsAt, want)
	}
}

func TestParseClaudeUsageBodyWindows(t *testing.T) {
	raw := []byte(`{
	  "five_hour": {"used_percent": 10, "resets_at": "2026-10-09T15:00:00Z"},
	  "weekly": {"used_percent": 90, "resets_at": "2026-10-14T12:00:00Z"}
	}`)
	result := ParseClaudeUsage(raw, nil, observedAt)
	if result.Status != reading.StatusReady {
		t.Fatalf("status = %q, want ready (%s)", result.Status, result.Error)
	}
	windows := Windows(result)
	if len(windows) != 2 {
		t.Fatalf("windows = %+v, want 5h and weekly", windows)
	}
}

func TestParseClaudeRateLimitError(t *testing.T) {
	headers := host.Header{"retry-after": []string{"3600"}}
	result := ParseClaudeRateLimitError(nil, headers, observedAt)
	if result.Status != reading.StatusReady {
		t.Fatalf("status = %q, want ready", result.Status)
	}
	if result.Remaining == nil || *result.Remaining != 0 {
		t.Fatalf("remaining = %v, want 0", result.Remaining)
	}
	want := observedAt.Add(time.Hour)
	if result.ResetAt == nil || !result.ResetAt.Equal(want) {
		t.Fatalf("reset = %v, want %v", result.ResetAt, want)
	}
}

func TestParseClaudeUsageRejectsEmpty(t *testing.T) {
	result := ParseClaudeUsage(nil, nil, observedAt)
	if result.Status != reading.StatusProbeFailed {
		t.Fatalf("status = %q, want probe_failed", result.Status)
	}
	if len(Windows(result)) != 0 {
		t.Fatalf("windows = %+v, want none", Windows(result))
	}
}
