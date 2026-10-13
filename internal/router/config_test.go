package router

import (
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	raw := "enabled: true\npriority: 1\nproviders: [' Claude ', codex, '']\nswitch_after: 90s\n" +
		"pins_file: /var/lib/cpa//pins.json\nquota_file: /var/lib/usage/quota.json\nmax_age: 20m\nreserve_percent: 2.5\n"
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SwitchAfter() != 90*time.Second {
		t.Errorf("SwitchAfter() = %s, want 90s", cfg.SwitchAfter())
	}
	if cfg.PinsFile() != "/var/lib/cpa/pins.json" {
		t.Errorf("PinsFile() = %q, want /var/lib/cpa/pins.json", cfg.PinsFile())
	}
	if cfg.QuotaFile() != "/var/lib/usage/quota.json" || cfg.MaxAge() != 20*time.Minute || cfg.ReservePercent() != 2.5 {
		t.Errorf("quota settings = %q, %s, %v", cfg.QuotaFile(), cfg.MaxAge(), cfg.ReservePercent())
	}
	for provider, want := range map[string]bool{"claude": true, "CODEX": true, "gemini": false} {
		if got := cfg.Applies(provider); got != want {
			t.Errorf("Applies(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig([]byte("enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SwitchAfter() != DefaultSwitchAfter {
		t.Errorf("SwitchAfter() = %s, want %s", cfg.SwitchAfter(), DefaultSwitchAfter)
	}
	if cfg.PinsFile() != "" || cfg.QuotaFile() != "" {
		t.Errorf("files = %q, %q; want none by default", cfg.PinsFile(), cfg.QuotaFile())
	}
	if cfg.MaxAge() != DefaultMaxAge || cfg.ReservePercent() != 0 {
		t.Errorf("MaxAge() = %s, ReservePercent() = %v", cfg.MaxAge(), cfg.ReservePercent())
	}
	if !cfg.Applies("anything") {
		t.Error("empty providers must apply to every provider")
	}
}

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{
		"switch_after: soon\n",
		"switch_after: -1m\n",
		"providers: claude\n",
		"pins_file: pins.json\n",
		"quota_file: quota.json\n",
		"max_age: 30s\n",
		"reserve_percent: -1\n",
		"reserve_percent: 100\n",
	} {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("ParseConfig(%q) succeeded, want error", raw)
		}
	}
}
