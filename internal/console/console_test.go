package console

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/live"
	"github.com/karlorz/cpa-expiring-first/internal/probe"
	"github.com/karlorz/cpa-expiring-first/internal/router"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

type fakeSources struct {
	refreshed int
	// pinned lists the routes the fake scheduler has a pin for.
	pinned []router.PinView
	// previewModels is the configured preview route list.
	previewModels []router.PreviewModel
	// quota is the readings the ranking uses.
	quota router.Quota
	// accounts is the latest probe outcome per credential.
	accounts []probe.Account
}

func newFakeSources() *fakeSources {
	return &fakeSources{
		pinned: []router.PinView{{
			Provider:     "antigravity",
			Model:        "gemini-3-pro",
			AuthID:       "antigravity-primary-account.json",
			PinnedAt:     now,
			LastPickedAt: now,
		}},
		previewModels: []router.PreviewModel{
			{Provider: "antigravity", Model: "gemini-3.8-flash-high"},
			{Provider: "codex", Model: "gpt-5-codex"},
		},
		quota: router.Quota{
			"antigravity-primary-account.json": {
				UpdatedAt: now,
				Windows: []router.QuotaWindow{
					{Minutes: 10080, UsedPercent: 40, ResetsAt: now.Add(48 * time.Hour), Models: []string{"gemini"}},
					{Minutes: 300, UsedPercent: 100, ResetsAt: now.Add(time.Hour), Models: []string{"gemini"}},
				},
			},
		},
		accounts: []probe.Account{{
			AuthID:     "antigravity-primary-account.json",
			Provider:   "antigravity",
			Group:      "gemini",
			ObservedAt: now,
		}},
	}
}

func (f *fakeSources) Pins() []router.PinView { return f.pinned }

func (f *fakeSources) Quota() router.Quota { return f.quota }

func (f *fakeSources) Accounts() []probe.Account { return f.accounts }

func (f *fakeSources) PickStats() map[string]router.PickStat {
	return map[string]router.PickStat{"antigravity": {Handled: 7, Ranked: 5}}
}

func (f *fakeSources) Candidates(ctx context.Context) (map[string][]pluginapi.SchedulerAuthCandidate, error) {
	return map[string][]pluginapi.SchedulerAuthCandidate{
		"antigravity": {{ID: "antigravity-primary-account.json", Provider: "antigravity"}},
		"claude":      {{ID: "claude-primary-account.json", Provider: "claude"}},
		"codex":       {{ID: "codex-primary-account.json", Provider: "codex"}},
		"xai":         {{ID: "xai-primary-account.json", Provider: "xai"}},
	}, nil
}

func (f *fakeSources) Preview(candidates []pluginapi.SchedulerAuthCandidate, provider, model string, at time.Time) (string, bool) {
	return "antigravity-primary-account.json", true
}

func (f *fakeSources) Config() router.Config {
	raw := "preview_models:\n"
	for _, preview := range f.previewModels {
		raw += fmt.Sprintf("  - {provider: %s, model: %s}\n", preview.Provider, preview.Model)
	}
	cfg, err := router.ParseConfig([]byte(raw))
	if err != nil {
		panic(err)
	}
	return cfg
}

func (f *fakeSources) Live() live.Config {
	return live.Config{AuthIDs: []string{"codex-live.json"}, Strategy: live.DefaultStrategy, FailClosed: true}
}

func (f *fakeSources) Refresh(ctx context.Context) error {
	f.refreshed++
	return nil
}

func (f *fakeSources) Enabled() bool { return true }

func (f *fakeSources) Version() string { return "test" }

func (f *fakeSources) ProbeInterval() time.Duration { return 5 * time.Minute }

func (f *fakeSources) LastProbeAt() time.Time { return now }

func newTestHandler() (*Handler, *fakeSources) {
	sources := newFakeSources()
	handler := NewHandler(sources)
	handler.now = func() time.Time { return now }
	return handler, sources
}

func TestStatusPageRedactsCredentialsAndEmbedsPayload(t *testing.T) {
	handler, _ := newTestHandler()
	resp, errHandle := handler.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/expiring-first/status",
	})
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := string(resp.Body)
	if strings.Contains(body, "antigravity-primary-account.json") {
		t.Fatal("page leaked a full credential ID")
	}
	if !strings.Contains(body, Redact("antigravity-primary-account.json")) {
		t.Fatalf("page does not carry the redacted credential ID: %s", body)
	}
	if !strings.Contains(body, "Routing previews") || !strings.Contains(body, "Weekly-soonest") {
		t.Fatal("page does not explain the ranking and the previews")
	}
	if strings.Contains(body, "__EXPIRING_FIRST_BOOTSTRAP_JSON__") {
		t.Fatal("bootstrap placeholder was not replaced")
	}
}

// TestPageKeepsKeyInMemoryOnly checks the page never persists the management key or sends it to
// the public snapshot route.
func TestPageKeepsKeyInMemoryOnly(t *testing.T) {
	for _, forbidden := range []string{
		"localStorage.setItem", "localStorage.getItem", "localStorage.removeItem", "localStorage[",
		"sessionStorage.setItem", "sessionStorage.getItem", "sessionStorage[",
		"document.cookie",
	} {
		if strings.Contains(StatusHTML, forbidden) {
			t.Errorf("page persists the management key through %s", forbidden)
		}
	}
	if !strings.Contains(StatusHTML, "var managementKey = ''") {
		t.Fatal("page does not keep the management key in a memory variable")
	}
	if !strings.Contains(StatusHTML, "DATA_URL") || !strings.Contains(StatusHTML, "REFRESH_URL") {
		t.Fatal("page does not separate the public snapshot route from the management probe route")
	}
	// The public snapshot fetch must not carry the key.
	snapshotFetch := StatusHTML[strings.Index(StatusHTML, "function fetchData"):strings.Index(StatusHTML, "function probeNow")]
	if strings.Contains(snapshotFetch, "managementKey") || strings.Contains(snapshotFetch, "Authorization") {
		t.Fatal("snapshot fetch carries the management key")
	}
	if !strings.Contains(StatusHTML, "POLL_MS = 30000") {
		t.Fatal("page does not poll the cached snapshot every 30 seconds")
	}
}

// TestPageIsResponsive checks the page keeps tables inside a horizontal scroll wrapper and never
// lets a long label widen the document.
func TestPageIsResponsive(t *testing.T) {
	for _, want := range []string{
		"overflow-x: hidden",
		".table-wrap { width: 100%; max-width: 100%; overflow-x: auto; }",
		"overflow-wrap: anywhere",
		"word-break: break-word",
	} {
		if !strings.Contains(StatusHTML, want) {
			t.Errorf("page is missing responsive rule %q", want)
		}
	}
	if strings.Contains(StatusHTML, "innerHTML") {
		t.Fatal("page injects data as HTML instead of textContent")
	}
}

func TestPinsRouteRedactsAuthIDs(t *testing.T) {
	handler, _ := newTestHandler()
	resp, errHandle := handler.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/expiring-first/pins",
	})
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	if strings.Contains(string(resp.Body), "antigravity-primary-account.json") {
		t.Fatal("pins route leaked a full credential ID")
	}
	var pins []PinView
	if errUnmarshal := json.Unmarshal(resp.Body, &pins); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(pins) != 1 || pins[0].Provider != "antigravity" {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestQuotaRouteDescribesWindows(t *testing.T) {
	handler, _ := newTestHandler()
	resp, errHandle := handler.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/expiring-first/quota",
	})
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	if strings.Contains(string(resp.Body), "antigravity-primary-account.json") {
		t.Fatal("quota route leaked a full credential ID")
	}
	var views []QuotaView
	if errUnmarshal := json.Unmarshal(resp.Body, &views); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(views) != 1 || len(views[0].Windows) != 2 {
		t.Fatalf("quota = %+v", views)
	}
	// Windows are ordered longest first, and the exhausted 5h window is marked.
	if views[0].Windows[0].Name != "weekly" {
		t.Errorf("first window = %q, want weekly", views[0].Windows[0].Name)
	}
	if !views[0].Windows[1].Exhausted {
		t.Errorf("5h window = %+v, want exhausted", views[0].Windows[1])
	}
	if views[0].Provider != "antigravity" || views[0].Group != "gemini" {
		t.Errorf("account = %+v, want the probe outcome merged in", views[0])
	}
}

// TestDataRouteServesCachedSnapshotWithoutProbing covers the public resource route the page
// reloads: it returns the same view as the page and never starts a probe. Every prefix the host
// may dispatch, including the resource-relative form, resolves to the same handler.
func TestDataRouteServesCachedSnapshotWithoutProbing(t *testing.T) {
	for _, path := range []string{
		"/v0/resource/plugins/expiring-first/data",
		"/v0/management/plugins/expiring-first/data",
		"/plugins/expiring-first/data",
		"/data",
	} {
		handler, sources := newTestHandler()
		resp, errHandle := handler.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: path})
		if errHandle != nil {
			t.Fatal(errHandle)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
		}
		if sources.refreshed != 0 {
			t.Fatalf("%s started %d probes, want none", path, sources.refreshed)
		}
		var view StatusView
		if errUnmarshal := json.Unmarshal(resp.Body, &view); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if !view.Registered || len(view.Previews) == 0 || len(view.Quota) != 1 {
			t.Fatalf("%s view = %+v", path, view)
		}
		if strings.Contains(string(resp.Body), "antigravity-primary-account.json") {
			t.Fatalf("%s leaked a full credential ID", path)
		}
	}
}

// TestDataRouteRejectsPost checks the public snapshot route stays read-only.
func TestDataRouteRejectsPost(t *testing.T) {
	handler, sources := newTestHandler()
	resp, errHandle := handler.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: StatusDataPath})
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	if resp.StatusCode != http.StatusNotFound || sources.refreshed != 0 {
		t.Fatalf("status = %d refreshed = %d, want 404 and no probe", resp.StatusCode, sources.refreshed)
	}
}

func TestRefreshRouteTriggersProbe(t *testing.T) {
	handler, sources := newTestHandler()
	resp, errHandle := handler.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/expiring-first/refresh",
	})
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	if resp.StatusCode != http.StatusOK || sources.refreshed != 1 {
		t.Fatalf("status = %d refreshed = %d, want 200 and one refresh", resp.StatusCode, sources.refreshed)
	}
	var view StatusView
	if errUnmarshal := json.Unmarshal(resp.Body, &view); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if view.GeneratedAt != now {
		t.Fatalf("refresh did not return the new snapshot: %+v", view.GeneratedAt)
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	handler, _ := newTestHandler()
	resp, errHandle := handler.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/expiring-first/nope",
	})
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestPreviewsComeFromConfigAndPinsOnly checks the page previews the configured routes and the
// routes with observed traffic, and invents nothing else.
func TestPreviewsComeFromConfigAndPinsOnly(t *testing.T) {
	handler, sources := newTestHandler()
	view := handler.statusView(context.Background())

	want := map[string]bool{
		"antigravity|gemini-3.8-flash-high": true,
		"codex|gpt-5-codex":                 true,
		"antigravity|gemini-3-pro":          true, // pinned, so real traffic was observed
	}
	if len(view.Previews) != len(want) {
		t.Fatalf("previews = %+v, want %d routes", view.Previews, len(want))
	}
	for _, preview := range view.Previews {
		key := preview.Provider + "|" + preview.Model
		if !want[key] {
			t.Errorf("preview %+v is not a configured or observed route", preview)
		}
		if !preview.Ranked || preview.Auth == "" {
			t.Errorf("preview %+v, want a ranked redacted pick", preview)
		}
	}
	for _, preview := range view.Previews {
		if preview.Provider == "antigravity" && preview.Model == "gemini-3-pro" && !preview.Observed {
			t.Error("a pinned route must be marked as observed traffic")
		}
	}

	// Nothing in the config or the pins means nothing to preview: the page must not fabricate a
	// provider or model from the probed credentials.
	sources.previewModels = nil
	sources.pinned = nil
	view = handler.statusView(context.Background())
	if len(view.Previews) != 0 {
		t.Fatalf("previews = %+v, want none without configured routes or pins", view.Previews)
	}
}

// TestPreviewsAreDedupedAndSorted checks a route configured twice, and a route that is both
// configured and pinned, appears once and in provider then model order.
func TestPreviewsAreDedupedAndSorted(t *testing.T) {
	handler, sources := newTestHandler()
	sources.previewModels = []router.PreviewModel{
		{Provider: "codex", Model: "gpt-5-codex"},
		{Provider: "antigravity", Model: "gemini-3-pro"},
		{Provider: "Antigravity", Model: " gemini-3-pro "},
	}
	sources.pinned = []router.PinView{{Provider: "antigravity", Model: "gemini-3-pro", AuthID: "a.json"}}

	view := handler.statusView(context.Background())
	if len(view.Previews) != 2 {
		t.Fatalf("previews = %+v, want two deduped routes", view.Previews)
	}
	if view.Previews[0].Provider != "antigravity" || view.Previews[1].Provider != "codex" {
		t.Fatalf("previews are not sorted: %+v", view.Previews)
	}
	if !view.Previews[0].Observed {
		t.Errorf("the pinned duplicate must stay marked as observed traffic: %+v", view.Previews[0])
	}
}

// TestStatusViewDistinguishesReadingsFromProbeOutcomes covers the counts the page shows: a failed
// probe still reports an outcome, so the two numbers differ, and the successful count excludes
// failed rows.
func TestStatusViewDistinguishesReadingsFromProbeOutcomes(t *testing.T) {
	handler, sources := newTestHandler()
	sources.accounts = []probe.Account{
		{AuthID: "antigravity-primary-account.json", Provider: "antigravity", ObservedAt: now},
		{AuthID: "codex-broken-account.json", Provider: "codex", ObservedAt: now, Error: "codex: HTTP 500"},
		{AuthID: "codex-empty-account.json", Provider: "codex", ObservedAt: now},
	}
	view := handler.statusView(context.Background())

	if view.Readings != 1 {
		t.Errorf("Readings = %d, want 1 (only one account has usable windows)", view.Readings)
	}
	if view.ProbeOutcomes != 3 {
		t.Errorf("ProbeOutcomes = %d, want 3", view.ProbeOutcomes)
	}
	if view.AccountsSucceeded != 2 || view.AccountsFailed != 1 {
		t.Errorf("accounts = %d ok / %d failed, want 2 and 1", view.AccountsSucceeded, view.AccountsFailed)
	}
	// The failed row keeps its error, and a row without readings is not reported as a reading.
	failed := false
	for _, account := range view.Quota {
		if account.AuthID == Redact("codex-broken-account.json") {
			failed = account.Error != ""
		}
	}
	if !failed {
		t.Error("the failed probe row lost its error")
	}
}

// TestStatusViewDoesNotClaimActiveScheduler checks registration is reported as registration and
// serving is reported from the counters only.
func TestStatusViewDoesNotClaimActiveScheduler(t *testing.T) {
	handler, _ := newTestHandler()
	view := handler.statusView(context.Background())
	if !view.Registered {
		t.Error("Registered = false, want true for a loaded plugin")
	}
	if view.PicksHandled != 7 || view.PicksRanked != 5 {
		t.Fatalf("picks = %d/%d, want 7/5", view.PicksHandled, view.PicksRanked)
	}
	if !strings.Contains(StatusHTML, "'registered'") {
		t.Error("the page does not label the plugin as registered")
	}
	if strings.Contains(StatusHTML, "'active'") || strings.Contains(StatusHTML, "scheduler active") {
		t.Error("the page still presents registration as an active scheduler")
	}
	if !strings.Contains(StatusHTML, "No picks observed yet") {
		t.Error("the page does not say when no picks were observed")
	}
}

func TestStatusViewShowsPreviewsAndCounts(t *testing.T) {
	handler, _ := newTestHandler()
	view := handler.statusView(context.Background())
	if len(view.PickCounts) != 1 || view.PickCounts[0].Handled != 7 || view.PickCounts[0].Ranked != 5 {
		t.Fatalf("pick counts = %+v", view.PickCounts)
	}
	if !view.LiveEnabled || view.LivePoolSize != 1 {
		t.Fatalf("live = %+v, want one configured credential", view)
	}
}

// TestRedactIsStableAndCollisionFree covers the labels the page publishes: the same credential ID
// always renders the same label, distinct IDs never collide, and no part of a private name leaks.
func TestRedactIsStableAndCollisionFree(t *testing.T) {
	names := []string{
		"antigravity-primary-account.json",
		"antigravity-backup-account.json",
		"antigravity-secondary-account.json",
		"codex-primary-account.json",
		"codex-backup-account.json",
		"codex-tertiary-account.json",
		"codex-live-account.json",
		"codex-shared-account.json",
	}
	labels := make(map[string]string, len(names))
	for _, name := range names {
		label := Redact(name)
		if other, exists := labels[label]; exists {
			t.Fatalf("Redact(%q) = Redact(%q) = %q, want distinct labels", name, other, label)
		}
		labels[label] = name
		if got := Redact(name); got != label {
			t.Errorf("Redact(%q) is not stable: %q then %q", name, label, got)
		}
		suffix := label[strings.LastIndex(label, "-")+1:]
		if len(suffix) != 10 {
			t.Errorf("Redact(%q) = %q, want 10 hex characters after the prefix", name, label)
		}
		if _, errHex := hex.DecodeString(suffix); errHex != nil {
			t.Errorf("Redact(%q) = %q, want a hex suffix", name, label)
		}
	}

	// The provider-ish prefix stays readable, so the page can group labels by provider.
	for _, name := range names[:3] {
		if !strings.HasPrefix(Redact(name), "antigravity-") {
			t.Errorf("Redact(%q) = %q, want an antigravity- prefix", name, Redact(name))
		}
	}
	for _, name := range names[3:] {
		if !strings.HasPrefix(Redact(name), "codex-") {
			t.Errorf("Redact(%q) = %q, want a codex- prefix", name, Redact(name))
		}
	}
	if got := Redact("user@example.com.json"); !strings.HasPrefix(got, "acct-") {
		t.Errorf("Redact of a private name = %q, want a neutral acct- prefix", got)
	}
	if got := Redact("user@example.com.json"); strings.Contains(got, "user") || strings.Contains(got, "example") {
		t.Errorf("Redact(%q) = %q, want no part of the name published", "user@example.com.json", got)
	}
	if got := Redact(""); got != "" {
		t.Errorf("Redact(%q) = %q, want empty", "", got)
	}
	if got := Redact("  "); got != "" {
		t.Errorf("Redact of blanks = %q, want empty", got)
	}
}

// TestRedactIsConsistentAcrossViews checks one credential renders the same label in pins, quota
// and previews, so the page can be read across sections.
func TestRedactIsConsistentAcrossViews(t *testing.T) {
	handler, sources := newTestHandler()
	const authID = "antigravity-primary-account.json"
	sources.pinned = []router.PinView{{Provider: "antigravity", Model: "gemini-3-pro", AuthID: authID, PinnedAt: now, LastPickedAt: now}}
	sources.quota = router.Quota{authID: {UpdatedAt: now, Windows: []router.QuotaWindow{{Minutes: 10080, UsedPercent: 10, ResetsAt: now.Add(time.Hour)}}}}
	sources.accounts = []probe.Account{{AuthID: authID, Provider: "antigravity", ObservedAt: now}}

	view := handler.statusView(context.Background())
	label := Redact(authID)
	if view.Pins[0].AuthID != label {
		t.Errorf("pin label = %q, want %q", view.Pins[0].AuthID, label)
	}
	if view.Quota[0].AuthID != label || view.Quota[0].Auth != label {
		t.Errorf("quota labels = %q/%q, want %q", view.Quota[0].AuthID, view.Quota[0].Auth, label)
	}
	for _, preview := range view.Previews {
		if preview.Auth != "" && preview.Auth != label {
			t.Errorf("preview label = %q, want %q", preview.Auth, label)
		}
	}
}

func TestResourcesIncludePageAndData(t *testing.T) {
	byPath := make(map[string]pluginapi.ResourceRoute)
	for _, route := range Resources() {
		byPath[route.Path] = route
	}
	if _, ok := byPath[ResourcePath]; !ok {
		t.Fatalf("resources = %+v, want the page route", byPath)
	}
	if _, ok := byPath[DataResourcePath]; !ok {
		t.Fatalf("resources = %+v, want the data route", byPath)
	}
	if byPath[ResourcePath].Menu == "" {
		t.Error("the page route needs a menu label")
	}
}

func TestRoutesIncludeDataAndRefresh(t *testing.T) {
	methods := make(map[string]string)
	for _, route := range Routes() {
		methods[route.Path] = route.Method
	}
	if methods[StatusDataPath] != http.MethodGet {
		t.Errorf("data route method = %q, want GET", methods[StatusDataPath])
	}
	if methods[RefreshPath] != http.MethodPost {
		t.Errorf("refresh route method = %q, want POST", methods[RefreshPath])
	}
	if RefreshURL != "/v0/management/plugins/expiring-first/refresh" {
		t.Errorf("RefreshURL = %q, want the host's v0 plugin management prefix", RefreshURL)
	}
	if DataURL != "/v0/resource/plugins/expiring-first/data" {
		t.Errorf("DataURL = %q, want the public resource prefix", DataURL)
	}
}

func TestRetainedEvidenceShowsOriginalAgeAndStaleness(t *testing.T) {
	handler, sources := newTestHandler()
	sources.quota = router.Quota{"retained": {UpdatedAt: now.Add(-time.Hour), Windows: []router.QuotaWindow{{Minutes: 10080, UsedPercent: 20}}}}
	sources.accounts = []probe.Account{{AuthID: "retained", Provider: "antigravity", ObservedAt: now, Error: "quota status 403"}}
	view := handler.quotaView(context.Background())
	if len(view) != 1 || !view[0].Stale || !view[0].Retained || !view[0].UpdatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("retained view=%+v", view)
	}
}
