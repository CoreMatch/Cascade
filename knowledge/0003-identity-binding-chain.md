# 0003 — Identity Binding Chain: Ed25519 JWT Before Game Data

- **Status**: accepted
- **Date**: 2026-11-04 (introduced in commit `51e81b8`, 2026-10-25)

## Context

Backends must not trust client-supplied identity fields as HA identity
(contract, "Transport and trust" section). The game UUID a player registers
with in the Rusted Warfare protocol is client-controlled; the only trusted
source of a player's identity is the proxy, which runs the HRPAuth
authentication flow. A mechanism is needed to tell the backend, per game
connection, which game UUID belongs to which backend session — before the
player may register or join a room.

## Decision

The chain, per connection:

1. **Session ID first.** The proxy generates a cryptographically random
   UUIDv4 session ID and sends it in the 24-byte `CASC` preamble before any
   game bytes (see 0004). The backend creates a `PENDING` session keyed by
   it (contract, "Game TCP preamble" / "Session state machine").
2. **Intercept `RegisterPlayer`.** When the client sends
   `RegisterPlayer` (type 110), the proxy decodes the player name and game
   UUID (`internal/proxy/session.go::bindRegisteredPlayer`,
   `internal/protocol/codec.go::DecodeRegister`) and records a proxy
   binding row (`internal/proxy/session.go::bindRegisteredPlayer`).
3. **Sign a one-time Ed25519 JWT.** The proxy signs a compact JWS with
   `alg: EdDSA`, `kid` from configuration, and claims `iss` (configured
   issuer), `aud` (exact backend ID), `sid` (session ID), `sub` (game
   UUID), `iat`, `exp` = iat + 30 s, and a cryptographically random 16-byte
   `jti` (`internal/proxy/token.go::Sign`). The private key must be
   Ed25519 in PKCS#8 PEM and its file permissions must not allow group or
   other access (`internal/proxy/token.go::loadSigner`).
4. **Bind before forwarding.** The proxy calls
   `PUT /v1/sessions/{sessionID}/identity` with `{"token": ...}`
   (`internal/backend/client.go::BindIdentity`) and only after that succeeds
   does it forward the client's `RegisterPlayer` to the backend
   (`internal/proxy/session.go::clientToBackend`). The bind call is bounded
   by `pending_session_timeout_sec` (default 5 s)
   (`internal/proxy/session.go::bindRegisteredPlayer`,
   `internal/config/config.go (field: pending_session_timeout_sec)`).
5. **Hard gates both directions.** Client game data before a successful
   bind closes the session (`internal/proxy/session.go::clientToBackend`);
   backend game data before the bind is rejected except the `Preregister`
   answer (`internal/proxy/session.go::backendToClient`). Covered by
   `TestProxyBindsRegisteredIdentityBeforeForwarding`
   (`internal/proxy/session_integration_test.go::TestProxyBindsRegisteredIdentityBeforeForwarding`).

Contract-side obligations (HA-Contract `Cascade-Backend-v1.md`):

- `PENDING` → `BOUND` → `CLOSED`: the backend must not accept registration,
  add the player to a room, or process game commands before entering
  `BOUND`; pending sessions expire 5 seconds after preamble acceptance
  ("Session state machine" section).
- **One-time `jti`:** a token ID can only be consumed once; the ledger is
  independent of the trust strategy and of signature verification. Replayed
  `jti` → `SESSION_IDENTITY_REPLAYED`; a different identity or `jti` on an
  already-bound session → `SESSION_IDENTITY_CONFLICT`; an exact
  same-sessionID-and-jti retry is idempotent and returns `204`
  ("Identity JWT" and "HTTP API" sections; OpenAPI
  `cascade-backend-v1.yaml`, `/sessions/{sessionID}/identity`).
- The backend does **not** verify the JWT signature, `kid`, or `iss` —
  proxy authenticity is carried by the selected trust strategy (see 0004).
  Structural claim checks remain: `alg` `EdDSA`, audience equal to the
  backend ID, non-empty `sid`/`sub`/`jti`, `exp` within 30 s of `iat`,
  not expired ("Identity JWT" section).
- The JWT must not contain HA UUID, HA login state, credentials, or
  authorization policy ("Identity JWT" section).

## Consequences

Costs and limitations:

- Every player registration pays one extra control-plane round trip before
  the first game packet is forwarded; the proxy's 5 s bind timeout and the
  backend's 5 s pending deadline are coupled — if binding cannot be
  confirmed in time, the proxy closes the game connection and must not
  forward the client's pending PoW response (contract, "Errors and
  retries").
- 30-second expiry plus one-time `jti` means tokens cannot be cached or
  replayed; a fresh token is signed per session, per bind.
- HA-side state (chat-command `/login`, `/claim`, cached auth state in
  Redis) is deliberately **not** in the JWT; it stays proxy-local
  (`internal/proxy/session.go::handleAuthChat`). The backend learns only
  the game UUID — by contract design, so identity transport and
  authorization policy stay separate.

Benefits:

- A backend that honors the contract can never register or seat a player
  whose game UUID was not vouched for by the proxy on that exact session.
- Bind failures are logged without material: only session ID, backend ID,
  and an error summary; never the JWT or key material
  (`internal/proxy/session.go::logBindFailure`).

## Evidence

- `internal/proxy/session.go::clientToBackend` (client→backend gate +
  bind), `internal/proxy/session.go::backendToClient` (backend→client
  gate), `internal/proxy/session.go::bindRegisteredPlayer` (bind, incl.
  bind timeout from `pending_session_timeout_sec`),
  `internal/proxy/session.go::logBindFailure` (redacted failure logging).
- `internal/proxy/token.go::loadSigner` (key loading: Ed25519 PKCS#8,
  permission check), `internal/proxy/token.go::Sign` (claims incl. `exp`
  = +30 s, random `jti`).
- `internal/backend/client.go::BindIdentity` — issues
  `PUT /v1/sessions/{sessionID}/identity`.
- `internal/protocol/types.go::TypeRegisterPlayer` —
  `TypeRegisterPlayer = 110`.
- `internal/protocol/codec.go::DecodeRegister`.
- `internal/proxy/session_integration_test.go::TestProxyBindsRegisteredIdentityBeforeForwarding`
  — end-to-end test that identity is bound before forwarding, against an
  mTLS fake backend.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`:
  "Session state machine", "Identity JWT", "HTTP API" (idempotency and
  conflict rules), "Errors and retries" (stable codes incl.
  `SESSION_IDENTITY_REPLAYED`, `SESSION_IDENTITY_CONFLICT`).
- HA-Contract repository, `docs/api/openapi/cascade-backend-v1.yaml`,
  `/sessions/{sessionID}/identity` — `204` on bind or exact idempotent
  retry.
- Commits: `51e81b8` (introduction), `df0feb3` (bind failure logging).
