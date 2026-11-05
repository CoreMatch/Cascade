# 0001 — Edge Proxy, Not a Game Server

- **Status**: accepted

## Context

Cascade began as a full Rusted Warfare relay/game server. The project was
then re-scoped: rooms, lobby state, game state, and all game protocol
behavior moved to independently developed backends, and this process became
the edge layer in front of them.

## Decision

Cascade is a **pure edge proxy**. It owns client ingress, HRPAuth identity,
route selection, and byte forwarding; it does not implement or maintain a
production backend. The ownership split is fixed by the backend contract:
"The backend owns rooms, lobby state, game state, and all game protocol
behavior. The proxy owns client ingress, HRPAuth identity, route selection,
and byte forwarding" (HA-Contract, `Cascade-Backend-v1.md`, "Cascade Backend
Contract" section, v1.2.1).

Stale proxy bindings are cleaned on their own tick:
`internal/app/cleanup.go::bindingCleanupLoop` runs
`database.Store.CleanupStaleProxyBindings` every 24 hours, started from
`app.Run` (`internal/app/app.go::Run`), with its own error handling (a
failed pass logs at error level and retries next tick; it never affects the
presence loop or request path). Covered by loop tests plus a real sqlite
test (stale unclaimed bindings deleted; claimed and fresh bindings survive).

Standing author decisions (2026-11-05):

- The `players` table (migration `000001`) and the `Player` model are
  **kept, deliberately** (author: "为什么要删？"). No code reads or writes
  player rows, but the table has no design defect, and dropping it would
  require a new migration with no clear benefit. No migration will be added
  to remove it; this item is closed, not pending.
- Test-only protocol primitives (`Reader`/`Writer` methods, `ReadPacket`,
  `backend.ReadPreamble`) are kept: they document and verify the wire format
  and are exercised by the integration tests.

## Consequences

Costs and limitations:

- Any game-behavior bug (lobby, simulation, protocol semantics) is out of
  scope for this repository by construction; fixes belong to the backend.

Benefits:

- A single, narrow responsibility keeps the trust surface small: the proxy
  never interprets game payloads beyond the packets needed for identity and
  chat commands.
- The contract boundary (see 0002–0004) can evolve without touching game
  logic, because there is no game logic here.

## Evidence

- Commit `51e81b8` "refactor: migrate to MCAGS-RW edge proxy setup":
  addition of the `internal/proxy/` and `internal/backend/` packages
  (verified via `git show --stat`).
- `README.md:L3-L5` — positioning: edge reverse proxy; backends own rooms,
  lobby, and game protocol behavior; this repository does not implement or
  maintain a production backend.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`,
  "Cascade Backend Contract" section — ownership split (v1.2.1).
- `internal/app/cleanup.go::bindingCleanupLoop` — 24-hour stale-binding
  cleanup tick with its own error handling;
  `internal/app/cleanup_test.go` (loop tests),
  `internal/database/store.go::CleanupStaleProxyBindings` (store policy).
- Author decision (2026-11-05): the `players` table and the test-only
  protocol primitives are kept.
