package router

import (
	"testing"
	"time"
)

func cooldownScheduler(t *testing.T, source *staticQuota) *Scheduler {
	t.Helper()
	s := NewScheduler(DefaultConfig(), nil)
	s.UseQuota(source)
	return s
}

func cooling(until time.Time) map[string]AuthState {
	return map[string]AuthState{"backup": {AuthIndex: "idx-backup", NextRetryAfter: until}}
}

func TestStaleCooldownNeedsReadingAfterItBegan(t *testing.T) {
	until := t0.Add(3 * 24 * time.Hour)
	// The reading predates the 429: it still shows quota, but cannot prove the limit was reset.
	source := &staticQuota{quota: Quota{"backup": {UpdatedAt: t0.Add(-time.Minute), Windows: []QuotaWindow{weekly(90, until)}}}}
	s := cooldownScheduler(t, source)
	if stale := s.StaleCooldowns(cooling(until), t0); len(stale) != 0 {
		t.Fatalf("cleared on a reading older than the cooldown: %+v", stale)
	}

	// A later reading still shows the weekly window used up: the cooldown is real.
	source.quota = Quota{"backup": {UpdatedAt: t0.Add(5 * time.Minute), Windows: []QuotaWindow{weekly(100, until)}}}
	if stale := s.StaleCooldowns(cooling(until), t0.Add(6*time.Minute)); len(stale) != 0 {
		t.Fatalf("cleared a real cooldown: %+v", stale)
	}

	// A limit reset was spent: the next reading shows quota again.
	source.quota = Quota{"backup": {UpdatedAt: t0.Add(15 * time.Minute), Windows: []QuotaWindow{weekly(0, until), session(0, time.Time{})}}}
	stale := s.StaleCooldowns(cooling(until), t0.Add(16*time.Minute))
	if len(stale) != 1 || stale[0].AuthID != "backup" || stale[0].AuthIndex != "idx-backup" || !stale[0].Until.Equal(until) || !stale[0].ReadAt.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("stale = %+v, want backup's cooldown", stale)
	}
}

func TestStaleCooldownRestartsWithEachNewCooldown(t *testing.T) {
	until := t0.Add(48 * time.Hour)
	source := &staticQuota{quota: Quota{"backup": {UpdatedAt: t0.Add(10 * time.Minute), Windows: []QuotaWindow{weekly(0, until)}}}}
	s := cooldownScheduler(t, source)
	s.StaleCooldowns(cooling(until), t0)
	if stale := s.StaleCooldowns(cooling(until), t0.Add(11*time.Minute)); len(stale) != 1 {
		t.Fatalf("stale = %+v, want one", stale)
	}

	// Cleared, the account ran out again: a new deadline needs a reading newer than itself.
	again := until.Add(time.Hour)
	if stale := s.StaleCooldowns(cooling(again), t0.Add(12*time.Minute)); len(stale) != 0 {
		t.Fatalf("cleared a fresh cooldown with the old reading: %+v", stale)
	}
}

func TestStaleCooldownIgnores(t *testing.T) {
	until := t0.Add(48 * time.Hour)
	fresh := QuotaAccount{UpdatedAt: t0.Add(time.Minute), Windows: []QuotaWindow{weekly(0, until)}}
	cases := map[string]struct {
		states map[string]AuthState
		quota  Quota
	}{
		"short cooldown":    {map[string]AuthState{"backup": {AuthIndex: "i", NextRetryAfter: t0.Add(QuotaCooldownFloor)}}, Quota{"backup": fresh}},
		"disabled":          {map[string]AuthState{"backup": {AuthIndex: "i", Disabled: true, NextRetryAfter: until}}, Quota{"backup": fresh}},
		"no auth index":     {map[string]AuthState{"backup": {NextRetryAfter: until}}, Quota{"backup": fresh}},
		"no reading":        {cooling(until), Quota{}},
		"reading too old":   {cooling(until), Quota{"backup": {UpdatedAt: t0.Add(-DefaultMaxAge), Windows: fresh.Windows}}},
		"reading empty":     {cooling(until), Quota{"backup": {UpdatedAt: t0.Add(time.Minute)}}},
		"session used up":   {cooling(until), Quota{"backup": {UpdatedAt: t0.Add(time.Minute), Windows: []QuotaWindow{weekly(0, until), session(100, t0.Add(4*time.Hour))}}}},
		"scoped window out": {cooling(until), Quota{"backup": {UpdatedAt: t0.Add(time.Minute), Windows: []QuotaWindow{weekly(0, until), {Minutes: week, UsedPercent: 100, ResetsAt: until, Models: []string{"fable"}}}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := cooldownScheduler(t, &staticQuota{quota: tc.quota})
			s.StaleCooldowns(tc.states, t0)
			if stale := s.StaleCooldowns(tc.states, t0.Add(2*time.Minute)); len(stale) != 0 {
				t.Fatalf("stale = %+v, want none", stale)
			}
		})
	}
}

func TestStaleCooldownCanBeDisabled(t *testing.T) {
	cfg, err := ParseConfig([]byte("clear_stale_cooldowns: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	until := t0.Add(48 * time.Hour)
	s := NewScheduler(cfg, nil)
	s.UseQuota(&staticQuota{quota: Quota{"backup": {UpdatedAt: t0.Add(time.Minute), Windows: []QuotaWindow{weekly(0, until)}}}})
	s.StaleCooldowns(cooling(until), t0)
	if stale := s.StaleCooldowns(cooling(until), t0.Add(2*time.Minute)); len(stale) != 0 {
		t.Fatalf("stale = %+v, want none when disabled", stale)
	}

	// Without a quota source there is nothing to compare against.
	if stale := NewScheduler(DefaultConfig(), nil).StaleCooldowns(cooling(until), t0); len(stale) != 0 {
		t.Fatalf("stale without quota = %+v", stale)
	}
}
