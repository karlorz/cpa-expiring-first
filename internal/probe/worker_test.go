package probe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"github.com/karlorz/cpa-expiring-first/internal/router"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type fakeHost struct {
	files   []host.AuthFile
	auth    map[string]json.RawMessage
	httpDo  func(req host.HTTPRequest) (host.HTTPResponse, error)
	listErr error
}

func (f *fakeHost) ListAuthFiles(ctx context.Context) ([]host.AuthFile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.files, nil
}

func (f *fakeHost) GetAuth(ctx context.Context, authIndex string) (host.AuthDocument, error) {
	raw, okAuth := f.auth[authIndex]
	if !okAuth {
		return host.AuthDocument{}, errors.New("no auth")
	}
	return host.AuthDocument{AuthIndex: authIndex, JSON: raw}, nil
}

func (f *fakeHost) HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	if f.httpDo == nil {
		return host.HTTPResponse{}, errors.New("no http")
	}
	return f.httpDo(req)
}

// TestRefreshMapsProbesIntoRouterWindows runs one full cycle against a fake host and checks that
// the published snapshot both carries the account detail and converts into router readings.
func TestRefreshMapsProbesIntoRouterWindows(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	weeklyReset := now.Add(72 * time.Hour)
	fake := &fakeHost{
		files: []host.AuthFile{
			{ID: "claude-a.json", AuthIndex: "idx-claude", Provider: "claude"},
			{ID: "codex-a.json", AuthIndex: "idx-codex", Provider: "codex"},
		},
		auth: map[string]json.RawMessage{
			"idx-claude": json.RawMessage(`{"access_token":"claude-token"}`),
			"idx-codex":  json.RawMessage(`{"access_token":"codex-token","account_id":"acct"}`),
		},
		httpDo: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			switch {
			case strings.Contains(req.URL, "anthropic"):
				return host.HTTPResponse{
					StatusCode: http.StatusOK,
					Headers: host.Header{
						"anthropic-ratelimit-unified-7d-utilization": []string{"0.5"},
						"anthropic-ratelimit-unified-7d-reset":       []string{weeklyReset.Format(time.RFC3339)},
					},
				}, nil
			case strings.Contains(req.URL, "wham/usage"):
				return host.HTTPResponse{
					StatusCode: http.StatusOK,
					Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary_window":{"used_percent":20,"reset_at":"` +
						weeklyReset.Format(time.RFC3339) + `","limit_window_seconds":604800}}}`),
				}, nil
			default:
				return host.HTTPResponse{}, errors.New("unexpected url")
			}
		},
	}

	memory := router.NewMemoryQuota()
	worker := NewWorker(memory)
	snapshot := worker.Refresh(context.Background(), fake)
	if len(snapshot.Accounts) != 2 {
		t.Fatalf("accounts = %+v, want claude and codex", snapshot.Accounts)
	}

	quota := memory.Load()
	if len(quota) != 2 {
		t.Fatalf("quota = %+v, want both accounts", quota)
	}
	claudeAccount, okClaude := quota["claude-a.json"]
	if !okClaude {
		t.Fatalf("quota has no claude-a.json: %+v", quota)
	}
	if len(claudeAccount.Windows) != 1 || claudeAccount.Windows[0].UsedPercent != 50 {
		t.Fatalf("claude windows = %+v, want one weekly window at 50%% used", claudeAccount.Windows)
	}
	if len(claudeAccount.Windows[0].Models) != 0 {
		t.Errorf("claude models = %v, want empty (applies to every model)", claudeAccount.Windows[0].Models)
	}
	codexAccount, okCodex := quota["codex-a.json"]
	if !okCodex {
		t.Fatalf("quota has no codex-a.json: %+v", quota)
	}
	if len(codexAccount.Windows) != 1 || codexAccount.Windows[0].UsedPercent != 20 {
		t.Fatalf("codex windows = %+v, want one weekly window at 20%% used", codexAccount.Windows)
	}
	if claudeAccount.UpdatedAt.IsZero() {
		t.Errorf("updated_at = %v, want a probe timestamp", claudeAccount.UpdatedAt)
	}
}

// TestAntigravityProbesBothModelGroups checks that one Antigravity credential is probed once per
// group and that each group's windows carry the matching model patterns.
func TestAntigravityProbesBothModelGroups(t *testing.T) {
	var urls []string
	fake := &fakeHost{
		files: []host.AuthFile{{ID: "antigravity.json", AuthIndex: "idx-ag", Provider: "antigravity"}},
		auth:  map[string]json.RawMessage{"idx-ag": json.RawMessage(`{"access_token":"ag-token","project_id":"proj"}`)},
		httpDo: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			urls = append(urls, req.URL)
			return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"groups":[
				{"displayName":"Gemini Models","buckets":[{"window":"weekly","remainingFraction":0.5,"resetTime":"2026-10-12T12:00:00Z"}]},
				{"displayName":"Claude and GPT Models","buckets":[{"window":"weekly","remainingFraction":0.1,"resetTime":"2026-10-16T12:00:00Z"}]}
			]}`)}, nil
		},
	}
	memory := router.NewMemoryQuota()
	worker := NewWorker(memory)
	worker.Refresh(context.Background(), fake)

	// The probe tries the model groups in order, and stops at the first URL that answers.
	if len(urls) < 2 {
		t.Fatalf("probe URLs = %v, want one request per model group", urls)
	}
	quota := memory.Load()
	account, okAccount := quota["antigravity.json"]
	if !okAccount {
		t.Fatalf("quota = %+v, want the antigravity account", quota)
	}
	if len(account.Windows) != 2 {
		t.Fatalf("windows = %+v, want one per model group", account.Windows)
	}
	seen := map[string][]string{}
	for _, window := range account.Windows {
		seen[strings.Join(window.Models, ",")] = window.Models
	}
	if _, okGemini := seen["gemini"]; !okGemini {
		t.Errorf("no gemini-scoped window: %+v", account.Windows)
	}
	if _, okClaudeGPT := seen["claude,gpt,openai"]; !okClaudeGPT {
		t.Errorf("no claude_gpt-scoped window: %+v", account.Windows)
	}
}

// TestRefreshKeepsReadingsWhenListingFails checks that a failed host listing does not wipe the
// good readings, which would send every route back to sticky order.
func TestRefreshKeepsReadingsWhenListingFails(t *testing.T) {
	fake := &fakeHost{
		files: []host.AuthFile{{ID: "codex-a.json", AuthIndex: "idx-codex", Provider: "codex"}},
		auth:  map[string]json.RawMessage{"idx-codex": json.RawMessage(`{"access_token":"t"}`)},
		httpDo: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary_window":{"used_percent":20,"reset_at":"2026-10-14T12:00:00Z","limit_window_seconds":604800}}}`)}, nil
		},
	}
	memory := router.NewMemoryQuota()
	worker := NewWorker(memory)
	worker.Refresh(context.Background(), fake)
	if len(memory.Load()) != 1 {
		t.Fatalf("quota after first cycle = %+v, want one account", memory.Load())
	}

	fake.listErr = errors.New("host unavailable")
	worker.Refresh(context.Background(), fake)
	if len(memory.Load()) != 1 {
		t.Fatalf("quota after a failed listing = %+v, want the readings kept", memory.Load())
	}
}

// TestSnapshotQuotaSkipsAccountsWithoutWindows keeps a failed probe out of the ranking.
func TestSnapshotQuotaSkipsAccountsWithoutWindows(t *testing.T) {
	snapshot := Snapshot{At: time.Now(), Accounts: []Account{
		{AuthID: "good", ObservedAt: time.Now(), Windows: []reading.Window{{Minutes: 10080, UsedPercent: 10, ResetsAt: time.Now().Add(time.Hour)}}},
		{AuthID: "failed", Error: "probe failed"},
	}}
	quota := snapshot.Quota()
	if len(quota) != 1 {
		t.Fatalf("quota = %+v, want only the account with windows", quota)
	}
	if _, okGood := quota["good"]; !okGood {
		t.Fatalf("quota = %+v, want the good account", quota)
	}
}

func TestFailedProbeRetainsOriginalAgeAndExpires(t *testing.T) {
	now := time.Now().UTC()
	previous := []Account{{AuthID: "a", ObservedAt: now.Add(-29 * time.Minute), Windows: []reading.Window{{Minutes: 10080, UsedPercent: 20, ResetsAt: now.Add(time.Hour)}}}}
	current := retainEvidence(previous, []Account{{AuthID: "a", ObservedAt: now, Error: "quota status 403"}})
	if len(current[0].Windows) != 1 || !current[0].ObservedAt.Equal(previous[0].ObservedAt) || current[0].Error == "" {
		t.Fatalf("retention=%+v", current)
	}
	s := router.NewScheduler(router.DefaultConfig(), nil)
	memory := router.NewMemoryQuota()
	memory.Replace(Snapshot{Accounts: current}.Quota())
	s.UseQuota(memory)
	candidates := []pluginapi.SchedulerAuthCandidate{{ID: "a"}}
	if _, ok := s.Preview(candidates, "antigravity", "gemini-3.8-flash-high", now); !ok {
		t.Fatal("retained fresh evidence missing")
	}
	if _, ok := s.Preview(candidates, "antigravity", "gemini-3.8-flash-high", now.Add(2*time.Minute)); ok {
		t.Fatal("retained evidence age was renewed")
	}
}
func TestDisabledAndDeletedCredentialsLoseProbeEvidence(t *testing.T) {
	fake := &fakeHost{files: []host.AuthFile{{ID: "disabled", AuthIndex: "idx", Provider: "codex", Disabled: true}}}
	memory := router.NewMemoryQuota()
	worker := NewWorker(memory)
	worker.publish(Snapshot{Accounts: []Account{{AuthID: "deleted", ObservedAt: time.Now(), Windows: []reading.Window{{Minutes: 10080}}}}})
	snapshot := worker.Refresh(context.Background(), fake)
	if len(snapshot.Accounts) != 0 || len(memory.Load()) != 0 {
		t.Fatalf("removed credentials remain: %+v", snapshot)
	}
}
func TestRuntimeProjectOverridesPhysicalProject(t *testing.T) {
	fake := &fakeHost{files: []host.AuthFile{{ID: "a", AuthIndex: "idx", Provider: "antigravity", ProjectID: "runtime-project"}}, auth: map[string]json.RawMessage{"idx": json.RawMessage(`{"access_token":"token","project_id":"old-project"}`)}}
	credentials, err := listCredentials(context.Background(), host.NewClient(fake))
	if err != nil || len(credentials) != 1 || credentials[0].material.ProjectID != "runtime-project" {
		t.Fatalf("credentials=%+v err=%v", credentials, err)
	}
	material := parseAuthMaterial(json.RawMessage(`{"token":{"access_token":"token"},"metadata":{"projectId":"nested-project"}}`))
	if material.AccessToken != "token" || material.ProjectID != "nested-project" {
		t.Fatal("nested credential fields missing")
	}
}

func TestPartialGroupFailureRetainsBothOriginalWindows(t *testing.T) {
	old := time.Date(2026, 10, 19, 11, 0, 0, 0, time.UTC)
	prior := []Account{{AuthID: "a", ObservedAt: old, Windows: []reading.Window{
		{Minutes: 10080, Models: []string{"gemini"}}, {Minutes: 300, Models: []string{"gemini"}},
	}}}
	fresh := []Account{{AuthID: "a", ObservedAt: old.Add(time.Minute), Error: "gemini failed", Windows: []reading.Window{{Minutes: 10080, Models: []string{"claude", "gpt", "openai"}}}}}
	result := retainEvidence(prior, fresh)
	if len(result[0].Windows) != 3 || !result[0].ObservedAt.Equal(old) {
		t.Fatalf("partial retention=%+v", result)
	}
}

func TestRefreshFailureRetainsEvidenceInPublishedMemory(t *testing.T) {
	fake := &fakeHost{files: []host.AuthFile{{ID: "a", AuthIndex: "idx", Provider: "codex"}}, auth: map[string]json.RawMessage{"idx": json.RawMessage(`{"access_token":"token"}`)}, httpDo: func(host.HTTPRequest) (host.HTTPResponse, error) {
		return host.HTTPResponse{StatusCode: 200, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary_window":{"used_percent":20,"reset_at":"2026-10-24T04:25:56Z","limit_window_seconds":604800}}}`)}, nil
	}}
	memory := router.NewMemoryQuota()
	worker := NewWorker(memory)
	first := worker.Refresh(context.Background(), fake)
	fake.httpDo = func(host.HTTPRequest) (host.HTTPResponse, error) { return host.HTTPResponse{}, errors.New("offline") }
	second := worker.Refresh(context.Background(), fake)
	if len(second.Accounts) != 1 || second.Accounts[0].Error == "" || !second.Accounts[0].ObservedAt.Equal(first.Accounts[0].ObservedAt) {
		t.Fatalf("failed refresh=%+v", second)
	}
	if len(memory.Load()["a"].Windows) != 1 || !memory.Load()["a"].UpdatedAt.Equal(first.Accounts[0].ObservedAt) {
		t.Fatal("published evidence missing or re-aged")
	}
}
