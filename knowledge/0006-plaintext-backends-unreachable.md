# 0006 — Transport Follows the Backend's Declared Trust Strategy

- **Status**: accepted
- **Date**: 2026-11-05

## Context

**Transport and trust are separate decisions on the backend** (HA-Contract
`Cascade-Backend-v1.md`, "Transport and trust" section): a backend may serve
TLS or plaintext on the game endpoint and the control plane, and trust is
chosen independently from `open` / `ip` / `pinned` ("Backend trust
strategies" section). Under `open` and `ip`, the backend performs no
client-certificate checks at all, so plaintext transport combined with
`open` or `ip` trust is a legitimate contract-level combination that needs
no certificate material on either side.

The proxy, however, originally always used mutual TLS toward every backend.
The reference backend (RW-HPS Go Server) defaults its trust strategy to
`open` and serves plaintext unless server certificates are configured
(`/home/lnb/RW-HPS/Go-Server/internal/net/gateway/trust.go::ParseTrustMode`,
DECISIONS D-24), so the two sides were mismatched by default: the proxy
insisted on mTLS toward a backend that checks nothing. This entry records
the fix: transport follows the backend's declared trust strategy.

## Decision

**Per-backend declaration drives the transport.** Each configured backend
carries two fields (`internal/config/config.go::BackendConfig`):

- `trust_mode` — the trust strategy the operator declares for that backend
  (`open`, `ip`, or `pinned`), mirroring the backend's own configuration.
- `tls_mode` — the transport selection: `auto` (default; empty is
  equivalent), `mtls`, or `plain`.

`ResolveTransport` (`internal/backend/transport_mode.go::ResolveTransport`)
derives the transport:

| `tls_mode` | `trust_mode` | Transport |
| --- | --- | --- |
| `auto` / empty | `open` or `ip` | plaintext |
| `auto` / empty | `pinned` | mTLS |
| `auto` / empty | missing | mTLS (safest available) |
| `auto` / empty | unparseable | mTLS + warning log (fail closed) |
| `mtls` | any | mTLS (override) |
| `plain` | `open`/`ip`/missing | plaintext (override) |
| `plain` | `pinned` | startup error (pinned checks TLS client-certificate fingerprints, which plaintext cannot carry) |

Missing or unparseable declarations resolve to the **safest available
transport** — TLS with a client certificate — per contract v1.2.1 ("Proxy
transport selection"): the proxy fails closed and never falls back to
plaintext silently.

**Transport constrains the control URL scheme**
(`internal/backend/client.go::NewClient`): mTLS requires `https`, plaintext
requires `http`; mismatches are startup errors whose messages name the
declared fields. Plaintext backends need no certificate material at all;
mTLS backends still require the client certificate, key, and CA pool
(`internal/backend/client.go::makeTLSConfig`), and the game dial uses
`tls.Dialer` or the plain `net.Dialer` accordingly
(`internal/backend/client.go::DialGame`). The resolved transport is
reported per backend in `/api/backends`
(`internal/backend/client.go::Status`).

**Identity semantics are transport-independent.** The 24-byte `CASC`
preamble is written first on every game connection regardless of transport
(`internal/proxy/server.go::handleConnection`); the proxy signs Ed25519
JWTs on every bind (`internal/proxy/token.go`); only `Preregister` passes
before a session is bound (`internal/proxy/session.go::backendToClient`);
bind failures are recorded by `logBindFailure` at error level without the
JWT (`internal/proxy/session.go::logBindFailure`). The one-time `jti`
ledger is the backend's and is independent of transport.

**Contract**: HA-Contract v1.2.1 defines the selection rule, the
fail-closed default, the override, and the plaintext consequences as proxy
behavior ("Proxy transport selection" section). The declaration is
proxy-side configuration, not wire content.

## Consequences

Costs and limitations:

- **Plaintext transport loses transport confidentiality and peer
  authentication.** Under `open` trust nothing on the path authenticates
  either side; only the structural JWT claim checks and the one-time `jti`
  ledger remain. This is recorded as an accepted consequence of serving
  backends whose trust strategy checks nothing; plaintext is a deliberate
  per-backend choice for trusted networks, never a silent fallback, and the
  README says so.
- **The declaration is operator-maintained.** The backend does not expose
  its trust strategy at runtime (`/v1/health` returns only `backendId` and
  `status`), so a wrong declaration is possible. The
  fail-closed default bounds the damage: an undeclared plaintext backend is
  unreachable (visible startup/dial failure) rather than silently trusted;
  an undeclared TLS backend is reached over mTLS. A deployment whose backend
  serves TLS while declaring `open`/`ip` must set `tls_mode: mtls`.
- Startup validation is scheme- and declaration-aware; error messages name
  both fields so misconfigurations are diagnosable.

Benefits:

- The two contract-valid combinations (plaintext + `open`, plaintext + `ip`)
  are reachable; open/ip deployments need no client certificates, keys, or
  CAs (the Ed25519 signing key is required — the proxy always signs JWTs).
- Behavior matches contract v1.2.1 and the RW-HPS Go Server defaults.

## Evidence

- `internal/backend/transport_mode.go::TransportMode`,
  `internal/backend/transport_mode.go::ResolveTransport` (derivation table,
  fail-closed default, plain/pinned conflict),
  `internal/backend/client.go::NewClient` (resolution, scheme validation,
  material only for mTLS), `internal/backend/client.go::DialGame` (plain vs
  TLS), `internal/config/config.go::BackendConfig` (`trust_mode`/`tls_mode`
  fields), `internal/backend/client.go::Status` +
  `internal/backend/directory.go::Statuses` (resolved transport in
  `/api/backends`). Commits `cfa374b` (implementation), `193de11`
  (config.example.yaml, README).
- HA-Contract repository: `docs/dev/Cascade/Cascade-Backend-v1.md` v1.2.1
  ("Proxy transport selection");
  `docs/api/openapi/cascade-backend-v1.yaml` `info.version: 1.2.1`.
- Tests (all `-race`): `internal/backend/transport_mode_test.go`
  (derivation table), `internal/backend/plaintext_test.go` (open/ip
  plaintext control + game + preamble with no cert material; pinned auto →
  mTLS with client certificate presented; unparseable declaration fails
  closed to mTLS; scheme and conflict rejections; directory startup without
  material), `internal/proxy/session_plaintext_test.go` (full
  identity-binding chain over plaintext), `internal/backend/transport_test.go`
  (preamble rejection: bad magic/version/size/non-UUIDv4/truncated).
- End-to-end against the real RW-HPS Go Server with the Helper.jar engine
  (2026-11-05): mTLS path `TestCascadeRealProxyE2E` PASS (14.3s; no
  declaration → `transport=mtls`, fail closed; routing → bind → real game →
  settlement) and plaintext path `TestCascadePlainTransportE2E` PASS
  (13.4s; gateway with no server certificates, Cascade `trust_mode: open` +
  `tls_mode: auto` → `transport=plain`; Preregister(160)→161, RegisterPlayer
  → bound, real game to settlement). The fake client receives the engine's
  unit-checksum KICK(150), expected anti-cheat behavior (DECISIONS D-16).
  The plaintext variant test belongs to the RW-HPS repository and is parked
  here in committable form: see
  [appendix-plain-transport-e2e.md](appendix-plain-transport-e2e.md) for the
  full source, layout, and reproduction steps.
- RW-HPS Go Server:
  `/home/lnb/RW-HPS/Go-Server/internal/net/gateway/trust.go::ParseTrustMode`
  (trust tiers; `""` ≡ `open`),
  `/home/lnb/RW-HPS/Go-Server/internal/net/gateway/gateway.go::loadServerTLS`
  (server TLS only when certificates are configured),
  `/home/lnb/RW-HPS/Go-Server/internal/app/cascade.go::loadCascadeGateway`
  (gateway assembly), README "Cascade 边缘代理模式" (D-24).
- Cross-references: 0004 (preamble and passthrough), 0003 (identity chain,
  transport-independent).
