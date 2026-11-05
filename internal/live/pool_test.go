package live

import (
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
	}, "team.json", now)

	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	if rows[0].AuthID != "team.json" || !rows[0].Eligible || !rows[0].NextPick || !rows[0].LastSelected {
		t.Fatalf("first row = %+v, want eligible Team as next and last pick", rows[0])
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
	}, "", now)
	if !rows[0].Cooling || rows[0].NextPick {
		t.Fatalf("first row = %+v, want cooling and not next", rows[0])
	}
	if !rows[1].Eligible || !rows[1].NextPick {
		t.Fatalf("second row = %+v, want next eligible pick", rows[1])
	}
}
