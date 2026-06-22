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
   cp pkg/integration/config.example.toml pkg/integration/config.toml
   ```

   and set `calendars` to your test calendar's ID (or unambiguous name).

If `config.toml` or the stored session is missing, the suite skips itself.

## Running

```sh
make integration
```

This runs every live test, which now spans four packages:

- `pkg/integration` - the original domain-layer suite (the `event` and
  `calendar` packages directly), plus the crash-recovery sweep.
- `pkg/calsvc` - the shared service layer end to end (create / get /
  update / list / delete, recurrence variants, all-day, DST wall-time
  stability, reminder inheritance, and live error paths).
- `pkg/mcpserver` - the MCP tool handlers (lifecycle with structured
  output, `clear_fields`, `get_calendar`).
- `pkg/cli` - the cobra commands driven in-process against the live
  service (JSON output, the `--location ""` field-clear tri-state,
  create/delete).

The service-, MCP- and CLI-layer tests build a real service with `calsvc.New`
(cache disabled) using the same stored session, so they require the same
prerequisites and the same `config.toml` test calendar. They skip themselves
when that setup is missing.

## What it does (and safety)

- The suite **creates, updates and deletes real events** on the configured
  calendars, scheduled ~30 days in the future.
- Every event it creates carries a `proton-cal-test <hex>` summary tag;
  the suite only ever deletes events carrying that tag. Each test cleans up
  after itself, and a final sweep (`TestZSweep`) scans the now+25d..now+40d
  window for leftovers from crashed runs.
- Rate limits: the API client retries HTTP 429 with backoff, but if Proton
  rate-limits you persistently, wait a while and re-run. Keep the
  `calendars` list short - one calendar is enough.
