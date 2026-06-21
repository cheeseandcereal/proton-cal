# Authentication, key unlocking, and the event encryption model

See [overview.md](overview.md) for the key-hierarchy summary and session
model. This document covers the auth handshake, how the key chain is unlocked,
and how an event's content is partitioned and encrypted.

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
   scope elevation (`/core/v4/users/unlock`, see [overview.md](overview.md)).

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

## Key unlocking chain

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

A single `GET /calendar/v2/{calID}/bootstrap` returns `Members`, `Passphrase`,
`Keys` and `CalendarSettings` together; the three fields below have the same
shapes they have on the standalone v1 routes, so the unlock logic is unchanged
- only the number of round-trips dropped from three to one (see
[api.md](api.md) for the endpoint and the v1/v2 versioning note).

1. `Members` - find *our* member entry (match `Email` case-insensitively
   against our addresses; shared calendars list other users too). Yields
   `MemberID` and `AddressID` (which selects the signing address key for
   writes).
2. `Passphrase` - `Passphrase.MemberPassphrases[]`, one entry per member, each
   an armored PGP `Passphrase` message plus a detached `Signature`. Decrypt
   our entry with an address private key. The passphrase may be encrypted to
   **any** of the account's address keys, not necessarily the member's - try
   all of them. The web client is lenient about the detached signature;
   verifying it is optional in practice.
3. `Keys` - armored calendar private keys (possibly several generations, each
   tagged with a `PassphraseID`). Unlock each with the decrypted passphrase;
   keep every key that unlocks, since old events may be encrypted to retired
   calendar keys.

## Event encryption model

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
