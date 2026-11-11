# 0008 — Out-of-Process Plugins (cascade-plugin/v1)

- **Status**: accepted

## Context

Cascade had one in-process extension seam (the MCA assignment router,
see 0002/0007 and the `mca` config block) but no plugin system. The author's
stance, informed by the competitive survey
(`/home/lnb/Desktop/HA/rw/PLUGIN-SYSTEM-SURVEY-20261107.md` [xref]) and by
Avalon's ADR-0006 (`/home/lnb/Desktop/avalon/Avalon/knowledge/0006` [xref]):
extension surface lives in the governance plane, plugins are **independent
processes**, the host never executes plugin code. All three surveyed
in-process plugin ecosystems (RW-HPS/Rukkit/IronCore) froze or died; the
process-isolated direction is where the ecosystem converged.

Cascade-specific facts that shape the design:

- The proxy is stateless per connection and latency-sensitive; a plugin
  failure must never wedge a player (the lobby is always a valid fallback).
- Assignment routing (MCA) already produces a decision point right before
  the 178 jump — the natural veto seam.
- Cascade has no console; observability is logs (the HTTP surface is under
  the `cascade-proxy.yaml` contract, so no new endpoint was added here).

## Decision

Implement an out-of-process plugin host in `internal/plugin`, mechanically
isomorphic to Avalon's (`internal/plugin` [xref] in the Avalon repo):

- Transport: HTTP + JSON, **Cascade is the caller**; each plugin is an HTTP
  server at an operator-declared base URL. Go standard library only.
- Manifest negotiation: `internal/plugin/manifest.go::fetchManifest` —
  `GET {base}/v1/manifest`, signed both ways; `schema`/`protocol` must match
  `internal/plugin/plugin.go::ManifestSchema`, `internal/plugin/plugin.go::ProtocolV1`
  exactly (the Avalon dialect is rejected and vice versa). Per-event version
  intersection; `veto` capability declared per event.
- Authentication: bidirectional HMAC-SHA256
  (`internal/plugin/signature.go::signRequest`, `internal/plugin/signature.go::verifyResponse`),
  headers `X-Cascade-Timestamp` / `X-Cascade-Signature`, signature covers
  `"<unix sec>.<raw body>"`, ±300s skew, hex secret ≥ 16 bytes, secret list
  for rotation overlap.
- Manifest file: `Plugins.json` in the working directory, loaded by
  `internal/plugin/config.go::LoadConfigFile`. Missing/empty = defaults with
  zero entries (zero regression). Invalid = startup failure (Cascade config
  discipline; no silent degradation).
- Runtime: `internal/plugin/hub.go::New` — one delivery worker per plugin
  (ordering), boot negotiation in parallel (unreachable plugins retry in the
  background, 5s→5min backoff), connection-level failures drop back to
  renegotiation.

### Event catalog (v1)

Declared in `internal/plugin/plugin.go::EventPlayerRegistered`,
`EventPlayerRoute`, `EventPlayerRouted`:

| Event | Tier | Vetoable |
|---|---|---|
| `player.registered` | observe | no |
| `player.route` | dual-mode | yes (grant + declaration required) |
| `player.routed` | observe | no |

Payloads: `internal/plugin/event.go::PlayerInfo`, `internal/plugin/event.go::RouteDecision`
(envelope identical in shape to Avalon's: `eventId` ULID, `event`,
`eventVersion`, `occurredAt`, host/player blocks). Full field tables in
`docs/plugins/EVENT-CATALOG.md`.

### Tiers and failure semantics

- observe: in-memory bounded queue (`internal/plugin/queue.go`), best-effort,
  drop on failure, no retry, drop-oldest when full.
- veto (`internal/plugin/hub.go::AdmitPlayerRoute`): parallel consult of
  ready + granted + declared plugins; any valid deny wins (reasons
  concatenated, sanitized to 200 runes, plugin-name prefixed); timeout /
  unreachable / malformed verdict / deny-without-reason all **fail open**
  and are counted. At most once, never retried.
- No deliver/WAL tier yet: Cascade has no event that must survive plugin
  downtime (the only async flow today is MCA routing itself, which is
  host-side). Adding a WAL tier = Avalon `outbox.go` [xref] shape.

### Wiring into the proxy

- `internal/proxy/session.go::emitPlayerRegistered` — after identity binding.
- `internal/proxy/mcaroute.go::routeWithAdmission` — before the 178 jump:
  veto can block an MCA route (player stays in the lobby with a readable
  localized reason); allowed decisions additionally get an observe copy
  (same `eventId`) and `player.routed` after the jump packet is written.
- Plugin state reaches the hub via `internal/app/app.go` (`pluginsHub`);
  shutdown prints per-plugin counters (`Hub.Close`).

## Consequences

- Plugins are deployable/replaceable independently of Cascade; a crashed or
  hung plugin degrades to fail-open routing and observe drops — the player
  chain is never blocked (verified by unit tests and stack drills:
  crash / timeout / deny).
- The in-process MCA router stays the decision producer; plugins participate
  at the decision boundary (veto/observe), not inside the query path.
- Observability is log-based until `HA-Contract` registers a status endpoint
  (the `cascade-proxy.yaml` surface is contract-gated; adding `/api/plugins`
  requires contract registration first — tracked as a gap, not silently
  bypassed).
- Contract registration: `cascade-plugin-v1` belongs in HA-Contract next to
  `avalon-plugin-v1.yaml` [xref]; until then `docs/plugins/` is the
  authoritative field-level spec.

## Evidence

- `internal/plugin/hub.go::New`, `internal/plugin/hub.go::admitCore`, `internal/plugin/hub.go::enqueueEvent`
- `internal/plugin/config.go::LoadConfigFile`, `internal/plugin/config.go::Entry`
- `internal/plugin/hub_test.go` (negotiation, observe drop, veto allow/deny,
  fail-open matrix incl. timeout/unsigned/malformed, queue drop-oldest,
  secret rotation, config validation)
- `internal/plugin/template_e2e_test.go` (template compiled to a real
  process: allow / deny / secret-mismatch paths)
- `internal/proxy/pluginroute_test.go` (veto blocks the 178 jump; allow
  proceeds and delivers observe copies)
- `docs/plugins/template` (route-watcher reference plugin with
  `-deny` / `-hang-ms` / `-crash-after` drill switches)
