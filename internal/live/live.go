// Package live selects credentials for CLIProxyAPI's Codex Live/Realtime path.
//
// The classifier and the Live branch of the selector are copied from cpa-live-voice so this
// plugin can be the only scheduler installed. Unlike that plugin, an unconfigured Live request
// falls through to the host's built-in scheduler instead of being delegated, and a Live request
// is never answered by the weekly-soonest ranking.
package live

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Live request classification.
const (
	// Provider is the provider key Codex Live traffic arrives under.
	Provider = "codex"
	// Model is the canonical Codex Live model ID.
	Model = "gpt-live-1-codex"
)

// RejectCodeLiveUnavailable is the reject code returned when no allowlisted credential is
// available and fail_closed is set.
const RejectCodeLiveUnavailable = "live_oauth_unavailable"

// Sanitized reject and delegation reasons, matching cpa-live-voice.
const (
	SanitizedReasonLiveNoAuth = "no eligible live voice OAuth credential available"
	SanitizedReasonFailOpen   = "no configured live match; fail_closed=false delegated to built-in"
)

// Built-in delegation strategies.
const (
	// StrategyFillFirst delegates to CLIProxyAPI's fill-first scheduler.
	StrategyFillFirst = pluginapi.SchedulerBuiltinFillFirst
	// StrategyRoundRobin delegates to CLIProxyAPI's round-robin scheduler.
	StrategyRoundRobin = pluginapi.SchedulerBuiltinRoundRobin
)

// DefaultStrategy is the selection order across allowlisted Live credentials.
const DefaultStrategy = StrategyFillFirst

// Config is the validated Live Voice configuration.
type Config struct {
	// AuthIDs is the allowlist of credential IDs Live traffic may use; empty disables the Live
	// branch entirely.
	AuthIDs []string
	// Strategy orders the matching credentials: fill-first or round-robin.
	Strategy string
	// FailClosed rejects a Live request when no allowlisted credential is available, instead of
	// delegating to the built-in scheduler.
	FailClosed bool
}

// Enabled reports whether Live requests are handled by this plugin.
func (c Config) Enabled() bool {
	return len(c.AuthIDs) > 0
}

// Selector picks a credential for Live requests.
type Selector struct {
	mu     sync.Mutex
	config Config
	// rrIndex is the next round-robin position across matching credentials.
	rrIndex uint64
}

// NewSelector returns a selector with no allowlist, which leaves Live requests to the host.
func NewSelector() *Selector {
	return &Selector{config: Config{Strategy: DefaultStrategy}}
}

// Configure replaces the Live configuration.
func (s *Selector) Configure(config Config) {
	if config.Strategy == "" {
		config.Strategy = DefaultStrategy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = config
}

// Config returns the active configuration.
func (s *Selector) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

// IsLiveRequest reports whether a scheduler request came from CPA's Codex Live path.
func IsLiveRequest(req pluginapi.SchedulerPickRequest) bool {
	if !hasProvider(req, Provider) {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(req.Model))
	if model == Model {
		return true
	}
	if model != "" || len(req.Candidates) == 0 {
		return false
	}
	// CPA v8.0.12 omits the model when its Live handler calls SelectAuthByKind, and that filter
	// treats Codex PAT files with access_token metadata as oauth even when auth_kind=pat. Only
	// an explicit API-key marker means this empty-model call is ordinary traffic.
	for _, candidate := range req.Candidates {
		if isExplicitAPIKeyKind(candidate.Attributes["auth_kind"]) {
			return false
		}
	}
	return true
}

func isExplicitAPIKeyKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "apikey", "api_key", "api-key":
		return true
	default:
		return false
	}
}

func hasProvider(req pluginapi.SchedulerPickRequest, provider string) bool {
	if strings.EqualFold(strings.TrimSpace(req.Provider), provider) {
		return true
	}
	for _, candidate := range req.Providers {
		if strings.EqualFold(strings.TrimSpace(candidate), provider) {
			return true
		}
	}
	return false
}

// Pick handles one Live request. handled is false when the request is not a Live request or no
// allowlist is configured, so the caller can fall through to the weekly-soonest scheduler and
// the host's built-in scheduler.
func (s *Selector) Pick(req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool) {
	if !IsLiveRequest(req) {
		return pluginapi.SchedulerPickResponse{}, false
	}
	s.mu.Lock()
	config := s.config
	s.mu.Unlock()
	if !config.Enabled() {
		return pluginapi.SchedulerPickResponse{}, false
	}

	allowlist := make(map[string]struct{}, len(config.AuthIDs))
	for _, id := range config.AuthIDs {
		allowlist[id] = struct{}{}
	}
	matching := make([]pluginapi.SchedulerAuthCandidate, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		if candidateMatchesAllowlist(candidate, allowlist) {
			matching = append(matching, candidate)
		}
	}

	if len(matching) == 0 {
		if config.FailClosed {
			return pluginapi.SchedulerPickResponse{
				Handled:      true,
				Reject:       true,
				RejectCode:   RejectCodeLiveUnavailable,
				RejectReason: SanitizedReasonLiveNoAuth,
			}, true
		}
		return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: config.Strategy}, true
	}

	selectedID := strings.TrimSpace(matching[0].ID)
	if config.Strategy == StrategyRoundRobin {
		s.mu.Lock()
		index := int(s.rrIndex % uint64(len(matching)))
		s.rrIndex++
		s.mu.Unlock()
		selectedID = strings.TrimSpace(matching[index].ID)
	}
	return pluginapi.SchedulerPickResponse{Handled: true, AuthID: selectedID}, true
}

func candidateMatchesAllowlist(candidate pluginapi.SchedulerAuthCandidate, allowlist map[string]struct{}) bool {
	for _, id := range candidateAliasIDs(candidate) {
		if _, okAllow := allowlist[id]; okAllow {
			return true
		}
	}
	return false
}

// candidateAliasIDs lists the IDs a configured allowlist entry may match: the candidate ID, its
// path and source attributes, and the basename of each.
func candidateAliasIDs(candidate pluginapi.SchedulerAuthCandidate) []string {
	values := []string{candidate.ID}
	if candidate.Attributes != nil {
		values = append(values, candidate.Attributes["path"], candidate.Attributes["source"])
	}
	return uniqueIDAliases(values...)
}

func uniqueIDAliases(ids ...string) []string {
	seen := make(map[string]struct{}, len(ids)*2)
	out := make([]string, 0, len(ids)*2)
	var add func(value string)
	add = func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, okSeen := seen[value]; okSeen {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
		base := filepath.Base(value)
		if base != "" && base != "." && base != string(filepath.Separator) {
			add(base)
		}
	}
	for _, id := range ids {
		add(id)
	}
	return out
}
