// Package console serves the plugin's read-only status page, the cached snapshot the page
// reloads, and the management routes.
//
// The page is a static resource, reachable in a browser at
// /v0/resource/plugins/expiring-first/status. It reloads its snapshot from the public resource
// /v0/resource/plugins/expiring-first/data; both are GETs that only read state and never start a
// probe. The JSON routes under /v0/management/plugins/expiring-first are guarded by the host's
// management key, and the probe trigger there is the only route that writes anything.
// Configuration stays in CLIProxyAPI's plugin config fields.
package console

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// RefreshPath triggers one probe cycle. The host exposes it on the authenticated
	// management prefix; the public resource prefix is GET-only, so it cannot be reached there.
	RefreshPath = "/plugins/expiring-first/refresh"
	// StatusPath is the plugin-relative path of the browser resource page.
	StatusPath = "/plugins/expiring-first/status"
	// StatusDataPath is the plugin-relative path of the cached snapshot as JSON. It is registered
	// both as a public resource and as a management route.
	StatusDataPath = "/plugins/expiring-first/data"
	// ResourcePath is the host-relative resource route the host resolves to the page.
	ResourcePath = "/status"
	// DataResourcePath is the host-relative resource route the host resolves to the snapshot.
	DataResourcePath = "/data"
	// ResourceURL is the absolute path the page is reachable at in a browser.
	ResourceURL = "/v0/resource/plugins/expiring-first/status"
	// DataURL is the absolute path the page reloads its snapshot from. It is same-origin with
	// the page and needs no management key.
	DataURL = "/v0/resource/plugins/expiring-first/data"
	// RefreshURL is the absolute management path the page posts to for an on-demand probe. It
	// requires the host's management key. The host mounts plugin management routes under
	// /v0/management only; it does not dispatch plugin-owned routes under /v8/management.
	RefreshURL = "/v0/management/plugins/expiring-first/refresh"
)

// Sources is the read-only view the console renders. main.go implements it over the scheduler,
// the probe worker and the host callbacks. Nothing here writes configuration or starts a probe:
// only Refresh does, and only the management route calls it.
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
	// Enabled reports that the plugin is loaded and its routes are registered. It is not proof
	// that the host routed traffic here, so the page must not present it as active serving.
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
		{Method: http.MethodGet, Path: StatusDataPath, Description: "Cached console snapshot as JSON. Reads state only and never starts a probe."},
		{Method: http.MethodPost, Path: RefreshPath, Description: "Run one quota probe cycle now. Requires the host management key."},
	}
}

// Resources returns the browser-navigable resource routes to register with the host. Paths are
// relative to /v0/resource/plugins/expiring-first/, so the page is reachable at ResourceURL and
// the snapshot at DataURL. Resource requests are GET-only and unauthenticated, so the snapshot
// route exposes the same redacted view as the page and never triggers a probe.
func Resources() []pluginapi.ResourceRoute {
	return []pluginapi.ResourceRoute{
		{
			Path:        ResourcePath,
			Menu:        "Expiring First",
			Description: "Quota windows, routing previews and pick counts for the expiring-first scheduler.",
		},
		{
			Path:        DataResourcePath,
			Description: "Cached expiring-first snapshot as JSON, reloaded by the status page. Never starts a probe.",
		},
	}
}

// Handle processes one management or resource request.
func (h *Handler) Handle(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	path := normalizePath(req.Path)
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	switch {
	case path == StatusPath && method == http.MethodGet:
		return h.statusPage(ctx), nil
	case path == StatusDataPath && method == http.MethodGet:
		return jsonResponse(http.StatusOK, h.statusView(ctx)), nil
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
// switches on, so the management prefix, the resource prefix and the legacy prefix all work. A
// host that dispatches the resource-relative form ("/data", "/status") is handled too.
func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	for _, prefix := range []string{"/v0/management", "/v0/resource"} {
		if strings.HasPrefix(path, prefix) {
			return strings.TrimPrefix(path, prefix)
		}
	}
	switch path {
	case ResourcePath:
		return StatusPath
	case DataResourcePath:
		return StatusDataPath
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
	Stale     bool              `json:"stale"`
	Retained  bool              `json:"retained"`
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

// PreviewView is one routing preview row: what the ranking would pick for a provider and model
// route right now. A preview is not a served request. Routes come from the configured
// preview_models list and from pins, which only exist for traffic the plugin actually routed.
type PreviewView struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	AuthID   string `json:"auth_id"`
	Auth     string `json:"auth"`
	// Ranked reports that fresh quota readings decided this preview, rather than the sticky pin.
	Ranked bool `json:"ranked"`
	// Observed reports that a pin exists for this route, so the plugin routed real traffic here.
	Observed bool `json:"observed_traffic"`
}

// PickCountView is the JSON shape of one provider's pick counters.
type PickCountView struct {
	Provider string `json:"provider"`
	Handled  int64  `json:"handled"`
	Ranked   int64  `json:"ranked"`
}

// StatusView is the JSON payload embedded into the page and returned by the JSON routes.
type StatusView struct {
	Version string `json:"version"`
	// Registered reports that the plugin loaded and registered its routes. It is not evidence
	// that the host routed traffic here; PickCounts is the serving evidence.
	Registered    bool            `json:"registered"`
	Providers     []string        `json:"providers,omitempty"`
	ProbeInterval string          `json:"probe_interval"`
	LastProbeAt   time.Time       `json:"last_probe_at"`
	LiveEnabled   bool            `json:"live_enabled"`
	LivePoolSize  int             `json:"live_pool_size"`
	LiveStrategy  string          `json:"live_strategy"`
	Pins          []PinView       `json:"pins"`
	Quota         []QuotaView     `json:"quota"`
	Previews      []PreviewView   `json:"previews"`
	PickCounts    []PickCountView `json:"pick_counts"`
	// Readings counts the accounts with at least one usable window; ProbeOutcomes counts the
	// accounts with a latest probe outcome, which can be higher because a failed probe still
	// reports an outcome.
	Readings      int `json:"readings"`
	ProbeOutcomes int `json:"probe_outcomes"`
	// AccountsSucceeded counts probe outcomes without an error; AccountsFailed counts the rest.
	AccountsSucceeded int `json:"accounts_succeeded"`
	AccountsFailed    int `json:"accounts_failed"`
	// PicksHandled and PicksRanked are totals over PickCounts.
	PicksHandled int64     `json:"picks_handled"`
	PicksRanked  int64     `json:"picks_ranked"`
	GeneratedAt  time.Time `json:"generated_at"`
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
		view.Stale = account.UpdatedAt.IsZero() || now.Sub(account.UpdatedAt) > h.sources.Config().MaxAge()
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
			view.Retained = len(view.Windows) > 0
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

// previewRoutes builds the routing previews from the configured preview_models plus the pins.
// Pins exist only for routes the plugin routed real traffic for, so the page never invents a
// provider or model that this deployment does not use.
func (h *Handler) previewRoutes(ctx context.Context) []PreviewView {
	now := h.now().UTC()

	routes := make(map[string]*PreviewView)
	for _, preview := range h.sources.Config().PreviewModels() {
		provider, model := previewProvider(preview.Provider), strings.TrimSpace(preview.Model)
		if provider == "" || model == "" {
			continue
		}
		routes[previewKey(provider, model)] = &PreviewView{Provider: provider, Model: model}
	}
	for _, pin := range h.sources.Pins() {
		provider, model := previewProvider(pin.Provider), strings.TrimSpace(pin.Model)
		if provider == "" || model == "" {
			continue
		}
		key := previewKey(provider, model)
		if view, okView := routes[key]; okView {
			view.Observed = true
			continue
		}
		routes[key] = &PreviewView{Provider: provider, Model: model, Observed: true}
	}

	views := make([]PreviewView, 0, len(routes))
	for _, view := range routes {
		views = append(views, *view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Provider != views[j].Provider {
			return views[i].Provider < views[j].Provider
		}
		return views[i].Model < views[j].Model
	})

	// One host listing serves every provider; a failed listing leaves the previews unknown
	// rather than failing the page.
	byProvider, errCandidates := h.sources.Candidates(ctx)
	if errCandidates != nil {
		byProvider = nil
	}
	for index := range views {
		authID, okPick := h.sources.Preview(byProvider[views[index].Provider], views[index].Provider, views[index].Model, now)
		if !okPick {
			continue
		}
		views[index].AuthID, views[index].Auth, views[index].Ranked = Redact(authID), Redact(authID), true
	}
	return views
}

// previewKey dedups a route. A thinking suffix such as "(high)" shares one route with its base
// model, matching how the scheduler keys pins.
func previewKey(provider, model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if open := strings.LastIndex(model, "("); open > 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:open])
	}
	return provider + "\x00" + model
}

func previewProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
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
	liveConfig := h.sources.Live()
	quota := h.quotaView(ctx)
	previews := h.previewRoutes(ctx)
	counts := h.pickCounts()
	readings, outcomes, succeeded, failed := h.probeCounts()

	view := StatusView{
		Version:           h.sources.Version(),
		Registered:        h.sources.Enabled(),
		Providers:         h.sources.Config().Providers(),
		ProbeInterval:     h.sources.ProbeInterval().String(),
		LastProbeAt:       h.sources.LastProbeAt().UTC(),
		LiveEnabled:       liveConfig.Enabled(),
		LivePoolSize:      len(liveConfig.AuthIDs),
		LiveStrategy:      liveConfig.Strategy,
		Pins:              h.pinsView(),
		Quota:             quota,
		Previews:          previews,
		PickCounts:        counts,
		Readings:          readings,
		ProbeOutcomes:     outcomes,
		AccountsSucceeded: succeeded,
		AccountsFailed:    failed,
		GeneratedAt:       h.now().UTC(),
	}
	for _, count := range counts {
		view.PicksHandled += count.Handled
		view.PicksRanked += count.Ranked
	}
	return view
}

// probeCounts separates what the ranking can use from what the probes reported. A reading is an
// account with at least one usable window; a probe outcome is an account the last probe cycle
// reported, which can be more because a failed probe still reports an outcome. The successful
// count excludes failed rows, so it never includes an account whose probe errored.
func (h *Handler) probeCounts() (readings, outcomes, succeeded, failed int) {
	for _, account := range h.sources.Quota() {
		if len(account.Windows) > 0 {
			readings++
		}
	}
	for _, account := range h.sources.Accounts() {
		outcomes++
		if account.Error != "" {
			failed++
			continue
		}
		succeeded++
	}
	return readings, outcomes, succeeded, failed
}

func (h *Handler) statusPage(ctx context.Context) pluginapi.ManagementResponse {
	payload, errMarshal := json.Marshal(h.statusView(ctx))
	if errMarshal != nil {
		payload = []byte(`null`)
	}
	html := strings.Replace(StatusHTML, "__EXPIRING_FIRST_BOOTSTRAP_JSON__", string(payload), 1)
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}},
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
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Cache-Control": []string{"no-store"}},
		Body:       raw,
	}
}

// redactPrefixes are credential-name prefixes whose provider is safe to publish. A credential
// file named after an email address or an account ID keeps the neutral acct- label, so no part
// of the name is published.
var redactPrefixes = []string{"antigravity", "codex", "chatgpt", "claude", "anthropic", "xai", "gemini", "openai", "vertex"}

// Redact turns a credential ID into a stable private label: a provider-ish prefix plus ten hex
// characters of the SHA-256 of the full ID. The digest distinguishes credentials that share
// a filename prefix, and the same ID always renders the same label, so
// the page can reference one credential consistently across pins, quota and previews.
func Redact(authID string) string {
	trimmed := strings.TrimSpace(authID)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return redactPrefix(trimmed) + hex.EncodeToString(sum[:])[:10]
}

func redactPrefix(authID string) string {
	lowered := strings.ToLower(authID)
	for _, provider := range redactPrefixes {
		if !strings.HasPrefix(lowered, provider) {
			continue
		}
		rest := lowered[len(provider):]
		if rest == "" || !isLowerAlphaNumeric(rest[0]) {
			return provider + "-"
		}
	}
	return "acct-"
}

func isLowerAlphaNumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
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
