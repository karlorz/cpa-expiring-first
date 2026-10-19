// Package antigravity probes the Antigravity quota summary endpoint. Antigravity meters two
// independent model groups per credential, so every credential is probed once per group.
package antigravity

import (
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

// ModelGroup identifies one independently metered Antigravity model group.
type ModelGroup = string

// Model groups.
const (
	// ModelGroupGemini serves Gemini models.
	ModelGroupGemini = reading.GroupGemini
	// ModelGroupClaudeGPT serves Claude, GPT and OpenAI models.
	ModelGroupClaudeGPT = reading.GroupClaudeGPT
)

// RetrieveUserQuotaSummaryURL is the Antigravity quota endpoint the CPA quota page uses.
const RetrieveUserQuotaSummaryURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"

// retrieveUserQuotaSummaryURLs lists the quota endpoints in the order they are tried; the
// daily and sandbox hosts serve different deployments.
var retrieveUserQuotaSummaryURLs = []string{
	RetrieveUserQuotaSummaryURL,
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
}

// WindowType identifies a quota window in an Antigravity response.
type WindowType string

// Window types.
const (
	WindowUnknown  WindowType = "unknown"
	WindowFiveHour WindowType = "5h"
	WindowWeekly   WindowType = "weekly"
)

// ProbeResult is one Antigravity probe's safe output for one model group.
type ProbeResult struct {
	AuthID               string
	AuthIndex            string
	ModelGroup           ModelGroup
	ObservedAt           time.Time
	ResetAt              *time.Time
	Remaining            *int64
	Window               WindowType
	Windows              []reading.QuotaWindow
	LongWindowResetAt    *time.Time
	ShortWindowRemaining *int64
	ShortWindowResetAt   *time.Time
	LongWindowRemaining  *int64
	Status               reading.Status
	PlanType             reading.PlanType
	Error                string
}

// ProbeRequest carries the credential context one Antigravity probe needs.
type ProbeRequest struct {
	AuthID      string
	AuthIndex   string
	AccessToken string
	ProjectID   string
	ModelGroup  ModelGroup
}

// WindowsForGroup converts a probe result into router windows for the model group it belongs
// to. A failed probe yields nothing.
func WindowsForGroup(result ProbeResult) []reading.Window {
	if result.Status != reading.StatusReady || len(result.Windows) == 0 {
		return nil
	}
	models := reading.ModelsGemini
	if result.ModelGroup == ModelGroupClaudeGPT {
		models = reading.ModelsClaudeGPT
	}
	windows := make([]reading.Window, 0, len(result.Windows))
	for _, quota := range result.Windows {
		if quota.Duration <= 0 {
			continue
		}
		windows = append(windows, reading.WindowFrom(quota, models))
	}
	return windows
}

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}
