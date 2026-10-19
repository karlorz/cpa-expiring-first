package console

import (
	"context"
	"encoding/json"
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
}

func (f *fakeSources) Pins() []router.PinView {
	return []router.PinView{{
		Provider:     "antigravity",
		Model:        "gemini-3-pro",
		AuthID:       "antigravity-primary-account.json",
		PinnedAt:     now,
		LastPickedAt: now,
	}}
}

func (f *fakeSources) Quota() router.Quota {
	return router.Quota{
		"antigravity-primary-account.json": {
			UpdatedAt: now,
			Windows: []router.QuotaWindow{
				{Minutes: 10080, UsedPercent: 40, ResetsAt: now.Add(48 * time.Hour), Models: []string{"gemini"}},
				{Minutes: 300, UsedPercent: 100, ResetsAt: now.Add(time.Hour), Models: []string{"gemini"}},
			},
		},
	}
}

func (f *fakeSources) Accounts() []probe.Account {
	return []probe.Account{{
		AuthID:     "antigravity-primary-account.json",
		Provider:   "antigravity",
		Group:      "gemini",
		ObservedAt: now,
	}}
}

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

func (f *fakeSources) Config() router.Config { return router.DefaultConfig() }

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
	sources := &fakeSources{}
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
	if !strings.Contains(body, "anti***on") {
		t.Fatalf("page does not carry the redacted credential ID: %s", body)
	}
	if !strings.Contains(body, "Weekly-soonest") || !strings.Contains(body, "Next pick per provider") {
		t.Fatal("page does not explain the ranking")
	}
	if strings.Contains(body, "__EXPIRING_FIRST_BOOTSTRAP_JSON__") {
		t.Fatal("bootstrap placeholder was not replaced")
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

func TestStatusViewShowsNextPicksAndCounts(t *testing.T) {
	handler, _ := newTestHandler()
	view := handler.statusView(context.Background())
	if len(view.NextPicks) != len(RepresentativePicks) {
		t.Fatalf("next picks = %+v, want one per representative model", view.NextPicks)
	}
	for _, pick := range view.NextPicks {
		if !pick.Ranked || pick.Auth == "" {
			t.Errorf("next pick = %+v, want a ranked redacted pick", pick)
		}
	}
	if len(view.PickCounts) != 1 || view.PickCounts[0].Handled != 7 || view.PickCounts[0].Ranked != 5 {
		t.Fatalf("pick counts = %+v", view.PickCounts)
	}
	if !view.LiveEnabled || view.LivePoolSize != 1 {
		t.Fatalf("live = %+v, want one configured credential", view)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"":                         "",
		"ab":                       "a*",
		"abc.json":                 "abc.***on",
		"antigravity-primary.json": "anti***on",
	}
	for input, want := range cases {
		if got := Redact(input); got != want {
			t.Errorf("Redact(%q) = %q, want %q", input, got, want)
		}
	}
}
