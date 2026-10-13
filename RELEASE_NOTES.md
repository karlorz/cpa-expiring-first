## v0.2.0

- **Clears stale cooldowns.** When a quota reading taken after an account's quota cooldown began shows quota left (for example after a limit reset was spent), the plugin clears the cooldown through `host.routing.reset_cooldown`, so the account is usable at once instead of at the original reset time. Only cooldowns longer than 30 minutes are considered; checked at most once a minute. Needs CLIProxyAPI 8.0.12 or newer; on older hosts the call fails with a logged warning. Turn off with `clear_stale_cooldowns: false`.

## v0.1.0

First release. Routes each provider and model pool to the available account whose longest usage window resets soonest and still has quota, read from a quota file (schema `expiring-first.quota.v1`). Falls back to sticky fill-first routing without fresh readings. Based on [sticky-fill-first](https://github.com/magoz/cpa-sticky-fill-first) v0.4.0: pins, `pins_file` and the pins route work the same way.

Requires CLIProxyAPI 7.3.8 or newer.
