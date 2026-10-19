// Package claude probes Anthropic's usage and rate-limit endpoints for one credential. It
// prefers the unified rate-limit response headers, which carry exact 5-hour and weekly
// utilization, and falls back to usage JSON.
package claude

import (
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

// API base URLs.
const (
	// DefaultAPIBaseURL is the Anthropic API root.
	DefaultAPIBaseURL = "https://api.anthropic.com"
	// DefaultWebBaseURL is the Claude web API root, used when a credential has no base URL.
	DefaultWebBaseURL = "https://claude.ai/api"
)

// WindowType identifies a quota window in a Claude response.
type WindowType string

// Window types.
const (
	WindowUnknown  WindowType = "unknown"
	WindowFiveHour WindowType = "5h"
	WindowDaily    WindowType = "daily"
	WindowWeekly   WindowType = "weekly"
	WindowSession  WindowType = "session"
)

// ProbeResult is one Claude probe's safe output.
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
	OrganizationUUID     string
	Error                string
}

// ProbeRequest carries the credential context one Claude probe needs.
type ProbeRequest struct {
	AuthID           string
	AuthIndex        string
	AccessToken      string
	OrganizationUUID string
	BaseURL          string
}

// Windows converts a probe result into router windows. Claude has a single metered pool, so
// the windows apply to every model.
func Windows(result ProbeResult) []reading.Window {
	if result.Status != reading.StatusReady || len(result.Windows) == 0 {
		return nil
	}
	windows := make([]reading.Window, 0, len(result.Windows))
	for _, quota := range result.Windows {
		if quota.Duration <= 0 {
			continue
		}
		windows = append(windows, reading.WindowFrom(quota, nil))
	}
	return windows
}

// Header is the response header map the parsers read.
type Header = host.Header

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}
