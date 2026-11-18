package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/codex"
	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"github.com/karlorz/cpa-expiring-first/internal/router"
)

func TestWindowStarterWaitsThenPicksOneUnusedCodex(t *testing.T) {
	starter := newWindowStarter()
	starter.randIntn = func(n int) int { return 0 }
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	accounts := []Account{
		{AuthID: "used.json", Provider: "codex", Windows: []reading.Window{{Minutes: 10080, UsedPercent: 26, ResetsAt: now.Add(48 * time.Hour)}}},
		{AuthID: "unused-b.json", Provider: "codex", Windows: []reading.Window{{Minutes: 10080, UsedPercent: 0, ResetsAt: now.Add(6 * 24 * time.Hour)}}},
		{AuthID: "unused-a.json", Provider: "codex", Windows: []reading.Window{{Minutes: 10080, UsedPercent: 0, ResetsAt: now.Add(6 * 24 * time.Hour)}}},
	}
	creds := []credential{
		{id: "used.json", material: authMaterial{AccessToken: "t"}},
		{id: "unused-b.json", material: authMaterial{AccessToken: "t"}},
		{id: "unused-a.json", material: authMaterial{AccessToken: "t"}},
	}
	if due := starter.due(accounts, creds, now, 30*time.Minute, 0); len(due) != 0 {
		t.Fatalf("due immediately = %+v", due)
	}
	due := starter.due(accounts, creds, now.Add(30*time.Minute), 30*time.Minute, 0)
	if len(due) != 2 || due[0].id != "unused-a.json" || due[1].id != "unused-b.json" {
		t.Fatalf("due after wait = %+v", due)
	}
	item, ok := starter.choose(due)
	if !ok || item.id != "unused-a.json" {
		t.Fatalf("choose = %+v", item)
	}
	starter.markPinged(item.id)
	due = starter.due(accounts, creds, now.Add(time.Hour), 30*time.Minute, 0)
	if len(due) != 1 || due[0].id != "unused-b.json" {
		t.Fatalf("after ping = %+v", due)
	}
}

func TestRefreshSendsOneUnusedCodexStartRequest(t *testing.T) {
	var posts []string
	weeklyReset := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	fake := &fakeHost{
		files: []host.AuthFile{
			{ID: "unused.json", AuthIndex: "idx-unused", Provider: "codex"},
			{ID: "used.json", AuthIndex: "idx-used", Provider: "codex"},
		},
		auth: map[string]json.RawMessage{
			"idx-unused": json.RawMessage(`{"access_token":"tok-unused","account_id":"acct-u"}`),
			"idx-used":   json.RawMessage(`{"access_token":"tok-used","account_id":"acct-v"}`),
		},
		httpDo: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			if strings.Contains(req.URL, "codex/responses") {
				posts = append(posts, req.AuthIndex+" "+string(req.Body))
				if req.Method != http.MethodPost {
					t.Fatalf("method=%s", req.Method)
				}
				if !strings.Contains(string(req.Body), `"store":false`) || strings.Contains(string(req.Body), `"stream":true`) {
					t.Fatalf("body=%s", req.Body)
				}
				return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"id":"resp"}`)}, nil
			}
			used := "0"
			if req.AuthIndex == "idx-used" {
				used = "26"
			}
			return host.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":0,"reset_at":"2026-11-18T16:00:00Z","limit_window_seconds":18000},"secondary_window":{"used_percent":` + used + `,"reset_at":"` + weeklyReset.Format(time.RFC3339) + `","limit_window_seconds":604800}}}`),
			}, nil
		},
	}
	cfg, err := router.ParseConfig([]byte("codex_window_start_after: 0s\ncodex_window_start_jitter: 0s\n"))
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(router.NewMemoryQuota())
	worker.Configure(cfg)
	worker.starter.randIntn = func(n int) int { return 0 }
	var logs []string
	worker.SetLog(func(message string) { logs = append(logs, message) })
	worker.Refresh(context.Background(), fake)
	if len(posts) != 1 || !strings.Contains(posts[0], "idx-unused") {
		t.Fatalf("posts=%v", posts)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "started unused Codex weekly window on unused.json") {
		t.Fatalf("logs=%v", logs)
	}
	worker.Refresh(context.Background(), fake)
	if len(posts) != 1 {
		t.Fatalf("second cycle posted again: %v", posts)
	}
}

func TestRefreshSkipsCodexStartWhenDisabled(t *testing.T) {
	posted := false
	fake := &fakeHost{
		files: []host.AuthFile{{ID: "unused.json", AuthIndex: "idx", Provider: "codex"}},
		auth:  map[string]json.RawMessage{"idx": json.RawMessage(`{"access_token":"tok"}`)},
		httpDo: func(req host.HTTPRequest) (host.HTTPResponse, error) {
			if strings.Contains(req.URL, "codex/responses") {
				posted = true
			}
			return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":0,"reset_at":"2026-11-18T16:00:00Z","limit_window_seconds":18000},"secondary_window":{"used_percent":0,"reset_at":"2026-11-24T12:00:00Z","limit_window_seconds":604800}}}`)}, nil
		},
	}
	cfg, err := router.ParseConfig([]byte("codex_window_start_ping: false\ncodex_window_start_after: 0s\n"))
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(router.NewMemoryQuota())
	worker.Configure(cfg)
	worker.Refresh(context.Background(), fake)
	if posted {
		t.Fatal("ping ran while disabled")
	}
}

func TestStartPromptsAreShort(t *testing.T) {
	if len(codex.StartPrompts) < 3 {
		t.Fatal("need a prompt pool")
	}
	for _, prompt := range codex.StartPrompts {
		if prompt == "" || len(prompt) > 40 {
			t.Fatalf("prompt %q", prompt)
		}
	}
}
