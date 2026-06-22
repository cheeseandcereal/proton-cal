# proton-cal

A Go CLI (and MCP server) for reading and writing Proton Calendar events via
Proton's undocumented internal API. There is no official Proton Calendar API
or CalDAV support because of the end-to-end encryption, so this reproduces
the web client's endpoints and client-side PGP key hierarchy.

Built on Proton's official [`go-proton-api`](https://github.com/ProtonMail/go-proton-api)
(auth, sessions, key unlocking) with the calendar write path implemented here.
The reverse-engineered API details are documented in [docs/](docs/).

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
- **Reminders & color**: set per-event reminders (`--reminder 15m`,
  repeatable; `email:`/`notify:` prefix) and a color (a Proton palette color by
  name or hex) on create/update; revert to the calendar default with
  `--no-reminders`/`--reminders-default`/`--color default`
- **`get event` / `get calendar`**: full single-resource detail, with
  `--fields`/`--all` to control which fields show and color swatches in the
  terminal. Reminders and color reflect the calendar's defaults when an event
  has none of its own (matching the Proton apps); `get calendar` shows the
  calendar's default reminders and event duration
- **iCalendar export** (`get event --ics`): for a recurring event this exports
  the whole series in one `.ics` (the master `VEVENT` with its `RRULE`/`EXDATE`s
  plus a `VEVENT` per edited occurrence, each with its `RECURRENCE-ID`); use
  `--no-series` for just the addressed `VEVENT`. The export carries the
  effective reminders/color (the calendar defaults when the event has none of
  its own)
- **`-o/--output json`** on every read/write command (machine-readable JSON
  on stdout; human messages on stderr)
- **MCP server**: stdio server exposing the same operations as 7 tools
  (text + machine-readable structured results)

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

# Target a calendar by name or ID (default: the account's default calendar,
# else the first)
proton-cal events --calendar Work

# Create an event (--end is optional for timed events: it defaults to the
# calendar's default duration)
proton-cal create event "Team standup" \
  --start "2026-05-26 09:00" --end "2026-05-26 09:30" \
  --description "Weekly sync" --location "Zoom"

# Recurring (daily/weekly/monthly/yearly; --every N, --count N or --until DATE)
proton-cal create event "Team standup" \
  --start "2026-05-26 09:00" --end "2026-05-26 09:30" \
  --repeat weekly --count 10

# ... or a raw RRULE for anything fancier
proton-cal create event "Payday" --start "2026-05-29 09:00" --end "2026-05-29 09:05" \
  --rrule "FREQ=MONTHLY;BYSETPOS=-1;BYDAY=FR"

# All-day events take dates (end is inclusive and optional)
proton-cal create event "Conference" --all-day --start 2026-06-10 --end 2026-06-12

# Reminders (repeatable; shorthand 15m/1h30m/2d/1w, prefix email: for email)
# and a color (a Proton color name or its hex)
proton-cal create event "Dentist" --start "2026-06-10 14:00" --end "2026-06-10 14:30" \
  --reminder 1d --reminder email:1h --color strawberry

# Update an event (only the flags you pass change; recurrence is preserved)
proton-cal update event <event-id> --summary "Renamed standup" --location "In person"

# Reminders: replace, remove all, or revert to the calendar default
proton-cal update event <event-id> --reminder 30m --reminder 1d
proton-cal update event <event-id> --no-reminders          # no reminders
proton-cal update event <event-id> --reminders-default     # use the calendar default
proton-cal update event <event-id> --color pacific         # set a Proton color
proton-cal update event <event-id> --color default         # revert to the calendar color

# Change or remove the recurrence (series changes drop edited occurrences)
proton-cal update event <event-id> --repeat daily --count 5
proton-cal update event <event-id> --no-repeat

# Edit / delete ONE occurrence of a recurring event (identified by its
# original start, as shown by `proton-cal events`)
proton-cal update event <event-id> --occurrence "2026-06-02 09:00" --start "2026-06-02 10:00"
proton-cal delete event <event-id> --occurrence "2026-06-02 09:00"

# Delete an event (recurring events: whole series incl. edited occurrences)
proton-cal delete event <event-id>

# Update a calendar's metadata, default settings, or default status
proton-cal update calendar Work --name "Work (2026)" --color cobalt
proton-cal update calendar Work --default-duration 60 --makes-busy
proton-cal update calendar Work --reminder 15m --full-day-reminder 1d  # replace default reminder sets
proton-cal update calendar Work --make-default                         # make it the account default

# Delete a calendar (--yes required; owned calendars prompt for the login
# password, or pass --password for non-interactive use; holidays calendars
# need no password)
proton-cal delete calendar "Old project" --yes

# Inspect one resource in full detail (labeled fields; color swatch in a terminal)
proton-cal get event <event-id>
proton-cal get event <event-id> --fields summary,location,attendees
proton-cal get event <event-id> --all            # also uid, calendar_id, raw rrule
proton-cal get event <event-id> --ics            # export iCalendar (whole series for recurring events)
proton-cal get event <event-id> --ics --no-series # export only the single addressed VEVENT
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
# base_url = "https://mail-api.proton.me"  # optional override
```

The default calendar (used when no `--calendar` is given, and shown as
`[default]`) is your **account's** default calendar from Proton, not a local
setting; change it with `proton-cal update calendar <name> --make-default`.

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
`get_calendar`, `list_events`, `get_event`, `create_event`, `update_event`,
`delete_event`, `update_calendar` and `delete_calendar` (most tools take an
optional `calendar` argument). `delete_calendar` requires `confirm: true` and,
for an owned calendar, a `password` argument (the login password, used only for
the deletion handshake); there is no `create_calendar`. Read tools
return both a human-readable text block and machine-readable structured content
(the same JSON schema as the CLI's `-o json`). Because JSON arguments can't tell
an omitted string from an empty one, `update_event` treats empty fields as
"keep"; pass `clear_fields: ["location", ...]` to blank summary/description/
location (or `"color"`, equivalent to `color: "default"`, to revert it to the
calendar color). `create_event` and `update_event` also take `reminders` (e.g.
`["15m", "email:1h"]`) and `color` (a Proton color name or hex, or `"default"`
for the calendar color); `update_event` uses `reminders_mode`
(`keep`/`inherit`/`none`/`custom`) to pick the reminder behavior. Run
`proton-cal login` once first.

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

The [docs/](docs/) directory documents the reverse-engineered Proton API this
is built on, split into
[overview](docs/overview.md) (key hierarchy, sessions/scopes, error codes),
[crypto](docs/crypto.md) (auth flow, key unlocking, event encryption model),
[api](docs/api.md) (endpoints, sync payload semantics, reading/pagination,
recurrence) and [libraries](docs/libraries.md) (the sharp edges of the Go
libraries involved) - all verified live against the API. Other key references:
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
- **Event color**: only Proton's fixed palette is accepted, and a color
  cannot be cleared to "none" - `--color default` reverts to the calendar's
  color (Proton has no per-event "no color" state once one is set).
- **Creating calendars**: not supported (it requires generating a new
  calendar key bundle). You can update and delete calendars, but create them
  in the Proton app.
- **Deleting an owned calendar is interactive**: it requires re-entering your
  login password (prompted, or `--password` for scripts), since Proton gates
  the deletion behind an elevated re-authentication scope. Holidays calendars
  delete without a password; subscribed calendars must be unsubscribed in the
  app.
- **FIDO2-only 2FA**: not supported (TOTP only).
- **API date filtering**: Proton ignores `Start`/`End` on the events listing
  and paginates everything at 100/page - the CLI paginates and filters
  client-side.
- **Reverse engineered**: Proton may change their internal API at any time.

## License

Public domain under [the Unlicense](UNLICENSE).
