# 0002 — Room-Code Routing: Snapshot, Prefix, Default

- **Status**: accepted
- **Date**: 2026-11-04 (routing introduced in commit `51e81b8`, 2026-10-25)

## Context

Multiple independently operated backends can sit behind one Cascade instance.
A client's first packet may name the room it wants to join, and the proxy
must pick exactly one backend **before** any game byte is forwarded. The
proxy keeps no room state of its own: rooms are created, listed, and destroyed
by backends, and a restarted proxy must be able to route correctly again
without persistent storage.

## Decision

Routing is decided once per connection, from the client's initial
`Preregister` packet, in this order:

1. **First packet gate.** The first packet on a new game connection must be
   `PreregisterReceive` (type 160); anything else closes the connection
   (`internal/proxy/server.go:136-142`, `internal/protocol/types.go:28`).
   Its `Query` field — the room code — is present from packet version 2 on
   (`internal/protocol/codec.go:378-414`).
2. **No code → default backend.** An empty code routes straight to the
   configured `default_backend_id` (`internal/backend/directory.go:260-265`;
   `default_backend_id` is mandatory, `directory.go:41-43`).
3. **Exact room match from snapshots.** Each backend's room ownership is
   polled via `GET /v1/rooms` every 5 seconds by default
   (`room_snapshot_interval_sec`, `internal/config/config.go:209`;
   fallback default in `directory.go:53-55`). A room code found in exactly
   one backend's latest snapshot routes to that backend
   (`directory.go:224-234`).
4. **Longest matching static prefix.** Unknown codes fall back to the
   longest configured `prefixes` entry that matches
   (`directory.go:235-246`). Prefixes of different backends must not
   overlap; this is validated at startup (`directory.go:78-96`).
5. **Default backend.** If nothing matches, the default backend takes the
   connection (`directory.go:247-249`).

Health gating is folded into every route decision: probes run every 5
seconds, three consecutive failures mark a backend unhealthy, two
consecutive successes restore health (`directory.go:154-169`), and routing
to an unhealthy backend fails with `ErrBackendDown`
(`directory.go:253-256`).

**Room-code ownership belongs to the backend, not the proxy.** Snapshots are
authoritative per backend; a room code claimed by multiple backends is a
conflict and is not routed (`ErrRoomConflict`, `directory.go:16-17,
228-230`; contract: "Snapshots are authoritative per backend; a room code
claimed by multiple backends is a conflict and must not be routed",
HA-Contract `Cascade-Backend-v1.md`, "Room snapshot" section). The rationale
follows from the ownership split in 0001: because backends own room and game
state, only a backend's own snapshot can say where a room lives. Keeping the
proxy stateless means any proxy instance — including a freshly restarted one
— rebuilds the full routing table from backend snapshots alone (contract:
"After proxy restart, the directory is rebuilt from backend snapshots").

An established stream is **never moved between backends**; a room code
submitted later (e.g. via a relay prompt) is handled by the backend the
connection was first routed to (contract, "Game TCP preamble" section).

## Consequences

Costs and limitations:

- Routing can lag backend reality by up to one snapshot interval (5 s by
  default): a freshly created room may briefly be routed by prefix or
  default instead of exact ownership.
- Conflicting room ownership is detected and surfaced via
  `GET /api/backends` (`directory.go:283-320`) but never resolved
  automatically; operators must fix the backend configuration or the
  backends themselves.
- Rooms that no backend reports simply do not exist for routing purposes;
  there is no proxy-side room cache to mask backend outages. An unhealthy
  owner means `ErrBackendDown`, not a failover of its rooms (contract:
  "Existing room ownership is retained and is not reassigned automatically").

Benefits:

- One decision per connection keeps the forwarding path simple and makes the
  "never move streams" guarantee trivial to hold.
- Prefixes give operators deterministic steering for codes that no backend
  owns yet (e.g. regional naming schemes) without any proxy state.

## Evidence

- `internal/proxy/server.go:131-175` — `handleConnection`: first-packet
  gate, `RouteInitial`, session ID generation, preamble write, raw
  forwarding of the first packet.
- `internal/protocol/types.go:28-29` — `TypePreregisterReceive = 160`,
  `TypePreregister = 161`.
- `internal/protocol/codec.go:378-414` — `DecodePreregister`; `Query`
  parsed when packet version >= 2.
- `internal/backend/directory.go:40-76` (construction, mandatory default),
  `:78-96` (prefix overlap validation), `:98-125` (health + snapshot
  tickers), `:154-169` (3-failure/2-success health rule), `:171-198`
  (snapshot refresh), `:220-258` (`RouteRoom` precedence), `:260-265`
  (`RouteInitial`).
- `internal/config/config.go:208-210` — defaults `health_interval_sec: 5`,
  `room_snapshot_interval_sec: 5`, `pending_session_timeout_sec: 5`.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`:
  "Game TCP preamble" (routing from the initial Preregister only; streams
  never moved), "Room snapshot" (5-second polling, snapshot authority,
  conflict rule, rebuild after restart), "Health and directory behavior"
  (probe cadence, health thresholds, no automatic reassignment).
- `README.md:44` — operator-facing statement of the same precedence.
- Commit `51e81b8` — introduction of `internal/backend/directory.go`.
