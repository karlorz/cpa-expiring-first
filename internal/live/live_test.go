package live

import (
	"testing"

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
}

func TestLiveRequestFailOpenDelegatesToBuiltin(t *testing.T) {
	selector := NewSelector()
	selector.Configure(Config{AuthIDs: []string{"live-a"}, Strategy: StrategyRoundRobin, FailClosed: false})
	resp, handled := selector.Pick(codexRequest(Model, pluginapi.SchedulerAuthCandidate{ID: "other"}))
	if !handled || !resp.Handled || resp.DelegateBuiltin != StrategyRoundRobin {
		t.Fatalf("Pick() = %+v handled=%v, want a round-robin delegation", resp, handled)
	}
}

func TestLiveRequestWithoutAllowlistIsUnhandled(t *testing.T) {
	selector := NewSelector()
	if resp, handled := selector.Pick(codexRequest(Model, pluginapi.SchedulerAuthCandidate{ID: "a"})); handled {
		t.Fatalf("Pick() = %+v handled=true, want unhandled so the host decides", resp)
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
