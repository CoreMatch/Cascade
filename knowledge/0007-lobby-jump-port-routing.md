# 0007 — Lobby Jump: Port Routing, Not Backend Addresses

- **Status**: accepted

## Context

Phase-1 of the lobby design (Avalon knowledge 0009) wants a player who lands in
the zero-mod lobby (the default backend) to be able to jump to any sub-server and
arrive in **that sub-server's lobby**, using a **vanilla client** (no mods, no
loader). Two hard constraints shape the mechanism:

- Backends are **not publicly reachable** when a proxy is present; Cascade is the
  only public ingress. A jump must therefore be routed *through* Cascade.
- `PACKET_RECONNECT_TO(178)` is the native Rusted Warfare "reconnect to
  host:port" packet and is the only vanilla-client-followable redirect. It carries
  only `host:port` — it must **not** carry a backend's real (private) address.

## Why the room-code Query selector is not enough

Routing today is decided once per connection from the initial `Preregister` room
code (`Query`) — see 0002. A client that is bounced by a 178 reconnects fresh and
sends an **empty** `Query` (a vanilla client has no way to know the target room
code), so `RouteInitial("")` sends it straight back to the **default backend (the
lobby)**. The room-code selector therefore cannot express "this reconnect goes to
backend X". A new selector is required.

## Decision

Add a **jump-port** selector plus proxy-side `/goto` / `/rooms` chat commands:

- Each backend may declare `jump_port` (`config.BackendConfig.JumpPort`). When set,
  the proxy listens on that extra public port and routes any connection landing on
  it **straight to that backend, ignoring the room code**
  (`proxy/server.go::serveGame` + `handleConnection(forcedBackendID)`).
- The proxy intercepts chat commands `/rooms` (list rooms from the directory
  snapshot) and `/goto <code>` (`proxy/session.go::commandRooms` / `commandGoto`),
  the same way it already intercepts `/login` etc.
- `/goto` resolves the room code to its backend (`Directory.RouteRoom`), then sends
  the client `PACKET_RECONNECT_TO` targeting `public_game_host:<jump_port>`
  (`protocol.EncodeReconnect`) — Cascade's own address, never the backend's.
- The vanilla client follows the 178, reconnects to the jump port, and the
  jump-port listener routes it to the target backend's lobby.

`public_game_host` is the host clients use to reach the proxy; when empty the jump
is disabled (`/goto` replies it is not configured).

## Consequences

- The jump works with a vanilla client: chat commands and the 178 reconnect are both
  native RW features; no client mod or loader is required.
- Backend addresses stay private; only Cascade ports are public. The direct-connect
  deployment form (no proxy) is untouched — jump ports are a proxy-only feature.
- Cost: one extra public port per jumpable backend, and the proxy now originates a
  game packet (178) rather than only forwarding. Room-code routing on the main
  listener is unchanged, so existing behavior is preserved.
- Rejected alternative: routing the reconnect by player UUID (register a pending
  jump, then match on `RegisterPlayer`). That needs delaying route selection past
  the first packet, changing the "route from the initial Preregister" contract; the
  port selector keeps routing at connection time.

## Evidence

- `internal/proxy/server.go::Run` / `serveGame` / `handleConnection` — jump
  listeners and forced-backend routing.
- `internal/proxy/session.go::commandGoto` / `commandRooms` — chat triggers.
- `internal/protocol/codec.go::EncodeReconnect`, `internal/protocol/types.go::
  TypeReconnectTo` — the 178 packet.
- `internal/config/config.go (field: jump_port, public_game_host)`.
- Tests: `internal/proxy/session_plaintext_test.go::TestJumpPortRoutesToForcedBackend`,
  `TestCommandGotoSendsReconnectToJumpPort`; `internal/protocol/codec_test.go::
  TestEncodeReconnect`.
- Avalon lobby side: Avalon knowledge 0009, `CascadeLobby` config.
