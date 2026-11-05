# 0004 — mTLS + 24-Byte CASC Preamble + Byte Passthrough

- **Status**: accepted
- **Date**: 2026-11-04 (introduced in commit `51e81b8`, 2026-10-25)

## Context

The proxy terminates client TCP connections and opens its own TCP
connections to backends. Two problems had to be solved at the transport
level: (1) the backend must be able to correlate a raw game TCP stream with
the control-plane identity bind (see 0003) before any Rusted Warfare bytes
arrive; (2) the proxy must forward the native game protocol without
interpreting it, because game protocol behavior is owned by the backend
(see 0001). Separately, the contract separates **transport** (TLS or
plaintext is a backend deployment property) from **trust** (how the backend
authenticates the proxy) and defines three backend-selected trust
strategies: `open`, `ip`, and `pinned` (HA-Contract `Cascade-Backend-v1.md`,
"Transport and trust" / "Backend trust strategies", v1.2.1).

## Decision

**24-byte `CASC` preamble.** After the transport is established, the proxy
writes exactly 24 bytes before any Rusted Warfare bytes
(`internal/backend/transport.go::EncodePreamble`):

| Offset | Size | Content |
| --- | ---: | --- |
| 0 | 4 | ASCII magic `CASC` |
| 4 | 2 | protocol version `1` (big-endian) |
| 6 | 2 | header size `24` (big-endian) |
| 8 | 16 | UUIDv4 session ID |

The session ID is generated with `crypto/rand` and forced to UUIDv4
(`internal/backend/transport.go::NewSessionID`); encoding validates the
UUID version bits (`internal/backend/transport.go::EncodePreamble`). This
layout matches the contract's preamble table exactly ("Game TCP preamble"
section).

**Byte passthrough.** After the preamble, both directions forward raw,
unmodified frames: the proxy reads packets with their raw bytes preserved
and writes those raw bytes onward
(`internal/proxy/session.go::clientToBackend` for client→backend,
`internal/proxy/session.go::backendToClient` for backend→client). The only
packets the proxy inspects are those it must act on (identity gate in 0003,
chat commands for HRPAuth login/claim at
`internal/proxy/session.go::handleAuthChat`).

**Interplay with the backend trust strategies.** The proxy presents its
client certificate only toward backends resolved to mTLS; plaintext backends
receive none. Identity JWTs are signed unconditionally on every bind,
regardless of which strategy the backend runs; the strategies differ only
in what the backend checks (contract, "Backend trust strategies"):

- `open` — backend checks neither certificate nor JWT signature; structural
  claim checks and the one-time `jti` ledger still apply.
- `ip` — backend checks the proxy's source address against an allowlist; no
  JWT signature verification.
- `pinned` — backend additionally requires a client certificate whose
  SHA-256 fingerprint over the certificate DER is in an accepted set
  (multiple fingerprints allowed for rotation). Mismatch: game connections
  are closed before the preamble is read; control-plane requests get `403`
  with code `FORBIDDEN`.

The per-backend transport selection — which backends are reached over mTLS
and which over plaintext — is defined in 0006.

## Consequences

Costs and limitations:

- The preamble is a hard compatibility boundary: unknown magic, version, or
  header size makes the backend close the connection (contract).
- Passthrough means the proxy does not interpret game payloads beyond the
  packets it must act on: the identity gate (0003) and HRPAuth chat
  commands. Recognized chat commands (`/login`, `/claim`, `/whoami`,
  `/lang`) are checked and rate-limited per player
  (`internal/config/config.go (field: chat_rate_limit)`,
  `internal/proxy/session.go::handleAuthChat`,
  `internal/proxy/session.go::allowCommand`); every other payload is
  forwarded unmodified. Connection-level limits (`max_connections`, read
  timeouts) bound the load
  (`internal/proxy/server.go::acquireConnection`).

Benefits:

- The preamble gives the backend a session key before the first game byte,
  which is what makes the PENDING/BOUND state machine of 0003 possible.
- Byte-level fidelity means backend protocol upgrades do not require proxy
  changes as long as the framing used for interception (packet type/length)
  stays compatible.

## Evidence

- `internal/backend/transport.go::PreambleSize`,
  `internal/backend/transport.go::PreambleVersion`,
  `internal/backend/transport.go::preambleMagic` (constants: size 24,
  version 1, magic `CASC`),
  `internal/backend/transport.go::NewSessionID` (UUIDv4 generation),
  `internal/backend/transport.go::EncodePreamble` (encoding with UUID
  version validation), `internal/backend/transport.go::ReadPreamble`
  (decoding).
- `internal/proxy/server.go::handleConnection` — session ID generation,
  preamble write, then raw forwarding of the first packet.
- `internal/proxy/session.go::clientToBackend`,
  `internal/proxy/session.go::backendToClient` — raw passthrough in both
  directions.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`:
  "Transport and trust", "Backend trust strategies" (open/ip/pinned,
  `FORBIDDEN`), "Game TCP preamble" (layout table, rejection behavior).
- Commits: `51e81b8` (transport introduced).
