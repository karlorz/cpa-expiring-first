package live

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expiring-first-live.json")
	until := time.Unix(1794043458, 0).UTC()
	want := Snapshot{
		LastAuthID: "team.json",
		LastAt:     time.Date(2026, 11, 5, 15, 37, 18, 0, time.UTC),
		Limits:     map[string]time.Time{"team.json": until},
	}
	if errSave := NewStateFile(path).Save(want); errSave != nil {
		t.Fatal(errSave)
	}
	got, errLoad := NewStateFile(path).Load()
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if got.LastAuthID != want.LastAuthID || !got.LastAt.Equal(want.LastAt) || !got.Limits["team.json"].Equal(until) {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestStateFileRoundTripDecisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expiring-first-live.json")
	at := time.Date(2026, 11, 5, 16, 27, 2, 0, time.UTC)
	want := Snapshot{Decisions: []Decision{{
		ID: 1, At: at, Outcome: OutcomePicked, AuthID: "alice.json", Matching: 3, Ready: 1,
		Skips: []Skip{{AuthID: "team.json", Reason: "Live usage limit until 2026-11-07T09:24:18Z"}},
	}}}
	if errSave := NewStateFile(path).Save(want); errSave != nil {
		t.Fatal(errSave)
	}
	got, errLoad := NewStateFile(path).Load()
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if len(got.Decisions) != 1 || got.Decisions[0].AuthID != "alice.json" || got.Decisions[0].Outcome != OutcomePicked || len(got.Decisions[0].Skips) != 1 {
		t.Fatalf("Load() decisions = %+v", got.Decisions)
	}
}
