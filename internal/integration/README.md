# Live integration tests

Opt-in test suite that exercises the **real Proton Calendar API** end to
end. Excluded from normal builds and `go test ./...` by the `integration`
build tag.

## Prerequisites

1. Log in once with this CLI so a session (with key passphrase) is stored
   in the real user config dir:

   ```sh
   proton-cal login
   ```

2. Create a **dedicated test calendar** in Proton Calendar. Do not use a
   personal calendar.

3. Configure the suite:

   ```sh
   cp internal/integration/config.example.toml internal/integration/config.toml
   ```

   and set `calendars` to your test calendar's ID (or unambiguous name).

If `config.toml` or the stored session is missing, the suite skips itself.

## Running

```sh
make integration
```

(equivalent to `go test -tags integration -count=1 -v ./internal/integration/...`)

## What it does (and safety)

- The suite **creates, updates and deletes real events** on the configured
  calendars, scheduled ~30 days in the future.
- Every event it creates carries a `proton-cal-go-test <hex>` summary tag;
  the suite only ever deletes events carrying that tag. Each test cleans up
  after itself, and a final sweep (`TestZSweep`) scans the now+25d..now+40d
  window for leftovers from crashed runs.
- Rate limits: the API client retries HTTP 429 with backoff, but if Proton
  rate-limits you persistently, wait a while and re-run. Keep the
  `calendars` list short - one calendar is enough.
