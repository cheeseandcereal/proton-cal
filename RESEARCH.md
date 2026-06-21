# Proton Calendar API - Research Notes

Proton Calendar has no official public API and no CalDAV support, because
event data is end-to-end encrypted: the server only ever sees ciphertext (plus
a small set of plaintext scheduling metadata). Everything below was reverse
engineered from the web client and verified live against the production API
(most recently June 2026) unless explicitly marked otherwise.

This document is pure research: API behavior, crypto model, server quirks,
and the sharp edges of the libraries involved.

## Architecture Overview

The end-to-end encryption is layered as a key hierarchy. Every level unlocks
the next:

```
Account password
  -> SRP auth                          -> Session (UID + access/refresh token)
  -> bcrypt(password, key salt)        -> Salted key passphrase
    -> User private key (PGP, ECC Curve25519)
      -> Address private key(s)        (per-key Token encrypted to user key)
        -> Calendar passphrase         (armored PGP message, per member)
          -> Calendar private key(s)
            -> Event session keys      (AES-256, two per event)
```

A client that persists the *salted key passphrase* (instead of the account
password) can restore full read/write access later without re-prompting:
the passphrase unlocks the key chain but cannot be used to authenticate.

## Authentication (SRP-6a)

**Base URL:** `https://mail-api.proton.me` (works for all core + calendar
endpoints used here; the web clients use per-product hosts such as
`calendar.proton.me/api`, which serve the same API).

Proton uses a custom SRP-6a variant with **PMHash** - the expansion hash is
four concatenated SHA-512 digests of the input with suffix bytes
`\x00`..`\x03`, producing a 2048-bit output. All SRP arithmetic is
little-endian over 256-byte (2048-bit) values.

### Auth sequence

1. **Get auth info:**
   ```
   POST /auth/v4/info
   Body: {"Username": "<username or email>"}
   Response: {Version, Modulus, ServerEphemeral, Salt, SRPSession}
   ```

2. **Verify the modulus** - it is a PGP cleartext-signed message; verify
   against Proton's hardcoded SRP modulus public key (fingerprint
   `248097092b458509c508dac0350585c4e9518f26`).

3. **SRP challenge/response:**
   - Password derivation (auth versions 3/4):
     `bcrypt(password, salt + "proton")`, then
     `PMHash(bcrypt_output || modulus_bytes)`
   - Compute client ephemeral `A = g^a mod N` and client proof
     `M = PMHash(A || B || K)`
   ```
   POST /auth/v4
   Body: {Username, ClientEphemeral: b64(A), ClientProof: b64(M), SRPSession}
   Response: {UID, AccessToken, RefreshToken, ServerProof, PasswordMode,
              2FA: {Enabled}}
   ```
   Always verify `ServerProof` against the locally computed expectation.

4. **2FA** (when `2FA.Enabled` is non-zero; it is a bitmask:
   `1 = TOTP`, `2 = FIDO2`, `3 = both`):
   ```
   POST /auth/v4/2fa
   Body: {"TwoFactorCode": "<TOTP code>"}
   ```

5. **Two-password mode**: when `PasswordMode == 2`, the *login* password
   authenticates but a separate *mailbox* password feeds the key-derivation
   chain (key salts, below). The login password remains the one required for
   scope elevation (`/core/v4/users/unlock`, below).

### Required headers on every request

```
x-pm-uid: <UID>
Authorization: Bearer <AccessToken>
x-pm-appversion: Other
User-Agent: <descriptive string>
Accept: application/vnd.protonmail.v1+json
Content-Type: application/json     (on requests with a body)
```

`x-pm-appversion: Other` is accepted by the production API (verified live);
no registered client identifier is needed. Auth is purely header-based - no
cookies, no CSRF tokens.

### Human verification (code 9001)

Logins from new environments can be challenged: the error envelope carries
`Code: 9001` and a `Details` object with `HumanVerificationMethods`,
`HumanVerificationToken`, and a hosted-page `WebUrl`. For the `captcha`
method, the hosted verification page posts a `HUMAN_VERIFICATION_SUCCESS`
message (with `payload.token`) to its embedding window; capture that token
(e.g. via a `window.addEventListener("message", ...)` snippet pasted into
the browser console before solving the captcha) and retry the original
request with:

```
x-pm-human-verification-token: <token>
x-pm-human-verification-token-type: captcha
```

## Sessions, token refresh, and scopes

- A session is fully described by `UID + AccessToken + RefreshToken` and can
  be persisted and restored.
- On 401, `POST /auth/v4/refresh` rotates **both** tokens. Refresh tokens
  are single-use: concurrent refreshes race and the loser's tokens are dead.
  Anything doing raw HTTP alongside a managed HTTP client must serialize
  refreshes and re-read the rotated tokens afterwards.
- `DELETE /auth/v4` revokes the session (logout).

### The "locked" scope (live-verified June 2026)

`GET /core/v4/keys/salts` requires an elevated scope that freshly logged-in
sessions hold but **restored sessions do not**: calling it on a restored
session fails with HTTP 403, code 9101 ("Access token does not have
sufficient scope"). Consequences:

- Fetch the salts (and derive the salted key passphrase) **at login time**,
  or
- Re-elevate later by re-proving knowledge of the **login** password via SRP
  (same `POST /auth/v4/info` handshake) to:
  ```
  PUT /core/v4/users/unlock
  Body: {ClientEphemeral, ClientProof, SRPSession}
  Response: {Code, ServerProof}
  ```
  This is always the login password, even in two-password mode. Verify the
  returned `ServerProof`.
- `PUT /core/v4/users/lock` (empty body) drops the elevated scope again;
  good hygiene once the salts are in hand.

## Key Unlocking Chain

1. `GET /core/v4/users` - user object with `Keys[]` (armored private keys,
   one marked `Primary`).
2. `GET /core/v4/addresses` - addresses, each with `Keys[]`. Address keys
   are **token-based**: each carries a `Token`, an armored PGP message
   encrypted to the user key whose decrypted content is that address key's
   passphrase.
3. `GET /core/v4/keys/salts` - per-key salts; select the salt whose `ID`
   matches the primary user key.
4. Derive the **salted key passphrase**: `bcrypt` the (mailbox) password
   with the base64-decoded salt; the passphrase is the bcrypt output with
   its 29-character `$2y$10$<salt>` prefix stripped (the trailing
   31-character hash). A wrong password only manifests as user keys failing
   to unlock, so verify by unlocking before persisting anything.
5. Unlock the user private key(s) with the salted key passphrase, then each
   address key via its decrypted `Token`.

### Calendar key unlock

1. `GET /calendar/v1/{calID}/members` - find *our* member entry (match
   `Email` case-insensitively against our addresses; shared calendars list
   other users too). Yields `MemberID` and `AddressID` (which selects the
   signing address key for writes).
2. `GET /calendar/v1/{calID}/passphrase` - returns
   `Passphrase.MemberPassphrases[]`, one entry per member, each an armored
   PGP `Passphrase` message plus a detached `Signature`. Decrypt our entry
   with an address private key. The passphrase may be encrypted to **any**
   of the account's address keys, not necessarily the member's - try all of
   them. The web client is lenient about the detached signature; verifying
   it is optional in practice.
3. `GET /calendar/v1/{calID}/keys` - armored calendar private keys (possibly
   several generations, each tagged with a `PassphraseID`). Unlock each with
   the decrypted passphrase; keep every key that unlocks, since old events
   may be encrypted to retired calendar keys.

## Calendar API Endpoints

### Calendar management

| Method | Endpoint                             | Purpose |
|--------|--------------------------------------|---------|
| GET    | `/calendar/v1`                       | List calendars |
| POST   | `/calendar/v1`                       | Create calendar |
| GET    | `/calendar/v1/{calID}`               | Get one calendar |
| DELETE | `/calendar/v1/{calID}`               | Delete calendar |
| GET    | `/calendar/v2/{calID}/bootstrap`     | **Keys + members + passphrase + settings in one call** - the calendar key unlock path uses this (replaces the three v1 calls below) |
| GET    | `/calendar/v1/{calID}/keys`          | Calendar private keys (superseded by `/v2/bootstrap`) |
| GET    | `/calendar/v1/{calID}/passphrase`    | Calendar passphrase (superseded by `/v2/bootstrap`) |
| GET    | `/calendar/v1/{calID}/members`       | Member entries (superseded by `/v2/bootstrap`) |

The unlock path (`calendar.Keychain.Unlock`) issues a single
`GET /calendar/v2/{calID}/bootstrap` whose body carries `Keys`, `Passphrase`,
`Members` (same shapes as the v1 endpoints) and `CalendarSettings`. The
settings are stored on `calendar.Access.Settings` and drive default-reminder
display (see below). The cache key for a calendar's key material is this one
bootstrap path.

**Response shape drift (live-verified June 2026):** `GET /calendar/v1`
returns `Calendars[]` where display metadata (`Name`, `Description`,
`Color`) now lives **only on the per-user member entry** (`Members[0]`; the
list endpoint returns just our own member). Older responses had these fields
top-level; treat top-level values as a legacy fallback. `Type` observed
values: `0` = normal, `1` = subscribed (read-only ICS), `2` = holidays.

### Event operations

| Method | Endpoint                                        | Purpose |
|--------|--------------------------------------------------|---------|
| GET    | `/calendar/v1/{calID}/events`                    | List raw event rows (windowed via `Type`+`Start`/`End`, `More`-paginated; see below) |
| GET    | `/calendar/v1/{calID}/events?UID=<uid>`          | Rows sharing an iCal UID (server-side filter, verified live) |
| GET    | `/calendar/v1/{calID}/events/{eventID}`          | Single event (`{"Event": {...}}` envelope) |
| PUT    | `/calendar/v1/{calID}/events/sync`               | **Create/update/delete (batch) - the only write path** |
| PUT    | `/calendar/v1/{calID}/events/{eventID}/personal` | Update personal part (notifications) only |

There is no standalone `POST .../events`; all mutations go through `sync`.

## Event Encryption Model

### Data partitioning

Each event is split into iCalendar VEVENT fragments ("cards"), stored in two
groups (`SharedEventContent`, `CalendarEventContent`) of up to two cards
each, plus an optional attendees group:

| Card                | Type | Crypto                       | Properties |
|---------------------|------|------------------------------|------------|
| Shared signed       | 2    | plaintext + detached sig     | `UID`, `DTSTAMP`, `DTSTART`, `DTEND`, [`RECURRENCE-ID`], [`RRULE`], `EXDATE`*, `SEQUENCE`, `ORGANIZER`, [`X-PM-CONFERENCE-ID`] |
| Shared encrypted    | 3    | encrypted + sig of plaintext | `UID`, `DTSTAMP`, `CREATED`, [`SUMMARY`], [`DESCRIPTION`], [`LOCATION`], [`X-PM-CONFERENCE-URL`], **plus every other ("rest") property** |
| Calendar signed     | 2    | plaintext + detached sig     | `UID`, `DTSTAMP`, `EXDATE`*, `STATUS`, `TRANSP` |
| Calendar encrypted  | 3    | encrypted + sig of plaintext | `UID`, `DTSTAMP`, `COMMENT` |
| Attendees encrypted | 3    | encrypted (shared key)       | `UID`, `ATTENDEE`* |

Card `Type` enum: `0` clear text, `1` encrypted, `2` signed,
`3` encrypted+signed.

The server reads the *shared signed* card to denormalize scheduling metadata
(times, recurrence) into queryable columns; everything user-visible
(summary, description, location) is ciphertext to it.

**The "rest" rule (verified in the web client's `getVeventParts`):** only the
named fields above are routed to specific cards. **Every other VEVENT
property** - third-party `X-*` extensions, `PRIORITY`, `CLASS`, `URL`,
`CATEGORIES`, `GEO`, `ATTACH`, etc. - is dumped verbatim into the **shared
encrypted** card. So an imported event's foreign properties survive round-trip
and are fully recoverable; the only field that is *not* an iCal property is
`color` (it is a top-level row column, see below). `VALARM` sub-components go
into a separate personal card, but the server also denormalizes them into the
plaintext `Notifications` row field, which is the source of truth clients use
on read.

### Plaintext row fields (not in any card)

These come back on every event row in the clear, with no decryption:

- `Color` - per-event color override (CSS hex, e.g. `#EC3E7C`), empty when the
  event uses the calendar color.
- `Notifications` - `[{Type, Trigger}]` reminders. `Type` is
  `NOTIFICATION_TYPE_API` (`0` email, `1` device/display); `Trigger` is an
  iCal duration offset (`-PT1H`, `-PT15M`). The web client synthesizes
  `VALARM` components from this field on read (`deserialize.ts`).
  **Tri-state (verified live):** the JSON value is `null`/absent when the
  event inherits the calendar's default reminders, `[]` when reminders are
  explicitly removed (none), or the array of custom reminders. The web
  client's `getHasDefaultNotifications` is exactly `!Notifications`. The
  *default* reminders are NOT denormalized onto the event - they come from
  the calendar's `CalendarSettings.DefaultPartDayNotifications` (timed) /
  `DefaultFullDayNotifications` (all-day). The clients apply the default at
  display time to events that carry none of their own. `caltypes.RawEvent`
  records the tri-state via `NotificationsSet` (a custom `UnmarshalJSON`
  distinguishes `null` from `[]`), and the sync update re-sends it verbatim
  so an edit neither wipes reminders nor silently re-enables the default on
  an event whose reminders were removed.
- `Attendees` / `AttendeesInfo` - per-attendee `{ID, Token, Status}` plus a
  `MoreAttendees` flag. `Status` is `ATTENDEE_STATUS_API`: `0` needs-action,
  `1` tentative, `2` declined, `3` accepted. The identities (email/CN/role)
  live encrypted in the attendees card; they join to these rows by `Token`
  (= the card's `X-PM-TOKEN` attendee parameter), which is how live RSVP
  status is recovered.
- `IsOrganizer`, `IsProtonProtonInvite`, `IsPersonalSingleEdit`,
  `CreateTime`/`ModifyTime`/`LastEditTime`.

### Video conferencing (Proton Meet / Zoom)

A conference is stored as two custom properties split across the shared cards:

- `X-PM-CONFERENCE-ID;X-PM-PROVIDER=<n>:<id>` in the **shared signed** card.
  `X-PM-PROVIDER` is `VIDEO_CONFERENCE_PROVIDER`: `1` Zoom, `2` Proton Meet.
- `X-PM-CONFERENCE-URL;X-PM-HOST=<email>:<url>` in the **shared encrypted**
  card. The URL carries the meeting password in its `#pwd-<...>` fragment.
  The join block is also mirrored into `DESCRIPTION`.

Reassembling the full conference therefore needs both cards.

Proton **also embeds** a human-readable join block into the `DESCRIPTION`
itself (so non-Proton clients show a link), bracketed by a fixed separator
constant `SEPARATOR_PROTON_EVENTS`:

```
~-~-~-~-~-~-~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~-~-~-~-~-~-~
```

The web/mobile clients strip the region matching
`\n?<SEPARATOR>[\s\S]*?<SEPARATOR>` from the description for display
(`removeVideoConfInfoFromDescription`), showing the conference as a separate
field. We do the same for structured/detail output (`ical.StripConferenceBlock`,
applied only when the event has conference data), but keep `DESCRIPTION`
verbatim in the ICS export so the portability fallback survives.

### Fragment format

- Each card is a full `BEGIN:VCALENDAR` / `BEGIN:VEVENT` wrapper with
  **CRLF** line endings, **no** `VERSION`/`PRODID`, and **no trailing
  CRLF**. Strict iCalendar parsers reject these fragments.
- TEXT property values are escaped per RFC 5545 §3.3.11 and content lines
  folded at 75 octets per §3.1 (the web client folds too).
- Signed cards are round-tripped byte-for-byte: property order and line
  endings matter because the detached signature covers the exact bytes.

### Date/time property forms (all accepted, verified live)

```
All-day:  DTSTART;VALUE=DATE:20260709
UTC:      DTSTART:20260709T160000Z
Zoned:    DTSTART;TZID=America/Los_Angeles:20260709T090000
```

- `TZID` is accepted **without** a `VTIMEZONE` component (the web client
  omits it as well); the server derives `StartTimezone`/`EndTimezone`
  metadata from the TZID parameter.
- All-day events use exclusive `DTEND;VALUE=DATE` (day after the last day).
  The server sets `FullDay: 1` and stores the instants at **midnight UTC**
  with `StartTimezone: "UTC"`.

### ICS reconstruction (read/export)

To export a full standards-complete iCalendar event, decrypt all four cards
and **merge their VEVENT property lines into one VEVENT** (our
`ical.MergeFragments` + `event.BuildICS`):

- The cards each repeat `UID`/`DTSTAMP` and may duplicate other single-valued
  properties. Resolution: the **shared signed** card wins for structural props
  (`UID`, `DTSTAMP`, `DTSTART`, `DTEND`, `RRULE`, `RECURRENCE-ID`, `SEQUENCE`);
  first-seen wins otherwise. Multi-valued props (`EXDATE`, `ATTENDEE`, ...) are
  unioned as a set.
- Unknown/third-party properties are preserved verbatim (they live in the
  shared encrypted card's "rest"). Nested `VALARM` components are preserved.
- **`X-PM-SESSION-KEY` is stripped.** It is the symmetric key that decrypts the
  event's stored ciphertext - a durable decryption *capability*, not snapshot
  content. It is only ever injected by Proton into invite-attachment ICS (never
  present in a normal event read), and is meaningless to a standard consumer, so
  it must not leak into an export. Other Proton-internal markers
  (`X-PM-PROTON-REPLY`, `X-PM-SHARED-EVENT-ID`, `X-PM-BOOKINGUID`) are kept.
- The row-level fields that are *not* iCal properties are re-injected: `COLOR`
  from the `Color` column, and a `VALARM` per `Notifications` entry (`Type 0`
  -> `ACTION:EMAIL`, else `DISPLAY`; `Trigger` used verbatim). This mirrors how
  the web client materializes alarms from `Notifications` on read.
- Output wraps with `VERSION:2.0` and `PRODID:-//proton-cal//proton-cal//EN`,
  CRLF endings, lines folded at 75 octets. Zoned times keep their bare `TZID`
  with no `VTIMEZONE` (as Proton stores them); this is accepted by
  Google/Apple/RFC-lenient consumers but strict Outlook may warn.

### Encryption process (create)

1. Generate **two** random AES-256 session keys:
   - *shared* session key - shared encrypted card (+ attendees card)
   - *calendar* session key - calendar encrypted card
2. Build the iCalendar fragments.
3. Sign plaintext cards with the member's **address** private key (detached,
   armored, binary-mode literal data / signature type 0x00 - what the web
   client produces).
4. For encrypted cards: encrypt the fragment with the session key
   (symmetric, SEIPD packet) and sign the **plaintext** (never the
   ciphertext) with the address key. The signature stays detached; it is
   not embedded in the PGP message.
5. Encrypt each session key to the **calendar** public key (ECDH X25519),
   producing a PKESK "key packet".

Wire encoding: key packets and data packets are standalone binary OpenPGP
packets, base64-encoded, carried in separate JSON fields (`SharedKeyPacket`,
`CalendarKeyPacket` once per event; `Data` per encrypted card). Signatures
are armored.

## The Sync Endpoint (write path)

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
- On **create** `Notifications`/`Color` are explicit `null` and `Attendees`
  is `[]` (no reminders/attendees yet).
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
  null/`[]`/array tri-state - see "Plaintext row fields"), `Color` and clear
  `Attendees` (token + live `Status`) on update.

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

## Reading Events and Pagination

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
  scanning, used by `GetByUID`.

The single-event endpoint wraps the row as `{"Event": {...}}`.

### Raw event row (plaintext columns)

Each row carries, unencrypted: `ID`, `UID`, `CalendarID`, `SharedEventID`,
`StartTime`/`EndTime` (unix), `StartTimezone`/`EndTimezone`, `FullDay`,
`RRule` (verbatim RRULE value or null), `RecurrenceID` (unix),
`Exdates[]` (unix), `Permissions`, `IsOrganizer`, `IsProtonProtonInvite`,
plus the card groups and key packets described above.

## Recurring Events (all live-verified)

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

## Error Codes (observed)

| Code | Meaning |
|------|---------|
| 1000 | Success (single) |
| 1001 | Success (batch envelope) |
| 2001 | Sequence violation on exception row ("Single edits should have a Sequence greater or equal to main event") |
| 2501 | Event does not exist (e.g. GET/sync against a deleted ID) |
| 9001 | Human verification required (details carry methods + token + URL) |
| 9101 | Insufficient token scope (HTTP 403; e.g. key salts on a restored session) |

HTTP 429 carries a `Retry-After` header (seconds); honor it with bounded
retries. Error envelopes are `{"Code": <int>, "Error": "<message>"}`.

## Library Caveats (Go ecosystem)

Hard-won notes on the libraries that cover parts of this API; all verified
during live development, not just read from docs.

### `github.com/ProtonMail/go-proton-api`

- **No calendar write support.** It has no sync-endpoint bindings; all event
  mutations need a raw HTTP path (with the headers listed above).
- **Stale calendar read types.** Its `GetCalendars` response types predate
  the metadata move onto member entries: `Name`/`Color`/`Description` come
  back empty (verified live June 2026). Decode the list endpoint with your
  own types that read `Members[0]`.
- **`CalendarEventPart.Decode` is unusable for lenient reads.** It insists
  on verifying the card signature during decryption (events written by
  other calendar members fail), and it has a value-receiver bug: the
  decoded result is written to a copy, so the caller never sees it. Decrypt
  cards manually (split key/data packets, skip verification).
- **Stale tags.** The latest tagged release long predates current API
  behavior; pin a pseudo-version of master.
- **Resty fork required.** It only builds with
  `replace github.com/go-resty/resty/v2 => github.com/ProtonMail/resty/v2`,
  which also means downstream `go install module@version` does not work
  (replace directives are ignored outside the main module).
- **Token refresh coordination.** Its client auto-refreshes on 401 and
  notifies registered auth handlers with the rotated tokens. Raw HTTP done
  beside it must not refresh independently (single-use refresh tokens
  race); instead trigger a cheap typed call to make the client refresh,
  then re-read the persisted tokens.
- **No `/core/v4/users/unlock` binding.** Scope elevation (the "locked"
  scope dance above) must be done via raw HTTP with an SRP proof from
  `github.com/ProtonMail/go-srp` (`NewAuth` + `GenerateProofs(2048)`).
- Its resty layer logs retry warnings to stderr by default; it accepts a
  custom logger to silence them.

### `github.com/ProtonMail/gopenpgp/v2`

- **`PlainMessage.GetString()` mangles CRLF.** It normalizes `\r\n` to
  `\n`, silently corrupting decrypted iCalendar fragments (and anything
  else CRLF-significant). Always use `GetBinary()`.
- `crypto.GenerateSessionKey()` defaults to AES-256 - correct for event
  session keys.
- Detached signing of `crypto.NewPlainMessage(...)` produces binary-mode
  (type 0x00) signatures, matching the web client.
- `crypto.NewPGPSplitMessage(keyPacket, dataPacket)` reassembles the
  separately stored packets for decryption; passing a nil verify keyring
  and verifyTime 0 to `Decrypt` skips signature verification (required for
  events authored by other members).

### `github.com/teambition/rrule-go`

- **Never round-trip an RRULE string through it.** Its serializer rewrites
  `UNTIL` into UTC datetime form, corrupting DATE-form (all-day) values
  like `UNTIL=20371231` - which the server then treats differently. Keep
  and sign your own canonical string; use rrule-go only for validation and
  expansion.
- `StrToROptionInLocation(rule, loc)` interprets DATE-form and local-form
  `UNTIL` values in `loc`; parse in the event's start timezone (UTC for
  all-day events) so `UNTIL` boundaries land on the right instants.
- Setting `Dtstart` to the event start *in its own timezone* makes
  iteration preserve local wall-clock time across DST transitions
  (verified against a DST-crossing series); anchoring in UTC instead would
  shift occurrences by an hour after a transition.

### Strict iCalendar parsers

Proton's card fragments lack `VERSION`/`PRODID` and a trailing CRLF; strict
parsers (e.g. `github.com/emersion/go-ical`) reject them outright. Parsing
fragments requires a tolerant parser. The reverse direction matters too:
cards are signed byte-for-byte, so generated fragments must keep stable
property order, CRLF endings, RFC 5545 TEXT escaping, and 75-octet line
folding.

## Reference Material

- `github.com/ProtonMail/go-proton-api` - endpoint shapes, SRP, key
  unlocking (`unlock.go`).
- `github.com/ProtonMail/WebClients` - ground truth for calendar crypto and
  validation rules (`packages/shared/lib/calendar/`, e.g.
  `getIsRruleSupported`, `getSupportedUntil`).
- `github.com/ProtonMail/go-srp` - PMHash/SRP implementation, mailbox
  password derivation.
- `github.com/Nojuza/proton-calendar-cli` - prior Python reverse-engineering
  of the same internal API: the auth/key-unlock chain, the four-part event
  card model (shared/calendar × signed/encrypted), the `events/sync` write
  endpoint, and the all-signatures-over-plaintext rule.