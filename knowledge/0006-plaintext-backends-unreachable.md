# 0006 — Proxy Transport Is TLS-Only: Plaintext Backends Are Unreachable

- **Status**: proposed (not approved, not implemented)
- **Date**: 2026-11-04

## Context

Since contract v1.1, **transport and trust are separate decisions on the
backend** (HA-Contract `Cascade-Backend-v1.md`, "Transport and trust"
section): a backend may serve TLS or plaintext on the game endpoint and the
control plane, and which one applies is a property of the backend
deployment, outside the contract. Trust is chosen independently from
`open` / `ip` / `pinned` ("Backend trust strategies" section). Under `open`
and `ip`, the backend performs no client-certificate checks at all, so a
backend deployment combining **plaintext transport with `open` or `ip`
trust is a legitimate contract-level combination** that needs no certificate
material on either side.

The proxy, however, was built against contract v1, when mutual TLS plus
Ed25519 signature verification was mandatory, and has not been relaxed since
(see 0004). Author confirmation (2026-11-04): there are **no plans to relax
the TLS requirement — because it had not been realized that it should be
relaxed**. This entry records the resulting structural mismatch so the
decision can be revisited explicitly.

## Decision

Record the gap as it stands, and propose a direction. Neither is approved or
implemented.

**Current state (the gap).** The proxy is TLS-only toward every backend:

- `control_url` must be an absolute `https` URL
  (`internal/backend/client.go:91-94`).
- `makeTLSConfig` requires a client certificate, key, and CA pool and
  refuses to start without them (`client.go:118-146`); configuration and
  documentation treat all three material paths as mandatory
  (`README.md:9,32`).
- The game connection dials through `tls.Dialer` with the same
  configuration (`client.go:148-157`).

Consequently, a backend deployed with plaintext transport — with `open` or
`ip` trust, as the contract explicitly allows — **cannot be reached by the
current proxy**: no non-`https` control URL is accepted, there is no
TLS-less game dial, and startup fails without certificate material. Two
valid contract-level combinations (plaintext + `open`, plaintext + `ip`)
are unreachable in practice.

**Optional direction.** Let the proxy choose the transport per backend from
a backend-declared setting — "TLS + preamble" or "plaintext + preamble".
The 24-byte `CASC` preamble is unaffected: the contract requires it after
transport is established, "TLS handshake where TLS applies"
(`Cascade-Backend-v1.md`, "Game TCP preamble" section), so it applies to
plaintext connections unchanged. The identity JWT flow (0003) is likewise
independent of transport.

## Consequences

Costs and limitations of the proposed direction, if it were ever taken:

- It touches the transport layer and the connection-establishment path:
  `NewClient` / `makeTLSConfig` / `DialGame` in
  `internal/backend/client.go`, the backend configuration schema (a
  per-backend transport declaration), startup validation, and the mTLS test
  suite (`internal/backend/mtls_test.go`,
  `internal/proxy/session_integration_test.go`).
- Plaintext removes peer authentication and confidentiality entirely; under
  `open` trust there would be no remaining authenticity check on the game
  stream (structural JWT checks and the one-time `jti` ledger still hold,
  but nothing authenticates the peer). It should therefore remain a
  deliberate, explicit opt-in per backend, never a default.

Cost of the status quo:

- Deployments that would legitimately run plaintext (e.g. a backend on a
  trusted private network using `ip` trust) are forced to generate and
  mount client certificates, keys, a CA, and an Ed25519 signing key that
  the backend never checks.

This entry is `proposed`: the direction above is **not approved by the
author and not implemented**. As of 2026-11-04 the proxy remains TLS-only
by implementation, and the mismatch with the contract's transport/trust
separation is recorded, not resolved.

## Evidence

- `internal/backend/client.go:91-94` (https-only control URL),
  `:118-146` (`makeTLSConfig`: certificate, key, and CA required; key
  permission check; TLS >= 1.2), `:148-157` (`DialGame` via `tls.Dialer`).
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`:
  "Transport and trust" (transport is a backend deployment property; TLS or
  plaintext), "Backend trust strategies" (`open` performs no peer
  authentication; `ip` checks only source addresses), "Game TCP preamble"
  (preamble applies after transport is established, "TLS handshake where
  TLS applies"), "Changelog" v1 → v1.1 (trust strategies replaced the
  mandatory mutual-TLS + signature-verification requirement).
- `README.md:9,32` — mTLS material is mandatory in configuration and
  documentation.
- Author confirmation (2026-11-04): no relaxation plans exist because the
  need for relaxation had not been realized; this entry records the gap.
- Cross-references: 0004 (current transport decision), 0003 (identity flow
  independent of transport).
