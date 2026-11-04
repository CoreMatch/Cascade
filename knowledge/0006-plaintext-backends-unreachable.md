# 0006 — Transport Follows the Backend's Declared Trust Strategy

- **Status**: accepted
- **Date**: 2026-11-04 (gap recorded as `proposed` under the title "Proxy
  Transport Is TLS-Only: Plaintext Backends Are Unreachable"); approved by
  the author and implemented 2026-11-05

## Context

Since contract v1.1, **transport and trust are separate decisions on the
backend** (HA-Contract `Cascade-Backend-v1.md`, "Transport and trust"
section): a backend may serve TLS or plaintext on the game endpoint and the
control plane, and trust is chosen independently from `open` / `ip` /
`pinned` ("Backend trust strategies" section). Under `open` and `ip`, the
backend performs no client-certificate checks at all, so plaintext transport
combined with `open` or `ip` trust is a legitimate contract-level
combination that needs no certificate material on either side.

The proxy, however, was built against contract v1, when mutual TLS plus
Ed25519 signature verification was mandatory, and had not been relaxed since
(see 0004). The reference backend (RW-HPS Go Server) defaults its trust
strategy to `open` and serves plaintext unless server certificates are
configured (`internal/net/gateway/trust.go`, DECISIONS D-24), so the two
sides were mismatched by default: the proxy insisted on mTLS toward a
backend that checks nothing. Author approval to fix this was granted
2026-11-05; contract v1.2 was issued first (contract-first), then this
implementation followed.

## Decision

**Per-backend declaration drives the transport.** Each configured backend
carries two fields (`internal/config/config.go:20-32`):

- `trust_mode` — the trust strategy the operator declares for that backend
  (`open`, `ip`, or `pinned`), mirroring the backend's own configuration.
- `tls_mode` — the transport selection: `auto` (default; empty is
  equivalent), `mtls`, or `plain`.

`ResolveTransport` (`internal/backend/transport_mode.go:56`) derives the
transport:

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
transport** — TLS with a client certificate — per contract v1.2 ("Proxy
transport selection"): the proxy fails closed and never falls back to
plaintext silently.

**Transport constrains the control URL scheme** (`client.go:95-143`): mTLS
requires `https`, plaintext requires `http`; mismatches are startup errors
whose messages name the declared fields. Plaintext backends need no
certificate material at all; mTLS backends still require the client
certificate, key, and CA pool (`client.go:145-174`), and the game dial uses
`tls.Dialer` or the plain `net.Dialer` accordingly (`client.go:177-189`).
The resolved transport is reported per backend in `/api/backends`
(`client.go:67`).

**Identity semantics are unchanged.** The 24-byte `CASC` preamble is still
written first on every game connection regardless of transport
(`internal/proxy/server.go:153-167`); the proxy still signs Ed25519 JWTs on
every bind (`internal/proxy/token.go`); only `Preregister` passes before a
session is bound (`internal/proxy/session.go:108-110`); bind failures are
still recorded by `logBindFailure` at error level without the JWT
(`session.go:155-168`). The one-time `jti` ledger is the backend's and is
independent of transport.

**Contract**: HA-Contract v1.2 (merge `6892fc4`, content `c7f8aff`) defines
the selection rule, the fail-closed default, the override, and the
plaintext consequences as proxy behavior; the OpenAPI companion was bumped
to 1.2.0 for traceability only (no endpoint, field, or enum changes — the
declaration is proxy-side configuration, not wire content).

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
  `status` as of 2026-11-05), so a wrong declaration is possible. The
  fail-closed default bounds the damage: an undeclared plaintext backend is
  unreachable (visible startup/dial failure) rather than silently trusted;
  an undeclared TLS backend works as before. A deployment whose backend
  serves TLS while declaring `open`/`ip` must set `tls_mode: mtls`.
- Startup validation became scheme- and declaration-aware; error messages
  must keep naming both fields so misconfigurations are diagnosable.

Benefits:

- The two contract-valid combinations (plaintext + `open`, plaintext + `ip`)
  are reachable; open/ip deployments no longer generate or mount client
  certificates, keys, and CAs that the backend never checks (the Ed25519
  signing key is still required — the proxy always signs JWTs).
- Behavior now matches contract v1.2 and the RW-HPS Go Server defaults.

## Evidence

- `internal/backend/transport_mode.go:22-97` (TransportMode, derivation
  table, fail-closed default, plain/pinned conflict),
  `internal/backend/client.go:95-143` (NewClient: resolution, scheme
  validation, material only for mTLS), `:177-189` (DialGame plain vs TLS),
  `internal/config/config.go:20-32` (`trust_mode`/`tls_mode` fields),
  `internal/backend/client.go:67` + `directory.go:288-295` (resolved
  transport in `/api/backends`). Commits `cfa374b` (implementation),
  `193de11` (config.example.yaml, README).
- HA-Contract repository: `docs/dev/Cascade/Cascade-Backend-v1.md` v1.2
  ("Proxy transport selection", Changelog v1.1.1 → v1.2), merge `6892fc4`;
  `docs/api/openapi/cascade-backend-v1.yaml` `info.version: 1.2.0`.
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
  The plaintext variant test lived in a throwaway Go-Server copy and is not
  committed to either repository; reproduce with: copy RW-HPS so that
  `Go-Server/internal/net/gateway` is 4 levels below the tree root, symlink
  `Helper` and `data` next to `Go-Server`, add a clone of
  `cascade_e2e_test.go` whose gateway config omits `TLSCert`/`TLSKey` and
  whose Cascade config uses `http://` control URL + `trust_mode: open`,
  then `RWHPS_CASCADE_SRC=<cascade checkout> go test ./internal/net/gateway/
  -run TestCascadePlainTransportE2E`.
- RW-HPS Go Server: `internal/net/gateway/trust.go` (trust tiers; `""` ≡
  `open`), `internal/net/gateway/gateway.go:125-153` (server TLS only when
  certificates are configured), `internal/app/cascade.go:26-88` (gateway
  assembly), README "Cascade 边缘代理模式" (D-24).
- Cross-references: 0004 (the preamble and passthrough are unchanged; its
  "always mutual TLS" transport statement is superseded here), 0003
  (identity chain, transport-independent).
