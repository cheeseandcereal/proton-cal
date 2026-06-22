# Calendar API endpoints, the sync write path, reading, and recurrence

See [crypto.md](crypto.md) for the encryption model these endpoints carry and
[overview.md](overview.md) for sessions and error codes.

## Calendar API endpoints

### Calendar management

| Method | Endpoint                             | Purpose |
|--------|--------------------------------------|---------|
| GET    | `/calendar/v1`                       | List calendars |
| POST   | `/calendar/v1`                       | Create calendar (we don't create calendars; needs calendar-key generation) |
| GET    | `/calendar/v1/{calID}`               | Get one calendar (returns only ID/Type/Owner; metadata lives on the member) |
| DELETE | `/calendar/v1/{calID}`               | Delete an OWNED calendar (we call it; needs the elevated "locked" scope) |
| DELETE | `/calendar/v1/{calID}/managed`       | Delete/leave a backend-managed (holidays) calendar (we call it; normal scope) |
| GET    | `/calendar/v2/{calID}/bootstrap`     | **Keys + members + passphrase + settings in one call** - our unlock path uses this; it is the *only* `v2` route |
| GET    | `/calendar/v1/{calID}/keys`          | Calendar private keys (we don't call it; bootstrap supplies the keys) |
| GET    | `/calendar/v1/{calID}/passphrase`    | Calendar passphrase (we don't call it; bootstrap supplies it) |
| GET    | `/calendar/v1/{calID}/members`       | Member entries (we don't call it; bootstrap supplies them) |
| PUT    | `/calendar/v1/{calID}/members/{memberID}` | Update our member's display metadata (Name/Description/Color); we call it for `update calendar` |
| GET    | `/calendar/v1/{calID}/settings`      | Calendar settings, standalone (we don't call it; bootstrap supplies them) |
| PUT    | `/calendar/v1/{calID}/settings`      | Update calendar default settings; we call it for `update calendar` |
| GET    | `/settings/calendar`                 | Per-account calendar user settings (carries `DefaultCalendarID`); we call it to resolve the default calendar |
| PUT    | `/settings/calendar`                 | Update calendar user settings; we call it for `update calendar --make-default` |

**Versioning note.** The API is almost entirely `calendar/v1`; in the Proton
web client (`packages/shared/lib/api/calendars.ts`) `calendar/v2` is used by a
single function, `getFullCalendar` -> `GET /calendar/v2/{calID}/bootstrap`.
`v2` is therefore just the namespace for the one consolidated bootstrap route,
not a newer API surface: **there is no `v2` calendar-list endpoint** (listing
is `GET /calendar/v1`) and settings have their own `v1` route. The standalone
`/keys`, `/passphrase`, `/members` and `/settings` routes are **not
deprecated** - the web client still uses them for narrower operations (key
reactivation, member management, settings reads/writes that pair with the
`PUT .../settings`). They are simply off our read/unlock path, which only ever
needs the bootstrap snapshot.

The unlock path (`calendar.Keychain.Unlock`) issues a single
`GET /calendar/v2/{calID}/bootstrap` whose body carries `Keys`, `Passphrase`,
`Members` (same shapes as the standalone v1 endpoints) and `CalendarSettings`.
The settings are stored on `calendar.Access.Settings` and drive
default-reminder display and the default event duration. The cache key for a
calendar's key material is this one bootstrap path.

`CalendarSettings.DefaultEventDuration` (minutes) is the calendar's default
event length. `create event` uses it to default a timed event's end when no
explicit end is given (`Settings.DefaultDuration` -> `applyDefaultDuration`,
resolved inside the unlocked-calendar closure so no extra fetch is needed); an
explicit end is required only when the calendar defines no default. `get
calendar` exposes the duration and both default-reminder sets in its JSON
(`default_duration`, `default_normal_notifications`,
`default_full_day_notifications`); the calendar *list* omits them since the
list endpoint carries no settings.

**Response shape drift (live-verified June 2026):** `GET /calendar/v1`
returns `Calendars[]` where display metadata (`Name`, `Description`,
`Color`) now lives **only on the per-user member entry** (`Members[0]`; the
list endpoint returns just our own member). Older responses had these fields
top-level; treat top-level values as a legacy fallback. `Type` observed
values: `0` = normal, `1` = subscribed (read-only ICS), `2` = holidays.

### The default calendar (live-verified)

Which calendar is "the default" is an **account-level server setting**, not a
local preference: `GET /settings/calendar` returns
`CalendarUserSettings.DefaultCalendarID`. `is_default` in `get calendar` /
`list_calendars` is computed by comparing each calendar's ID to that value,
and an unspecified `--calendar` selector resolves to it (falling back to the
first calendar when it is unset or names a calendar no longer present).
`update calendar --make-default` writes it via a partial
`PUT /settings/calendar {"DefaultCalendarID": "<id>"}` (normal scope, no
password). The fetch is cached and invalidated on a make-default write.

### Updating calendar metadata and settings (live-verified)

Both update routes accept **partial** bodies (the server preserves omitted
fields) and run with the normal session scope:

- **Metadata** (`Name`, `Description`, `Color`) -
  `PUT /calendar/v1/{calID}/members/{memberID}`. `Color` must be one of
  Proton's fixed accent-palette colors, else `400 code 2011 "Not a valid
  Proton color"` (same palette as event colors). A calendar has no
  inheritable color, so there is no "default" sentinel.
- **Settings** (`DefaultEventDuration`, `DefaultPartDayNotifications`,
  `DefaultFullDayNotifications`, `MakesUserBusy`) -
  `PUT /calendar/v1/{calID}/settings`. Notification sets serialize as `[]`
  (never `null`) when cleared. Only owned (`Type 0`) calendars are updatable.

After a settings write the cached bootstrap **and** the in-memory unlocked
`Access` are invalidated, so a subsequent read in the same session reflects
the change.

### Deleting a calendar (live-verified)

- **Owned (`Type 0`)** - `DELETE /calendar/v1/{calID}` requires the elevated
  "locked" scope: a restored session's token lacks it and the call fails with
  `403 code 9101`. Gaining it re-proves the login password via SRP
  (`PUT /core/v4/users/unlock`; see overview.md), so the CLI prompts for the
  password (`delete calendar`, or `--password`) and the MCP `delete_calendar`
  tool takes a `password` argument.
- **Backend-managed (`Type 2`, holidays)** - `DELETE /calendar/v1/{calID}/managed`
  works with the **normal** scope (no password). The routes are not
  interchangeable: the managed route on a normal calendar returns
  `400 code 2011 "Not a backend managed calendar"`, and the normal route on a
  managed/subscribed calendar returns `403 code 9101`.
- **Subscribed (`Type 1`)** - not deletable here (unsubscribe in the app).

### Event operations

| Method | Endpoint                                        | Purpose |
|--------|--------------------------------------------------|---------|
| GET    | `/calendar/v1/{calID}/events`                    | List raw event rows (windowed via `Type`+`Start`/`End`, `More`-paginated; see below) |
| GET    | `/calendar/v1/{calID}/events?UID=<uid>`          | Rows sharing an iCal UID (server-side filter, verified live) |
| GET    | `/calendar/v1/{calID}/events/{eventID}`          | Single event (`{"Event": {...}}` envelope) |
| PUT    | `/calendar/v1/{calID}/events/sync`               | **Create/update/delete (batch) - the only write path** |
| PUT    | `/calendar/v1/{calID}/events/{eventID}/personal` | Update personal part (notifications) only |

There is no standalone `POST .../events`; all mutations go through `sync`.

## The sync endpoint (write path)

`PUT /calendar/v1/{calID}/events/sync` takes a batch:

```json
{
  "MemberID": "<calendar member ID>",
  "IsImport": 0,
  "Events": [ { ...one of the three operation shapes... } ]
}
```

Operation shapes inside `Events[]`:

- **Create:** `{"Overwrite": 0, "Event": {...full body with key packets...}}`
- **Update:** `{"ID": "<eventID>", "Event": {...body without key packets...}}`
- **Delete:** `{"ID": "<eventID>"}`

`IsImport` and `Overwrite` are sent only on creates.

### Event body (minimal accepted shape, verified live)

```json
{
  "Permissions": 1,
  "SharedKeyPacket": "<b64 PKESK>",        // create only
  "CalendarKeyPacket": "<b64 PKESK>",      // create only
  "SharedEventContent": [
    {"Type": 2, "Data": "<fragment>", "Signature": "<armored sig>"},
    {"Type": 3, "Data": "<b64 SEIPD>", "Signature": "<armored sig of plaintext>"}
  ],
  "CalendarEventContent": [
    {"Type": 2, "Data": "<fragment>", "Signature": "<armored sig>"},
    {"Type": 3, "Data": "<b64 SEIPD>", "Signature": "<armored sig of plaintext>"}
  ],
  "AttendeesEventContent": [],
  "Attendees": [],
  "Notifications": null,
  "Color": null
}
```

Field-presence rules (the server is picky):

- The content arrays must serialize as `[]`, never `null`.
- On **create** `Notifications`/`Color` are `null` when the event inherits the
  calendar defaults, but may also carry a custom reminder array / palette color
  (proton-cal supports setting them). `Attendees` is `[]` (no attendees yet).
- `Color` must be one of Proton's fixed accent palette colors (the web client's
  `ACCENT_COLORS`); any other value is rejected with `code 2011: Not a valid
  Proton color`. There is **no** way to clear a per-event color back to null:
  reverting to the calendar default means setting the event color explicitly to
  the calendar's own color (what the web client does).
- `Permissions: 1` is accepted for self-owned events; `IsOrganizer` is
  optional.

### The sync body fully replaces the event (updates must re-send everything)

A sync `update` is a whole-object replace, not a patch: any field omitted (or
sent as `null`/`[]`) is reset on the server. This is easy to get wrong and
silently destroys data. Two failure modes we hit and fixed:

- **Card content.** Rebuilding the cards from a fixed set of known fields
  (summary/description/location/times/recurrence) drops everything else the
  event carried - conferencing (`X-PM-CONFERENCE-*`), `ORGANIZER`, the
  attendees card, third-party `X-` properties. The correct approach is to
  decrypt the existing cards and **patch only the edited properties in
  place**, preserving every other line verbatim (then re-sign/re-encrypt).
- **Row-level personal data.** `Notifications`, `Color` and the clear
  `Attendees` rows live outside the cards. The web client's `formatData`
  re-sends them on every update (`Notifications: notificationsPart || null`,
  `Color: colorPart || null`, `Attendees: [{Token, Status, Comment}]`).
  Sending the wrong value **wipes the reminders and attendee RSVP rows**.
  We re-send the event's existing `Notifications` (preserving the
  null/`[]`/array tri-state - see [crypto.md](crypto.md) "Plaintext row
  fields"), `Color` and clear `Attendees` (token + live `Status`) on update -
  unless the caller is explicitly changing reminders/color, in which case the
  new value replaces the carried-over one. Note `Color: null` on a sync update
  is ignored server-side (the color sticks); reverting requires setting the
  calendar's own color explicitly.

Note: reminders can also live in a member-specific personal card
(`PersonalEvents`), but in practice the events observed live carry **no**
`PersonalEvents` card; the plaintext `Notifications` tri-state row field is
the source of truth, and re-sending it verbatim on the sync update preserves
the event's reminders (or its explicit "none", or its inheritance of the
calendar default). `PersonalEvents` is therefore not decrypted.

### Updates reuse session keys

Update bodies carry **no key packets**; the server keeps the originals. The
client must therefore decrypt the event's existing session keys from the
stored `SharedKeyPacket`/`CalendarKeyPacket` (using the calendar private
key) and re-encrypt the (patched) card plaintexts with those same session
keys, producing fresh data packets only. Events created by the Proton web
app may have **no encrypted calendar card** (and thus no
`CalendarKeyPacket`); the update must not attempt to decrypt a calendar
session key in that case, and must not emit an encrypted calendar card with
no key packet.

### Response envelope

```json
{
  "Code": 1001,
  "Responses": [
    {"Index": 0, "Response": {"Code": 1000, "Event": { ...raw event row... }}}
  ]
}
```

- Top-level `Code` 1001 = batch accepted (1000 for some single-op cases).
- Per-event `Code` 1000 = success; failures carry a non-1000 code plus an
  `Error` message string.
- Creates echo the full stored row; updates may omit it; pure deletes return
  only the top-level code with no `Responses`.

## Reading events and pagination

`GET /calendar/v1/{calID}/events` accepts `Start`, `End`, `Timezone`,
`Type`, `Page`, `PageSize` (and `MetaDataOnly`). The decisive parameter is
**`Type`**: the server only window-filters by `Start`/`End` when a `Type` is
supplied. With no `Type` it returns *every* row in the calendar paginated
100-at-a-time (this is the trap the earlier implementation fell into - it
made every list scale with total calendar size). With a `Type` it returns
just the rows in the window. All live-verified.

### Windowed query (the way the web client does it, and now us)

`Type` is the `CalendarEventsQueryType` enum. There are four buckets and you
must query **all four** to see everything overlapping a window (the web
client fires all four in parallel):

| `Type` | meaning |
|--------|---------|
| `0` PartDayInsideWindow | timed event whose start is inside the window |
| `1` PartDayBeforeWindow | timed event whose start is before the window but extends/recurs into it |
| `2` FullDayInsideWindow | all-day event whose start is inside the window |
| `3` FullDayBeforeWindow | all-day event whose start is before the window but extends/recurs into it |

- **Pagination is by `More` (cursor), not `Total`.** A `Type`-scoped
  response carries `More` (`0`/`1`), not `Total`; iterate `Page` from 0
  while `More == 1`. (The legacy unscoped query is the one that carries
  `Total`.) `PageSize` is capped at 100 (`PageSize > 100` -> `400 code 2021`).
- **Maximum window span is 93 days** = `93 * 86400` = `8035200s`. A wider
  `End - Start` is rejected with `code 2000 "Time window is too big"` (the
  cap is exact: `8035201s` is rejected). Split wider requests into
  <=93-day chunks and union the results.
- **Recurring masters that started before the window come back under the
  `*BeforeWindow` types** (`1`/`3`), keyed by their *master* row - the
  server does not expand occurrences. Live-verified against a Birthdays
  calendar of `FREQ=YEARLY` full-day masters all dated years in the past: a
  future window returned exactly the relevant masters under `Type=3`. The
  client still expands occurrences and does the final overlap filtering
  (recurring masters must never be window-filtered by their own
  `StartTime`/`EndTime`, which describe only the first occurrence).
- **Pad the window by ~1 day each side** before querying (the server buckets
  by timezone-local start/end; a day of slack avoids missing all-day or
  boundary rows). Mirrors the web client's +/-1 day search padding.
- A row can be returned by more than one `(chunk, Type)` slice, so
  **deduplicate by `ID`** when unioning.
- `MetaDataOnly=1` is accepted but was observed to have **no effect** on the
  payload (full encrypted blobs still returned); not a useful lever.
- `?UID=<ical-uid>` filters server-side (independent of `Type`) - the cheap
  way to fetch a single recurring series (master + exception rows) without
  scanning, used by `GetByUID`. `GetByUID` paginates on the `More` cursor
  (like the windowed query) so series larger than one page are fetched in
  full; `More` is documented for `Type`-scoped queries, and the loop
  terminates after the first page regardless.

The single-event endpoint wraps the row as `{"Event": {...}}`.

### Raw event row (plaintext columns)

Each row carries, unencrypted: `ID`, `UID`, `CalendarID`, `SharedEventID`,
`StartTime`/`EndTime` (unix), `StartTimezone`/`EndTimezone`, `FullDay`,
`RRule` (verbatim RRULE value or null), `RecurrenceID` (unix),
`Exdates[]` (unix), `Permissions`, `IsOrganizer`, `IsProtonProtonInvite`,
plus the card groups and key packets described in [crypto.md](crypto.md).

## Recurring events (all live-verified)

- **Recurrence metadata is plaintext.** `RRULE`/`EXDATE`/`RECURRENCE-ID`
  live in the shared *signed* card; the server denormalizes them into the
  top-level `RRule` (verbatim value string), `Exdates` (unix timestamps) and
  `RecurrenceID` (unix timestamp of the original occurrence start) columns.
  Reading recurrence requires no decryption.
- **No server-side expansion.** Listings return master rows only; clients
  expand occurrences themselves.
- **Single-occurrence delete** = add an `EXDATE` to the master (a full
  update of the master's cards).
- **Single-occurrence edit** = a separate "exception row": a new event with
  the **same UID** plus `RECURRENCE-ID` set to the original occurrence
  start. Exception rows may carry fresh key packets (no need to reuse the
  master's session keys).
- **SEQUENCE rule (server-enforced):** exception rows must satisfy
  `SEQUENCE >= master's SEQUENCE`, otherwise the sync fails with code 2001
  ("Single edits should have a Sequence greater or equal to main event").
  Per RFC 5546, bump `SEQUENCE` only on date/time/recurrence changes - never
  on field-only edits, or a master edit would leapfrog its exceptions.
- **Deleting a master ORPHANS its exception rows** (no cascade). A series
  delete must batch the master plus all same-UID rows in one sync call.
- **Series-level significant changes invalidate exceptions:** after changing
  a master's times or rule, its existing exception rows describe occurrences
  that may no longer exist; clean them up explicitly.
- **Server-enforced RRULE limits** (mirroring the web client's
  `getIsRruleSupported`): `FREQ` in {DAILY, WEEKLY, MONTHLY, YEARLY};
  `COUNT <= 49`; `UNTIL <= 2037-12-31`; `COUNT` and `UNTIL` mutually
  exclusive; MONTHLY allows one `BYDAY`+`BYSETPOS` (setpos in
  {-1,1,2,3,4}) or one `BYMONTHDAY`; YEARLY `BYMONTHDAY` requires
  `BYMONTH`; `DTSTART` must equal the first occurrence (moving a series
  start requires adjusting the rule).
- **UNTIL semantics:** for timed events, `UNTIL` is the *end of that day*
  (23:59:59) in the event-start timezone, expressed in UTC
  (`...T075959Z`-style). For all-day events it is a floating `YYYYMMDD`
  date.
- **DST:** occurrences of a zoned series keep their local wall-clock time
  across DST transitions; expansion must iterate in the event's start
  timezone, not in UTC. All-day masters anchor at midnight UTC (matching
  the stored instants).
- **Whole-series ICS export** (`get event --ics`): `GetByUID` fetches the
  master + every exception row, and one VCALENDAR is emitted with the master
  VEVENT (RRULE/EXDATEs) followed by one VEVENT per edited occurrence (each
  carrying its `RECURRENCE-ID` straight from the row's signed card). The
  injected COLOR/VALARMs are the *effective* values (the row's own, else the
  calendar default). `--no-series` falls back to the single addressed VEVENT.
