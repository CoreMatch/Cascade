# 0002 — Room-Code Routing: Snapshot, Prefix, Default

- **Status**: accepted

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
   (`internal/proxy/server.go::handleConnection`,
   `internal/protocol/types.go::TypePreregisterReceive`).
   Its `Query` field — the room code — is present from packet version 2 on
   (`internal/protocol/codec.go::DecodePreregister`).
2. **No code → default backend.** An empty code routes straight to the
   configured `default_backend_id`
   (`internal/backend/directory.go::RouteInitial`;
   `default_backend_id` is mandatory,
   `internal/backend/directory.go::NewDirectory`).
3. **Exact room match from snapshots.** Each backend's room ownership is
   polled via `GET /v1/rooms` every 5 seconds by default
   (`room_snapshot_interval_sec`,
   `internal/config/config.go (field: room_snapshot_interval_sec)`;
   fallback default in `internal/backend/directory.go::NewDirectory`). A
   room code found in exactly one backend's latest snapshot routes to that
   backend (`internal/backend/directory.go::RouteRoom`).
4. **Longest matching static prefix.** Unknown codes fall back to the
   longest configured `prefixes` entry that matches
   (`internal/backend/directory.go::RouteRoom`). Prefixes of different
   backends must not overlap; this is validated at startup
   (`internal/backend/directory.go::validatePrefixes`).
5. **Default backend.** If nothing matches, the default backend takes the
   connection (`internal/backend/directory.go::RouteRoom`).

Health gating is folded into every route decision: probes run every 5
seconds, three consecutive failures mark a backend unhealthy, two
consecutive successes restore health
(`internal/backend/directory.go::recordHealthResult`), and routing
to an unhealthy backend fails with `ErrBackendDown`
(`internal/backend/directory.go::RouteRoom`).

**Room-code ownership belongs to the backend, not the proxy.** Snapshots are
authoritative per backend; a room code claimed by multiple backends is a
conflict and is not routed
(`internal/backend/directory.go::ErrRoomConflict`; contract: "Snapshots are
authoritative per backend; a room code claimed by multiple backends is a
conflict and must not be routed",
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
  `GET /api/backends` (`internal/backend/directory.go::Statuses`) but never
  resolved automatically; operators must fix the backend configuration or
  the backends themselves.
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

- `internal/proxy/server.go::handleConnection` — first-packet
  gate, `RouteInitial`, session ID generation, preamble write, raw
  forwarding of the first packet.
- `internal/protocol/types.go::TypePreregisterReceive` —
  `TypePreregisterReceive = 160`;
  `internal/protocol/types.go::TypePreregister` — `TypePreregister = 161`.
- `internal/protocol/codec.go::DecodePreregister` — `Query`
  parsed when packet version >= 2.
- `internal/backend/directory.go::NewDirectory` (construction, mandatory
  default, snapshot-interval fallback),
  `internal/backend/directory.go::validatePrefixes` (prefix overlap
  validation), `internal/backend/directory.go::Start` (health + snapshot
  tickers), `internal/backend/directory.go::recordHealthResult`
  (3-failure/2-success health rule),
  `internal/backend/directory.go::refreshSnapshots` (snapshot refresh),
  `internal/backend/directory.go::RouteRoom` (`RouteRoom` precedence),
  `internal/backend/directory.go::RouteInitial` (`RouteInitial`).
- `internal/config/config.go (field: health_interval_sec)`,
  `internal/config/config.go (field: room_snapshot_interval_sec)`,
  `internal/config/config.go (field: pending_session_timeout_sec)` —
  defaults `health_interval_sec: 5`, `room_snapshot_interval_sec: 5`,
  `pending_session_timeout_sec: 5`.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`:
  "Game TCP preamble" (routing from the initial Preregister only; streams
  never moved), "Room snapshot" (5-second polling, snapshot authority,
  conflict rule, rebuild after restart), "Health and directory behavior"
  (probe cadence, health thresholds, no automatic reassignment).
- `README.md:L44` — operator-facing statement of the same precedence.
- Commit `51e81b8` — introduction of `internal/backend/directory.go`.
