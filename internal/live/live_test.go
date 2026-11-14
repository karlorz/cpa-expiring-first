package live

import (
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func codexRequest(model string, candidates ...pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickRequest {
	return pluginapi.SchedulerPickRequest{
		Provider:   "codex",
		Providers:  []string{"codex"},
		Model:      model,
		Candidates: candidates,
	}
}

func TestIsLiveRequest(t *testing.T) {
	cases := map[string]struct {
		req  pluginapi.SchedulerPickRequest
		want bool
	}{
		"canonical model": {
			req:  codexRequest(Model, pluginapi.SchedulerAuthCandidate{ID: "a"}),
			want: true,
		},
		"empty model with oauth candidates": {
			req:  codexRequest("", pluginapi.SchedulerAuthCandidate{ID: "a", Attributes: map[string]string{"auth_kind": "oauth"}}),
			want: true,
		},
		"empty model with explicit api key": {
			req:  codexRequest("", pluginapi.SchedulerAuthCandidate{ID: "a", Attributes: map[string]string{"auth_kind": "apikey"}}),
			want: false,
		},
		"ordinary model": {
			req:  codexRequest("gpt-5-codex", pluginapi.SchedulerAuthCandidate{ID: "a"}),
			want: false,
		},
		"other provider": {
			req: pluginapi.SchedulerPickRequest{
				Provider:   "claude",
				Providers:  []string{"claude"},
				Model:      Model,
				Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a"}},
			},
			want: false,
		},
		"no candidates": {
			req:  codexRequest(""),
			want: false,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsLiveRequest(testCase.req); got != testCase.want {
				t.Fatalf("IsLiveRequest() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestLiveRequestPicksAllowlistedCredential(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"live-a"}, Strategy: StrategyFillFirst, FailClosed: true})

	req := codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "other", Priority: 100},
		pluginapi.SchedulerAuthCandidate{ID: "live-a"},
	)
	resp, handled := selector.Pick(req)
	if !handled || !resp.Handled || resp.AuthID != "live-a" {
		t.Fatalf("Pick() = %+v handled=%v, want live-a", resp, handled)
	}
}

func TestLiveFillFirstFollowsAllowlistOrder(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{
		AuthIDs:    []string{"live-karl", "live-alice", "live-team"},
		Strategy:   StrategyFillFirst,
		FailClosed: true,
	})

	req := codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "live-alice", Priority: 100},
		pluginapi.SchedulerAuthCandidate{ID: "live-team", Priority: 99},
		pluginapi.SchedulerAuthCandidate{ID: "live-karl", Priority: 98},
	)
	resp, handled := selector.Pick(req)
	if !handled || !resp.Handled || resp.AuthID != "live-karl" {
		t.Fatalf("Pick() = %+v handled=%v, want live-karl from live_auth_ids order", resp, handled)
	}
	stats := selector.Stats()
	if stats.Picks != 1 || stats.LastAuthID != "live-karl" || stats.LastAt.IsZero() {
		t.Fatalf("Stats() = %+v, want one pick of live-karl", stats)
	}
}

func TestLiveRequestRoundRobinRotates(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"live-a", "live-b"}, Strategy: StrategyRoundRobin})
	req := codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "live-a"},
		pluginapi.SchedulerAuthCandidate{ID: "live-b"},
	)
	first, _ := selector.Pick(req)
	second, _ := selector.Pick(req)
	if first.AuthID == second.AuthID {
		t.Fatalf("round robin returned %q twice", first.AuthID)
	}
}

func TestLiveRequestFailClosedRejectsWithoutAllowlistedCredential(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"live-a"}, FailClosed: true})
	resp, handled := selector.Pick(codexRequest(Model, pluginapi.SchedulerAuthCandidate{ID: "other"}))
	if !handled || !resp.Handled || !resp.Reject || resp.RejectCode != RejectCodeLiveUnavailable {
		t.Fatalf("Pick() = %+v handled=%v, want a fail-closed reject", resp, handled)
	}
	stats := selector.Stats()
	if stats.Rejects != 1 || stats.Picks != 0 || stats.LastAuthID != "" {
		t.Fatalf("Stats() = %+v, want one reject and no successful pick", stats)
	}
	if len(stats.Decisions) != 1 || stats.Decisions[0].Outcome != OutcomeRejected {
		t.Fatalf("Decisions = %+v, want one reject in the audit log", stats.Decisions)
	}
}

func TestLiveRequestFailOpenDelegatesToBuiltin(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"live-a"}, Strategy: StrategyRoundRobin, FailClosed: false})
	resp, handled := selector.Pick(codexRequest(Model, pluginapi.SchedulerAuthCandidate{ID: "other"}))
	if !handled || !resp.Handled || resp.DelegateBuiltin != StrategyRoundRobin {
		t.Fatalf("Pick() = %+v handled=%v, want a round-robin delegation", resp, handled)
	}
	stats := selector.Stats()
	if len(stats.Decisions) != 1 || stats.Decisions[0].Outcome != OutcomeDelegated {
		t.Fatalf("Decisions = %+v, want one delegated row in the audit log", stats.Decisions)
	}
}

func TestLiveRequestWithoutAllowlistPicksAvailableCodex(t *testing.T) {
	selector := NewSelector()
	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json", Provider: "codex", Priority: 10},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json", Provider: "codex", Priority: 99},
		pluginapi.SchedulerAuthCandidate{ID: "key.json", Provider: "codex", Attributes: map[string]string{"auth_kind": "apikey"}},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v, want available Codex fill-first (alice.json)", resp, handled)
	}
}

func TestLiveRequestWithoutAllowlistUsesAllAvailableThenSkipsCooling(t *testing.T) {
	selector := NewSelector()
	selector.NoteHostStates([]HostState{{
		AuthID:         "team.json",
		NextRetryAfter: time.Now().Add(time.Hour),
	}}, time.Now())
	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json", Provider: "codex", Priority: 100},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json", Provider: "codex", Priority: 90},
		pluginapi.SchedulerAuthCandidate{ID: "karl.json", Provider: "codex", Priority: 80},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v, want next of all available OAuth after Team cooldown", resp, handled)
	}
}

func TestLiveRequestWithoutAllowlistUnhandledWhenNoneEligible(t *testing.T) {
	selector := NewSelector()
	selector.NoteHostStates([]HostState{{
		AuthID:         "team.json",
		NextRetryAfter: time.Now().Add(time.Hour),
	}}, time.Now())
	if resp, handled := selector.Pick(codexRequest(Model, pluginapi.SchedulerAuthCandidate{ID: "team.json", Provider: "codex"})); handled {
		t.Fatalf("Pick() = %+v handled=true, want unhandled when no available Live source", resp)
	}
}

func TestLiveRequestWithoutAllowlistIgnoresAPIKey(t *testing.T) {
	selector := NewSelector()
	if resp, handled := selector.Pick(codexRequest(Model, pluginapi.SchedulerAuthCandidate{
		ID: "key.json", Provider: "codex", Attributes: map[string]string{"auth_kind": "apikey"},
	})); handled {
		t.Fatalf("Pick() = %+v handled=true, want unhandled for an API key", resp)
	}
}

func TestLivePickSkipsUsageLimitAndSelectsNextAllowlisted(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"team.json", "alice.json"}, FailClosed: true})
	now := time.Now()
	resetsAt := now.Add(48 * time.Hour).Unix()
	selector.NoteHostStates([]HostState{{
		AuthID:        "team.json",
		StatusMessage: `{"error":{"type":"usage_limit_reached","plan_type":"team","resets_at":` + strconv.FormatInt(resetsAt, 10) + `,"resets_in_seconds":172800}}`,
	}}, now)

	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json"},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json"},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v, want alice.json after Team Live limit", resp, handled)
	}
	skips := selector.LastSkips()
	if len(skips) != 1 || skips[0].AuthID != "team.json" || skips[0].Reason == "" {
		t.Fatalf("LastSkips() = %+v, want Team skipped for Live usage limit", skips)
	}
}

func TestLivePickSkipsHostCooldown(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"team.json", "alice.json"}, FailClosed: true})
	selector.NoteHostStates([]HostState{{
		AuthID:         "team.json",
		NextRetryAfter: time.Now().Add(time.Hour),
	}}, time.Now())

	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json"},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json"},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v, want alice.json after Team cooldown", resp, handled)
	}
}

func TestRememberLimitSurvivesEmptyHostListingWithinHour(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"team.json", "alice.json"}, FailClosed: true})
	until := time.Now().UTC().Add(30 * time.Minute)
	selector.RememberLimit("team.json", until)
	selector.NoteHostStates([]HostState{{AuthID: "team.json"}, {AuthID: "alice.json"}}, time.Now())

	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json"},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json"},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v, want alice.json after remembered Team limit", resp, handled)
	}
	stats := selector.Stats()
	if untilGot, ok := stats.Limits["team.json"]; !ok || !untilGot.Equal(until) {
		t.Fatalf("Stats.Limits = %+v, want remembered team.json until %s", stats.Limits, until)
	}
}

func TestNoteHostStatesDropsUnconfirmedLongRemember(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"team.json", "alice.json"}, FailClosed: true})
	selector.RememberLimit("team.json", time.Now().UTC().Add(7*24*time.Hour))
	selector.NoteHostStates([]HostState{{AuthID: "team.json"}, {AuthID: "alice.json"}}, time.Now())

	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json"},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json"},
	))
	if !handled || resp.AuthID != "team.json" {
		t.Fatalf("Pick() = %+v handled=%v, want team.json after dropping a stale weekly guess", resp, handled)
	}
	if _, ok := selector.Stats().Limits["team.json"]; ok {
		t.Fatalf("Stats.Limits = %+v, want Team's unconfirmed long remember dropped", selector.Stats().Limits)
	}
}

func TestNoteHostStatesKeepsRememberWhenCredentialOmitted(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"team.json", "alice.json"}, FailClosed: true})
	until := time.Now().UTC().Add(48 * time.Hour)
	selector.RememberLimit("team.json", until)
	selector.NoteHostStates([]HostState{{AuthID: "alice.json"}}, time.Now())

	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json"},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json"},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v, want alice.json while Team is omitted from the listing", resp, handled)
	}
	if untilGot, ok := selector.Stats().Limits["team.json"]; !ok || !untilGot.Equal(until) {
		t.Fatalf("Stats.Limits = %+v, want remembered team.json kept when listing omitted it", selector.Stats().Limits)
	}
}

func TestNoteHostStatesPrefersStatusMessageReset(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"team.json", "alice.json"}, FailClosed: true})
	now := time.Now().UTC()
	selector.RememberLimit("team.json", now.Add(7*24*time.Hour))
	resetsAt := now.Add(2 * time.Hour).Unix()
	selector.NoteHostStates([]HostState{{
		AuthID:        "team.json",
		StatusMessage: `{"error":{"type":"usage_limit_reached","plan_type":"team","resets_at":` + strconv.FormatInt(resetsAt, 10) + `}}`,
	}, {AuthID: "alice.json"}}, now)

	until, ok := selector.Stats().Limits["team.json"]
	if !ok || until.Unix() != resetsAt {
		t.Fatalf("Stats.Limits = %+v, want StatusMessage reset %d", selector.Stats().Limits, resetsAt)
	}
	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json"},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json"},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v, want alice.json while Team has a confirmed Live limit", resp, handled)
	}
}

func TestLivePickRecordsAuditDecision(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"team.json", "alice.json"}, FailClosed: true})
	selector.RememberLimit("team.json", time.Now().UTC().Add(48*time.Hour))
	resp, handled := selector.Pick(codexRequest(Model,
		pluginapi.SchedulerAuthCandidate{ID: "team.json"},
		pluginapi.SchedulerAuthCandidate{ID: "alice.json"},
	))
	if !handled || resp.AuthID != "alice.json" {
		t.Fatalf("Pick() = %+v handled=%v", resp, handled)
	}
	stats := selector.Stats()
	if len(stats.Decisions) != 1 || stats.Decisions[0].Outcome != OutcomePicked || stats.Decisions[0].AuthID != "alice.json" {
		t.Fatalf("Decisions = %+v, want one Alice pick", stats.Decisions)
	}
	if len(stats.Decisions[0].Skips) != 1 || stats.Decisions[0].Skips[0].AuthID != "team.json" {
		t.Fatalf("decision skips = %+v, want Team skipped", stats.Decisions[0].Skips)
	}
}

func TestRestoreSnapshotMarksLastUsed(t *testing.T) {
	selector := NewSelector()
	at := time.Date(2026, 11, 5, 15, 47, 0, 0, time.UTC)
	selector.Restore(Snapshot{LastAuthID: "alice.json", LastAt: at})
	stats := selector.Stats()
	if stats.LastAuthID != "alice.json" || !stats.LastAt.Equal(at) {
		t.Fatalf("Stats() = %+v, want restored last used alice.json", stats)
	}
}

func TestRestoreSnapshotDecisions(t *testing.T) {
	selector := NewSelector()
	at := time.Date(2026, 11, 5, 16, 27, 2, 0, time.UTC)
	selector.Restore(Snapshot{Decisions: []Decision{
		{ID: 7, At: at, Outcome: OutcomePicked, AuthID: "alice.json", Matching: 3, Ready: 1},
	}})
	stats := selector.Stats()
	if len(stats.Decisions) != 1 || stats.Decisions[0].ID != 7 || stats.Decisions[0].AuthID != "alice.json" {
		t.Fatalf("Decisions = %+v, want restored Alice pick 7", stats.Decisions)
	}
}

func TestDecisionHistoryCapsAt32(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"alice.json"}, FailClosed: true})
	req := codexRequest(Model, pluginapi.SchedulerAuthCandidate{ID: "alice.json"})
	for i := 0; i < MaxDecisionHistory+1; i++ {
		if resp, handled := selector.Pick(req); !handled || resp.AuthID != "alice.json" {
			t.Fatalf("Pick() = %+v handled=%v", resp, handled)
		}
	}
	stats := selector.Stats()
	if len(stats.Decisions) != MaxDecisionHistory {
		t.Fatalf("len(Decisions) = %d, want %d", len(stats.Decisions), MaxDecisionHistory)
	}
	if stats.Decisions[0].ID != int64(MaxDecisionHistory+1) || stats.Decisions[len(stats.Decisions)-1].ID != 2 {
		t.Fatalf("decision IDs = %d..%d, want newest %d and oldest 2", stats.Decisions[0].ID, stats.Decisions[len(stats.Decisions)-1].ID, MaxDecisionHistory+1)
	}
}

func TestNonLiveRequestIsUnhandled(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"live-a"}, FailClosed: true})
	req := codexRequest("gpt-5-codex", pluginapi.SchedulerAuthCandidate{ID: "live-a"})
	if resp, handled := selector.Pick(req); handled {
		t.Fatalf("Pick() = %+v handled=true, want unhandled for an ordinary model", resp)
	}
}
