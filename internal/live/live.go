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
	"time"

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

// Stats is Live pick usage recorded by this plugin.
type Stats struct {
	Picks      int64
	Rejects    int64
	LastAuthID string
	LastAt     time.Time
	// Limits is auth ID alias → Live usage-limit reset, copied for the status page.
	Limits map[string]time.Time
	// Skips is the sanitized skip list from the latest Live pick.
	Skips []Skip
	// Decisions is the bounded recent Live audit log, newest first.
	Decisions []Decision
}

// DecisionOutcome is one Live scheduler result in the audit log.
type DecisionOutcome string

const (
	OutcomePicked    DecisionOutcome = "picked"
	OutcomeRejected  DecisionOutcome = "rejected"
	OutcomeDelegated DecisionOutcome = "delegated"
)

// MaxDecisionHistory is the number of Live decisions kept for the status page and live_state_file.
const MaxDecisionHistory = 32

// Decision is one sanitized Live routing decision. It holds credential IDs and skip reasons, not
// tokens, status-message JSON, or SDP.
type Decision struct {
	ID       int64           `json:"id"`
	At       time.Time       `json:"at"`
	Outcome  DecisionOutcome `json:"outcome"`
	AuthID   string          `json:"auth_id,omitempty"`
	Matching int             `json:"matching"`
	Ready    int             `json:"ready"`
	Skips    []Skip          `json:"skips,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

// Snapshot is last-used Live pick state plus remembered usage-limit deadlines, saved across
// plugin reloads. It never includes tokens or status message text.
type Snapshot struct {
	LastAuthID string
	LastAt     time.Time
	Limits     map[string]time.Time
	Decisions  []Decision
}

// Selector picks a credential for Live requests.
type Selector struct {
	mu     sync.Mutex
	config Config
	stats  Stats
	// hostLimits are usage_limit_reached deadlines from the latest host listing.
	hostLimits map[string]time.Time
	// remembered are Live usage-limit deadlines kept after the host listing drops them.
	remembered map[string]time.Time
	// retries are host next_retry_after deadlines, including model-level Live cooldowns when
	// the host still offers the credential because Live selection omits the model.
	retries map[string]time.Time
	// blocked is alias → disabled/unavailable reason from the host listing.
	blocked map[string]string
	// lastSkips is the sanitized skip list from the latest Live pick.
	lastSkips []Skip
	// decisions is the bounded Live audit log, oldest first.
	decisions   []Decision
	decisionSeq int64
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

// Stats returns Live pick usage.
func (s *Selector) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	now := time.Now()
	out.Limits = s.activeLimitsLocked(now)
	if len(s.lastSkips) > 0 {
		out.Skips = make([]Skip, len(s.lastSkips))
		copy(out.Skips, s.lastSkips)
	}
	out.Decisions = copyDecisionsNewestFirst(s.decisions)
	return out
}

// Snapshot returns last-used pick state and remembered Live limits for persistence.
func (s *Selector) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{LastAuthID: s.stats.LastAuthID, LastAt: s.stats.LastAt}
	if limits := copyFutureLimits(s.remembered, time.Now()); len(limits) > 0 {
		out.Limits = limits
	}
	if len(s.decisions) > 0 {
		out.Decisions = append([]Decision(nil), s.decisions...)
	}
	return out
}

// Restore reapplies a saved last-used pick and remembered Live limits. Host listing state is
// not restored; the next Live pick refreshes that.
func (s *Selector) Restore(snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := strings.TrimSpace(snapshot.LastAuthID); id != "" {
		s.stats.LastAuthID = id
		s.stats.LastAt = snapshot.LastAt
	}
	if s.remembered == nil {
		s.remembered = make(map[string]time.Time)
	}
	mergeLimitMap(s.remembered, snapshot.Limits)
	if len(snapshot.Decisions) > 0 {
		cloned := make([]Decision, 0, len(snapshot.Decisions))
		for _, decision := range snapshot.Decisions {
			decision.Skips = copySkips(decision.Skips)
			cloned = append(cloned, decision)
		}
		if extra := len(cloned) - MaxDecisionHistory; extra > 0 {
			cloned = append([]Decision(nil), cloned[extra:]...)
		}
		s.decisions = cloned
		s.decisionSeq = 0
		for _, decision := range s.decisions {
			if decision.ID > s.decisionSeq {
				s.decisionSeq = decision.ID
			}
		}
	}
}

func copyDecisionsNewestFirst(in []Decision) []Decision {
	if len(in) == 0 {
		return nil
	}
	out := make([]Decision, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

func (s *Selector) activeLimitsLocked(now time.Time) map[string]time.Time {
	return unionFutureLimits(now, s.remembered, s.hostLimits)
}

// LastSkips returns the sanitized skip list from the latest Live pick.
func (s *Selector) LastSkips() []Skip {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lastSkips) == 0 {
		return nil
	}
	out := make([]Skip, len(s.lastSkips))
	copy(out, s.lastSkips)
	return out
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
	now := time.Now()
	limits := s.activeLimitsLocked(now)
	retries := s.retries
	blocked := s.blocked
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

	matching = orderByAllowlist(matching, config.AuthIDs)
	ready := make([]pluginapi.SchedulerAuthCandidate, 0, len(matching))
	skipped := make([]Skip, 0)
	for _, candidate := range matching {
		if reason := exclusionReason(candidate, limits, retries, blocked, now); reason != "" {
			skipped = append(skipped, Skip{AuthID: strings.TrimSpace(candidate.ID), Reason: reason})
			continue
		}
		ready = append(ready, candidate)
	}

	if len(ready) == 0 {
		if config.FailClosed {
			resp := pluginapi.SchedulerPickResponse{
				Handled:      true,
				Reject:       true,
				RejectCode:   RejectCodeLiveUnavailable,
				RejectReason: SanitizedReasonLiveNoAuth,
			}
			s.record(resp, skipped, len(matching), 0)
			return resp, true
		}
		s.record(pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: config.Strategy}, skipped, len(matching), 0)
		return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: config.Strategy}, true
	}

	selectedID := strings.TrimSpace(ready[0].ID)
	if config.Strategy == StrategyRoundRobin {
		s.mu.Lock()
		index := int(s.rrIndex % uint64(len(ready)))
		s.rrIndex++
		s.mu.Unlock()
		selectedID = strings.TrimSpace(ready[index].ID)
	}
	resp := pluginapi.SchedulerPickResponse{Handled: true, AuthID: selectedID}
	s.record(resp, skipped, len(matching), len(ready))
	return resp, true
}

func (s *Selector) record(resp pluginapi.SchedulerPickResponse, skipped []Skip, matching, ready int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSkips = skipped
	decision := Decision{
		Matching: matching,
		Ready:    ready,
		Skips:    copySkips(skipped),
	}
	switch {
	case resp.Reject:
		s.stats.Rejects++
		decision.Outcome = OutcomeRejected
		decision.Reason = strings.TrimSpace(resp.RejectReason)
	case strings.TrimSpace(resp.DelegateBuiltin) != "":
		decision.Outcome = OutcomeDelegated
		decision.Reason = "delegated to " + strings.TrimSpace(resp.DelegateBuiltin)
	default:
		authID := strings.TrimSpace(resp.AuthID)
		if authID == "" {
			return
		}
		s.stats.Picks++
		s.stats.LastAuthID = authID
		s.stats.LastAt = time.Now()
		decision.Outcome = OutcomePicked
		decision.AuthID = authID
	}
	s.appendDecisionLocked(decision)
}

func (s *Selector) appendDecisionLocked(decision Decision) {
	s.decisionSeq++
	decision.ID = s.decisionSeq
	if decision.At.IsZero() {
		decision.At = time.Now().UTC()
	}
	s.decisions = append(s.decisions, decision)
	if extra := len(s.decisions) - MaxDecisionHistory; extra > 0 {
		s.decisions = append([]Decision(nil), s.decisions[extra:]...)
	}
}

func copySkips(in []Skip) []Skip {
	if len(in) == 0 {
		return nil
	}
	out := make([]Skip, len(in))
	copy(out, in)
	return out
}

// orderByAllowlist returns matching credentials in live_auth_ids order. Host candidate order is
// weekly-soonest text ranking and is not the Live fill-first sequence.
func orderByAllowlist(matching []pluginapi.SchedulerAuthCandidate, authIDs []string) []pluginapi.SchedulerAuthCandidate {
	if len(matching) <= 1 || len(authIDs) == 0 {
		return matching
	}
	used := make(map[string]struct{}, len(matching))
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(matching))
	for _, want := range authIDs {
		allow := map[string]struct{}{strings.TrimSpace(want): {}}
		for _, candidate := range matching {
			id := strings.TrimSpace(candidate.ID)
			if id == "" {
				continue
			}
			if _, seen := used[id]; seen {
				continue
			}
			if candidateMatchesAllowlist(candidate, allow) {
				out = append(out, candidate)
				used[id] = struct{}{}
				break
			}
		}
	}
	for _, candidate := range matching {
		id := strings.TrimSpace(candidate.ID)
		if id == "" {
			continue
		}
		if _, seen := used[id]; seen {
			continue
		}
		out = append(out, candidate)
		used[id] = struct{}{}
	}
	return out
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
