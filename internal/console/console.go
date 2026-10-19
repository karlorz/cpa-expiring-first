// Package console serves the plugin's read-only status page and its management routes.
//
// The page is a static resource, reachable in a browser at
// /v0/resource/plugins/expiring-first/status. The JSON routes under
// /v0/management/plugins/expiring-first are guarded by the host's management key. Nothing on the
// page edits configuration: configuration stays in CLIProxyAPI's plugin config fields.
package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/live"
	"github.com/karlorz/cpa-expiring-first/internal/probe"
	"github.com/karlorz/cpa-expiring-first/internal/router"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Route paths. Management and resource requests are both normalized to the plugin-relative
// form before matching, so one set of paths serves every prefix the host uses.
const (
	// PinsPath lists the current pins.
	PinsPath = "/plugins/expiring-first/pins"
	// QuotaPath lists the readings the ranking currently uses, with auth IDs redacted.
	QuotaPath = "/plugins/expiring-first/quota"
	// RefreshPath triggers one probe cycle.
	RefreshPath = "/plugins/expiring-first/refresh"
	// StatusPath is the plugin-relative path of the browser resource page.
	StatusPath = "/plugins/expiring-first/status"
	// ResourcePath is the host-relative resource route the host resolves to the page.
	ResourcePath = "/status"
	// ResourceURL is the absolute path the page is reachable at in a browser.
	ResourceURL = "/v0/resource/plugins/expiring-first/status"
)

// RepresentativePick is one next-pick preview row on the console.
type RepresentativePick struct {
	Provider string
	Model    string
}

// RepresentativePicks is the set of provider+model pairs the page uses to preview the next pick.
// Antigravity has two rows because gemini and claude_gpt are metered separately.
var RepresentativePicks = []RepresentativePick{
	{Provider: "antigravity", Model: "gemini-3-pro"},
	{Provider: "antigravity", Model: "claude-sonnet-5"},
	{Provider: "claude", Model: "claude-sonnet-5"},
	{Provider: "codex", Model: "gpt-5-codex"},
	{Provider: "xai", Model: "grok-4"},
}

// Sources is the read-only view the console renders. main.go implements it over the scheduler,
// the probe worker and the host callbacks.
type Sources interface {
	Pins() []router.PinView
	// Quota returns the readings the ranking uses.
	Quota() router.Quota
	// Accounts returns the latest probe outcome per credential.
	Accounts() []probe.Account
	// PickStats returns the per-provider pick counters.
	PickStats() map[string]router.PickStat
	// Candidates returns the credentials the host currently offers, keyed by provider. One call
	// covers every provider, so the page costs the host a single listing.
	Candidates(ctx context.Context) (map[string][]pluginapi.SchedulerAuthCandidate, error)
	// Preview returns the credential the ranking would pick next, without changing a pin.
	Preview(candidates []pluginapi.SchedulerAuthCandidate, provider, model string, now time.Time) (string, bool)
	// Config describes the active configuration.
	Config() router.Config
	// Live describes the active Live Voice configuration.
	Live() live.Config
	// Refresh runs one probe cycle.
	Refresh(ctx context.Context) error
	// Enabled reports whether routing is active.
	Enabled() bool
	// Version is the plugin version.
	Version() string
	// ProbeInterval is the configured probe interval.
	ProbeInterval() time.Duration
	// LastProbeAt is when the last probe cycle published its readings.
	LastProbeAt() time.Time
}

// Handler serves the console routes.
type Handler struct {
	sources Sources
	now     func() time.Time
	// refreshMu keeps a manual refresh from overlapping the background cycle.
	refreshMu sync.Mutex
}

// NewHandler returns a handler over sources.
func NewHandler(sources Sources) *Handler {
	return &Handler{sources: sources, now: time.Now}
}

// Routes returns the management routes to register with the host.
func Routes() []pluginapi.ManagementRoute {
	return []pluginapi.ManagementRoute{
		{Method: http.MethodGet, Path: PinsPath, Description: "Current pins: the credential each provider and model route is routed to."},
		{Method: http.MethodGet, Path: QuotaPath, Description: "Quota readings used by the ranking, with auth IDs redacted."},
		{Method: http.MethodPost, Path: RefreshPath, Description: "Run one quota probe cycle now."},
	}
}

// Resources returns the browser-navigable resource routes to register with the host. The path is
// relative to /v0/resource/plugins/expiring-first/, so the page is reachable at ResourceURL.
func Resources() []pluginapi.ResourceRoute {
	return []pluginapi.ResourceRoute{{
		Path:        ResourcePath,
		Menu:        "Expiring First",
		Description: "Quota windows, next picks per provider and pick counts for the expiring-first scheduler.",
	}}
}

// Handle processes one management request.
func (h *Handler) Handle(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	path := normalizePath(req.Path)
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	switch {
	case path == StatusPath && method == http.MethodGet:
		return h.statusPage(ctx), nil
	case path == PinsPath && method == http.MethodGet:
		return jsonResponse(http.StatusOK, h.pinsView()), nil
	case path == QuotaPath && method == http.MethodGet:
		return jsonResponse(http.StatusOK, h.quotaView(ctx)), nil
	case path == RefreshPath && method == http.MethodPost:
		return h.refresh(ctx), nil
	default:
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "route not found"}), nil
	}
}

// normalizePath maps the host's absolute route back to the plugin-relative path this handler
// switches on, so the management prefix, the resource prefix and the legacy prefix all work.
func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	for _, prefix := range []string{"/v0/management", "/v0/resource"} {
		if strings.HasPrefix(path, prefix) {
			return strings.TrimPrefix(path, prefix)
		}
	}
	return path
}

// PinView is the JSON shape of one pin.
type PinView struct {
	Provider     string     `json:"provider"`
	Model        string     `json:"model"`
	AuthID       string     `json:"auth_id"`
	PinnedAt     time.Time  `json:"pinned_at"`
	LastPickedAt time.Time  `json:"last_picked_at"`
	MissingSince *time.Time `json:"missing_since,omitempty"`
}

// QuotaView is the JSON shape of one credential's readings.
type QuotaView struct {
	AuthID    string            `json:"auth_id"`
	Auth      string            `json:"auth"`
	Provider  string            `json:"provider"`
	Group     string            `json:"antigravity_group,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
	ProbeAge  string            `json:"probe_age"`
	Windows   []QuotaWindowView `json:"windows"`
	Error     string            `json:"error,omitempty"`
}

// QuotaWindowView is the JSON shape of one usage window.
type QuotaWindowView struct {
	Name          string     `json:"name"`
	WindowMinutes float64    `json:"window_minutes"`
	UsedPercent   float64    `json:"used_percent"`
	RemainingPct  float64    `json:"remaining_percent"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	ResetIn       string     `json:"reset_in,omitempty"`
	Models        []string   `json:"models,omitempty"`
	Exhausted     bool       `json:"exhausted"`
}

// NextPickView is the JSON shape of one provider's next pick.
type NextPickView struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	AuthID   string `json:"auth_id"`
	Auth     string `json:"auth"`
	Ranked   bool   `json:"ranked"`
}

// PickCountView is the JSON shape of one provider's pick counters.
type PickCountView struct {
	Provider string `json:"provider"`
	Handled  int64  `json:"handled"`
	Ranked   int64  `json:"ranked"`
}

// StatusView is the JSON payload embedded into the page and returned by the JSON routes.
type StatusView struct {
	Version       string          `json:"version"`
	Enabled       bool            `json:"enabled"`
	Providers     []string        `json:"providers,omitempty"`
	ProbeInterval string          `json:"probe_interval"`
	LastProbeAt   time.Time       `json:"last_probe_at"`
	LiveEnabled   bool            `json:"live_enabled"`
	LivePoolSize  int             `json:"live_pool_size"`
	LiveStrategy  string          `json:"live_strategy"`
	Pins          []PinView       `json:"pins"`
	Quota         []QuotaView     `json:"quota"`
	NextPicks     []NextPickView  `json:"next_picks"`
	PickCounts    []PickCountView `json:"pick_counts"`
	GeneratedAt   time.Time       `json:"generated_at"`
}

func (h *Handler) pinsView() []PinView {
	pins := h.sources.Pins()
	views := make([]PinView, 0, len(pins))
	for _, pin := range pins {
		view := PinView{
			Provider:     pin.Provider,
			Model:        pin.Model,
			AuthID:       Redact(pin.AuthID),
			PinnedAt:     pin.PinnedAt.UTC(),
			LastPickedAt: pin.LastPickedAt.UTC(),
		}
		if !pin.MissingSince.IsZero() {
			missingSince := pin.MissingSince.UTC()
			view.MissingSince = &missingSince
		}
		views = append(views, view)
	}
	return views
}

// quotaView merges the readings in use with the latest probe outcomes, so the page shows both
// the numbers the ranking used and any probe error for the same credential.
func (h *Handler) quotaView(ctx context.Context) []QuotaView {
	now := h.now().UTC()
	quota := h.sources.Quota()
	accounts := h.sources.Accounts()

	views := make(map[string]*QuotaView, len(quota)+len(accounts))
	order := make([]string, 0, len(quota)+len(accounts))
	ensure := func(authID string) *QuotaView {
		if view, okView := views[authID]; okView {
			return view
		}
		view := &QuotaView{AuthID: Redact(authID), Auth: Redact(authID)}
		views[authID] = view
		order = append(order, authID)
		return view
	}

	for authID, account := range quota {
		view := ensure(authID)
		view.UpdatedAt = account.UpdatedAt.UTC()
		view.ProbeAge = ageString(now, account.UpdatedAt)
		for _, window := range account.Windows {
			view.Windows = append(view.Windows, windowView(now, window))
		}
	}
	for _, account := range accounts {
		view := ensure(account.AuthID)
		if view.Provider == "" {
			view.Provider = account.Provider
		}
		if view.Group == "" {
			view.Group = account.Group
		}
		if account.Error != "" {
			view.Error = account.Error
		}
		if view.UpdatedAt.IsZero() {
			view.UpdatedAt = account.ObservedAt.UTC()
			view.ProbeAge = ageString(now, account.ObservedAt)
		}
	}

	out := make([]QuotaView, 0, len(order))
	for _, authID := range order {
		view := views[authID]
		sort.SliceStable(view.Windows, func(i, j int) bool {
			return view.Windows[i].WindowMinutes > view.Windows[j].WindowMinutes
		})
		out = append(out, *view)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].AuthID < out[j].AuthID
	})
	return out
}

func windowView(now time.Time, window router.QuotaWindow) QuotaWindowView {
	view := QuotaWindowView{
		Name:          windowName(window.Minutes),
		WindowMinutes: window.Minutes,
		UsedPercent:   window.UsedPercent,
		RemainingPct:  100 - window.UsedPercent,
		Models:        window.Models,
		Exhausted:     window.UsedPercent >= 100 && window.ResetsAt.After(now),
	}
	if !window.ResetsAt.IsZero() {
		resetsAt := window.ResetsAt.UTC()
		view.ResetsAt = &resetsAt
		view.ResetIn = ageString(now, window.ResetsAt)
	}
	return view
}

func windowName(minutes float64) string {
	switch {
	case minutes >= 28*24*60:
		return "monthly"
	case minutes >= 6*24*60:
		return "weekly"
	case minutes >= 20*60:
		return "daily"
	default:
		return "5h"
	}
}

func (h *Handler) nextPicks(ctx context.Context) []NextPickView {
	now := h.now().UTC()

	// One host listing serves every provider; a failed listing leaves the picks unknown rather
	// than failing the page.
	byProvider, errCandidates := h.sources.Candidates(ctx)
	if errCandidates != nil {
		byProvider = nil
	}

	views := make([]NextPickView, 0, len(RepresentativePicks))
	for _, pick := range RepresentativePicks {
		view := NextPickView{Provider: pick.Provider, Model: pick.Model}
		if authID, okPick := h.sources.Preview(byProvider[pick.Provider], pick.Provider, pick.Model, now); okPick {
			view.AuthID, view.Auth, view.Ranked = Redact(authID), Redact(authID), true
		}
		views = append(views, view)
	}
	return views
}

func (h *Handler) pickCounts() []PickCountView {
	stats := h.sources.PickStats()
	providers := make([]string, 0, len(stats))
	for provider := range stats {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	views := make([]PickCountView, 0, len(providers))
	for _, provider := range providers {
		views = append(views, PickCountView{Provider: provider, Handled: stats[provider].Handled, Ranked: stats[provider].Ranked})
	}
	return views
}

func (h *Handler) statusView(ctx context.Context) StatusView {
	config := h.sources.Live()
	return StatusView{
		Version:       h.sources.Version(),
		Enabled:       h.sources.Enabled(),
		Providers:     h.sources.Config().Providers(),
		ProbeInterval: h.sources.ProbeInterval().String(),
		LastProbeAt:   h.sources.LastProbeAt().UTC(),
		LiveEnabled:   config.Enabled(),
		LivePoolSize:  len(config.AuthIDs),
		LiveStrategy:  config.Strategy,
		Pins:          h.pinsView(),
		Quota:         h.quotaView(ctx),
		NextPicks:     h.nextPicks(ctx),
		PickCounts:    h.pickCounts(),
		GeneratedAt:   h.now().UTC(),
	}
}

func (h *Handler) statusPage(ctx context.Context) pluginapi.ManagementResponse {
	payload, errMarshal := json.Marshal(h.statusView(ctx))
	if errMarshal != nil {
		payload = []byte(`null`)
	}
	html := strings.Replace(StatusHTML, "__EXPIRING_FIRST_BOOTSTRAP_JSON__", string(payload), 1)
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(html),
	}
}

func (h *Handler) refresh(ctx context.Context) pluginapi.ManagementResponse {
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	if errRefresh := h.sources.Refresh(ctx); errRefresh != nil {
		return jsonResponse(http.StatusBadGateway, map[string]string{"error": errRefresh.Error()})
	}
	return jsonResponse(http.StatusOK, h.statusView(ctx))
}

func jsonResponse(status int, body any) pluginapi.ManagementResponse {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		status, raw = http.StatusInternalServerError, []byte(`{"error":"encode response"}`)
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       raw,
	}
}

// Redact shortens a credential ID so the page and the JSON routes never print a full auth file
// name. The first characters stay readable, so two credentials remain distinguishable.
func Redact(authID string) string {
	trimmed := strings.TrimSpace(authID)
	if trimmed == "" {
		return ""
	}
	const keep = 4
	if len(trimmed) <= keep+2 {
		return trimmed[:1] + strings.Repeat("*", len(trimmed)-1)
	}
	return fmt.Sprintf("%s***%s", trimmed[:keep], trimmed[len(trimmed)-2:])
}

// ageString renders how long ago or how far ahead a moment is, in a compact form.
func ageString(now, moment time.Time) string {
	if moment.IsZero() {
		return ""
	}
	delta := now.Sub(moment)
	suffix := "ago"
	if delta < 0 {
		delta, suffix = -delta, "left"
	}
	switch {
	case delta < time.Minute:
		return fmt.Sprintf("%ds %s", int(delta.Seconds()), suffix)
	case delta < time.Hour:
		return fmt.Sprintf("%dm %s", int(delta.Minutes()), suffix)
	case delta < 48*time.Hour:
		return fmt.Sprintf("%dh %s", int(delta.Hours()), suffix)
	default:
		return fmt.Sprintf("%dd %s", int(delta.Hours()/24), suffix)
	}
}
