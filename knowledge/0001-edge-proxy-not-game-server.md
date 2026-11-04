# 0001 — Edge Proxy, Not a Game Server

- **Status**: accepted
- **Date**: 2026-11-04 (decision landed in commit `51e81b8`, 2026-10-25)

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
Contract v1.1.1" section).

The pivot was executed in commit `51e81b8` ("refactor: migrate to MCAGS-RW
edge proxy setup"), which deleted `internal/relay/commands.go` (1144 lines),
`internal/relay/room.go` (1566 lines), and `internal/relay/server.go`
(751 lines), and added the `internal/proxy/` and `internal/backend/`
packages. The project was then renamed from MCAGS-RW/rwgin to Cascade in
commit `69faeb7`, including wire-visible identifiers (preamble magic
`MCAG` → `CASC`, `proxy_issuer`, service name).

Deliberately or not, the pivot **kept dead code**:

- `internal/game/` (`map.go` 99 lines, `state.go` 113 lines, `mod/mod.go`
  150 lines) has **zero references**: no file in the repository imports
  `cascade/internal/game` (verified by repository-wide search on 2026-11-04).
- Legacy game-server config fields are still declared, defaulted, and
  written into generated `config.yaml`, but have no consumer outside
  `internal/config/config.go`: `relay_handshake_id`, `room_code_prefix`,
  `default_map_name`, `default_room_name`, `default_max_players`,
  `default_client_version`, `default_income`, `default_max_units`,
  `maps_dir`, `mods_dir`, `heartbeat_interval_sec` (verified by
  repository-wide search on 2026-11-04).

## Consequences

Costs and limitations:

- The dead `internal/game` package keeps the `github.com/lafriks/go-tiled`
  dependency alive (`go.mod`; the only import is `internal/game/map.go`),
  and `assets/` (maps/mods directories) is still shipped.
- The legacy config fields surface in every freshly generated `config.yaml`
  and can mislead operators into thinking they still control behavior.
- Any game-behavior bug (lobby, simulation, protocol semantics) is out of
  scope for this repository by construction; fixes belong to the backend.

Benefits:

- A single, narrow responsibility keeps the trust surface small: the proxy
  never interprets game payloads beyond the packets needed for identity and
  chat commands.
- The contract boundary (see 0002–0004) can evolve without touching game
  logic, because there is no game logic here.

Why `internal/game/*` was retained is **not documented anywhere in the code
or commit history** — pending author confirmation. The only hint is the doc
comment in `internal/game/mod/mod.go`, which describes a possible "future
server mode"; whether the package is kept for that reason, or simply not yet
deleted, cannot be determined from the repository.

## Evidence

- Commit `51e81b8` "refactor: migrate to MCAGS-RW edge proxy setup":
  deletion of `internal/relay/{commands,room,server}.go`, addition of
  `internal/proxy/` and `internal/backend/` (verified via `git show --stat`).
- Commit `69faeb7` "refactor!: rename project to Cascade": module/binary
  rename and preamble magic `MCAG` → `CASC`, flagged as BREAKING CHANGE
  (verified via `git show`).
- `README.md:3-5` — positioning: edge reverse proxy; backends own rooms,
  lobby, and game protocol behavior; this repository does not implement or
  maintain a production backend.
- HA-Contract repository, `docs/dev/Cascade/Cascade-Backend-v1.md`,
  "Cascade Backend Contract v1.1.1" section — ownership split.
- `internal/game/map.go`, `internal/game/state.go`, `internal/game/mod/mod.go`
  — present at 99/113/150 lines; repository-wide search for
  `cascade/internal/game` returns no imports (verified 2026-11-04).
- `internal/game/map.go:7` — sole repository import of
  `github.com/lafriks/go-tiled`; dependency listed in `go.mod`.
- `internal/config/config.go:32-41` (Config fields), `:79-88` (YAML tags),
  `:180-189` (defaults) — legacy fields; repository-wide search confirms no
  consumer outside `internal/config/config.go` (verified 2026-11-04).
