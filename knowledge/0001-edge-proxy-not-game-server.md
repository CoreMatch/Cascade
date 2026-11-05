# 0001 — Edge Proxy, Not a Game Server

- **Status**: accepted
- **Date**: 2026-11-04 (decision landed in commit `51e81b8`, 2026-10-25);
  2026-11-05 the leftover dead code below was deleted (commit `9801368`)
  after author confirmation

## Context

Cascade began as a full Rusted Warfare relay/game server: `internal/relay/`
implemented room management, moderation commands, and the relay protocol, and
`internal/game/` implemented map loading, unit state, and mod metadata. The
project was then re-scoped: rooms, lobby state, game state, and all game
protocol behavior moved to independently developed backends, and this process
became the edge layer in front of them.

## Decision

Cascade is a **pure edge proxy**. It owns client ingress, HRPAuth identity,
route selection, and byte forwarding; it does not implement or maintain a
production backend. The ownership split is fixed by the backend contract:
"The backend owns rooms, lobby state, game state, and all game protocol
behavior. The proxy owns client ingress, HRPAuth identity, route selection,
and byte forwarding" (HA-Contract, `Cascade-Backend-v1.md`, "Cascade Backend
Contract" section; v1.1.1 at the time of writing, v1.2 since 2026-11-05).

The pivot was executed in commit `51e81b8` ("refactor: migrate to MCAGS-RW
edge proxy setup"), which deleted `internal/relay/commands.go` (1144 lines),
`internal/relay/room.go` (1566 lines), and `internal/relay/server.go`
(751 lines), and added the `internal/proxy/` and `internal/backend/`
packages. The project was then renamed from MCAGS-RW/rwgin to Cascade in
commit `69faeb7`, including wire-visible identifiers (preamble magic
`MCAG` → `CASC`, `proxy_issuer`, service name).

Deliberately or not, the pivot **kept dead code** until 2026-11-05:

- `internal/game/` (`map.go` 99 lines, `state.go` 113 lines, `mod/mod.go`
  150 lines) had **zero references**: no file in the repository imported
  `cascade/internal/game` (verified by repository-wide search on 2026-11-04
  and re-verified 2026-11-05 before deletion).
- Legacy game-server config fields were declared, defaulted, and written
  into generated `config.yaml`, but had no consumer outside
  `internal/config/config.go`: `server_id`, `relay_handshake_id`,
  `room_code_prefix`, `default_map_name`, `default_room_name`,
  `default_max_players`, `default_client_version`, `default_income`,
  `default_max_units`, `maps_dir`, `mods_dir`, `heartbeat_interval_sec`
  (verified by repository-wide search).

**Both were deleted in commit `9801368` (2026-11-05)** after the author
confirmed they are leftovers that were simply forgotten, not a deliberate
retention ("应该只是忘了清了，不是有什么小巧思"). Deletion scope:
`internal/game/{map.go,state.go,mod/mod.go}`, `assets/` (maps/mods, only
reachable through the dead `maps_dir`/`mods_dir` fields), the twelve config
fields above together with their `RWGIN_*` env overrides
(`RWGIN_SERVER_ID`, `RWGIN_RELAY_ID`, `RWGIN_ROOM_PREFIX`,
`RWGIN_DEFAULT_MAP`, `RWGIN_DEFAULT_ROOM`, `RWGIN_MAX_PLAYERS`,
`RWGIN_CLIENT_VERSION`, `RWGIN_INCOME`, `RWGIN_MAX_UNITS`, `RWGIN_MAPS_DIR`,
`RWGIN_MODS_DIR`, `RWGIN_HEARTBEAT_SEC`), the then-unused
`lookupEnvFloat32` helper, the `heartbeat_interval_sec` injection in the
v0→v2 migration, and the `github.com/lafriks/go-tiled` dependency (its only
import was `internal/game/map.go`). Existing `config.yaml` files keep
loading: removed keys are ignored on unmarshal.

A follow-up sweep on the same day (commits `ee4ed17`, `fef51a7`) removed
the remaining relay-era leftovers with zero callers: the relay/game
simulation codec (prompt/relay/tick/sync/server-info/start-game encoders,
`game_subcommand.go`, gzip block machinery, and the packet-type constants
only they referenced), the 79 unused i18n moderation/game-flow messages and
label tables, the `Store` methods `GetBindingByPlayerName`,
`UpdateBindingHAUUID`, and the DB-side `SetQuickAuth`, the
`InternalKey()` getter, `redis.Client.ClearAuthenticated`, and
`backend.Client.BaseURL`.

Author disposition of the remaining items (2026-11-05, "无用的就清；能便宜
接上的功能缺口就接"):

- `database.Store.CleanupStaleProxyBindings` — **wired up** (commit
  `ec9d9e0`): `internal/app/cleanup.go::bindingCleanupLoop` runs it on
  a 24-hour tick started from `app.Run`, with its own error handling (a
  failed pass logs at error level and retries next tick; it never affects
  the presence loop or request path). Covered by loop tests plus a real
  sqlite test (stale unclaimed bindings deleted; claimed and fresh
  bindings survive).
- The Redis quick-auth pair `SetQuickAuth`/`CheckQuickAuth` and the
  `quick_auth` branch of `session.restoreAuth` — **deleted** (commit
  `dc04f10`). Relay-era leftover with no writer: double-verified via
  deadcode analysis and repository-wide grep that nothing ever wrote the
  `quick_auth:` key, so the branch could never fire. `restoreAuth` keeps
  the `IsAuthenticated` + binding check, now covered by tests. The DB
  `quick_auth` column is migration schema and stays untouched.
- The `players` table (migration `000001`) and the `Player` model —
  **decided 2026-11-05: kept, deliberately** (author: "为什么要删？"). No
  code reads or writes player rows, but the table has no design defect,
  and dropping it would require a new migration with no clear benefit.
  No migration will be added to remove it; this item is closed, not
  pending.
- Test-only protocol primitives (`Reader`/`Writer` methods, `ReadPacket`,
  `backend.ReadPreamble`) — kept: they document and verify the wire format
  and are exercised by the integration tests.

## Consequences

Costs and limitations:

- Any game-behavior bug (lobby, simulation, protocol semantics) is out of
  scope for this repository by construction; fixes belong to the backend.

(The earlier costs — the dead `internal/game` package keeping
`github.com/lafriks/go-tiled` and `assets/` alive, and the legacy config
fields surfacing in every freshly generated `config.yaml` — were removed
together with the dead code in commit `9801368`, 2026-11-05.)

Benefits:

- A single, narrow responsibility keeps the trust surface small: the proxy
  never interprets game payloads beyond the packets needed for identity and
  chat commands.
- The contract boundary (see 0002–0004) can evolve without touching game
  logic, because there is no game logic here.

Why `internal/game/*` and the legacy config fields survived the pivot was
long undocumented in the code and commit history. **Author confirmation
(2026-11-04): they are leftovers that were simply never cleaned up after the
pivot, not a deliberate retention.** The doc comment in
`internal/game/mod/mod.go` about a possible "future server mode" does not
constitute a rationale for keeping the package. **Follow-up author
confirmation (2026-11-05): the leftovers should be deleted** ("应该只是忘了
清了，不是有什么小巧思"); the deletion was carried out in commit `9801368`
with zero-reference re-verification beforehand and a full green test run
plus the real end-to-end `TestCascadeRealProxyE2E` afterwards.

## Evidence

- Commit `51e81b8` "refactor: migrate to MCAGS-RW edge proxy setup":
  deletion of `internal/relay/{commands,room,server}.go`, addition of
  `internal/proxy/` and `internal/backend/` (verified via `git show --stat`).
- Commit `69faeb7` "refactor!: rename project to Cascade": module/binary
  rename and preamble magic `MCAG` → `CASC`, flagged as BREAKING CHANGE
  (verified via `git show`).
- `README.md:L3-L5` — positioning: edge reverse proxy; backends own rooms,
  lobby, and game protocol behavior; this repository does not implement or
  maintain a production backend.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`,
  "Cascade Backend Contract" section — ownership split (unchanged through
  contract v1.2).
- `internal/game/map.go`, `internal/game/state.go`, `internal/game/mod/mod.go`
  — were present at 99/113/150 lines; repository-wide search for
  `cascade/internal/game` returned no imports (verified 2026-11-04,
  re-verified 2026-11-05). **Deleted in `9801368`.**
- `internal/game/map.go:L7 @ 730d347` (the commit before the deletion
  commit `9801368`) — was the sole repository import of
  `github.com/lafriks/go-tiled`; `go mod tidy` removed the dependency in
  `9801368` once the package was gone.
- `internal/config/config.go` — legacy fields had no consumer outside this
  file (repository-wide search, verified 2026-11-04 and 2026-11-05); the
  fields, their `RWGIN_*` env overrides, and the `heartbeat_interval_sec`
  migration injection were deleted in `9801368`.
- Author confirmation (2026-11-04): `internal/game/*` and the legacy config
  fields are leftovers never cleaned up after the pivot, not an intentional
  design choice. Author confirmation (2026-11-05): delete them.
- Commit `9801368` "chore: delete internal/game dead package and legacy
  game-server config" (verified via `git show --stat`): 229 files changed,
  −4188 lines; `go build/vet/test -race` green and real end-to-end
  `TestCascadeRealProxyE2E` PASS after the deletion.
