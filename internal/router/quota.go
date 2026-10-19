package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// QuotaSchema identifies version 1 of the quota file format. See README.md.
const QuotaSchema = "expiring-first.quota.v1"

// QuotaWindow is one usage window of an account, such as a five-hour or weekly limit.
type QuotaWindow struct {
	Minutes     float64
	UsedPercent float64
	// ResetsAt is when the window resets; zero when it has not started.
	ResetsAt time.Time
	// Models restricts the window to models whose ID contains one of these strings
	// (lower-case); empty applies it to every model.
	Models []string
}

// QuotaAccount is the latest quota reading of one credential.
type QuotaAccount struct {
	UpdatedAt time.Time
	Windows   []QuotaWindow
}

// Quota holds every account of a quota file, keyed by auth ID.
type Quota map[string]QuotaAccount

type quotaFileBody struct {
	Schema   string             `json:"schema"`
	Accounts []quotaFileAccount `json:"accounts"`
}

type quotaFileAccount struct {
	AuthID    string            `json:"auth_id"`
	UpdatedAt time.Time         `json:"updated_at"`
	Windows   []quotaFileWindow `json:"windows"`
}

type quotaFileWindow struct {
	WindowMinutes *float64   `json:"window_minutes"`
	UsedPercent   *float64   `json:"used_percent"`
	ResetsAt      *time.Time `json:"resets_at"`
	Models        []string   `json:"models"`
}

// ParseQuota decodes a quota file. Accounts without an auth ID or update time, and windows
// without a positive length or a used percentage, are dropped.
func ParseQuota(raw []byte) (Quota, error) {
	var body quotaFileBody
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil {
		return nil, fmt.Errorf("decode quota file: %w", errUnmarshal)
	}
	if body.Schema != QuotaSchema {
		return nil, fmt.Errorf("quota file schema %q, want %q", body.Schema, QuotaSchema)
	}
	quota := make(Quota, len(body.Accounts))
	for _, entry := range body.Accounts {
		authID := strings.TrimSpace(entry.AuthID)
		if authID == "" || entry.UpdatedAt.IsZero() {
			continue
		}
		account := QuotaAccount{UpdatedAt: entry.UpdatedAt}
		for _, w := range entry.Windows {
			if w.WindowMinutes == nil || *w.WindowMinutes <= 0 || w.UsedPercent == nil {
				continue
			}
			window := QuotaWindow{Minutes: *w.WindowMinutes, UsedPercent: min(max(*w.UsedPercent, 0), 100)}
			if w.ResetsAt != nil {
				window.ResetsAt = *w.ResetsAt
			}
			for _, model := range w.Models {
				if model = strings.ToLower(strings.TrimSpace(model)); model != "" {
					window.Models = append(window.Models, model)
				}
			}
			account.Windows = append(account.Windows, window)
		}
		quota[authID] = account
	}
	return quota, nil
}

// QuotaSource supplies the latest quota. Load returns nil when no quota has been read yet.
type QuotaSource interface {
	Load() Quota
}

// QuotaFile reads a quota file and re-reads it only when it changes, checking at most once
// per ttl so picks do not hit the file system.
type QuotaFile struct {
	path   string
	ttl    time.Duration
	report func(error)
	now    func() time.Time

	mu        sync.Mutex
	checkedAt time.Time
	modTime   time.Time
	size      int64
	// statFailed suppresses repeated reports of the same unreadable file.
	statFailed bool
	quota      Quota
}

// NewQuotaFile returns a QuotaFile for path. report receives read and parse errors, once per
// change of the file; the last good quota stays in use meanwhile.
func NewQuotaFile(path string, ttl time.Duration, report func(error)) *QuotaFile {
	return &QuotaFile{path: path, ttl: ttl, report: report, now: time.Now}
}

// Load implements QuotaSource.
func (f *QuotaFile) Load() Quota {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if !f.checkedAt.IsZero() && now.Sub(f.checkedAt) < f.ttl {
		return f.quota
	}
	f.checkedAt = now

	info, errStat := os.Stat(f.path)
	if errStat != nil {
		// A missing file is expected before the producer first writes it.
		if !errors.Is(errStat, fs.ErrNotExist) && !f.statFailed {
			f.report(fmt.Errorf("stat quota file: %w", errStat))
		}
		f.statFailed = true
		return f.quota
	}
	f.statFailed = false
	if info.ModTime().Equal(f.modTime) && info.Size() == f.size {
		return f.quota
	}
	f.modTime, f.size = info.ModTime(), info.Size()

	raw, errRead := os.ReadFile(f.path)
	if errRead != nil {
		f.report(fmt.Errorf("read quota file: %w", errRead))
		return f.quota
	}
	quota, errParse := ParseQuota(raw)
	if errParse != nil {
		f.report(errParse)
		return f.quota
	}
	f.quota = quota
	return f.quota
}

// quotaChoice is the account the quota ranking picked, with the reset that ranked it.
type quotaChoice struct {
	authID string
	// resetsAt is the reset of the account's longest window; zero when it has not started.
	resetsAt time.Time
}

type rankedCandidate struct {
	candidate pluginapi.SchedulerAuthCandidate
	resetsAt  time.Time
}

// rankByQuota picks the candidate whose longest window resets soonest and still has quota.
// Candidates without a reading younger than maxAge do not take part; ok is false when no
// candidate does, so the caller falls back to sticky routing.
func rankByQuota(candidates []pluginapi.SchedulerAuthCandidate, quota Quota, model string, now time.Time, maxAge time.Duration, reservePercent float64) (quotaChoice, bool) {
	var best *rankedCandidate
	for _, candidate := range candidates {
		resetsAt, reason := quotaCandidateRank(candidate, quota, model, now, maxAge, reservePercent)
		if reason != "" {
			continue
		}
		next := rankedCandidate{candidate: candidate, resetsAt: resetsAt}
		if best == nil || ranksBefore(next, *best) {
			best = &next
		}
	}
	if best == nil {
		return quotaChoice{}, false
	}
	return quotaChoice{authID: best.candidate.ID, resetsAt: best.resetsAt}, true
}

// accountRank returns the reset of the account's longest window that applies to model, or
// zero when that window has not started. usable is false when the account has no applicable
// window or any applicable window is used up to the reserve.
func accountRank(account QuotaAccount, model string, now time.Time, reservePercent float64) (resetsAt time.Time, usable bool) {
	longest := 0.0
	for _, w := range account.Windows {
		if !appliesTo(w, model) {
			continue
		}
		started := !w.ResetsAt.IsZero() && w.ResetsAt.After(now)
		// A window whose reset has passed is fresh, whatever the last reading said.
		if started && w.UsedPercent >= 100-reservePercent {
			return time.Time{}, false
		}
		usable = true
		switch {
		case w.Minutes > longest:
			longest = w.Minutes
			resetsAt = time.Time{}
			if started {
				resetsAt = w.ResetsAt
			}
		case w.Minutes == longest && started && (resetsAt.IsZero() || w.ResetsAt.Before(resetsAt)):
			resetsAt = w.ResetsAt
		}
	}
	return resetsAt, usable
}

func appliesTo(w QuotaWindow, model string) bool {
	if len(w.Models) == 0 {
		return true
	}
	for _, pattern := range w.Models {
		if strings.Contains(model, pattern) {
			return true
		}
	}
	return false
}

// ranksBefore orders started windows by soonest reset, then windows that have not started,
// then breaks ties by priority (higher first) and auth ID.
func ranksBefore(a, b rankedCandidate) bool {
	aStarted, bStarted := !a.resetsAt.IsZero(), !b.resetsAt.IsZero()
	if aStarted != bStarted {
		return aStarted
	}
	if aStarted && !a.resetsAt.Equal(b.resetsAt) {
		return a.resetsAt.Before(b.resetsAt)
	}
	if a.candidate.Priority != b.candidate.Priority {
		return a.candidate.Priority > b.candidate.Priority
	}
	return a.candidate.ID < b.candidate.ID
}

// CandidateRank explains a candidate's model-specific quota order. Order is zero for exclusions.
type CandidateRank struct {
	AuthID   string
	Priority int
	Order    int
	ResetsAt time.Time
	FiveHour string
	Reason   string
}

func quotaCandidateRank(candidate pluginapi.SchedulerAuthCandidate, quota Quota, model string, now time.Time, maxAge time.Duration, reserve float64) (time.Time, string) {
	account, known := quota[candidate.ID]
	if !known || len(account.Windows) == 0 {
		return time.Time{}, "No quota reading"
	}
	if account.UpdatedAt.IsZero() || now.Sub(account.UpdatedAt) > maxAge {
		return time.Time{}, "Quota reading expired"
	}
	reset, usable := accountRank(account, model, now, reserve)
	if usable {
		return reset, ""
	}
	for _, window := range account.Windows {
		if appliesTo(window, model) && window.ResetsAt.After(now) && window.UsedPercent >= 100-reserve {
			if window.Minutes == 300 {
				return time.Time{}, "Five-hour quota exhausted"
			}
			return time.Time{}, "Applicable quota exhausted"
		}
	}
	return time.Time{}, "No quota window for this model"
}

// RankCandidates returns the same quota ordering used by picks, followed by excluded accounts.
// Runtime attributes are supplied by the console's host listing; request candidates are already filtered.
func RankCandidates(candidates []pluginapi.SchedulerAuthCandidate, quota Quota, provider, model string, now time.Time, cfg Config) []CandidateRank {
	if !cfg.ProbeAntigravityClaudeGPT() {
		quota = dropClaudeGPTQuota(quota)
	}
	rows := make([]CandidateRank, 0, len(candidates))
	delegated := normalizeProvider(provider) == "codex" && anyWebsocketCandidate(eligibleRankCandidates(candidates, now))
	for _, candidate := range candidates {
		row := CandidateRank{AuthID: candidate.ID, Priority: candidate.Priority, FiveHour: "Not reported"}
		for _, w := range quota[candidate.ID].Windows {
			if !appliesTo(w, baseModel(model)) || w.Minutes != 300 {
				continue
			}
			if row.FiveHour == "Exhausted" {
				continue
			}
			if !w.ResetsAt.After(now) {
				row.FiveHour = "Reset / available"
			} else if w.UsedPercent >= 100-cfg.ReservePercent() {
				row.FiveHour = "Exhausted"
			} else {
				row.FiveHour = "Available"
			}
		}
		evidence := quota[candidate.ID]
		unchecked := QuotaAccount{Windows: make([]QuotaWindow, len(evidence.Windows))}
		copy(unchecked.Windows, evidence.Windows)
		for index := range unchecked.Windows {
			unchecked.Windows[index].UsedPercent = 0
		}
		row.ResetsAt, _ = accountRank(unchecked, baseModel(model), now, cfg.ReservePercent())
		if evidence.UpdatedAt.IsZero() || now.Sub(evidence.UpdatedAt) > cfg.MaxAge() {
			row.FiveHour = "Unknown / no fresh evidence"
		}
		row.Reason = runtimeExclusion(candidate, now)
		if row.Reason == "" {
			switch {
			case !cfg.Applies(provider):
				row.Reason = "Provider delegated to host"
			case delegated:
				row.Reason = "Codex websocket pool delegated to host"
			default:
				_, row.Reason = quotaCandidateRank(candidate, quota, baseModel(model), now, cfg.MaxAge(), cfg.ReservePercent())
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Reason == "") != (b.Reason == "") {
			return a.Reason == ""
		}
		if a.Reason != "" {
			return a.AuthID < b.AuthID
		}
		return ranksBefore(rankedCandidate{candidate: pluginapi.SchedulerAuthCandidate{ID: a.AuthID, Priority: a.Priority}, resetsAt: a.ResetsAt}, rankedCandidate{candidate: pluginapi.SchedulerAuthCandidate{ID: b.AuthID, Priority: b.Priority}, resetsAt: b.ResetsAt})
	})
	order := 0
	for i := range rows {
		if rows[i].Reason == "" {
			order++
			rows[i].Order = order
		}
	}
	return rows
}

func runtimeExclusion(candidate pluginapi.SchedulerAuthCandidate, now time.Time) string {
	if disabled, _ := strconv.ParseBool(candidate.Attributes["disabled"]); disabled {
		return "Account disabled"
	}
	if retry, err := time.Parse(time.RFC3339Nano, candidate.Attributes["next_retry_after"]); err == nil && retry.After(now) {
		return "Host cooldown"
	}
	if unavailable, _ := strconv.ParseBool(candidate.Attributes["unavailable"]); unavailable {
		return "Host unavailable"
	}
	return ""
}

func eligibleRankCandidates(candidates []pluginapi.SchedulerAuthCandidate, now time.Time) []pluginapi.SchedulerAuthCandidate {
	eligible := make([]pluginapi.SchedulerAuthCandidate, 0, len(candidates))
	for _, c := range candidates {
		if runtimeExclusion(c, now) == "" {
			eligible = append(eligible, c)
		}
	}
	return eligible
}

// ClaudeGPTWindow reports an Antigravity Claude/GPT/OpenAI quota window.
func ClaudeGPTWindow(models []string) bool {
	if len(models) == 0 {
		return false
	}
	for _, model := range models {
		switch strings.ToLower(strings.TrimSpace(model)) {
		case "claude", "gpt", "openai":
			return true
		}
	}
	return false
}

func dropClaudeGPTQuota(quota Quota) Quota {
	if len(quota) == 0 {
		return quota
	}
	out := make(Quota, len(quota))
	for id, account := range quota {
		windows := make([]QuotaWindow, 0, len(account.Windows))
		for _, window := range account.Windows {
			if ClaudeGPTWindow(window.Models) {
				continue
			}
			windows = append(windows, window)
		}
		account.Windows = windows
		out[id] = account
	}
	return out
}
