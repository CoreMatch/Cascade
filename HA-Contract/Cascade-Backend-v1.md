# Cascade Backend Contract v1

This contract defines the boundary between the Cascade edge proxy and an
independently developed Rusted Warfare backend. The backend owns rooms, lobby
state, game state, and all game protocol behavior. The proxy owns client
ingress, HRPAuth identity, route selection, and byte forwarding.

## Transport and trust

- Control endpoints use HTTPS with mutual TLS. The proxy validates the
  backend certificate and the backend requires a trusted proxy client
  certificate.
- The game endpoint uses TLS with mutual TLS. The backend requires the same
  proxy identity before reading a gateway preamble.
- The proxy JWT signing key is Ed25519. The backend receives trusted public
  keys out-of-band. The `kid` header selects a key; key rotation must allow
  old and new public keys to overlap for at least the maximum JWT lifetime.
- The backend must not trust client-supplied identity fields as HA identity.
  Only a verified proxy JWT establishes the game UUID for a pending session.
- Direct client access is a backend deployment decision and is outside this
  contract.

## Game TCP preamble

After the TLS handshake, the proxy writes exactly 24 bytes before any Rusted
Warfare bytes:

| Offset | Size | Encoding | Meaning |
| --- | ---: | --- | --- |
| 0 | 4 | ASCII | Magic `CASC` |
| 4 | 2 | unsigned big-endian | Protocol version, `1` |
| 6 | 2 | unsigned big-endian | Header size, `24` |
| 8 | 16 | bytes | Cryptographically random UUIDv4 session ID |

The backend rejects unknown magic, version, or header length and closes the
connection. Once accepted, the backend creates a pending session keyed by
session ID and starts the 5-second identity-binding deadline. Bytes following
the preamble are an unmodified native Rusted Warfare TCP stream.

The proxy selects a backend using only a room code present in the client's
initial `Preregister` packet. If no code is present, it uses the configured
default backend. A room code submitted later in a prompt is handled by that
selected backend; an established stream is never moved between backends.

## Session state machine

1. `PENDING`: backend accepted the preamble and waits for identity binding.
2. `BOUND`: backend verified and atomically consumed a valid JWT for the
   pending session. The backend may now accept the player's native game
   protocol traffic.
3. `CLOSED`: the connection ended, binding timed out, or a fatal protocol
   error occurred.

The backend may answer the initial `Preregister` while the session is pending,
but it must not accept registration, add the player to a room, or process game
commands before entering `BOUND`. The proxy binds identity after inspecting
`RegisterPlayer` and forwards that packet only after the identity API succeeds.
Pending sessions expire 5 seconds after preamble acceptance. Closing the TCP
connection at any point closes the associated pending or bound session.

## Identity JWT

The proxy signs a compact JWS JWT using EdDSA (Ed25519). Required protected
header:

```json
{"alg":"EdDSA","kid":"proxy-key-2026-01","typ":"JWT"}
```

Required claims:

| Claim | Type | Meaning |
| --- | --- | --- |
| `iss` | string | Configured proxy issuer |
| `aud` | string | Exact backend ID receiving this session |
| `sid` | string | Session ID from the TCP preamble |
| `sub` | string | Game UUID from the RW `RegisterPlayer` packet |
| `iat` | integer | Unix issue time |
| `exp` | integer | Expiry, no more than 30 seconds after issue |
| `jti` | string | Unique cryptographically random token ID |

The JWT must not contain HA UUID, HA login state, credentials, or authorization
policy. The backend validates signature, key ID, issuer, audience, session ID,
subject, issue/expiry times, and token ID. A token ID can only be consumed once.

## HTTP API

Base path: `/v1`. Bodies and responses use `application/json`. The OpenAPI
document `openapi/cascade-backend-v1.yaml` is the machine-readable companion.

- `GET /health`: readiness of the backend control and game services.
- `GET /rooms`: complete room ownership snapshot for this backend.
- `PUT /sessions/{sessionID}/identity`: bind the signed JWT to a pending
  session.
- Room management endpoints support the proxy's existing room API facade:
  retrieve room list/detail, create/delete room, update settings, kick/ban a
  player, and retrieve room players.

The backend must make `PUT .../identity` idempotent for the exact same
sessionID and jti. A retry with a different identity or jti for an already
bound session fails with `SESSION_IDENTITY_CONFLICT`.

## Room snapshot

`GET /rooms` returns all rooms currently owned by this backend:

```json
{
  "backendId": "rw-backend-a",
  "generatedAt": "2026-10-24T00:00:00Z",
  "rooms": [
    {
      "code": "NA1234",
      "title": "Lobby",
      "mapName": "maps/standard.tmx",
      "maxPlayers": 8,
      "players": 2,
      "inGame": false,
      "ownerId": "game-player-uuid",
      "ownerName": "Host",
      "adminCount": 1,
      "createdAt": "2026-10-24T00:00:00Z"
    }
  ]
}
```

The `rooms` property is always an array, including when the backend owns no
rooms. Each room snapshot item includes the legacy room-summary fields shown
above; room detail responses also include a `members` array.

The proxy polls every 5 seconds and uses the latest successful snapshot for
routing and list aggregation. Snapshots are authoritative per backend; a room
code claimed by multiple backends is a conflict and must not be routed.
After proxy restart, the directory is rebuilt from backend snapshots.

## Errors and retries

Errors use this shape:

```json
{"code":"SESSION_NOT_FOUND","message":"session is not pending"}
```

Stable error codes:

- `INVALID_REQUEST`
- `UNSUPPORTED_VERSION`
- `SESSION_NOT_FOUND`
- `SESSION_EXPIRED`
- `SESSION_IDENTITY_INVALID`
- `SESSION_IDENTITY_REPLAYED`
- `SESSION_IDENTITY_CONFLICT`
- `ROOM_NOT_FOUND`
- `ROOM_CONFLICT`
- `BACKEND_NOT_READY`
- `INTERNAL_ERROR`

The proxy retries only idempotent control requests, at most twice after the
initial attempt, with bounded backoff. It does not retry room creation unless
the backend supports an idempotency key. If identity binding cannot be
confirmed within the 5-second pending deadline, the proxy closes the game
connection and must not forward the client's pending PoW response. Client
errors must not expose tokens, certificates, or internal backend details.

## Health and directory behavior

- The proxy probes each backend every 5 seconds.
- Three consecutive failures mark a backend unhealthy; two consecutive
  successes restore health.
- A backend health failure closes its active client streams. Existing room
  ownership is retained and is not reassigned automatically.
- The proxy routes an exact known room to its snapshot owner. An unknown room
  uses the longest matching configured static prefix; otherwise it uses the
  default backend.
- Configured prefixes for different backends must not overlap.
- Duplicate room ownership is reported as a conflict and rejected for
  routing.
