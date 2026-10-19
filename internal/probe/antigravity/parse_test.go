package antigravity

import (
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

var observedAt = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

const quotaSummary = `{
  "groups": [
    {
      "displayName": "Gemini Models",
      "buckets": [
        {"window": "5h", "remainingFraction": 0.4, "resetTime": "2026-10-09T15:00:00Z"},
        {"window": "weekly", "remainingFraction": 0.25, "resetTime": "2026-10-12T12:00:00Z"}
      ]
    },
    {
      "displayName": "Claude and GPT Models",
      "buckets": [
        {"window": "5h", "remainingFraction": 0.9, "resetTime": "2026-10-09T14:00:00Z"},
        {"window": "weekly", "remainingFraction": 0.8, "resetTime": "2026-10-16T12:00:00Z"}
      ]
    }
  ]
}`

func TestParseAvailableModelsSplitsModelGroups(t *testing.T) {
	gemini := ParseAvailableModels([]byte(quotaSummary), observedAt, ModelGroupGemini)
	if gemini.Status != reading.StatusReady {
		t.Fatalf("gemini status = %q, want ready (%s)", gemini.Status, gemini.Error)
	}
	geminiWindows := WindowsForGroup(gemini)
	if len(geminiWindows) != 2 {
		t.Fatalf("gemini windows = %+v, want 5h and weekly", geminiWindows)
	}
	for _, window := range geminiWindows {
		if len(window.Models) != 1 || window.Models[0] != "gemini" {
			t.Errorf("gemini window models = %v, want [gemini]", window.Models)
		}
	}
	// The 5h window is 60% used and the weekly window 75% used.
	byName := map[string]reading.Window{}
	for _, window := range geminiWindows {
		byName[windowName(window.Minutes)] = window
	}
	if got := byName["5h"].UsedPercent; got != 60 {
		t.Errorf("gemini 5h used = %v, want 60", got)
	}
	if got := byName["weekly"].UsedPercent; got != 75 {
		t.Errorf("gemini weekly used = %v, want 75", got)
	}
	if want := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC); !byName["weekly"].ResetsAt.Equal(want) {
		t.Errorf("gemini weekly reset = %v, want %v", byName["weekly"].ResetsAt, want)
	}

	claudeGPT := ParseAvailableModels([]byte(quotaSummary), observedAt, ModelGroupClaudeGPT)
	if claudeGPT.Status != reading.StatusReady {
		t.Fatalf("claude_gpt status = %q, want ready (%s)", claudeGPT.Status, claudeGPT.Error)
	}
	claudeWindows := WindowsForGroup(claudeGPT)
	if len(claudeWindows) != 2 {
		t.Fatalf("claude_gpt windows = %+v, want 5h and weekly", claudeWindows)
	}
	for _, window := range claudeWindows {
		if len(window.Models) != 3 || window.Models[0] != "claude" || window.Models[1] != "gpt" || window.Models[2] != "openai" {
			t.Errorf("claude_gpt window models = %v, want [claude gpt openai]", window.Models)
		}
	}
}

func TestParseAvailableModelsRejectsUnknownGroup(t *testing.T) {
	result := ParseAvailableModels([]byte(`{"groups":[{"displayName":"Gemini Models","buckets":[]}]}`), observedAt, ModelGroupClaudeGPT)
	if result.Status != reading.StatusProbeFailed {
		t.Fatalf("status = %q, want probe_failed", result.Status)
	}
	if len(WindowsForGroup(result)) != 0 {
		t.Fatalf("windows = %+v, want none", WindowsForGroup(result))
	}
}

func TestClassifyWindow(t *testing.T) {
	cases := map[string]WindowType{
		"weekly":  WindowWeekly,
		"7d":      WindowWeekly,
		"5d 15h":  WindowWeekly,
		"5h":      WindowFiveHour,
		"5 hours": WindowFiveHour,
		"15h":     WindowUnknown,
		"25hr":    WindowUnknown,
		"":        WindowUnknown,
		"rolling": WindowUnknown,
	}
	for name, want := range cases {
		if got := classifyWindow(name); got != want {
			t.Errorf("classifyWindow(%q) = %q, want %q", name, got, want)
		}
	}
}

func windowName(minutes float64) string {
	if minutes >= 6*24*60 {
		return "weekly"
	}
	return "5h"
}
