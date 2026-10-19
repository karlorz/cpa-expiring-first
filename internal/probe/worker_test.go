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
