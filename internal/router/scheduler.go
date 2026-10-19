// Package router implements expiring-first credential selection for CLIProxyAPI.
//
// With fresh quota readings, every pick goes to the available credential whose longest
// window (usually weekly) resets soonest and still has quota, so quota that would expire
// unused is spent first. Without them it falls back to sticky fill-first: each
// provider+model route stays on the credential that last served it while that credential
// stays available. When it drops out, the pin moves to the fill-first choice (highest
// priority, then lowest auth ID) as soon as the host reports the credential locked out (a
// quota cooldown or a disabled credential), or after the configured grace period when the
// host has no conclusive answer. Both modes record the serving credential as the route's
// pin; pins live in memory and, when a pins file is configured, are saved on every change so
// they survive a host restart.
package router

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// QuotaCooldownFloor separates quota lockouts from short-lived cooldowns. CLIProxyAPI's
// cooldowns for transient errors (one minute by default), auth failures (30 minutes), its own
// quota backoff without a reset hint and Cloudflare challenges (both capped at 30 minutes) stay
// within it, while a provider quota reset deadline is usually hours away. Two longer non-quota
// cooldowns also move the pin, which is intended because the credential cannot serve the route
// meanwhile: a model the credential does not support (12 hours) and an error carrying a long
// Retry-After.
const QuotaCooldownFloor = 30 * time.Minute

// Reason explains why a pin moved.
type Reason int

const (
	// ReasonGraceExpired: the pinned credential stayed absent for the configured grace period.
	ReasonGraceExpired Reason = iota
	// ReasonCooldown: the host reported a cooldown longer than the lockout threshold.
	ReasonCooldown
	// ReasonDisabled: the host reported the pinned credential disabled.
	ReasonDisabled
	// ReasonQuota: the quota ranking picked another credential.
	ReasonQuota
)

// Switch describes a pin that moved to another credential.
type Switch struct {
	Provider   string
	Model      string
	From       string
	To         string
	Reason     Reason
	MissingFor time.Duration
	// Until is the reported end of the cooldown for ReasonCooldown, and the reset of the new
	// credential's longest window for ReasonQuota (zero when that window has not started).
	Until time.Time
}

// AuthState is the host's runtime view of one credential.
type AuthState struct {
	// AuthIndex is the host's stable credential index, used to clear its cooldown.
	AuthIndex      string
	Disabled       bool
	NextRetryAfter time.Time
}

// Inspector reports the host's runtime state for a credential. ok is false when the host gave
// no usable answer, in which case the scheduler falls back to the grace period.
type Inspector interface {
	Inspect(authID string) (state AuthState, ok bool)
}

type routeKey struct {
	provider string
	model    string
}

type pin struct {
	authID string
	// pinnedAt is when the route was pinned to authID; lastPickedAt is the latest pick that served it.
	pinnedAt     time.Time
	lastPickedAt time.Time
	// missingSince is when a pick first found authID absent from the candidates; zero while present.
	missingSince time.Time
}

// SavedPin is the part of a pin that survives a restart.
type SavedPin struct {
	Provider string
	Model    string
	AuthID   string
	PinnedAt time.Time
}

// Scheduler holds the pins for every route. It is safe for concurrent use.
type Scheduler struct {
	mu        sync.Mutex
	cfg       Config
	pins      map[routeKey]*pin
	inspector Inspector
	// quota supplies quota readings; nil routes stickily only.
	quota QuotaSource

	// cooldownSeen records when each host cooldown, keyed by credential and deadline, was first
	// seen; see StaleCooldowns.
	cooldownMu   sync.Mutex
	cooldownSeen map[cooldownKey]time.Time

	// statsMu guards the per-provider pick counters shown on the console page.
	statsMu sync.Mutex
	stats   map[string]PickStat

	// saveMu serializes saves. Each save snapshots the pins while holding it, so the last
	// write always carries the newest state. Lock order: saveMu, then mu.
	saveMu sync.Mutex
	save   func([]SavedPin)
}

// NewScheduler returns a scheduler with no pins. inspector may be nil, in which case pins move
// only after the grace period.
func NewScheduler(cfg Config, inspector Inspector) *Scheduler {
	return &Scheduler{cfg: cfg, pins: make(map[routeKey]*pin), inspector: inspector}
}

// Configure replaces the configuration and keeps existing pins.
func (s *Scheduler) Configure(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// Configuration returns the active configuration.
func (s *Scheduler) Configuration() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// UseQuota sets where quota readings come from; nil routes stickily only.
func (s *Scheduler) UseQuota(source QuotaSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quota = source
}

// Persist restores saved pins for routes that have no pin in memory yet, then passes every
// later pin change to save. A nil save stops persisting. Restored pins are treated like any
// other: a pin to a credential that no longer exists moves once the grace period expires.
func (s *Scheduler) Persist(saved []SavedPin, save func([]SavedPin)) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.save = save

	s.mu.Lock()
	for _, p := range saved {
		key := routeKey{provider: p.Provider, model: p.Model}
		if _, pinned := s.pins[key]; !pinned {
			s.pins[key] = &pin{authID: p.AuthID, pinnedAt: p.PinnedAt, lastPickedAt: p.PinnedAt}
		}
	}
	s.mu.Unlock()

	// Write the merged state at once, so the file matches memory from the start.
	s.saveLocked()
}

// Pick chooses a candidate for the request. It returns Handled=false when the request is out of
// scope, so the host falls back to its configured built-in strategy. The returned Switch is
// non-nil only when this pick moved a pin.
func (s *Scheduler) Pick(req pluginapi.SchedulerPickRequest, now time.Time) (pluginapi.SchedulerPickResponse, *Switch) {
	resp, moved, created := s.pick(req, now)
	if created || moved != nil {
		s.saveMu.Lock()
		s.saveLocked()
		s.saveMu.Unlock()
	}
	return resp, moved
}

// saveLocked hands the current pins to the save hook. The caller holds saveMu.
func (s *Scheduler) saveLocked() {
	if s.save == nil {
		return
	}
	s.mu.Lock()
	saved := make([]SavedPin, 0, len(s.pins))
	for key, current := range s.pins {
		saved = append(saved, SavedPin{Provider: key.provider, Model: key.model, AuthID: current.authID, PinnedAt: current.pinnedAt})
	}
	s.mu.Unlock()
	sort.Slice(saved, func(i, j int) bool {
		if saved[i].Provider != saved[j].Provider {
			return saved[i].Provider < saved[j].Provider
		}
		return saved[i].Model < saved[j].Model
	})
	s.save(saved)
}

// pick implements Pick. created reports that this pick pinned a route for the first time.
func (s *Scheduler) pick(req pluginapi.SchedulerPickRequest, now time.Time) (resp pluginapi.SchedulerPickResponse, moved *Switch, created bool) {
	unhandled := pluginapi.SchedulerPickResponse{}

	provider, ok := routeProvider(req)
	if !ok || len(req.Candidates) == 0 {
		return unhandled, nil, false
	}
	// The host cannot tell the plugin whether a Codex request arrived over a downstream
	// websocket, so leave websocket-capable Codex pools to the built-in websocket preference.
	if provider == "codex" && anyWebsocketCandidate(req.Candidates) {
		return unhandled, nil, false
	}

	key := routeKey{provider: provider, model: baseModel(req.Model)}

	s.mu.Lock()
	if !s.cfg.Applies(provider) {
		s.mu.Unlock()
		return unhandled, nil, false
	}
	cfg, source := s.cfg, s.quota
	s.mu.Unlock()

	// Count handled picks per provider so the console page can show this plugin serving them.
	ranked := false
	defer func() {
		if resp.Handled {
			s.recordPick(provider, ranked)
		}
	}()

	// Read quota outside the lock: the source may touch the file system.
	if source != nil {
		if quota := source.Load(); quota != nil {
			if !cfg.ProbeAntigravityClaudeGPT() {
				quota = dropClaudeGPTQuota(quota)
			}
			if choice, ok := rankByQuota(req.Candidates, quota, key.model, now, cfg.MaxAge(), cfg.ReservePercent()); ok {
				ranked = true
				return s.followQuota(key, choice, now)
			}
		}
	}

	fallback := fillFirst(req.Candidates)

	s.mu.Lock()
	current, pinned := s.pins[key]
	if !pinned {
		s.pins[key] = newPin(fallback, now)
		s.mu.Unlock()
		return handled(fallback), nil, true
	}
	if containsCandidate(req.Candidates, current.authID) {
		current.missingSince = time.Time{}
		current.lastPickedAt = now
		s.mu.Unlock()
		return handled(current.authID), nil, false
	}

	// The pinned credential is absent. That may be for this pick only (a retry that already
	// tried it, a request restricted to other credentials, a short cooldown), so the pin moves
	// only on conclusive host state or once the absence outlasts the grace period.
	if current.missingSince.IsZero() {
		current.missingSince = now
	}
	missingFor := now.Sub(current.missingSince)
	if missingFor >= s.cfg.SwitchAfter() {
		moved := s.movePinLocked(key, fallback, now, Switch{Reason: ReasonGraceExpired, MissingFor: missingFor})
		s.mu.Unlock()
		return handled(fallback), moved, false
	}
	pinnedID := current.authID
	lockout := lockoutThreshold(s.cfg.SwitchAfter())
	inspector := s.inspector
	s.mu.Unlock()

	// Ask the host outside the lock so concurrent picks are not serialized behind the callback.
	if inspector == nil {
		return handled(fallback), nil, false
	}
	state, known := inspector.Inspect(pinnedID)
	if !known {
		return handled(fallback), nil, false
	}
	var event Switch
	switch {
	case state.Disabled:
		event = Switch{Reason: ReasonDisabled, MissingFor: missingFor}
	case state.NextRetryAfter.Sub(now) > lockout:
		event = Switch{Reason: ReasonCooldown, MissingFor: missingFor, Until: state.NextRetryAfter}
	default:
		return handled(fallback), nil, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Another pick may have moved the pin while the lock was released.
	if current, pinned := s.pins[key]; !pinned || current.authID != pinnedID {
		return handled(fallback), nil, false
	}
	return handled(fallback), s.movePinLocked(key, fallback, now, event), false
}

// followQuota serves the quota ranking's choice and records it as the route's pin, so the
// sticky fallback continues from it when quota readings go stale.
func (s *Scheduler) followQuota(key routeKey, choice quotaChoice, now time.Time) (pluginapi.SchedulerPickResponse, *Switch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, pinned := s.pins[key]
	switch {
	case !pinned:
		s.pins[key] = newPin(choice.authID, now)
		return handled(choice.authID), nil, true
	case current.authID == choice.authID:
		current.missingSince = time.Time{}
		current.lastPickedAt = now
		return handled(choice.authID), nil, false
	default:
		return handled(choice.authID), s.movePinLocked(key, choice.authID, now, Switch{Reason: ReasonQuota, Until: choice.resetsAt}), false
	}
}

func (s *Scheduler) movePinLocked(key routeKey, to string, now time.Time, event Switch) *Switch {
	event.Provider = key.provider
	event.Model = key.model
	event.From = s.pins[key].authID
	event.To = to
	s.pins[key] = newPin(to, now)
	return &event
}

func newPin(authID string, now time.Time) *pin {
	return &pin{authID: authID, pinnedAt: now, lastPickedAt: now}
}

// PinView is a read-only snapshot of one route's pin.
type PinView struct {
	Provider     string
	Model        string
	AuthID       string
	PinnedAt     time.Time
	LastPickedAt time.Time
	// MissingSince is when the pinned credential was first found unavailable; zero while it is available.
	MissingSince time.Time
}

// Pins returns every pin, ordered by provider then model.
func (s *Scheduler) Pins() []PinView {
	s.mu.Lock()
	views := make([]PinView, 0, len(s.pins))
	for key, current := range s.pins {
		views = append(views, PinView{
			Provider:     key.provider,
			Model:        key.model,
			AuthID:       current.authID,
			PinnedAt:     current.pinnedAt,
			LastPickedAt: current.lastPickedAt,
			MissingSince: current.missingSince,
		})
	}
	s.mu.Unlock()
	sort.Slice(views, func(i, j int) bool {
		if views[i].Provider != views[j].Provider {
			return views[i].Provider < views[j].Provider
		}
		return views[i].Model < views[j].Model
	})
	return views
}

// lockoutThreshold is the cooldown length that proves a quota lockout. It never drops below
// the grace period, so a cooldown the grace period would wait out never moves the pin early.
func lockoutThreshold(switchAfter time.Duration) time.Duration {
	return max(QuotaCooldownFloor, switchAfter)
}

// Preview returns the credential the ranking would pick for a model without changing any pin.
// It is used by the console page to show the next pick per provider. ok is false when no
// candidate has a usable fresh reading, which means the next pick follows the sticky pin or the
// host's built-in order instead.
func (s *Scheduler) Preview(candidates []pluginapi.SchedulerAuthCandidate, provider, model string, now time.Time) (string, bool) {
	s.mu.Lock()
	cfg, source := s.cfg, s.quota
	s.mu.Unlock()
	if source == nil || !cfg.Applies(provider) || len(candidates) == 0 || (normalizeProvider(provider) == "codex" && anyWebsocketCandidate(candidates)) {
		return "", false
	}
	quota := source.Load()
	if quota == nil {
		return "", false
	}
	if !cfg.ProbeAntigravityClaudeGPT() {
		quota = dropClaudeGPTQuota(quota)
	}
	choice, okChoice := rankByQuota(candidates, quota, baseModel(model), now, cfg.MaxAge(), cfg.ReservePercent())
	if !okChoice {
		return "", false
	}
	return choice.authID, true
}

// QuotaSnapshot returns the readings currently in use, keyed by auth ID. It is used by the
// console page; picks never call it.
func (s *Scheduler) QuotaSnapshot() Quota {
	s.mu.Lock()
	source := s.quota
	s.mu.Unlock()
	if source == nil {
		return nil
	}
	return source.Load()
}

// PickStat counts the picks served for one provider, and how many of them the weekly-soonest
// ranking decided, so the console page can show that this plugin is the active scheduler.
type PickStat struct {
	Handled int64
	Ranked  int64
}

// PickStats returns the pick counters per provider.
func (s *Scheduler) PickStats() map[string]PickStat {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	stats := make(map[string]PickStat, len(s.stats))
	for provider, stat := range s.stats {
		stats[provider] = stat
	}
	return stats
}

func (s *Scheduler) recordPick(provider string, ranked bool) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.stats == nil {
		s.stats = make(map[string]PickStat)
	}
	stat := s.stats[provider]
	stat.Handled++
	if ranked {
		stat.Ranked++
	}
	s.stats[provider] = stat
}

// routeProvider returns the single provider a request routes to. The host leaves Provider
// empty on its mixed-provider path even when Providers holds one entry; routes spanning
// several providers have no single provider to pin against.
func routeProvider(req pluginapi.SchedulerPickRequest) (string, bool) {
	if provider := normalizeProvider(req.Provider); provider != "" {
		return provider, len(req.Providers) <= 1
	}
	if len(req.Providers) == 1 {
		provider := normalizeProvider(req.Providers[0])
		return provider, provider != ""
	}
	return "", false
}

func handled(authID string) pluginapi.SchedulerPickResponse {
	return pluginapi.SchedulerPickResponse{AuthID: authID, Handled: true}
}

// fillFirst mirrors CLIProxyAPI's built-in fill-first order: highest priority, then lowest ID.
// candidates must be non-empty.
func fillFirst(candidates []pluginapi.SchedulerAuthCandidate) string {
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Priority > best.Priority || (candidate.Priority == best.Priority && candidate.ID < best.ID) {
			best = candidate
		}
	}
	return best.ID
}

func containsCandidate(candidates []pluginapi.SchedulerAuthCandidate, authID string) bool {
	for _, candidate := range candidates {
		if candidate.ID == authID {
			return true
		}
	}
	return false
}

func anyWebsocketCandidate(candidates []pluginapi.SchedulerAuthCandidate) bool {
	for _, candidate := range candidates {
		enabled, errParse := strconv.ParseBool(strings.TrimSpace(candidate.Attributes["websockets"]))
		if errParse == nil && enabled {
			return true
		}
	}
	return false
}

// baseModel drops a CLIProxyAPI thinking suffix such as "(high)" so every reasoning level of a
// model shares one pin, matching how the host matches credentials to models.
func baseModel(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.LastIndex(model, "("); open > 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:open])
	}
	return strings.ToLower(model)
}
