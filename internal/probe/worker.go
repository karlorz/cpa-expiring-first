// Package probe runs the in-process quota probes and keeps their latest readings in memory.
//
// The worker probes Antigravity, Claude, Codex and xAI credentials through the host's HTTP
// transport, never through the provider SDKs, and publishes one snapshot per cycle. Picks read
// the snapshot; they never call a provider. The only credential write the plugin ever performs
// stays in main.go, where stale cooldowns are cleared through host.routing.reset_cooldown.
package probe

import (
	"context"
	"sync"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/antigravity"
	"github.com/karlorz/cpa-expiring-first/internal/probe/claude"
	"github.com/karlorz/cpa-expiring-first/internal/probe/codex"
	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"github.com/karlorz/cpa-expiring-first/internal/probe/xai"
	"github.com/karlorz/cpa-expiring-first/internal/router"
)

// DefaultInterval is how often the worker refreshes every credential's quota readings.
const DefaultInterval = 5 * time.Minute

// maxConcurrentProbes bounds how many probes run at once, so one slow provider cannot delay the
// whole cycle without limit.
const maxConcurrentProbes = 4

// Account is one credential's latest probe outcome, kept for the console page.
type Account struct {
	AuthID     string
	AuthIndex  string
	Provider   string
	Group      string
	ObservedAt time.Time
	Windows    []reading.Window
	PlanType   reading.PlanType
	Error      string
}

// Snapshot is one probe cycle's complete result.
type Snapshot struct {
	At       time.Time
	Accounts []Account
}

// Quota converts the snapshot into router readings, keyed by auth ID.
func (s Snapshot) Quota() router.Quota {
	if len(s.Accounts) == 0 {
		return nil
	}
	quota := make(router.Quota, len(s.Accounts))
	for _, account := range s.Accounts {
		if len(account.Windows) == 0 {
			continue
		}
		windows := make([]router.QuotaWindow, 0, len(account.Windows))
		for _, window := range account.Windows {
			if window.Minutes <= 0 {
				continue
			}
			windows = append(windows, router.QuotaWindow{
				Minutes:     window.Minutes,
				UsedPercent: window.UsedPercent,
				ResetsAt:    window.ResetsAt,
				Models:      window.Models,
			})
		}
		if len(windows) == 0 {
			continue
		}
		quota[account.AuthID] = router.QuotaAccount{UpdatedAt: account.ObservedAt, Windows: windows}
	}
	return quota
}

// Worker probes every credential on an interval and publishes snapshots. It is safe for
// concurrent use.
type Worker struct {
	memory *router.MemoryQuota

	mu       sync.Mutex
	interval time.Duration
	snapshot Snapshot
	// running guards against overlapping cycles when a refresh is requested while one runs.
	running bool
	// cancel stops the background loop; nil while it is not running.
	cancel context.CancelFunc
	done   chan struct{}
}

// NewWorker returns a worker that publishes readings into memory.
func NewWorker(memory *router.MemoryQuota) *Worker {
	return &Worker{memory: memory, interval: DefaultInterval}
}

// SetInterval changes how often the background loop probes.
func (w *Worker) SetInterval(interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	w.mu.Lock()
	w.interval = interval
	w.mu.Unlock()
}

// Start launches the background loop if it is not running. The first cycle starts after one
// interval, so plugin registration never calls back into the host on its own stack. Each wait
// re-reads the interval, so a reconfigure takes effect without restarting the loop.
func (w *Worker) Start(callbacks host.Callbacks) {
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	done := w.done
	w.mu.Unlock()

	go func() {
		defer close(done)
		timer := time.NewTimer(w.currentInterval())
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			w.Refresh(ctx, callbacks)
			timer.Reset(w.currentInterval())
		}
	}()
}

func (w *Worker) currentInterval() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.interval <= 0 {
		return DefaultInterval
	}
	return w.interval
}

// Stop ends the background loop and waits for the current cycle to finish.
func (w *Worker) Stop(ctx context.Context) {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	w.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Snapshot returns the latest published readings.
func (w *Worker) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshot
}

// Refresh runs one probe cycle and publishes it. It returns the snapshot it published, or the
// previous one when a cycle was already running or the host gave no credentials.
func (w *Worker) Refresh(ctx context.Context, callbacks host.Callbacks) Snapshot {
	w.mu.Lock()
	if w.running {
		previous := w.snapshot
		w.mu.Unlock()
		return previous
	}
	w.running = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
	}()

	client := host.NewClient(callbacks)
	credentials, errList := listCredentials(ctx, client)
	if errList != nil {
		// A failed listing says nothing about quota: keep the last readings instead of
		// replacing them with nothing, which would send every route back to sticky order.
		return w.Snapshot()
	}
	accounts := w.probeAll(ctx, client, credentials)
	return w.publish(Snapshot{At: time.Now().UTC(), Accounts: accounts})
}

func (w *Worker) publish(snapshot Snapshot) Snapshot {
	w.mu.Lock()
	w.snapshot = snapshot
	w.mu.Unlock()
	if w.memory != nil {
		w.memory.Replace(snapshot.Quota())
	}
	return snapshot
}

// probeAll runs one probe per credential, bounded by maxConcurrentProbes.
func (w *Worker) probeAll(ctx context.Context, client *host.Client, credentials []credential) []Account {
	if len(credentials) == 0 {
		return nil
	}
	accounts := make([]Account, len(credentials))
	slots := make(chan struct{}, maxConcurrentProbes)
	var wg sync.WaitGroup
	for index, item := range credentials {
		index, item := index, item
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			accounts[index] = probeCredential(ctx, client, item)
		}()
	}
	wg.Wait()
	return accounts
}

// probeCredential probes one credential and returns one account entry per model group. An
// Antigravity credential is probed once per group because the provider meters them separately.
func probeCredential(ctx context.Context, client *host.Client, item credential) Account {
	now := time.Now().UTC()
	switch item.provider {
	case "antigravity":
		return probeAntigravityCredential(ctx, client, item, now)
	case "claude":
		result := claude.NewProber(client, fixedClock{now: now}).Probe(ctx, claude.ProbeRequest{
			AuthID:           item.id,
			AuthIndex:        item.authIndex,
			AccessToken:      item.material.AccessToken,
			OrganizationUUID: item.material.OrganizationUUID,
			BaseURL:          item.material.BaseURL,
		})
		return Account{
			AuthID:     item.id,
			AuthIndex:  item.authIndex,
			Provider:   item.provider,
			ObservedAt: now,
			Windows:    claude.Windows(result),
			PlanType:   result.PlanType,
			Error:      sanitizeError(result.Error),
		}
	case "xai":
		result := xai.NewProber(client, fixedClock{now: now}).Probe(ctx, xai.ProbeRequest{
			AuthID:      item.id,
			AuthIndex:   item.authIndex,
			AccessToken: item.material.AccessToken,
			BaseURL:     item.material.BaseURL,
			AuthKind:    item.material.AuthKind,
			UserID:      item.material.UserID,
		})
		return Account{
			AuthID:     item.id,
			AuthIndex:  item.authIndex,
			Provider:   item.provider,
			ObservedAt: now,
			Windows:    xai.Windows(result),
			PlanType:   result.PlanType,
			Error:      sanitizeError(result.Error),
		}
	default:
		result := codex.NewProber(client, fixedClock{now: now}).Probe(ctx, codex.ProbeRequest{
			AuthID:      item.id,
			AuthIndex:   item.authIndex,
			AccountID:   item.material.AccountID,
			AccessToken: item.material.AccessToken,
		})
		return Account{
			AuthID:     item.id,
			AuthIndex:  item.authIndex,
			Provider:   item.provider,
			ObservedAt: now,
			Windows:    codex.Windows(result),
			PlanType:   result.PlanType,
			Error:      sanitizeError(result.Error),
		}
	}
}

// probeAntigravityCredential probes both Antigravity model groups. The gemini group's windows
// apply only to Gemini models and the claude_gpt group's only to Claude, GPT and OpenAI models,
// matching how the provider meters them.
func probeAntigravityCredential(ctx context.Context, client *host.Client, item credential, now time.Time) Account {
	prober := antigravity.NewProber(client, fixedClock{now: now})
	account := Account{
		AuthID:     item.id,
		AuthIndex:  item.authIndex,
		Provider:   item.provider,
		Group:      antigravity.ModelGroupGemini,
		ObservedAt: now,
	}
	for _, group := range []antigravity.ModelGroup{antigravity.ModelGroupGemini, antigravity.ModelGroupClaudeGPT} {
		result := prober.Probe(ctx, antigravity.ProbeRequest{
			AuthID:      item.id,
			AuthIndex:   item.authIndex,
			AccessToken: item.material.AccessToken,
			ProjectID:   item.material.ProjectID,
			ModelGroup:  group,
		})
		account.Windows = append(account.Windows, antigravity.WindowsForGroup(result)...)
		if result.PlanType != reading.PlanTypeUnknown {
			account.PlanType = result.PlanType
		}
		if message := sanitizeError(result.Error); message != "" {
			account.Error = joinErrors(account.Error, group+": "+message)
		}
	}
	return account
}

func joinErrors(existing, message string) string {
	if existing == "" {
		return message
	}
	return existing + "; " + message
}

// sanitizeError keeps probe errors short and free of credential-shaped text.
func sanitizeError(message string) string {
	trimmed := host.RedactText(message)
	if len(trimmed) > 240 {
		trimmed = trimmed[:240]
	}
	return trimmed
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now.UTC()
}
