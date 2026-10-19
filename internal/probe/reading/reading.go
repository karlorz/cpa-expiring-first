// Package reading defines the quota readings the provider probes produce, before they are
// mapped onto the router's quota view. It also carries the small shared enums the provider
// parsers use, so each parser stays a direct port of the quota-pacer original.
package reading

import "time"

// PlanType is the subscription plan a probe classified, when it can.
type PlanType string

// Plan types.
const (
	PlanTypeUnknown PlanType = "unknown"
	PlanTypeFree    PlanType = "free"
	PlanTypePlus    PlanType = "plus"
	PlanTypePro     PlanType = "pro"
	PlanTypeTeam    PlanType = "team"
)

// QuotaWindow is one usage window as a parser reports it, with the remaining share still
// available rather than the used share.
type QuotaWindow struct {
	Duration  time.Duration
	Remaining int64
	ResetAt   time.Time
}

// Status is the outcome of one probe.
type Status string

// Probe outcomes.
const (
	StatusReady       Status = "ready"
	StatusProbeFailed Status = "probe_failed"
)

// Model group names. An empty Group means the reading applies to every model of the provider.
const (
	// GroupGemini is the Antigravity model group that serves Gemini models.
	GroupGemini = "gemini"
	// GroupClaudeGPT is the Antigravity model group that serves Claude, GPT and OpenAI models.
	GroupClaudeGPT = "claude_gpt"
)

// Model patterns restrict a window to matching model IDs. They mirror how CLIProxyAPI matches
// a model to a credential: a lower-case substring test.
var (
	// ModelsGemini matches Gemini models.
	ModelsGemini = []string{"gemini"}
	// ModelsClaudeGPT matches Claude, GPT and OpenAI models.
	ModelsClaudeGPT = []string{"claude", "gpt", "openai"}
)

// Window is one usage window of a reading: how long it is, how much of it is used, when it
// resets, and which models it applies to.
type Window struct {
	Minutes     float64
	UsedPercent float64
	ResetsAt    time.Time
	Models      []string
}

// WindowFrom converts a parser window into a reading window.
func WindowFrom(quota QuotaWindow, models []string) Window {
	used := float64(100 - quota.Remaining)
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	return Window{
		Minutes:     quota.Duration.Minutes(),
		UsedPercent: used,
		ResetsAt:    quota.ResetAt,
		Models:      models,
	}
}
