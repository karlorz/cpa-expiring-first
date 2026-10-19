package main

import (
	"encoding/json"
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
