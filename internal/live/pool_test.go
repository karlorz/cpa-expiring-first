package live

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestDescribePoolFollowsAllowlistAndMarksEligibility(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	rows := DescribePool(Config{
		AuthIDs:  []string{"team.json", "karl.json", "alice.json", "missing.json"},
		Strategy: StrategyFillFirst,
	}, []pluginapi.SchedulerAuthCandidate{
		{ID: "karl.json", Provider: "codex", Priority: 99, Attributes: map[string]string{"disabled": "true"}},
		{ID: "alice.json", Provider: "codex", Priority: 98, Attributes: map[string]string{"unavailable": "true"}},
		{ID: "team.json", Provider: "codex", Priority: 100},
	}, "team.json", now, nil, now)

	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	if rows[0].AuthID != "team.json" || !rows[0].Eligible || !rows[0].NextPick || !rows[0].LastSelected || !rows[0].InUse {
		t.Fatalf("first row = %+v, want eligible Team as next, last used and in use", rows[0])
	}
	if rows[1].Eligible || rows[1].NextPick || rows[1].Reason != "Account disabled" {
		t.Fatalf("karl row = %+v, want disabled and skipped", rows[1])
	}
	if rows[2].Eligible || rows[2].Reason != "Unavailable" {
		t.Fatalf("alice row = %+v, want unavailable", rows[2])
	}
	if rows[3].Present || rows[3].Eligible || rows[3].Reason != "Not in the host credential list" {
		t.Fatalf("missing row = %+v, want absent", rows[3])
	}
}

func TestDescribePoolMarksCooldownAndSkipsToNextEligible(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	rows := DescribePool(Config{AuthIDs: []string{"first.json", "second.json"}}, []pluginapi.SchedulerAuthCandidate{
		{ID: "first.json", Attributes: map[string]string{"next_retry_after": now.Add(time.Hour).Format(time.RFC3339Nano)}},
		{ID: "second.json"},
	}, "", time.Time{}, nil, now)
	if !rows[0].Cooling || rows[0].NextPick {
		t.Fatalf("first row = %+v, want cooling and not next", rows[0])
	}
	if !rows[1].Eligible || !rows[1].NextPick {
		t.Fatalf("second row = %+v, want next eligible pick", rows[1])
	}
}

func TestDescribePoolSeparatesLiveNextFromHostFillFirst(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	rows := DescribePool(Config{AuthIDs: []string{"team.json", "karl.json", "alice.json"}}, []pluginapi.SchedulerAuthCandidate{
		{ID: "team.json", Provider: "codex", Priority: 0},
		{ID: "karl.json", Provider: "codex", Priority: 0},
		{ID: "alice.json", Provider: "codex", Priority: 99},
	}, "", time.Time{}, nil, now)
	if !rows[0].Eligible || !rows[0].NextPick || rows[0].FillFirstNext {
		t.Fatalf("team = %+v, want Live next pick and not host fill-first", rows[0])
	}
	if rows[1].NextPick || rows[1].FillFirstNext {
		t.Fatalf("karl = %+v, want neither next pick", rows[1])
	}
	if !rows[2].FillFirstNext || rows[2].NextPick {
		t.Fatalf("alice = %+v, want host fill-first next", rows[2])
	}
}

func TestDescribePoolTreatsStatusDisabledAsDisabled(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	rows := DescribePool(Config{AuthIDs: []string{"first.json", "second.json"}}, []pluginapi.SchedulerAuthCandidate{
		{ID: "first.json", Status: "disabled"},
		{ID: "second.json"},
	}, "", time.Time{}, nil, now)
	if !rows[0].Disabled || rows[0].Eligible || rows[0].NextPick {
		t.Fatalf("first = %+v, want status-disabled and skipped", rows[0])
	}
	if !rows[1].NextPick {
		t.Fatalf("second = %+v, want next pick", rows[1])
	}
}

func TestDescribePoolMarksLiveLimitAndLastUsedWithoutInUse(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	until := now.Add(41 * time.Hour)
	rows := DescribePool(Config{AuthIDs: []string{"team.json", "alice.json"}}, []pluginapi.SchedulerAuthCandidate{
		{ID: "team.json"},
		{ID: "alice.json"},
	}, "team.json", now.Add(-time.Hour), map[string]time.Time{"team.json": until}, now)
	if !rows[0].LiveLimit || rows[0].Eligible || rows[0].NextPick || !rows[0].LastSelected || rows[0].InUse {
		t.Fatalf("team = %+v, want last used, live-limited, not in use", rows[0])
	}
	if !rows[1].NextPick || rows[1].LastSelected {
		t.Fatalf("alice = %+v, want next Live pick", rows[1])
	}
}

func TestDescribePoolDefaultsToAvailableCodexCredentials(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	rows := DescribePool(Config{Strategy: StrategyFillFirst}, []pluginapi.SchedulerAuthCandidate{
		{ID: "key.json", Provider: "codex", Priority: 100, Attributes: map[string]string{"auth_kind": "apikey"}},
		{ID: "alice.json", Provider: "codex", Priority: 98},
		{ID: "team.json", Provider: "codex", Priority: 99},
	}, "", time.Time{}, nil, now)
	if len(rows) != 2 || rows[0].AuthID != "team.json" || !rows[0].NextPick || rows[1].AuthID != "alice.json" {
		t.Fatalf("rows = %+v, want Team then Alice from available Codex fill-first", rows)
	}
	if PoolSource(Config{}, []pluginapi.SchedulerAuthCandidate{
		{ID: "team.json", Provider: "codex"},
	}) != PoolSourceAvailable {
		t.Fatal("want available pool source")
	}
}

func TestDescribePoolParsesUsageLimitStatusMessage(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	message := `{"error":{"type":"usage_limit_reached","plan_type":"team","resets_at":1794043458}}`
	rows := DescribePool(Config{AuthIDs: []string{"team.json", "alice.json"}}, []pluginapi.SchedulerAuthCandidate{
		{ID: "team.json", Attributes: map[string]string{"status_message": message}},
		{ID: "alice.json"},
	}, "", time.Time{}, nil, now)
	if !rows[0].LiveLimit || rows[0].NextPick || !strings.Contains(rows[0].Reason, "team") {
		t.Fatalf("team = %+v, want parsed Live usage limit", rows[0])
	}
	if !rows[1].NextPick {
		t.Fatalf("alice = %+v, want next pick after Team Live limit", rows[1])
	}
}
