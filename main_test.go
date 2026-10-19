package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/live"
	"github.com/karlorz/cpa-expiring-first/internal/router"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// decodePick runs the plugin's pick entry point and returns the scheduling response.
func decodePick(t *testing.T, req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
	t.Helper()
	raw, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	envelopeRaw, errPick := pick(raw)
	if errPick != nil {
		t.Fatal(errPick)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(envelopeRaw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK {
		t.Fatalf("pick envelope not ok: %s", envelopeRaw)
	}
	var resp pluginapi.SchedulerPickResponse
	if errUnmarshal := json.Unmarshal(envelope.Result, &resp); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return resp
}

func codexPick(model string, candidates ...pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickRequest {
	return pluginapi.SchedulerPickRequest{
		Provider:   "codex",
		Providers:  []string{"codex"},
		Model:      model,
		Candidates: candidates,
	}
}

// TestLiveRequestUsesAllowlistBeforeRanking checks that a Live request is answered from this
// plugin's own allowlist, ahead of the weekly-soonest ranking.
func TestLiveRequestUsesAllowlistBeforeRanking(t *testing.T) {
	liveSelector.Configure(live.Config{AuthIDs: []string{"codex-live.json"}, Strategy: live.DefaultStrategy, FailClosed: true})
	t.Cleanup(func() { liveSelector.Configure(live.Config{}) })

	resp := decodePick(t, codexPick(live.Model,
		pluginapi.SchedulerAuthCandidate{ID: "codex-other.json", Priority: 100},
		pluginapi.SchedulerAuthCandidate{ID: "codex-live.json"},
	))
	if !resp.Handled || resp.AuthID != "codex-live.json" {
		t.Fatalf("pick = %+v, want the allowlisted live credential", resp)
	}
}

// TestLiveRequestWithoutAllowlistIsUnhandled checks that a Live request never falls back to the
// weekly-soonest ranking: with no allowlist it is left to the host's built-in scheduler.
func TestLiveRequestWithoutAllowlistIsUnhandled(t *testing.T) {
	liveSelector.Configure(live.Config{})
	now := time.Now()
	memoryQuota.Replace(router.Quota{
		"codex-a.json": {UpdatedAt: now, Windows: []router.QuotaWindow{{Minutes: 10080, UsedPercent: 0, ResetsAt: now.Add(24 * time.Hour)}}},
	})
	t.Cleanup(func() { memoryQuota.Replace(nil) })

	resp := decodePick(t, codexPick(live.Model, pluginapi.SchedulerAuthCandidate{ID: "codex-a.json"}))
	if resp.Handled {
		t.Fatalf("pick = %+v, want unhandled so the host decides", resp)
	}
}

// TestNonLiveAntigravityRequestUsesWeeklySoonest checks that an ordinary Antigravity request is
// answered by the weekly-soonest ranking over the probe readings.
func TestNonLiveAntigravityRequestUsesWeeklySoonest(t *testing.T) {
	liveSelector.Configure(live.Config{})
	scheduler.Configure(router.DefaultConfig())
	scheduler.UseQuota(router.NewOverlayQuota(memoryQuota, nil, router.DefaultMaxAge))
	now := time.Now()
	memoryQuota.Replace(router.Quota{
		// sooner.json resets first but its weekly window is exhausted.
		"sooner.json": {UpdatedAt: now, Windows: []router.QuotaWindow{{Minutes: 10080, UsedPercent: 100, ResetsAt: now.Add(12 * time.Hour)}}},
		"later.json":  {UpdatedAt: now, Windows: []router.QuotaWindow{{Minutes: 10080, UsedPercent: 10, ResetsAt: now.Add(72 * time.Hour)}}},
	})
	t.Cleanup(func() {
		memoryQuota.Replace(nil)
		scheduler.UseQuota(nil)
	})

	req := pluginapi.SchedulerPickRequest{
		Provider:  "antigravity",
		Providers: []string{"antigravity"},
		Model:     "gemini-3-pro",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "sooner.json", Provider: "antigravity", Priority: 100},
			{ID: "later.json", Provider: "antigravity"},
		},
	}
	resp := decodePick(t, req)
	if !resp.Handled || resp.AuthID != "later.json" {
		t.Fatalf("pick = %+v, want later.json from the weekly-soonest ranking", resp)
	}
}

// TestCodexWebsocketPoolsStayUnhandled keeps the host's websocket preference intact.
func TestCodexWebsocketPoolsStayUnhandled(t *testing.T) {
	scheduler.Configure(router.DefaultConfig())
	scheduler.UseQuota(router.NewOverlayQuota(memoryQuota, nil, router.DefaultMaxAge))
	now := time.Now()
	memoryQuota.Replace(router.Quota{
		"codex-ws.json": {UpdatedAt: now, Windows: []router.QuotaWindow{{Minutes: 10080, UsedPercent: 0, ResetsAt: now.Add(24 * time.Hour)}}},
	})
	t.Cleanup(func() {
		memoryQuota.Replace(nil)
		scheduler.UseQuota(nil)
	})

	req := codexPick("gpt-5-codex", pluginapi.SchedulerAuthCandidate{
		ID:         "codex-ws.json",
		Provider:   "codex",
		Attributes: map[string]string{"websockets": "true"},
	})
	if resp := decodePick(t, req); resp.Handled {
		t.Fatalf("pick = %+v, want unhandled for a websocket-capable Codex pool", resp)
	}
}

func TestPreviewCandidatesRespectRuntimeEligibility(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{
		{ID: "ready", Provider: "antigravity", Priority: 42},
		{ID: "disabled", Provider: "antigravity", Disabled: true},
		{ID: "unavailable", Provider: "antigravity", Unavailable: true},
		{ID: "cooldown", Provider: "antigravity", NextRetryAfter: testNow.Add(time.Hour)},
		{ID: "ws", Type: "codex", Websockets: true},
	}
	got := previewCandidates(files, testNow)
	if len(got["antigravity"]) != 1 || got["antigravity"][0].ID != "ready" || got["antigravity"][0].Priority != 42 {
		t.Fatalf("eligible antigravity candidates = %+v", got["antigravity"])
	}
	if len(got["codex"]) != 1 || got["codex"][0].Attributes["websockets"] != "true" {
		t.Fatalf("codex candidates = %+v", got["codex"])
	}
}

func TestQuotaOverlayUsesReconfiguredMaxAge(t *testing.T) {
	const authID = "overlay-account"
	now := time.Now()
	path := t.TempDir() + "/quota.json"
	body := fmt.Sprintf(`{"schema":%q,"accounts":[{"auth_id":%q,"updated_at":%q,"windows":[{"window_minutes":10080,"used_percent":20}]}]}`, router.QuotaSchema, authID, now.Add(-10*time.Minute).UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { attachQuotaFile("", router.DefaultMaxAge) })
	attachQuotaFile(path, 5*time.Minute)
	if _, ok := scheduler.QuotaSnapshot()[authID]; ok {
		t.Fatal("overlay included expired reading")
	}
	attachQuotaFile(path, 20*time.Minute)
	if _, ok := scheduler.QuotaSnapshot()[authID]; !ok {
		t.Fatal("reconfigured overlay omitted fresh reading")
	}
}

func TestConfiguredLivePoolRejectsUnavailableCredentials(t *testing.T) {
	cfg, err := router.ParseConfig([]byte("live_auth_ids: [live-a, live-b, live-c]\nfail_closed: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	attachLive(cfg)
	t.Cleanup(func() { liveSelector.Configure(live.Config{}) })
	resp := decodePick(t, codexPick(live.Model, pluginapi.SchedulerAuthCandidate{ID: "ordinary"}))
	if !resp.Handled || !resp.Reject || resp.RejectCode != live.RejectCodeLiveUnavailable {
		t.Fatalf("pick = %+v, want fail-closed rejection", resp)
	}
}
