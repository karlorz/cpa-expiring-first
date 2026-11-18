package probe

import (
	"context"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/codex"
	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
)

type windowStartState struct {
	windowKey string
	seenAt    time.Time
	delay     time.Duration
	pinged    bool
}

type windowStarter struct {
	mu       sync.Mutex
	states   map[string]windowStartState
	now      func() time.Time
	randIntn func(int) int
	log      func(string)
}

func newWindowStarter() *windowStarter {
	return &windowStarter{
		states:   map[string]windowStartState{},
		now:      func() time.Time { return time.Now().UTC() },
		randIntn: rand.IntN,
	}
}

func (s *windowStarter) setLog(log func(string)) {
	s.mu.Lock()
	s.log = log
	s.mu.Unlock()
}

func (s *windowStarter) report(message string) {
	s.mu.Lock()
	log := s.log
	s.mu.Unlock()
	if log != nil && strings.TrimSpace(message) != "" {
		log(message)
	}
}

func weeklyUsed(account Account) (used float64, key string, ok bool) {
	longest := 0.0
	found := false
	for _, window := range account.Windows {
		if window.Minutes < 24*60 {
			continue
		}
		if !found || window.Minutes > longest {
			found = true
			longest = window.Minutes
			used = window.UsedPercent
			if window.ResetsAt.IsZero() {
				key = "unstarted"
			} else {
				key = window.ResetsAt.UTC().Format(time.RFC3339)
			}
		}
	}
	return used, key, found
}

func (s *windowStarter) pickPrompt() string {
	s.mu.Lock()
	n := s.randIntn
	s.mu.Unlock()
	if n == nil {
		n = rand.IntN
	}
	return codex.PickStartPrompt(n)
}

func (s *windowStarter) due(accounts []Account, credentials []credential, now time.Time, after, jitter time.Duration) []credential {
	byID := make(map[string]credential, len(credentials))
	for _, item := range credentials {
		byID[item.id] = item
	}
	var due []credential
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if account.Provider != "codex" || strings.TrimSpace(account.Error) != "" {
			continue
		}
		used, key, okWeekly := weeklyUsed(account)
		if !okWeekly || used > 0 {
			delete(s.states, account.AuthID)
			continue
		}
		item, okCred := byID[account.AuthID]
		if !okCred || strings.TrimSpace(item.material.AccessToken) == "" {
			continue
		}
		seen[account.AuthID] = struct{}{}
		state, okState := s.states[account.AuthID]
		if !okState || state.windowKey != key {
			state = windowStartState{windowKey: key, seenAt: now, delay: s.delayForLocked(after, jitter)}
			s.states[account.AuthID] = state
		}
		if state.pinged || now.Before(state.seenAt.Add(state.delay)) {
			continue
		}
		due = append(due, item)
	}
	for authID := range s.states {
		if _, okSeen := seen[authID]; !okSeen {
			delete(s.states, authID)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].id < due[j].id })
	return due
}

func (s *windowStarter) delayForLocked(after, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return after
	}
	seconds := int(jitter / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	n := s.randIntn
	if n == nil {
		n = rand.IntN
	}
	return after + time.Duration(n(seconds+1))*time.Second
}

func (s *windowStarter) markPinged(authID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, okState := s.states[authID]
	if !okState {
		return
	}
	state.pinged = true
	s.states[authID] = state
}

func (s *windowStarter) choose(due []credential) (credential, bool) {
	if len(due) == 0 {
		return credential{}, false
	}
	s.mu.Lock()
	n := s.randIntn
	s.mu.Unlock()
	if n == nil {
		n = rand.IntN
	}
	return due[n(len(due))], true
}

func (w *Worker) startUnusedCodexWindows(ctx context.Context, client *host.Client, credentials []credential, snapshot Snapshot) {
	if client == nil || !w.codexWindowStartEnabled() {
		return
	}
	w.mu.Lock()
	cfg := w.ranking
	starter := w.starter
	w.mu.Unlock()
	if starter == nil || !cfg.CodexWindowStartPing() {
		return
	}
	now := snapshot.At
	if now.IsZero() {
		now = starter.now()
	}
	due := starter.due(snapshot.Accounts, credentials, now, cfg.CodexWindowStartAfter(), cfg.CodexWindowStartJitter())
	item, okItem := starter.choose(due)
	if !okItem {
		return
	}
	req, errReq := codex.StartHTTPRequest(codex.StartRequest{
		AuthIndex:   item.authIndex,
		AccountID:   item.material.AccountID,
		AccessToken: item.material.AccessToken,
		Model:       cfg.CodexWindowStartModel(),
		Prompt:      starter.pickPrompt(),
	})
	if errReq != nil {
		starter.report("expiring-first: unused Codex window start encode failed")
		return
	}
	resp, errDo := client.HTTPDoRaw(ctx, req)
	if errDo != nil {
		starter.report("expiring-first: unused Codex window start failed for " + item.id + ": " + host.RedactText(errDo.Error()))
		return
	}
	ok, retryable, message := codex.EvaluateStart(resp.StatusCode, resp.Body)
	if ok {
		starter.markPinged(item.id)
		starter.report("expiring-first: started unused Codex weekly window on " + item.id)
		return
	}
	if retryable {
		starter.report("expiring-first: unused Codex window start retry later for " + item.id + ": " + message)
		return
	}
	starter.markPinged(item.id)
	starter.report("expiring-first: unused Codex window start gave up for " + item.id + ": " + message)
}

func (w *Worker) codexWindowStartEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ranking.CodexWindowStartPing()
}
