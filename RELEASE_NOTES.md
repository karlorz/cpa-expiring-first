## v0.1.0

First release. Routes each provider and model pool to the available account whose longest usage window resets soonest and still has quota, read from a quota file (schema `expiring-first.quota.v1`). Falls back to sticky fill-first routing without fresh readings. Based on [sticky-fill-first](https://github.com/magoz/cpa-sticky-fill-first) v0.4.0: pins, `pins_file` and the pins route work the same way.

Requires CLIProxyAPI 7.3.8 or newer.
