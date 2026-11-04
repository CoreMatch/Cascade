# 0004 — mTLS + 24-Byte CASC Preamble + Byte Passthrough

- **Status**: accepted
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
(`internal/backend/transport.go:13-50`):

| Offset | Size | Content |
| --- | ---: | --- |
| 0 | 4 | ASCII magic `CASC` |
| 4 | 2 | protocol version `1` (big-endian) |
| 6 | 2 | header size `24` (big-endian) |
| 8 | 16 | UUIDv4 session ID |

The session ID is generated with `crypto/rand` and forced to UUIDv4
(`transport.go:20-29`); encoding validates the UUID version bits
(`transport.go:31-50`). This layout matches the contract's preamble table
exactly ("Game TCP preamble" section).

**Byte passthrough.** After the preamble, both directions forward raw,
unmodified frames: the proxy reads packets with their raw bytes preserved
and writes those raw bytes onward (`internal/proxy/session.go:93` for
client→backend, `:112` for backend→client). The only packets the proxy
inspects are those it must act on (identity gate in 0003, chat commands for
HRPAuth login/claim at `session.go:90-92`).

**Proxy-side transport is always mutual TLS.** Toward every backend, both
the `/v1` control plane and the game TCP dial use TLS with a client
certificate: `control_url` must be an absolute `https` URL
(`internal/backend/client.go:91-94`); the TLS configuration requires a
client certificate, key, and CA pool, rejects group/other-readable private
keys, and enforces TLS >= 1.2 (`client.go:118-146`); the game connection
dials with the same configuration (`client.go:148-157`). Configuration
refuses to start without all three material paths (`README.md:9,32`).

**Interplay with the backend trust strategies.** The proxy unconditionally
presents its client certificate and signs identity JWTs regardless of which
strategy the backend runs; the strategies differ only in what the backend
checks (contract, "Backend trust strategies"):

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
signature verification) and has not been relaxed since: it still always
provisions mTLS material and still signs JWTs. That is compatible with all
three strategies — contract v1.1 explicitly notes the proxy "still signs
JWTs and may still present client certificates — backends without
fingerprint checks simply do not require them" (Changelog, v1 → v1.1).

## Consequences

Costs and limitations:

- The proxy is **over-provisioned under `open` and `ip` backends**:
  deployments must generate and mount client certificates, keys, CAs, and an
  Ed25519 signing key even when the backend checks none of them. Author
  confirmation (2026-11-04): there are no plans to relax this, because it
  had not been realized that relaxation should be considered; the resulting
  structural mismatch with the contract's transport/trust separation is
  recorded in 0006 (proposed, not implemented).
- The preamble is a hard compatibility boundary: unknown magic, version, or
  header size makes the backend close the connection (contract). The magic
  rename `MCAG` → `CASC` in `69faeb7` therefore invalidates old backends
  (flagged as BREAKING CHANGE in that commit; upgrade note in `README.md:42`).
- Passthrough means the proxy cannot inspect, filter, or rate-limit game
  payloads; connection-level limits (`max_connections`, read timeouts) are
  the only controls (`internal/proxy/server.go:84-99`).

Benefits:

- The preamble gives the backend a session key before the first game byte,
  which is what makes the PENDING/BOUND state machine of 0003 possible.
- Byte-level fidelity means backend protocol upgrades do not require proxy
  changes as long as the framing used for interception (packet type/length)
  stays compatible.

## Evidence

- `internal/backend/transport.go:13-18` (constants: size 24, version 1,
  magic `CASC`), `:20-29` (UUIDv4 generation), `:31-50` (encoding with
  UUID version validation), `:67-88` (decoding).
- `internal/proxy/server.go:153-167` — session ID generation, preamble
  write, then raw forwarding of the first packet.
- `internal/proxy/session.go:93,112` — raw passthrough in both directions.
- `internal/backend/client.go:91-94` (https-only control URL),
  `:118-146` (`makeTLSConfig`: cert/key/CA required, key permission check,
  TLS >= 1.2), `:148-157` (`DialGame` with the same TLS config).
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`:
  "Transport and trust", "Backend trust strategies" (open/ip/pinned,
  `FORBIDDEN`), "Game TCP preamble" (layout table, rejection behavior),
  "Changelog" v1 → v1.1 (trust strategies replace mandatory mTLS +
  signature verification).
- Commits: `51e81b8` (transport introduced), `69faeb7` (magic `MCAG` →
  `CASC`, BREAKING CHANGE), `7750528` (in-repo contract copies removed once
  HA-Contract became authoritative).
- `README.md:9,32,42` — mandatory mTLS material, permission requirements,
  MCAGS-RW upgrade note.
