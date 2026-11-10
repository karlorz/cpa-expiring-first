## v0.3.14

- Probe FreeTier WorkBuddy (`workbuddyglobal`, `workbuddycn`) credit packages in-process and rank them on Expiring First. Remaining/total credits map to used percent; empty packs such as `0 / 440` count as exhausted (priority 0).
- `prefer_used` (default true) ranks WorkBuddy accounts that already have usage ahead of unused 0% used accounts, then still uses soonest package expiry inside each group. Set `prefer_used: false` for expiry-only order.

## v0.3.13

- Show a bounded **Recent Live decisions** audit on the Live Voice page (last 32 picks, rejects, and skips, with credential IDs). Persist it in `live_state_file`. This is the table from the old `cpa-live-voice` page, now on the scheduler that is actually loaded.

## v0.3.12

- Persist **in use** / **last used** and remembered Live `usage_limit_reached` deadlines across reloads (`live_state_file`). Keep those deadlines after the host listing drops StatusMessage. Scan `live_access_log` for a later `POST /v1/realtime/calls` 429 and skip that account until the weekly Live window. Show last skips on the status page.

## v0.3.11

- Skip Live `usage_limit_reached` and host-cooling `live_auth_ids` credentials, then pick the next allowlisted account. Mark **in use** (last successful Live pick within two minutes) and **last used** on the status page. Log the Live pick and each sanitized skip. Do not clear Live-pool cooldowns from Codex text quota probes.

## v0.3.10

- Separate Live next pick (`live_auth_ids` among host-enabled credentials) from CPA host fill-first (account priority) on the status page. Priority 0 from weekly-text ranking is no longer shown as a green Eligible next pick.

## v0.3.9

- Show Live Voice pool usage and fill-first scheduling on the status page: `live_auth_ids` order, host eligibility, next/last pick, and pick/reject counts. Weekly-soonest Schedule outcome stays text quota and is no longer labelled as the Live account list.

## v0.3.8

- Live Voice fill-first follows the configured `live_auth_ids` order. Host credential priority, including weekly-soonest integers from `write_priorities`, no longer chooses which allowlisted Live account is tried first.

## v0.3.7

- Name credentials by CPA's own credential ID in the console page and every JSON route, the same ID the management API uses, instead of the hashed `antigravity-…`/`acct-…` labels. Rename credentials if a name should stay private: the status page and its `/data` route are public.

## v0.3.6

- List every changeable plugin setting on the CPA Plugins form with the ParseConfig default in the field description: `max_age=30m`, `probe_interval=5m`, `switch_after=5m`, `reserve_percent=0`, `clear_stale_cooldowns=true`, `write_priorities=true`, `probe_antigravity_claude_gpt=false`, `fail_closed=true`.

## v0.3.5

- Show Codex weekly-soonest **Order** on the schedule table and write those integers into credential priority. Websocket Codex requests still go to the host, which fill-firsts using 100/99/…. Five-hour exhausted Codex accounts stay Priority 0.

## v0.3.4

- After each probe, write weekly-soonest **Order** into credential **priority** integers (`#1` = 100, then 99, 98…; exhausted = 0). Larger value means higher CPA fill-first priority. Failed probes keep the current integer. Disable with `write_priorities: false`.

## v0.3.3

- Combine ranking and quota into one **Schedule outcome** table: order, next/last pick, saved account priority, Gemini weekly and five-hour remaining.
- Skip Antigravity Claude/GPT/OpenAI probes and windows by default (`probe_antigravity_claude_gpt`, default false).
- Reload the live host account list and priorities on every display refresh. Quota numbers still follow `probe_interval` or Probe now.

## v0.3.2

- Show **Account selection order** above quota details for configured and observed models. Include ranked accounts, predicted next pick, last selected account, ranking reset, five-hour eligibility, saved account priority, and exclusion reasons.
- Reuse the request scheduler's quota eligibility and reset comparison logic for the displayed order. Preserve excluded runtime accounts in the read-only listing so disabled, unavailable, cooling down, stale, exhausted, and missing-reading states remain visible.
- Use one ranking snapshot for the winner preview and ordered table. Keep account priorities and routing state unchanged when rendering.
- Add regressions for display/pick consistency, five-hour reset return, priority ties, websocket delegation, private labels, and read-only rendering.

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
