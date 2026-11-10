package live

import (
	"testing"
	"time"
)

func TestParseUsageLimitReadsResetAndPlan(t *testing.T) {
	now := time.Unix(1794043458-150420, 0).UTC()
	until, plan, ok := parseUsageLimit(
		`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"team","resets_at":1794043458,"resets_in_seconds":150420}}`,
		now,
	)
	if !ok || plan != "team" || until.Unix() != 1794043458 {
		t.Fatalf("parseUsageLimit() = %v %q %v, want team reset 1794043458", until, plan, ok)
	}
}

func TestParseUsageLimitFallbackWithoutReset(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	until, plan, ok := parseUsageLimit(`usage_limit_reached`, now)
	if !ok || plan != "" || !until.Equal(now.Add(liveLimitFallback)) {
		t.Fatalf("parseUsageLimit() = %v %q %v, want 1h fallback", until, plan, ok)
	}
}

func TestParseUsageLimitIgnoresOtherErrors(t *testing.T) {
	if _, _, ok := parseUsageLimit(`{"error":{"type":"server_error"}}`, time.Now()); ok {
		t.Fatal("parseUsageLimit() accepted a non-usage-limit error")
	}
}

func TestMatchesAllowlistUsesBasename(t *testing.T) {
	if !MatchesAllowlist("team.json", []string{"/auths/team.json"}) {
		t.Fatal("MatchesAllowlist() = false, want basename match")
	}
	if MatchesAllowlist("alice.json", []string{"team.json"}) {
		t.Fatal("MatchesAllowlist() = true, want no match")
	}
}
