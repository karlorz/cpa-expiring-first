package router

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestQuotaSkipsExhaustedShortWindowThenReturnsAfterReset covers the interplay of the short and
// long windows: an account whose weekly window resets sooner loses the pick while its five-hour
// window is used up, and wins again once that window has reset.
func TestQuotaSkipsExhaustedShortWindowThenReturnsAfterReset(t *testing.T) {
	shortReset := t0.Add(time.Hour)
	weeklyReset := t0.Add(24 * time.Hour)
	source := &staticQuota{quota: Quota{
		// a expires first weekly, but has no five-hour quota left until shortReset.
		"a": reading(weekly(10, weeklyReset), session(100, shortReset)),
		// b expires later, but has quota on both windows right now.
		"b": reading(weekly(10, weeklyReset.Add(48*time.Hour)), session(10, t0.Add(4*time.Hour))),
	}}
	s := NewScheduler(DefaultConfig(), nil)
	s.UseQuota(source)

	mustPick(t, s, request("m", candidate("a", 10), candidate("b", 0)), t0, "b")

	// After a's five-hour window resets, its sooner weekly reset wins again.
	afterReset := shortReset.Add(time.Minute)
	source.quota = Quota{
		"a": QuotaAccount{UpdatedAt: afterReset, Windows: []QuotaWindow{weekly(10, weeklyReset), session(0, time.Time{})}},
		"b": QuotaAccount{UpdatedAt: afterReset, Windows: []QuotaWindow{weekly(10, weeklyReset.Add(48*time.Hour)), session(10, t0.Add(4*time.Hour))}},
	}
	mustPick(t, s, request("m", candidate("a", 10), candidate("b", 0)), afterReset, "a")
}

// TestQuotaRanksByModelGroup checks that one Antigravity credential carrying both a gemini and a
// claude_gpt weekly window is ranked on the window that matches the requested model.
func TestQuotaRanksByModelGroup(t *testing.T) {
	geminiReset := t0.Add(24 * time.Hour)
	claudeReset := t0.Add(96 * time.Hour)
	quota := Quota{
		"antigravity": reading(
			QuotaWindow{Minutes: week, UsedPercent: 10, ResetsAt: geminiReset, Models: []string{"gemini"}},
			QuotaWindow{Minutes: week, UsedPercent: 10, ResetsAt: claudeReset, Models: []string{"claude", "gpt", "openai"}},
		),
		"backup": reading(weekly(10, t0.Add(48*time.Hour))),
	}
	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: "antigravity", Provider: "antigravity", Priority: 10},
		{ID: "backup", Provider: "antigravity", Priority: 0},
	}
	antigravityRequest := func(model string) pluginapi.SchedulerPickRequest {
		return pluginapi.SchedulerPickRequest{
			Provider:   "antigravity",
			Providers:  []string{"antigravity"},
			Model:      model,
			Candidates: candidates,
		}
	}

	// gemini resets first, so the gemini model ranks on the gemini window.
	mustPick(t, quotaScheduler(t, quota), antigravityRequest("gemini-3-pro"), t0, "antigravity")

	// claude_gpt resets later than backup's weekly window, so a Claude model ranks on it.
	mustPick(t, quotaScheduler(t, quota), antigravityRequest("claude-sonnet-5"), t0, "backup")

	// An OpenAI model is part of the claude_gpt group too.
	mustPick(t, quotaScheduler(t, quota), antigravityRequest("gpt-5-codex"), t0, "backup")
}

// TestQuotaOverlayPrefersFreshFileReading covers the file overlay: a file reading younger than
// max_age wins over the probe reading for the same auth ID, and an older one does not.
func TestQuotaOverlayPrefersFreshFileReading(t *testing.T) {
	memory := &staticQuota{quota: Quota{
		"primary": reading(weekly(10, t0.Add(24*time.Hour))),
		"backup":  reading(weekly(10, t0.Add(48*time.Hour))),
	}}
	overlay := NewOverlayQuota(memory, &staticQuota{quota: Quota{
		"primary": reading(weekly(10, t0.Add(72*time.Hour))),
	}}, DefaultMaxAge)
	overlay.now = func() time.Time { return t0 }

	s := NewScheduler(DefaultConfig(), nil)
	s.UseQuota(overlay)
	// The file pushes primary's reset out past backup's, so backup wins.
	mustPick(t, s, request("m", primary, backup), t0, "backup")

	// Once the file reading is older than max_age, the probe reading decides again.
	stale := NewOverlayQuota(memory, &staticQuota{quota: Quota{
		"primary": QuotaAccount{UpdatedAt: t0.Add(-DefaultMaxAge - time.Minute), Windows: []QuotaWindow{weekly(10, t0.Add(72*time.Hour))}},
	}}, DefaultMaxAge)
	stale.now = func() time.Time { return t0 }
	s.UseQuota(stale)
	mustPick(t, s, request("m", primary, backup), t0, "primary")
}

// TestQuotaOverlayEmptyFileUsesMemory covers an empty overlay: the probe readings stay in use.
func TestQuotaOverlayEmptyFileUsesMemory(t *testing.T) {
	memory := &staticQuota{quota: Quota{"a": reading(weekly(10, t0.Add(24*time.Hour)))}}
	overlay := NewOverlayQuota(memory, &staticQuota{}, DefaultMaxAge)
	overlay.now = func() time.Time { return t0 }

	s := NewScheduler(DefaultConfig(), nil)
	s.UseQuota(overlay)
	mustPick(t, s, request("m", primary), t0, "primary")

	quota := overlay.Load()
	if len(quota) != 1 || !quota["a"].UpdatedAt.Equal(t0) {
		t.Fatalf("Load() = %+v, want the memory reading", quota)
	}
}

func TestElapsedFiveHourResetRestoresSoonerWeeklyAccount(t *testing.T) {
	reset := t0.Add(10 * time.Minute)
	quota := Quota{
		"a": reading(weekly(20, t0.Add(24*time.Hour)), session(100, reset)),
		"b": reading(weekly(20, t0.Add(48*time.Hour)), session(20, t0.Add(2*time.Hour))),
	}
	s := quotaScheduler(t, quota)
	req := request("gemini-3.8-flash-high", candidate("a", 0), candidate("b", 0))
	mustPick(t, s, req, t0, "b")
	mustPick(t, s, req, reset.Add(time.Second), "a")
	if !quota["a"].UpdatedAt.Equal(t0) {
		t.Fatal("elapsed reset changed original evidence age")
	}
}

func TestWeeklyRolloverMovesToNextExpiringAccount(t *testing.T) {
	reset := t0.Add(10 * time.Minute)
	s := quotaScheduler(t, Quota{
		"a": reading(weekly(20, reset)),
		"b": reading(weekly(20, t0.Add(48*time.Hour))),
	})
	req := request("gemini-3.8-flash-high", candidate("a", 100), candidate("b", 0))
	mustPick(t, s, req, t0, "a")
	mustPick(t, s, req, reset.Add(time.Second), "b")
}

func TestPreviewMirrorsCodexWebsocketDelegation(t *testing.T) {
	s := quotaScheduler(t, Quota{"ws": reading(weekly(10, t0.Add(time.Hour)))})
	id, ok := s.Preview([]pluginapi.SchedulerAuthCandidate{{ID: "ws", Attributes: map[string]string{"websockets": "true"}}}, "codex", "gpt-5-codex", t0)
	if ok || id != "" {
		t.Fatalf("Preview = %q,%v; want delegated", id, ok)
	}
}

func TestRankCandidatesMatchesPickAndExplainsExclusions(t *testing.T) {
	cfg := DefaultConfig()
	quota := Quota{
		"soon":      reading(weekly(20, t0.Add(time.Hour)), session(10, t0.Add(10*time.Minute))),
		"later":     reading(weekly(10, t0.Add(2*time.Hour))),
		"five-hour": reading(weekly(10, t0.Add(30*time.Minute)), session(100, t0.Add(10*time.Minute))),
		"expired":   {UpdatedAt: t0.Add(-time.Hour), Windows: []QuotaWindow{weekly(10, t0.Add(time.Hour))}},
		"scoped":    reading(QuotaWindow{Minutes: 10080, Models: []string{"imagen"}, ResetsAt: t0.Add(time.Hour)}),
		"disabled":  reading(weekly(10, t0.Add(time.Minute))),
	}
	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: "later", Priority: 100}, {ID: "soon"}, {ID: "five-hour"}, {ID: "expired"}, {ID: "scoped"}, {ID: "missing"},
		{ID: "disabled", Attributes: map[string]string{"disabled": "true"}},
		{ID: "cooldown", Attributes: map[string]string{"next_retry_after": t0.Add(time.Hour).Format(time.RFC3339Nano)}},
	}
	rows := RankCandidates(candidates, quota, "antigravity", "gemini-3.8-flash-high", t0, cfg)
	if rows[0].AuthID != "soon" || rows[0].Order != 1 || rows[1].AuthID != "later" || rows[1].Order != 2 {
		t.Fatalf("order=%+v", rows)
	}
	choice, ok := rankByQuota(candidates[:6], quota, "gemini-3.8-flash-high", t0, cfg.MaxAge(), cfg.ReservePercent())
	if !ok || rows[0].AuthID != choice.authID {
		t.Fatal("display and pick differ")
	}
	reasons := map[string]string{}
	for _, row := range rows {
		reasons[row.AuthID] = row.Reason
	}
	for id, want := range map[string]string{"five-hour": "Five-hour quota exhausted", "expired": "Quota reading expired", "scoped": "No quota window for this model", "missing": "No quota reading", "disabled": "Account disabled", "cooldown": "Host cooldown"} {
		if reasons[id] != want {
			t.Errorf("%s reason=%q want %q", id, reasons[id], want)
		}
	}
	after := RankCandidates(candidates, quota, "antigravity", "gemini-3.8-flash-high", t0.Add(11*time.Minute), cfg)
	if after[0].AuthID != "five-hour" {
		t.Fatalf("reset order=%+v", after)
	}
}

func TestRankCandidatesTiesAndDelegation(t *testing.T) {
	quota := Quota{"a": reading(weekly(10, t0.Add(time.Hour))), "b": reading(weekly(10, t0.Add(time.Hour))), "unstarted": reading(weekly(0, time.Time{}))}
	candidates := []pluginapi.SchedulerAuthCandidate{{ID: "a", Priority: 1}, {ID: "b", Priority: 2}, {ID: "unstarted", Priority: 100}}
	rows := RankCandidates(candidates, quota, "antigravity", "gemini-3.8-flash-high", t0, DefaultConfig())
	if rows[0].AuthID != "b" || rows[1].AuthID != "a" || rows[2].AuthID != "unstarted" {
		t.Fatalf("ties=%+v", rows)
	}
	candidates[0].Attributes = map[string]string{"websockets": "true"}
	rows = RankCandidates(candidates, quota, "codex", "gpt-test", t0, DefaultConfig())
	for _, row := range rows {
		if row.Order != 0 || row.Reason != "Codex websocket pool delegated to host" {
			t.Fatalf("delegation=%+v", rows)
		}
	}
}
