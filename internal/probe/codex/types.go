// Package codex probes ChatGPT's wham usage endpoint for a Codex credential. The response
// carries a paid five-hour and weekly window, or a free monthly window.
package codex

import (
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

// Endpoints.
const (
	// WhamUsageURL is ChatGPT's wham usage endpoint, called through the host transport.
	WhamUsageURL = "https://chatgpt.com/backend-api/wham/usage"
)

// WindowType identifies a quota window in a wham usage response.
type WindowType string

// Window types.
const (
	WindowUnknown  WindowType = "unknown"
	WindowFiveHour WindowType = "5h"
	WindowWeekly   WindowType = "weekly"
	WindowMonthly  WindowType = "monthly"
)

// ProbeResult is one Codex probe's safe output.
type ProbeResult struct {
	AuthID               string
	AuthIndex            string
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

// ProbeRequest carries the credential context one Codex probe needs.
type ProbeRequest struct {
	AuthID      string
	AuthIndex   string
	AccountID   string
	AccessToken string
}

// Windows converts a probe result into router windows. Codex has a single metered pool, so the
// windows apply to every model.
func Windows(result ProbeResult) []reading.Window {
	return reading.WindowsFrom(result.Status, result.Windows, nil)
}

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}
