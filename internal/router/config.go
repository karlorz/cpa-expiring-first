package router

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultSwitchAfter is how long a pinned credential must stay unavailable before the sticky
// fallback moves the pin. It outlasts CLIProxyAPI's one-minute transient-error cooldown, so a
// single 5xx does not move the pin, while quota exhaustion (cooldowns of hours) does.
const DefaultSwitchAfter = 5 * time.Minute

// DefaultMaxAge is how old an account's quota reading may be before it is ignored. It covers
// two or three missed polls at a ten-minute interval.
const DefaultMaxAge = 30 * time.Minute

// DefaultProbeInterval is how often the in-process probes refresh every credential's readings.
const DefaultProbeInterval = 5 * time.Minute

// PreviewModel is one provider+model route the console previews the next pick for. The host
// configures these in preview_models; nothing else invents a route, so the page never shows a
// provider or model the deployment does not use.
type PreviewModel struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model" json:"model"`
}

// Config is a validated plugin configuration. Build it with ParseConfig.
type Config struct {
	// providers restricts routing to these provider keys; nil means every provider.
	providers   map[string]struct{}
	switchAfter time.Duration
	// pinsFile is an absolute path where pins are saved across restarts; empty keeps them in memory only.
	pinsFile string
	// quotaFile is an absolute path to the quota file; empty routes stickily only.
	quotaFile string
	maxAge    time.Duration
	// reservePercent is quota left untouched: a window counts as exhausted at 100 - reservePercent.
	reservePercent float64
	// clearCooldowns lets the plugin clear quota cooldowns that newer quota readings show over.
	clearCooldowns bool
	// probeInterval is how often the in-process probes refresh readings.
	probeInterval time.Duration
	// liveAuthIDs is the allowlist for Codex Live requests; empty leaves Live traffic to the host.
	liveAuthIDs []string
	// failClosed rejects a Live request when no allowlisted credential is available.
	failClosed bool
	// previewModels lists the provider+model routes the console previews next picks for.
	previewModels []PreviewModel
}

// DefaultConfig routes every provider stickily, with no quota file.
func DefaultConfig() Config {
	return Config{switchAfter: DefaultSwitchAfter, maxAge: DefaultMaxAge, clearCooldowns: true, probeInterval: DefaultProbeInterval, failClosed: true}
}

// ClearCooldowns reports whether stale quota cooldowns are cleared.
func (c Config) ClearCooldowns() bool {
	return c.clearCooldowns
}

// SwitchAfter reports the grace period before an unavailable pinned credential is replaced.
func (c Config) SwitchAfter() time.Duration {
	return c.switchAfter
}

// PinsFile reports where pins are saved, or "" when they live in memory only.
func (c Config) PinsFile() string {
	return c.pinsFile
}

// QuotaFile reports the quota file path, or "" when routing is sticky only.
func (c Config) QuotaFile() string {
	return c.quotaFile
}

// MaxAge reports how old an account's quota reading may be.
func (c Config) MaxAge() time.Duration {
	return c.maxAge
}

// ReservePercent reports the quota share left untouched on every window.
func (c Config) ReservePercent() float64 {
	return c.reservePercent
}

// ProbeInterval reports how often the in-process probes refresh readings.
func (c Config) ProbeInterval() time.Duration {
	if c.probeInterval <= 0 {
		return DefaultProbeInterval
	}
	return c.probeInterval
}

// LiveAuthIDs reports the Codex Live allowlist; empty leaves Live requests to the host.
func (c Config) LiveAuthIDs() []string {
	return c.liveAuthIDs
}

// FailClosed reports whether a Live request without an available allowlisted credential is
// rejected rather than delegated to the built-in scheduler.
func (c Config) FailClosed() bool {
	return c.failClosed
}

// PreviewModels reports the provider+model routes the console previews, in configuration order.
// DefaultConfig leaves it empty: the deployment supplies its own routes.
func (c Config) PreviewModels() []PreviewModel {
	if len(c.previewModels) == 0 {
		return nil
	}
	models := make([]PreviewModel, len(c.previewModels))
	copy(models, c.previewModels)
	return models
}

// Applies reports whether the plugin routes the given provider key.
func (c Config) Applies(provider string) bool {
	if c.providers == nil {
		return true
	}
	_, ok := c.providers[normalizeProvider(provider)]
	return ok
}

// Providers reports the configured provider restriction, or nil when every provider is routed.
func (c Config) Providers() []string {
	if c.providers == nil {
		return nil
	}
	keys := make([]string, 0, len(c.providers))
	for key := range c.providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type rawConfig struct {
	Providers      []string       `yaml:"providers"`
	SwitchAfter    string         `yaml:"switch_after"`
	PinsFile       string         `yaml:"pins_file"`
	QuotaFile      string         `yaml:"quota_file"`
	MaxAge         string         `yaml:"max_age"`
	ReservePercent *float64       `yaml:"reserve_percent"`
	ClearCooldowns *bool          `yaml:"clear_stale_cooldowns"`
	ProbeInterval  string         `yaml:"probe_interval"`
	LiveAuthIDs    []string       `yaml:"live_auth_ids"`
	FailClosed     *bool          `yaml:"fail_closed"`
	PreviewModels  []PreviewModel `yaml:"preview_models"`
}

// ParseConfig decodes the plugins.configs.expiring-first YAML block. Unknown keys such as
// enabled and priority belong to the host and are ignored.
func ParseConfig(raw []byte) (Config, error) {
	var parsed rawConfig
	if errUnmarshal := yaml.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return Config{}, fmt.Errorf("decode config: %w", errUnmarshal)
	}

	cfg := DefaultConfig()
	var errDuration error
	if cfg.switchAfter, errDuration = parseDuration("switch_after", parsed.SwitchAfter, cfg.switchAfter, 0); errDuration != nil {
		return Config{}, errDuration
	}
	if cfg.maxAge, errDuration = parseDuration("max_age", parsed.MaxAge, cfg.maxAge, time.Minute); errDuration != nil {
		return Config{}, errDuration
	}
	if cfg.probeInterval, errDuration = parseDuration("probe_interval", parsed.ProbeInterval, cfg.probeInterval, time.Minute); errDuration != nil {
		return Config{}, errDuration
	}

	var errPath error
	if cfg.pinsFile, errPath = parsePath("pins_file", parsed.PinsFile); errPath != nil {
		return Config{}, errPath
	}
	if cfg.quotaFile, errPath = parsePath("quota_file", parsed.QuotaFile); errPath != nil {
		return Config{}, errPath
	}

	if parsed.ReservePercent != nil {
		reserve := *parsed.ReservePercent
		if reserve < 0 || reserve >= 100 {
			return Config{}, fmt.Errorf("reserve_percent must be at least 0 and below 100, got %v", reserve)
		}
		cfg.reservePercent = reserve
	}
	if parsed.ClearCooldowns != nil {
		cfg.clearCooldowns = *parsed.ClearCooldowns
	}
	if parsed.FailClosed != nil {
		cfg.failClosed = *parsed.FailClosed
	}

	seenLive := make(map[string]struct{}, len(parsed.LiveAuthIDs))
	for _, id := range parsed.LiveAuthIDs {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if _, okSeen := seenLive[id]; okSeen {
			continue
		}
		seenLive[id] = struct{}{}
		cfg.liveAuthIDs = append(cfg.liveAuthIDs, id)
	}

	for _, provider := range parsed.Providers {
		key := normalizeProvider(provider)
		if key == "" {
			continue
		}
		if cfg.providers == nil {
			cfg.providers = make(map[string]struct{}, len(parsed.Providers))
		}
		cfg.providers[key] = struct{}{}
	}

	seenPreview := make(map[PreviewModel]struct{}, len(parsed.PreviewModels))
	for index, preview := range parsed.PreviewModels {
		provider := normalizeProvider(preview.Provider)
		model := strings.TrimSpace(preview.Model)
		if provider == "" || model == "" {
			return Config{}, fmt.Errorf("preview_models[%d] needs both a provider and a model", index)
		}
		entry := PreviewModel{Provider: provider, Model: model}
		if _, okSeen := seenPreview[entry]; okSeen {
			continue
		}
		seenPreview[entry] = struct{}{}
		cfg.previewModels = append(cfg.previewModels, entry)
	}
	return cfg, nil
}

// parseDuration reads an optional Go duration that must be at least minimum.
func parseDuration(name, value string, fallback, minimum time.Duration) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	duration, errParse := time.ParseDuration(value)
	if errParse != nil {
		return 0, fmt.Errorf("%s: %w", name, errParse)
	}
	if duration < minimum {
		return 0, fmt.Errorf("%s must be at least %s, got %s", name, minimum, value)
	}
	return duration, nil
}

// parsePath reads an optional absolute path. The host's working directory is not part of its
// contract, so a relative path has no stable meaning.
func parsePath(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path, got %s", name, value)
	}
	return filepath.Clean(value), nil
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
