package router

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// staticQuota is a QuotaSource with fixed readings.
type staticQuota struct {
	quota Quota
}

func (s *staticQuota) Load() Quota {
	return s.quota
}

const week = 7 * 24 * 60

func weekly(used float64, resetsAt time.Time) QuotaWindow {
	return QuotaWindow{Minutes: week, UsedPercent: used, ResetsAt: resetsAt}
}

func session(used float64, resetsAt time.Time) QuotaWindow {
	return QuotaWindow{Minutes: 300, UsedPercent: used, ResetsAt: resetsAt}
}

func reading(windows ...QuotaWindow) QuotaAccount {
	return QuotaAccount{UpdatedAt: t0, Windows: windows}
}

func quotaScheduler(t *testing.T, quota Quota) *Scheduler {
	t.Helper()
	s := NewScheduler(DefaultConfig(), nil)
	s.UseQuota(&staticQuota{quota: quota})
	return s
}

func TestParseQuota(t *testing.T) {
	raw := `{
		"schema": "expiring-first.quota.v1",
		"generated_at": "2026-10-09T12:00:00Z",
		"accounts": [
			{"auth_id": "a", "provider": "claude", "updated_at": "2026-10-09T12:00:00Z", "windows": [
				{"kind": "weekly", "window_minutes": 10080, "used_percent": 120, "resets_at": "2026-10-10T12:00:00Z"},
				{"kind": "weekly-scoped-fable", "window_minutes": 10080, "used_percent": 0, "resets_at": null, "models": [" Fable ", ""]},
				{"kind": "no length", "used_percent": 5},
				{"kind": "no usage", "window_minutes": 300}
			]},
			{"auth_id": "", "updated_at": "2026-10-09T12:00:00Z", "windows": []},
			{"auth_id": "no-update", "windows": []}
		]
	}`
	quota, err := ParseQuota([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(quota) != 1 {
		t.Fatalf("accounts = %+v, want only a", quota)
	}
	a := quota["a"]
	if !a.UpdatedAt.Equal(t0) || len(a.Windows) != 2 {
		t.Fatalf("a = %+v", a)
	}
	if w := a.Windows[0]; w.Minutes != week || w.UsedPercent != 100 || !w.ResetsAt.Equal(t0.Add(24*time.Hour)) || len(w.Models) != 0 {
		t.Errorf("weekly = %+v, want used clamped to 100", w)
	}
	if w := a.Windows[1]; !w.ResetsAt.IsZero() || len(w.Models) != 1 || w.Models[0] != "fable" {
		t.Errorf("scoped = %+v", w)
	}
}

func TestParseQuotaRejectsOtherSchemas(t *testing.T) {
	for _, raw := range []string{`{`, `{"schema": "expiring-first.quota.v2", "accounts": []}`, `{"accounts": []}`} {
		if _, err := ParseQuota([]byte(raw)); err == nil {
			t.Errorf("ParseQuota(%q) succeeded, want error", raw)
		}
	}
}

func TestQuotaFileReloadsOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	var reported []error
	now := t0
	file := NewQuotaFile(path, 2*time.Second, func(err error) { reported = append(reported, err) })
	file.now = func() time.Time { return now }

	if q := file.Load(); q != nil || len(reported) != 0 {
		t.Fatalf("missing file: Load() = %v, reported %v; want nothing", q, reported)
	}

	write := func(content string, modTime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schema":"expiring-first.quota.v1","accounts":[{"auth_id":"a","updated_at":"2026-10-09T12:00:00Z","windows":[]}]}`, t0)

	// Within the check interval the missing-file result is kept.
	if q := file.Load(); q != nil {
		t.Fatalf("Load() before the interval = %v, want cached nil", q)
	}
	now = now.Add(2 * time.Second)
	if q := file.Load(); len(q) != 1 {
		t.Fatalf("Load() = %v, want account a", q)
	}

	// A broken rewrite is reported once and the last good quota stays.
	write(`{"schema":"other","accounts":[]}`, t0.Add(time.Minute))
	now = now.Add(2 * time.Second)
	if q := file.Load(); len(q) != 1 || len(reported) != 1 {
		t.Fatalf("after bad rewrite: Load() = %v, reported %v", q, reported)
	}
	now = now.Add(2 * time.Second)
	if file.Load(); len(reported) != 1 {
		t.Fatalf("unchanged bad file reported again: %v", reported)
	}

	write(`{"schema":"expiring-first.quota.v1","accounts":[]}`, t0.Add(2*time.Minute))
	now = now.Add(2 * time.Second)
	if q := file.Load(); q == nil || len(q) != 0 {
		t.Fatalf("Load() after a good rewrite = %v, want empty quota", q)
	}
}

func TestQuotaPicksSoonestWeeklyReset(t *testing.T) {
	s := quotaScheduler(t, Quota{
		"primary": reading(weekly(30, t0.Add(4*24*time.Hour))),
		"backup":  reading(weekly(80, t0.Add(24*time.Hour))),
	})
	// Priority and stickiness do not override the ranking.
	mustPick(t, s, request("m", primary, backup), t0, "backup")
}

func TestQuotaPrefersStartedWindowsOverUnstarted(t *testing.T) {
	s := quotaScheduler(t, Quota{
		"primary": reading(weekly(0, time.Time{})),
		"backup":  reading(weekly(10, t0.Add(6*24*time.Hour))),
	})
	mustPick(t, s, request("m", primary, backup), t0, "backup")
}

func TestQuotaTiesFollowPriorityThenID(t *testing.T) {
	s := quotaScheduler(t, Quota{
		"primary": reading(weekly(0, time.Time{})),
		"backup":  reading(weekly(0, time.Time{})),
	})
	mustPick(t, s, request("m", backup, primary), t0, "primary")

	tied := quotaScheduler(t, Quota{"b": reading(weekly(0, time.Time{})), "c": reading(weekly(0, time.Time{}))})
	mustPick(t, tied, request("m", candidate("c", 1), candidate("b", 1)), t0, "b")
}

func TestQuotaSkipsExhaustedWindows(t *testing.T) {
	soon := t0.Add(24 * time.Hour)
	cases := map[string]Quota{
		"weekly used up":  {"backup": reading(weekly(100, soon)), "primary": reading(weekly(10, soon.Add(time.Hour)))},
		"session used up": {"backup": reading(weekly(10, soon), session(100, t0.Add(time.Hour))), "primary": reading(weekly(10, soon.Add(time.Hour)))},
	}
	for name, quota := range cases {
		t.Run(name, func(t *testing.T) {
			mustPick(t, quotaScheduler(t, quota), request("m", primary, backup), t0, "primary")
		})
	}
}

func TestQuotaTreatsPassedResetAsFresh(t *testing.T) {
	s := quotaScheduler(t, Quota{
		// backup's weekly reading is from before its reset, which has now passed.
		"backup":  reading(weekly(100, t0.Add(-time.Minute))),
		"primary": reading(weekly(50, t0.Add(48*time.Hour))),
	})
	// backup is usable again but its new window has not started, so primary still ranks first.
	mustPick(t, s, request("m", primary, backup), t0, "primary")

	s = quotaScheduler(t, Quota{"backup": reading(weekly(100, t0.Add(-time.Minute)))})
	mustPick(t, s, request("m", backup), t0, "backup")
}

func TestQuotaReserve(t *testing.T) {
	cfg, err := ParseConfig([]byte("reserve_percent: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(cfg, nil)
	s.UseQuota(&staticQuota{quota: Quota{
		"backup":  reading(weekly(96, t0.Add(24*time.Hour))),
		"primary": reading(weekly(10, t0.Add(48*time.Hour))),
	}})
	mustPick(t, s, request("m", primary, backup), t0, "primary")
}

func TestQuotaModelScopedWindows(t *testing.T) {
	scopedOut := QuotaWindow{Minutes: week, UsedPercent: 100, ResetsAt: t0.Add(24 * time.Hour), Models: []string{"fable"}}
	s := quotaScheduler(t, Quota{
		"backup":  reading(weekly(10, t0.Add(24*time.Hour)), scopedOut),
		"primary": reading(weekly(10, t0.Add(48*time.Hour))),
	})
	mustPick(t, s, request("claude-fable-5", primary, backup), t0, "primary")
	mustPick(t, s, request("claude-opus-5-5", primary, backup), t0, "backup")
}

func TestQuotaFallsBackToStickyWithoutFreshReadings(t *testing.T) {
	stale := QuotaAccount{UpdatedAt: t0.Add(-DefaultMaxAge - time.Second), Windows: []QuotaWindow{weekly(0, t0.Add(time.Hour))}}
	cases := map[string]QuotaSource{
		"no file yet":       &staticQuota{},
		"stale reading":     &staticQuota{quota: Quota{"backup": stale}},
		"unknown accounts":  &staticQuota{quota: Quota{"other": reading(weekly(0, t0.Add(time.Hour)))}},
		"all exhausted":     &staticQuota{quota: Quota{"backup": reading(weekly(100, t0.Add(time.Hour))), "primary": reading(weekly(100, t0.Add(time.Hour)))}},
		"no window applies": &staticQuota{quota: Quota{"backup": reading(QuotaWindow{Minutes: week, ResetsAt: t0.Add(time.Hour), Models: []string{"fable"}})}},
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			s := NewScheduler(DefaultConfig(), nil)
			s.UseQuota(source)
			mustPick(t, s, request("m", primary, backup), t0, "primary")
		})
	}
}

func TestQuotaMovesPinAndStickyContinuesFromIt(t *testing.T) {
	source := &staticQuota{quota: Quota{
		"primary": reading(weekly(10, t0.Add(48*time.Hour))),
		"backup":  reading(weekly(10, t0.Add(24*time.Hour))),
	}}
	var rec recorder
	s := NewScheduler(DefaultConfig(), nil)
	s.UseQuota(source)
	s.Persist(nil, rec.save)

	mustPick(t, s, request("m", primary, backup), t0, "backup")
	if got := rec.last(t); len(got) != 1 || got[0].AuthID != "backup" {
		t.Fatalf("saved = %+v, want backup pinned", got)
	}

	// backup's weekly window resets and its new window has not started: primary now expires first.
	later := t0.Add(24*time.Hour + time.Minute)
	source.quota = Quota{
		"primary": QuotaAccount{UpdatedAt: later, Windows: []QuotaWindow{weekly(10, t0.Add(48*time.Hour))}},
		"backup":  QuotaAccount{UpdatedAt: later, Windows: []QuotaWindow{weekly(0, time.Time{})}},
	}
	moved := mustPick(t, s, request("m", primary, backup), later, "primary")
	if moved == nil || moved.Reason != ReasonQuota || moved.From != "backup" || moved.To != "primary" || !moved.Until.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("switch = %+v, want backup -> primary by quota", moved)
	}
	if got := rec.last(t); got[0].AuthID != "primary" {
		t.Fatalf("saved = %+v, want primary pinned", got)
	}

	// Readings go stale: sticky routing keeps the last quota choice instead of priority order.
	source.quota = nil
	mustPick(t, s, request("m", backup, primary), later.Add(time.Hour), "primary")
	if moved := mustPick(t, s, request("m", backup, primary), later.Add(time.Hour), "primary"); moved != nil {
		t.Fatalf("unexpected switch %+v", moved)
	}
}

func TestQuotaSourceErrorsAreIsolated(t *testing.T) {
	// A source that never loads leaves sticky routing intact.
	s := NewScheduler(DefaultConfig(), nil)
	s.UseQuota(NewQuotaFile(filepath.Join(t.TempDir(), "missing.json"), time.Second, func(err error) {
		t.Errorf("missing file reported: %v", err)
	}))
	mustPick(t, s, request("m", primary, backup), t0, "primary")
}
