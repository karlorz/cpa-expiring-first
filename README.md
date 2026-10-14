# Expiring First

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) scheduler plugin for pools of subscription accounts. It sends each request to the account whose quota **expires first**: the available account whose longest usage window (usually the weekly one) resets soonest and still has quota left. Quota that would reset unused is spent before quota that would still be there next week.

The plugin does not call any provider. It reads a [quota file](#quota-file) that another program keeps up to date, for example the [usage dashboard](https://github.com/magoz/usage). Without fresh quota readings it routes like [sticky-fill-first](https://github.com/magoz/cpa-sticky-fill-first): it stays on the account currently serving until that account runs out.

Plugin ID: `expiring-first`. Requires CLIProxyAPI 7.3.8 or newer (scheduler plugins that see every priority tier).

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
| A limit reset or a provider change moves a reset date | The next quota reading re-ranks the accounts. |

**Without fresh quota readings** (no `quota_file`, the file is missing or unreadable, or no candidate has a usable reading), the plugin routes stickily:

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

## Stale cooldowns

When an account runs out, CLIProxyAPI cools it down until the provider's reset time, which can be days away. If quota comes back earlier, for example because a limit reset was spent on the account, CLIProxyAPI would keep skipping it until that deadline. The plugin clears such cooldowns through `host.routing.reset_cooldown` (CLIProxyAPI 8.0.12 or newer):

- At most once a minute, on a request, it lists the credentials in a cooldown longer than 30 minutes (quota lockouts; shorter ones are transient or auth errors).
- It clears one only when a quota reading **taken after the plugin first saw that cooldown** shows quota left on every window. A reading from before the 429 cannot clear it, so a real lockout is never undone by stale data, and each new cooldown needs a newer reading.
- Each clear is logged at info level: `expiring-first: cleared cooldown of <auth_id> (until …): quota reading at … shows quota left`.
- Set `clear_stale_cooldowns: false` to turn it off. On older hosts the call fails and is logged as a warning.

## Quota file

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
      priority: 1
      # Provider keys to route. Omit or leave empty to apply to every provider.
      providers: [claude, codex]
      # Absolute path of the quota file. Omit to route stickily only.
      quota_file: /home/me/.local/state/usage/quota.json
      # How old an account's reading may be before it is ignored (Go duration, at least 1m).
      max_age: 30m
      # Quota left untouched on every window, in percent (0 to below 100).
      reserve_percent: 0
      # Clear quota cooldowns that newer quota readings show to be over (CLIProxyAPI 8.0.12+).
      clear_stale_cooldowns: true
      # Sticky fallback: how long the pinned account must stay unavailable before the pin moves.
      switch_after: 5m
      # Optional absolute path that keeps pins across restarts. Omit to keep pins in memory only.
      pins_file: /home/me/cliproxyapi/expiring-first-pins.json
```

`priority` under the plugin orders scheduler plugins: CLIProxyAPI consults only the highest-priority one, so run one routing plugin at a time.

With `pins_file`, the plugin writes the file (mode `0600`, replaced atomically) whenever a pin is created or moves, and restores it on load. It holds provider, model, auth ID and pin time only. Pair it with CLIProxyAPI's `save-cooldown-status: true`, so cooldowns also survive restarts.

## Inspecting pins

```bash
curl -s -H "Authorization: Bearer $MANAGEMENT_KEY" http://127.0.0.1:8317/v0/management/plugins/expiring-first/pins
```

```json
{"pins":[{"provider":"claude","model":"claude-opus-5-5","auth_id":"claude-1234-user@example.com.json","pinned_at":"2026-10-09T19:14:40Z","last_picked_at":"2026-10-09T19:30:02Z","missing_since":null}]}
```

The route is read-only and protected by the management key. Each pin is the account currently serving that route; `missing_since` is set while that account is unavailable.

## Scope and caveats

- For the providers it handles, the plugin takes precedence over `routing.strategy` and `routing.session-affinity`.
- It leaves a request to the configured strategy when the route spans several providers, when it is a Codex pool with a websocket-enabled credential (plugins cannot see whether the client uses a websocket), or when the provider is not in `providers`.
- Quota readings are only as fresh as the program writing the file. Running out is still noticed at once, through CLIProxyAPI's cooldown after a 429; a limit reset spent on an account is noticed at the next reading.
- After a limit reset, CLIProxyAPI would keep skipping the account until its original cooldown ends. The plugin clears such cooldowns itself; see [Stale cooldowns](#stale-cooldowns).
- It logs no credential data other than auth IDs and makes no network requests.
- Cooldowns on a single model are not visible to plugins, so only credential-wide quota cooldowns are cleared.

## Install

From the [CLIProxyAPI plugin store](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store), or manually: download the zip for your platform from the [latest release](https://github.com/magoz/cpa-expiring-first/releases/latest), check it against `checksums.txt`, extract `expiring-first.so`, `.dylib` or `.dll` into `<plugins.dir>/<goos>/<goarch>/`, and restart CLIProxyAPI. Remove other scheduler plugins for the same providers.

## Development

```bash
go test -race ./...
go build -buildmode=c-shared -o expiring-first.so .
```

Routing logic lives in `internal/router` and has no cgo. `main.go` is the C ABI glue. Releases are built by `.github/workflows/build.yml` when a `v<version>` tag is pushed.

## License

MIT
