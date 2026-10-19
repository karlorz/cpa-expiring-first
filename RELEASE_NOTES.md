## v0.3.1

- Replace example model previews with configured `preview_models` and routes observed in scheduler picks. Preview the daily Antigravity route with `provider: antigravity`, `model: gemini-3.8-flash-high`.
- Use distinct stable hashed account labels across quota, pins and previews; retain full IDs only in private host state and existing logs.
- Add 30-second cached display refresh, Refresh display, and a management-key-protected Probe now action. Keep the key in page memory. Contain wide tables on mobile.
- Separate registration from observed scheduler decisions and count successful quota readings accurately. Apply runtime disabled/unavailable/cooldown eligibility and preserve Codex websocket delegation in previews.
- Correct Antigravity quota request identity, project resolution, endpoint fallback and deterministic window parsing. Report safe provider failure statuses.
- Retain last successful quota evidence with its original age during failed probes, expire it through `max_age`, and remove disabled/deleted credentials from probe snapshots.
- Add deterministic five-hour exhaustion/reset and weekly rollover regressions. Use configured `max_age` for quota-file overlays.

## v0.3.0 (karlorz fork v1)

- **In-process quota probes.** The plugin now reads Antigravity, Claude, Codex and xAI quota itself, on a background `probe_interval` (default `5m`), through the host's HTTP transport. Picks read the in-memory snapshot and never call a provider. Antigravity is probed once per model group (`gemini` and `claude_gpt`); Claude, Codex and xAI have a single metered pool each.
- **Quota file is now optional.** A file reading younger than `max_age` still wins over the probe reading for the same account; an empty or missing file leaves the probe readings in use.
- **Live Voice passthrough.** Codex Live/Realtime traffic is decided by a `live_auth_ids` allowlist, ahead of the weekly-soonest ranking, with `fill-first` and optional `fail_closed`. With no allowlist, Live requests are left to the host's built-in scheduler. This replaces running `cpa-live-voice` alongside this plugin: give `expiring-first` priority `110` and `cpa-live-voice` priority `100`, and this plugin serves both paths.
- **Console page** at `/v0/resource/plugins/expiring-first/status`: pins, per-account windows (provider, redacted auth, 5-hour and weekly remaining/reset, Antigravity model group, probe age and error), the next pick per provider, and the picks this plugin served. Management routes `GET .../quota` and `POST .../refresh`.
- Credential IDs are redacted on the page and in the JSON routes.
- Requires CLIProxyAPI 8.0.12 or newer.

## v0.2.0

- **Clears stale cooldowns.** When a quota reading taken after an account's quota cooldown began shows quota left (for example after a limit reset was spent), the plugin clears the cooldown through `host.routing.reset_cooldown`, so the account is usable at once instead of at the original reset time. Only cooldowns longer than 30 minutes are considered; checked at most once a minute. Needs CLIProxyAPI 8.0.12 or newer; on older hosts the call fails with a logged warning. Turn off with `clear_stale_cooldowns: false`.

## v0.1.0

First release. Routes each provider and model pool to the available account whose longest usage window resets soonest and still has quota, read from a quota file (schema `expiring-first.quota.v1`). Falls back to sticky fill-first routing without fresh readings. Based on [sticky-fill-first](https://github.com/magoz/cpa-sticky-fill-first) v0.4.0: pins, `pins_file` and the pins route work the same way.

Requires CLIProxyAPI 7.3.8 or newer.
