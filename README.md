# proton-cal

A Go CLI (and MCP server) for reading and writing Proton Calendar events via
Proton's undocumented internal API. There is no official Proton Calendar API
or CalDAV support because of the end-to-end encryption, so this reproduces
the web client's endpoints and client-side PGP key hierarchy.

Built on Proton's official [`go-proton-api`](https://github.com/ProtonMail/go-proton-api)
(auth, sessions, key unlocking) with the calendar write path implemented here.
The reverse-engineered API details are documented in [RESEARCH.md](RESEARCH.md).

> Unofficial. Not affiliated with or endorsed by Proton AG. Use at your own
> risk - the API is undocumented and may change.

## Features

- **Auth**: SRP login, TOTP 2FA, manual CAPTCHA fallback, two-password mode,
  persistent session with automatic token refresh
- **Credentials**: bridge-style - after `login`, a derived *salted key
  passphrase* is stored (mode 0600), never your account password
- **Calendars**: list; select by ID or name; configurable default
- **Events**: list, create, update, delete with full client-side PGP
  encrypt/decrypt
- **Recurring events**: daily/weekly/monthly/yearly rules (or raw `--rrule`),
  client-side occurrence expansion, single-occurrence edit/delete (EXDATE +
  exception rows), series-wide changes with stale-exception cleanup
 - **All-day events**, multi-address signing
- **`get event` / `get calendar`**: full single-resource detail, with
  `--fields`/`--all` to control which fields show and color swatches in the
  terminal
- **`-o/--output json`** on every read/write command (machine-readable JSON
  on stdout; human messages on stderr)
- **MCP server**: stdio server exposing the same operations as 5 tools

## Install

Requires Go 1.26+. Because `go-proton-api` needs a `replace` directive for
Proton's resty fork, `go install` does not work - build from a clone:

```bash
git clone https://github.com/cheeseandcereal/proton-cal.git
cd proton-cal
make build        # produces ./proton-cal
```

## Usage

```bash
# Authenticate (prompts for username, password and 2FA; stores session +
# derived key passphrase in ~/.config/proton-cal/, mode 0600)
proton-cal login

# List calendars
proton-cal calendars

# List events for the next 7 days (recurring events expanded)
proton-cal events --days 7

# Target a calendar by name or ID (default: config default_calendar, else first)
proton-cal events --calendar Work

# Create an event
proton-cal create "Team standup" \
  --start "2026-05-26 09:00" --end "2026-05-26 09:30" \
  --description "Weekly sync" --location "Zoom"

# Recurring (daily/weekly/monthly/yearly; --every N, --count N or --until DATE)
proton-cal create "Team standup" \
  --start "2026-05-26 09:00" --end "2026-05-26 09:30" \
  --repeat weekly --count 10

# ... or a raw RRULE for anything fancier
proton-cal create "Payday" --start "2026-05-29 09:00" --end "2026-05-29 09:05" \
  --rrule "FREQ=MONTHLY;BYSETPOS=-1;BYDAY=FR"

# All-day events take dates (end is inclusive and optional)
proton-cal create "Conference" --all-day --start 2026-06-10 --end 2026-06-12

# Update (only the flags you pass change; recurrence is preserved)
proton-cal update <event-id> --summary "Renamed standup" --location "In person"

# Change or remove the recurrence (series changes drop edited occurrences)
proton-cal update <event-id> --repeat daily --count 5
proton-cal update <event-id> --no-repeat

# Edit / delete ONE occurrence of a recurring event (identified by its
# original start, as shown by `proton-cal events`)
proton-cal update <event-id> --occurrence "2026-06-02 09:00" --start "2026-06-02 10:00"
proton-cal delete <event-id> --occurrence "2026-06-02 09:00"

# Delete (recurring events: whole series incl. edited occurrences)
proton-cal delete <event-id>

# Inspect one resource in full detail (labeled fields; color swatch in a terminal)
proton-cal get event <event-id>
proton-cal get event <event-id> --fields summary,location,attendees
proton-cal get event <event-id> --all            # also uid, calendar_id, raw rrule
proton-cal get event <event-id> --ics            # export the raw iCalendar document
proton-cal get calendar                          # the default calendar
proton-cal get calendar Work --all               # also email, member/address IDs

# Machine-readable output (any command; JSON on stdout, messages on stderr)
proton-cal events --days 7 -o json
proton-cal get event <event-id> -o json

# Disable color (also auto-disabled when piped or when NO_COLOR is set)
proton-cal get calendar --no-color
```

All time-based commands accept `--tz` (default: the `timezone` saved in
`~/.config/proton-cal/config.toml`, detected from the system on first login).

### Configuration

`~/.config/proton-cal/config.toml`:

```toml
username = "you"                 # written by login
timezone = "America/Los_Angeles" # written by login; override freely
default_calendar = "Work"        # optional: calendar ID or name
# base_url = "https://mail-api.proton.me"  # optional override
```

`session.json` (same directory, mode 0600) holds the session tokens and the
derived salted key passphrase. It can unlock your calendar keys but cannot be
used to log in to your account; delete it (or run `proton-cal logout`) to
revoke. Your account password is never stored.

### Bootstrap cache

`cache.json` (same directory, mode 0600) caches the bootstrap responses that
otherwise cost 5 sequential API round-trips on every invocation: your user and
address keys (encrypted, 30-day TTL), per-calendar passphrases and keys
(encrypted, 30-day TTL) and the calendar list (7-day TTL). **Event content is
never cached** - every command fetches events fresh.

The liberal TTLs are safe because staleness self-heals: wrong key material
fails cryptographically, which invalidates the cached entries and retries once
with fresh data; unknown calendar selectors refresh the list automatically,
and `proton-cal calendars` always fetches fresh. The cache is scoped to the
session (re-login discards it) and `proton-cal logout` deletes it. Pass
`--no-cache` to bypass it entirely.

## MCP server

`proton-cal mcp` speaks MCP over stdio, exposing `list_calendars`,
`list_events`, `create_event`, `update_event` and `delete_event` (each tool
takes an optional `calendar` argument). Run `proton-cal login` once first.

```json
{
  "mcpServers": {
    "proton-calendar": {
      "command": "/path/to/proton-cal",
      "args": ["mcp"]
    }
  }
}
```

Or for [opencode](https://opencode.ai) (`opencode.json`):

```json
{
  "mcp": {
    "proton-calendar": {
      "type": "local",
      "command": ["/path/to/proton-cal", "mcp"]
    }
  }
}
```

## Development

| Target             | What it does                                              |
|--------------------|-----------------------------------------------------------|
| `make build`       | Build `./proton-cal`                                      |
| `make lint`        | gofmt check, `go vet`, golangci-lint (required)           |
| `make test`        | Offline unit tests                                        |
| `make integration` | Live tests against a real Proton account (see below)      |

### Architecture

```
cmd/proton-cal        entry point
internal/cli          cobra commands (argument binding + rendering)
internal/mcpserver    MCP stdio server (tool schemas + rendering)
internal/calsvc       shared service layer: bootstrap, input parsing,
                      list/create/update/delete orchestration
internal/auth         login (SRP/2FA/captcha), key unlocking, salted passphrase
internal/papi         go-proton-api wrapper + raw calls for endpoints it lacks
internal/calendar     calendar listing, member resolution, calendar key unlock
internal/event        event CRUD: decrypt, encrypt, sync payloads, recurrence ops
internal/pgp          PGP primitives (detached sign, session-key encrypt/reuse)
internal/ical         iCalendar fragment builder/parser (RFC 5545 escaping)
internal/icaltime     shared iCalendar date/time codec + timezone helpers
internal/recurrence   RRULE building/validation + occurrence expansion
internal/caltypes     event-row wire types shared by recurrence and event
internal/config       config + session store (flock-guarded, atomic writes)
internal/integration  opt-in live test suite (build tag `integration`)
```

[RESEARCH.md](RESEARCH.md) documents the reverse-engineered Proton API this
is built on: auth flow, key hierarchy, event encryption model, sync payload
semantics, recurrence behavior (all verified live against the API), plus the
sharp edges of the libraries involved. Other key references:
[`go-proton-api`](https://github.com/ProtonMail/go-proton-api),
[`ProtonMail/WebClients`](https://github.com/ProtonMail/WebClients) (calendar
crypto in `packages/shared/lib/calendar/`), and
[`proton-calendar-cli`](https://github.com/Nojuza/proton-calendar-cli).

### Integration tests

`internal/integration` exercises the real Proton API with a real account and
is opt-in:

1. `proton-cal login` once.
2. `cp internal/integration/config.example.toml internal/integration/config.toml`
   and list a **dedicated test calendar** (ID or name) - never your main one.
3. `make integration`

The suite only ever touches events it created itself (tagged summaries ~30
days in the future) and sweeps up after itself. Without `config.toml` or a
stored session it skips.

## Known limitations

- **Recurrence edge cases**: occurrence expansion is client-side; "this and
  future events" splits are not supported (edit single occurrences or the
  whole series). The server requires a series' start time to match its RRULE
  pattern - adjust `--rrule` when moving a series' start.
- **Attendees / invitations**: not supported; events are created without
  attendees.
- **FIDO2-only 2FA**: not supported (TOTP only).
- **API date filtering**: Proton ignores `Start`/`End` on the events listing
  and paginates everything at 100/page - the CLI paginates and filters
  client-side.
- **Reverse engineered**: Proton may change their internal API at any time.

## License

Public domain under [the Unlicense](UNLICENSE).
