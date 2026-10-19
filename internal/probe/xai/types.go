// Package xai probes xAI credentials. xAI does not expose a usage percentage through a stable
// endpoint, so the probe classifies the plan and, for OAuth credentials, reads the weekly
// billing window from the CLI chat-proxy billing endpoint. The resulting weekly window ranks the
// credential by when its quota resets.
package xai

import (
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

// DefaultAPIBaseURL is the xAI API root, matching CLIProxyAPI's xai auth default.
const DefaultAPIBaseURL = "https://api.x.ai/v1"

// CLIChatProxyBillingURL is the xAI OAuth weekly billing endpoint (Management Center contract).
const CLIChatProxyBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"

// PlanClass is the coarse plan classification, independent of any quota cooldown.
type PlanClass string

// Plan classes.
const (
	PlanClassUnknown PlanClass = ""
	PlanClassFree    PlanClass = "free"
	PlanClassPaid    PlanClass = "paid"
)

// ProbeResult is one xAI probe's safe output.
type ProbeResult struct {
	AuthID      string
	AuthIndex   string
	ObservedAt  time.Time
	PlanClass   PlanClass
	PlanType    reading.PlanType
	Source      string
	HTTPStatus  int
	AuthFailed  bool
	Windows     []reading.QuotaWindow
	LongResetAt *time.Time
	Status      reading.Status
	Error       string
}

// ProbeRequest carries the credential context one xAI probe needs.
type ProbeRequest struct {
	AuthID      string
	AuthIndex   string
	AccessToken string
	BaseURL     string
	// AuthKind comes from the credential JSON auth_kind; only oauth reads the weekly billing.
	AuthKind string
	// UserID comes only from a non-secret subject claim, used for the x-userid header.
	UserID string
}

// Windows converts a probe result into router windows. The xAI reading applies to every model.
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
