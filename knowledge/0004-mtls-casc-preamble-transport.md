# 0004 — mTLS + 24-Byte CASC Preamble + Byte Passthrough

- **Status**: accepted (preamble and passthrough current; the "always mTLS"
  transport decision below is superseded by 0006 as of 2026-11-05)
- **Date**: 2026-11-04 (introduced in commit `51e81b8`, 2026-10-25; magic
  renamed `MCAG` → `CASC` in `69faeb7`)

## Context

The proxy terminates client TCP connections and opens its own TCP
connections to backends. Two problems had to be solved at the transport
level: (1) the backend must be able to correlate a raw game TCP stream with
the control-plane identity bind (see 0003) before any Rusted Warfare bytes
arrive; (2) the proxy must forward the native game protocol without
interpreting it, because game protocol behavior is owned by the backend
(see 0001). Separately, contract v1.1 split **transport** (TLS or plaintext
is a backend deployment property) from **trust** (how the backend
authenticates the proxy), defining three backend-selected trust strategies:
`open`, `ip`, and `pinned` (HA-Contract `Cascade-Backend-v1.md`, "Transport
and trust" / "Backend trust strategies").

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

**Proxy-side transport was always mutual TLS.** _(Superseded by 0006,
2026-11-05: transport is now derived per backend from the declared trust
strategy — plaintext for `open`/`ip`, mTLS for `pinned` or when the
declaration is missing/unparseable. The description below is the historical
v1-era behavior.)_ Toward every backend, both the `/v1` control plane and
the game TCP dial used TLS with a client certificate: `control_url` had to
be an absolute `https` URL; the TLS configuration required a client
certificate, key, and CA pool, rejected group/other-readable private keys,
and enforced TLS >= 1.2; the game connection dialed with the same
configuration. Startup refused to start without all three material paths.

**Interplay with the backend trust strategies.** The proxy unconditionally
presents its client certificate and signs identity JWTs regardless of which
strategy the backend runs _(since 0006, 2026-11-05, the client certificate
is presented only toward backends resolved to mTLS; plaintext backends
receive none. JWT signing is still unconditional.)_; the strategies differ
only in what the backend checks (contract, "Backend trust strategies"):

- `open` — backend checks neither certificate nor JWT signature; structural
  claim checks and the one-time `jti` ledger still apply.
- `ip` — backend checks the proxy's source address against an allowlist; no
  JWT signature verification.
- `pinned` — backend additionally requires a client certificate whose
  SHA-256 fingerprint over the certificate DER is in an accepted set
  (multiple fingerprints allowed for rotation). Mismatch: game connections
  are closed before the preamble is read; control-plane requests get `403`
  with code `FORBIDDEN`.

The proxy implementation predates v1.1 (v1 mandated mutual TLS plus Ed25519
signature verification). It originally always provisioned mTLS material and
always signed JWTs; contract v1.1 noted the proxy "still signs JWTs and may
still present client certificates — backends without fingerprint checks
simply do not require them" (Changelog, v1 → v1.1). Since 0006 (2026-11-05,
contract v1.2) the proxy derives the transport from the declared trust
strategy instead of always provisioning mTLS, but it still signs JWTs on
every bind regardless of strategy.

## Consequences

Costs and limitations:

- The proxy **was over-provisioned under `open` and `ip` backends**:
  deployments had to generate and mount client certificates, keys, CAs, and
  an Ed25519 signing key even when the backend checked none of them.
  Resolved by 0006 (accepted 2026-11-05): `open`/`ip` backends now connect
  in plaintext with no certificate material; only the Ed25519 signing key is
  still always required, because the proxy signs JWTs on every bind.
- The preamble is a hard compatibility boundary: unknown magic, version, or
  header size makes the backend close the connection (contract). The magic
  rename `MCAG` → `CASC` in `69faeb7` therefore invalidates old backends
  (flagged as BREAKING CHANGE in that commit; upgrade note in
  `README.md:L40`).
- Passthrough means the proxy cannot inspect, filter, or rate-limit game
  payloads; connection-level limits (`max_connections`, read timeouts) are
  the only controls (`internal/proxy/server.go::acquireConnection`).

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
- `internal/backend/client.go` as of commit `1858901` (time-point anchors;
  the always-mTLS transport was replaced in 0006):
  `internal/backend/client.go:L91-L94 @ 1858901` (https-only control URL),
  `internal/backend/client.go:L118-L146 @ 1858901` (`makeTLSConfig`:
  cert/key/CA required, key permission check, TLS >= 1.2),
  `internal/backend/client.go:L148-L157 @ 1858901` (`DialGame` with the
  same TLS config). Current transport evidence lives in 0006.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`:
  "Transport and trust", "Backend trust strategies" (open/ip/pinned,
  `FORBIDDEN`), "Game TCP preamble" (layout table, rejection behavior),
  "Changelog" v1 → v1.1 (trust strategies replace mandatory mTLS +
  signature verification).
- Commits: `51e81b8` (transport introduced), `69faeb7` (magic `MCAG` →
  `CASC`, BREAKING CHANGE), `7750528` (in-repo contract copies removed once
  HA-Contract became authoritative).
- `README.md` as of commit `1858901` (historical; reworded in 0006's commit
  `193de11`) — mandatory mTLS material, permission requirements, MCAGS-RW
  upgrade note.
