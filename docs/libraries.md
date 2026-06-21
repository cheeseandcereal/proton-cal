# Library caveats (Go ecosystem)

Hard-won notes on the libraries that cover parts of this API; all verified
during live development, not just read from docs. See [overview.md](overview.md)
for the broader research index.

## `github.com/ProtonMail/go-proton-api`

- **No calendar write support.** It has no sync-endpoint bindings; all event
  mutations need a raw HTTP path (with the headers listed in
  [crypto.md](crypto.md)).
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
  scope dance in [overview.md](overview.md)) must be done via raw HTTP with
  an SRP proof from `github.com/ProtonMail/go-srp` (`NewAuth` +
  `GenerateProofs(2048)`).
- Its resty layer logs retry warnings to stderr by default; it accepts a
  custom logger to silence them.

## `github.com/ProtonMail/gopenpgp/v2`

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

## `github.com/teambition/rrule-go`

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

## Strict iCalendar parsers

Proton's card fragments lack `VERSION`/`PRODID` and a trailing CRLF; strict
parsers (e.g. `github.com/emersion/go-ical`) reject them outright. Parsing
fragments requires a tolerant parser. The reverse direction matters too:
cards are signed byte-for-byte, so generated fragments must keep stable
property order, CRLF endings, RFC 5545 TEXT escaping, and 75-octet line
folding.
