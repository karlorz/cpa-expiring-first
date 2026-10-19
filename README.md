# Expiring First

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) scheduler plugin for pools of subscription accounts. It sends each request to the account whose quota **expires first**: the available account whose longest usage window (usually the weekly one) resets soonest and still has quota left. Quota that would reset unused is spent before quota that would still be there next week.

This is the `karlorz` fork of [magoz/cpa-expiring-first](https://github.com/magoz/cpa-expiring-first). It keeps the weekly-soonest ranking and the sticky fallback, and adds:

- **In-process quota probes** for Antigravity, Claude, Codex and xAI, run on a background interval. The plugin no longer depends on an external writer for its readings.
- **An optional quota file overlay**, which can still override individual accounts (see [Quota file](#quota-file)).
- **Live Voice passthrough** for Codex Live/Realtime traffic, so this can be the only scheduler plugin installed.
- **A console page** showing quota windows, configured and observed model previews, and the picks this plugin served.

Without fresh readings it routes like [sticky-fill-first](https://github.com/magoz/cpa-sticky-fill-first): it stays on the account currently serving until that account runs out.

Plugin ID: `expiring-first`. Requires CLIProxyAPI 8.0.12 or newer (`host.http.do`, `host.auth.list` and `host.routing.reset_cooldown`).

## Probes

Every `probe_interval` (default `5m`) the plugin reads each credential's quota through the host's own HTTP transport (`host.http.do`), never through a provider SDK, and publishes one snapshot in memory. Picks read that snapshot; they never call a provider. The only credential write the plugin makes is the existing `host.routing.reset_cooldown` call that clears stale cooldowns.

| Provider | Probe | Windows |
| --- | --- | --- |
| Antigravity | `retrieveUserQuotaSummary` | 5-hour and weekly, scoped to `gemini` and `claude_gpt` model groups |
| Claude | unified rate-limit headers, else usage JSON | 5-hour and weekly |
| Codex | `wham/usage` | 5-hour and weekly (monthly on free plans) |
| xAI | plan classification plus the OAuth weekly billing window | weekly |

`used_percent` is `100 - remaining`, `auth_id` is the CLIProxyAPI credential ID (the auth file name), and each reading carries the reset time the provider reports.

**Antigravity model groups.** One Antigravity credential is metered twice. The `gemini` group's windows apply only to models containing `gemini`; the `claude_gpt` group's windows apply only to models containing `claude`, `gpt` or `openai`. Both are probed for every Antigravity credential, and the ranking uses whichever group matches the requested model. The console page shows the group for each account.

**Claude, Codex and xAI** have a single metered pool per credential, so their windows apply to every model of that provider.

## Behavior

Every provider and model route (for example `claude` + `claude-opus-5-5`) is decided on its own. A thinking suffix such as `(high)` counts as its base model.

**With fresh quota readings**, each request goes through these steps:

1. Start from the candidates CLIProxyAPI offers: accounts that are not cooling down or disabled.
2. Drop accounts that have no reading younger than `max_age` (default `30m`).
3. Drop accounts with a used-up window. An applicable window counts as used up at `100 - reserve_percent` percent; a window whose reset has passed counts as fresh again.
4. Rank the rest by the reset of each account's longest applicable window, soonest first. Accounts whose longest window has not started yet (`resets_at: null`, or a reset that has passed) come after them. Ties go to the higher credential `priority`, then the lower auth ID.
5. Serve the top account.

A window applies to a request when it has no `models` list, or when the model ID contains one of its `models` strings. A model-only weekly limit therefore affects only the models it names.

In practice:

| Event | Result |
| --- | --- |
| Normal use | Traffic stays on the account whose weekly window resets soonest. The order changes only at resets, so prompt caches are not disturbed. |
| That account runs out | CLIProxyAPI cools it down after its 429, so it drops out, and the next soonest takes over. |
| Its five-hour limit runs out | Another account covers until the five-hour window resets, then traffic returns: its weekly window still resets first. |
| Its weekly window resets | Its new window has not started, so the account that resets next takes over. Traffic does not snap back. |
| A limit reset or a provider change moves a reset date | The next probe cycle re-ranks the accounts. |

**Without fresh readings** (no probe has succeeded yet, the readings are older than `max_age`, or no candidate has a usable reading), the plugin routes stickily:

- Each route stays on the account that last served it, even when a higher-priority account is also available.
- When that account is missing from the candidates, the request goes to the `fill-first` choice (highest priority, then lowest auth ID). Whether the route moves there depends on why the account is missing. The plugin asks CLIProxyAPI for its runtime state (`host.auth.list`, at most once every 2 seconds):

  | Host state of the pinned account | Pin |
  | --- | --- |
  | Cooldown ending more than `max(30m, switch_after)` from now: a provider quota reset | Moves now |
  | Disabled | Moves now |
  | Shorter cooldown: a transient error (1 minute), an auth error being refreshed (30 minutes), or quota backoff without a reset time | Stays, until the grace period expires |
  | No cooldown, or not reported by the host | Stays, until the grace period expires |

- The grace period is the fallback: the pin moves once the account has been missing for `switch_after` (default `5m`).
- Two longer non-quota cooldowns also move the pin, which is intended because the account cannot serve the route meanwhile: a model the account does not support (12 hours), and an error whose `Retry-After` is longer than 30 minutes.

Both modes record the serving account as the route's pin, so sticky routing continues from the last quota-based choice when readings go stale. When a pin moves, the plugin logs at info level, for example:

- `expiring-first: pin moved from <old> to <new>: quota ranking, window resets 2026-10-16T18:00:00Z provider=claude model=...`
- `... : in cooldown until 2026-10-09T21:00:00Z`, `... : disabled`, `... : unavailable for 5m0s`

## Live Voice

Codex Live/Realtime traffic (`gpt-live-1-codex`) is decided by its own allowlist, ahead of the weekly-soonest ranking:

- Configure `live_auth_ids` with the credential IDs allowed to serve Live traffic.
- A Live request is never answered by the weekly-soonest ranking. With **no** `live_auth_ids` configured, the plugin leaves the request unhandled, so CLIProxyAPI's built-in scheduler decides, exactly as if this plugin were not installed.
- With an allowlist, the plugin picks from the matching candidates: `fill-first`.
- `fail_closed: true` (default) rejects the request with `live_oauth_unavailable` when no allowlisted credential is available; `fail_closed: false` delegates to the built-in scheduler instead.

This is the same classification and selection as [karlorz/cpa-live-voice](https://github.com/karlorz/cpa-live-voice), folded into this plugin so the two no longer have to run together.

### Running alongside cpa-live-voice

Only one scheduler plugin is consulted: CLIProxyAPI uses the highest-priority one. To make `expiring-first` the sole scheduler, give it a **higher** priority than `cpa-live-voice`:

```yaml
plugins:
  configs:
    expiring-first:
      priority: 110   # consulted first, and handles Live itself
    cpa-live-voice:
      priority: 100   # retained for hosts where it is the selected scheduler
```

With `priority: 110` on `expiring-first` and `100` on `cpa-live-voice`, `expiring-first` serves both Live and ordinary traffic and `cpa-live-voice` is never invoked. Remove `cpa-live-voice` if you do not need it elsewhere.

## Stale cooldowns

When an account runs out, CLIProxyAPI cools it down until the provider's reset time, which can be days away. If quota comes back earlier, for example because a limit reset was spent on the account, CLIProxyAPI would keep skipping it until that deadline. The plugin clears such cooldowns through `host.routing.reset_cooldown`:

- At most once a minute, on a request, it lists the credentials in a cooldown longer than 30 minutes (quota lockouts; shorter ones are transient or auth errors).
- It clears one only when a quota reading **taken after the plugin first saw that cooldown** shows quota left on every window. A reading from before the 429 cannot clear it, so a real lockout is never undone by stale data, and each new cooldown needs a newer reading.
- Each clear is logged at info level: `expiring-first: cleared cooldown of <auth_id> (until …): quota reading at … shows quota left`.
- Set `clear_stale_cooldowns: false` to turn it off.

## Console

The read-only status page is served at:

```
http://<host>:<port>/v0/resource/plugins/expiring-first/status
```

It needs no management key and shows:

- Plugin registration, observed routing activity, version, probe interval, last probe time, and Live Voice pool. Registration alone does not prove that the host selected this scheduler.
- The current **pins** table: provider, model, redacted auth, pin time, last pick time and availability.
- **Per-account windows**: provider, redacted auth, Antigravity model group, 5-hour remaining/reset, weekly remaining/reset, probe age and probe error.
- **Model previews** from `preview_models` and routes observed in scheduler picks. Previews estimate quota ranking from account-level eligibility; the host applies model support, request restrictions, and model-specific cooldowns at request time.
- **Pick counts** per provider (handled and quota-ranked). These count scheduler decisions; successful upstream completions require request logs.

The cached display refreshes every 30 seconds while visible. **Refresh display** reloads cached data. **Probe now** runs provider probes using a management key kept only in page memory. Configuration stays in CLIProxyAPI's plugin config fields. Failed probes retain the last successful reading with its original timestamp until `max_age` expires; the page shows the latest error alongside retained evidence.

The management routes below are protected by the management key:

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v0/management/plugins/expiring-first/pins` | Current pins. |
| `GET` | `/v0/management/plugins/expiring-first/quota` | The readings the ranking uses, with auth IDs redacted. |
| `POST` | `/v0/management/plugins/expiring-first/refresh` | Run one probe cycle now. |

```bash
curl -s -H "Authorization: Bearer $MANAGEMENT_KEY" \
  http://127.0.0.1:8317/v0/management/plugins/expiring-first/quota
```

```json
[{"auth_id":"anti***on","auth":"anti***on","provider":"antigravity","antigravity_group":"gemini","updated_at":"2026-10-09T19:30:02Z","probe_age":"12s ago","windows":[{"name":"weekly","window_minutes":10080,"used_percent":41,"remaining_percent":59,"resets_at":"2026-10-16T18:00:00Z","reset_in":"6d 22h left","models":["gemini"],"exhausted":false}]}]
```

Credential IDs use stable, distinct hashed labels in the page and in every JSON route. Full IDs stay in the plugin logs, as before.

## Quota file (optional)

The probes are the primary source. A quota file can still override an individual account: a file reading **younger than `max_age`** wins over the probe reading for the same `auth_id`. An empty, missing or unreadable file leaves the probe readings in use.

A JSON document, schema `expiring-first.quota.v1`. Any program can write it; the plugin only reads it.

```json
{
  "schema": "expiring-first.quota.v1",
  "generated_at": "2026-10-13T21:00:00Z",
  "accounts": [
    {
      "auth_id": "claude-a1b2c3d4-user@example.com.json",
      "provider": "claude",
      "updated_at": "2026-10-13T20:58:12Z",
      "windows": [
        { "kind": "five-hour", "window_minutes": 300, "used_percent": 3, "resets_at": "2026-10-13T22:50:00Z" },
        { "kind": "weekly", "window_minutes": 10080, "used_percent": 41, "resets_at": "2026-10-16T18:00:00Z" },
        { "kind": "weekly-scoped-fable", "window_minutes": 10080, "used_percent": 0, "resets_at": null, "models": ["fable"] }
      ]
    }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `schema` | Must be `expiring-first.quota.v1`. Other values make the plugin ignore the file. |
| `generated_at` | When the file was written. Informational. |
| `accounts[].auth_id` | CLIProxyAPI's credential ID. For auth files, the file name. Required. |
| `accounts[].provider` | CLIProxyAPI provider key, such as `claude` or `codex`. Informational. |
| `accounts[].updated_at` | When this account's quota was last read successfully (RFC 3339). Required. Readings older than `max_age` are ignored. |
| `windows[].window_minutes` | Window length in minutes, such as `300` or `10080`. Required, positive. The longest applicable window ranks the account. |
| `windows[].used_percent` | Used share, 0 to 100. Required. |
| `windows[].resets_at` | When the window resets (RFC 3339), or `null` when it has not started. |
| `windows[].models` | Optional. The window applies only to models whose ID contains one of these strings (case-insensitive). |
| `windows[].kind` | Optional label, such as `weekly`. Informational. |

- Write the file atomically (to a temporary file in the same directory, then rename), so the plugin never reads a partial file.
- Accounts without `auth_id` or `updated_at`, and windows without a positive `window_minutes` or a `used_percent`, are skipped. Unknown fields are ignored.
- The plugin checks the file for changes at most every 2 seconds and re-reads it only when its size or modification time changes. A broken file is logged once and the last good readings stay in use until they age out.
- The file names credentials (often by email) and holds quota state. It must not contain credentials.

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    expiring-first:
      enabled: true
      priority: 110   # higher than cpa-live-voice (100): this plugin handles Live too
      # Provider keys to route. Omit or leave empty to apply to every provider.
      providers: [antigravity, claude, codex, xai]
      # How often the in-process probes refresh every credential's readings (at least 1m).
      probe_interval: 5m
      # Optional absolute path of a quota file that overrides individual accounts.
      quota_file: /home/me/.local/state/usage/quota.json
      # How old an account's reading may be before it is ignored (Go duration, at least 1m).
      max_age: 30m
      # Quota left untouched on every window, in percent (0 to below 100).
      reserve_percent: 0
      # Clear quota cooldowns that newer quota readings show to be over.
      clear_stale_cooldowns: true
      # Sticky fallback: how long the pinned account must stay unavailable before the pin moves.
      switch_after: 5m
      # Optional absolute path that keeps pins across restarts. Omit to keep pins in memory only.
      pins_file: /home/me/cliproxyapi/expiring-first-pins.json
      # Preview this daily route even before this plugin observes traffic for it.
      preview_models:
        - provider: antigravity
          model: gemini-3.8-flash-high
      # Credential IDs allowed for Codex Live/Realtime voice traffic. Omit to leave Live to the host.
      live_auth_ids: [codex-live-account.json]
      # Reject a Live request when no live_auth_ids credential is available (default true).
      fail_closed: true
```

`priority` under the plugin orders scheduler plugins: CLIProxyAPI consults only the highest-priority one, so run one routing plugin at a time.

With `pins_file`, the plugin writes the file (mode `0600`, replaced atomically) whenever a pin is created or moves, and restores it on load. It holds provider, model, auth ID and pin time only. Pair it with CLIProxyAPI's `save-cooldown-status: true`, so cooldowns also survive restarts.

## Scope and caveats

- For the providers it handles, the plugin takes precedence over `routing.strategy` and `routing.session-affinity`.
- It leaves a request to the configured strategy when the route spans several providers, when it is a Codex pool with a websocket-enabled credential (plugins cannot see whether the client uses a websocket), or when the provider is not in `providers`.
- Readings are only as fresh as the last probe cycle (`probe_interval`). Running out is still noticed at once, through CLIProxyAPI's cooldown after a 429; a limit reset spent on an account is noticed at the next cycle.
- After a limit reset, CLIProxyAPI would keep skipping the account until its original cooldown ends. The plugin clears such cooldowns itself; see [Stale cooldowns](#stale-cooldowns).
- The console page and the JSON routes redact credential IDs. Plugin logs carry auth IDs, as before, and no tokens.
- Cooldowns on a single model are not visible to plugins, so only credential-wide quota cooldowns are cleared.

## Install

### Plugin marketplace

Add this fork's registry URL as a custom plugin store source:

```text
https://raw.githubusercontent.com/karlorz/cpa-expiring-first/main/registry.json
```

Or append it to your existing `plugins.store-sources` configuration:

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/karlorz/cpa-expiring-first/main/registry.json"
```

Keep any existing source entries. Reload the configuration or restart CLIProxyAPI, refresh the Plugin Store, then select **Expiring First (karlorz)** from this custom source and install it. The store resolves the latest release from `karlorz/cpa-expiring-first`, downloads the archive for your platform, and verifies its checksum.

The official source also lists magoz's plugin under the same `expiring-first` ID. Select the **karlorz custom source** for installation and future updates. This fork uses the same plugin filename and `plugins.configs.expiring-first` configuration key. After installation, enable the plugin and give it a higher scheduler priority than `cpa-live-voice`, as described above.

### Manual installation

Download the zip for your platform from the [latest release](https://github.com/karlorz/cpa-expiring-first/releases/latest), check it against `checksums.txt`, extract `expiring-first.so`, `.dylib` or `.dll` into `<plugins.dir>/<goos>/<goarch>/`, and restart CLIProxyAPI.

## Development

```bash
gofmt -w .
go test -race ./...
go build -buildmode=c-shared -o expiring-first.so .
```

Routing logic lives in `internal/router` and has no cgo. The probes live in `internal/probe`, the Live Voice selector in `internal/live`, and the console in `internal/console`; none of them use cgo. `main.go` is the C ABI glue that also wires the host callbacks. Releases are built by `.github/workflows/build.yml` when a `v<version>` tag is pushed. See [dist/BUILD.md](dist/BUILD.md) for the local c-shared build notes.

## License

MIT
