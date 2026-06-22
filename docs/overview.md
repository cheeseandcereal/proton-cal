# Proton Calendar API - overview

Proton Calendar has no official public API and no CalDAV support, because
event data is end-to-end encrypted: the server only ever sees ciphertext (plus
a small set of plaintext scheduling metadata). Everything in these documents
was reverse engineered from the web client and verified live against the
production API (most recently June 2026) unless explicitly marked otherwise.

These are research notes - API behavior, crypto model, server quirks, and the
sharp edges of the libraries involved. They are split across:

- **overview.md** (this file) - the key hierarchy, sessions/scopes, error
  codes, reference material.
- **[crypto.md](crypto.md)** - authentication (SRP-6a), the key-unlocking
  chain, and the event encryption model.
- **[api.md](api.md)** - the calendar/event endpoints, the sync write path,
  reading/pagination, and recurrence behavior.
- **[libraries.md](libraries.md)** - caveats of the Go libraries involved.

## Architecture overview

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

The same elevation is required to **delete an owned calendar**
(`DELETE /calendar/v1/{calID}` returns `9101` without it). `auth.WithLockedScope`
wraps the unlock -> run -> lock dance for both uses; calendar deletion prompts
for the login password at the point of use (it is never persisted). Deleting a
backend-managed (holidays) calendar uses `DELETE .../managed` and needs no
elevation.

## Error codes (observed)

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

## Reference material

- `github.com/ProtonMail/go-proton-api` - endpoint shapes, SRP, key
  unlocking (`unlock.go`).
- `github.com/ProtonMail/WebClients` - ground truth for calendar crypto and
  validation rules (`packages/shared/lib/calendar/`, e.g.
  `getIsRruleSupported`, `getSupportedUntil`).
- `github.com/ProtonMail/go-srp` - PMHash/SRP implementation, mailbox
  password derivation.
- `github.com/Nojuza/proton-calendar-cli` - prior Python reverse-engineering
  of the same internal API: the auth/key-unlock chain, the four-part event
  card model (shared/calendar x signed/encrypted), the `events/sync` write
  endpoint, and the all-signatures-over-plaintext rule.
