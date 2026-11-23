<div align="center">
  <h1>Cascade</h1>
  <p><strong>Many Rusted Warfare backends. One public entrance.</strong></p>
  <p><a href="./README.zh-CN.md">简体中文</a></p>
</div>

Cascade is an edge reverse proxy that speaks the native Rusted Warfare protocol. It gives operators and integrators one public entrance to multiple independent game backends.

## Why Cascade

- **Unify the front door.** Players connect to Cascade while each backend owns its rooms and matches.
- **Route with context.** Exact-room, longest-prefix, and default routing use health gates before selecting a backend.
- **Carry trusted identity.** HRPAuth-bound JWT validation supports identity-aware access, not universal JWT issuer support.
- **Keep backends private.** `/rooms` and `/goto` guide players through public reconnects without exposing backend addresses.
- **Choose transport explicitly.** Plaintext and mTLS modes fit different backend trust boundaries.
- **Extend in isolation.** Process-isolated, signed plugins add edge behavior without running inside the proxy.

## Learn more

- [Plugin guide](docs/plugins/README.md) — signed, process-isolated extensions
- [HA Contract](https://github.com/CoreMatch/HA-Contract) — canonical proxy and backend contracts
- [Avalon](https://github.com/CoreMatch/Avalon) — a compatible Rusted Warfare server runtime
- [HA Wiki](https://wiki.mcnb.dev/) — ecosystem deployment and development guides

## Limits

Cascade is not a game server, does not provide automatic failover or room reassignment, and has not been proven in production.

## License and provenance

[AGPL-3.0](LICENSE) · Derived from [RW-HPS](https://github.com/RW-HPS/RW-HPS); upstream copyright notices are retained.
