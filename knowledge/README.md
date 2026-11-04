# Knowledge Base

## What This Is

This knowledge base is **the Cascade project's memo to itself**: it serves
self-hosting and maintenance of this proxy — implementation orientation,
internal boundaries, and why things are designed the way they are. It is not
written for external developers building against Cascade, and it **never
overrides outward behavior definitions**.

Outward behavior — the backend contract between this proxy and independently
developed Rusted Warfare backends — is defined authoritatively in the
HA-Contract repository:

- [`docs/dev/Cascade/Cascade-Backend-v1.md`](https://github.com/CoreMatch/HA-Contract/blob/main/docs/dev/Cascade/Cascade-Backend-v1.md)
- [`docs/api/openapi/cascade-backend-v1.yaml`](https://github.com/CoreMatch/HA-Contract/blob/main/docs/api/openapi/cascade-backend-v1.yaml)

The in-repo contract copies were removed once HA-Contract became authoritative
(commit `7750528`).

## Order of Authority

If an entry here ever appears to contradict the contract, **the contract wins
and the entry is stale — fix the entry, never the contract**, and surface the
conflict to the owner.

## Format

Each entry is one page in ADR style:

- **Status** (`accepted` / `proposed` / `superseded` / ...), **Date**
- **Context** — the problem and constraints
- **Decision** — what was chosen
- **Consequences** — including costs and limitations
- **Evidence** — `file:line` references and commit hashes. Every piece of
  evidence was actually checked against this repository (or the HA-Contract
  repository, noted as such); anything that could not be verified is marked
  "not verified". Design intent that cannot be determined from the code or
  history is listed as "pending author confirmation"; once the author
  answers, the entry records the confirmation with its date. Do not
  fabricate.

## Entries

| # | Title | Status | Date |
|---|-------|--------|------|
| [0001](0001-edge-proxy-not-game-server.md) | Edge Proxy, Not a Game Server | accepted (leftover dead code deleted 2026-11-05) | 2026-11-04 |
| [0002](0002-room-code-routing.md) | Room-Code Routing: Snapshot, Prefix, Default | accepted | 2026-11-04 |
| [0003](0003-identity-binding-chain.md) | Identity Binding Chain: Ed25519 JWT Before Game Data | accepted | 2026-11-04 |
| [0004](0004-mtls-casc-preamble-transport.md) | mTLS + 24-Byte CASC Preamble + Byte Passthrough | accepted (transport part superseded by 0006) | 2026-11-04 |
| [0005](0005-hrpauth-presence-and-path-resolution.md) | HRPAuth Presence and Runtime Path Resolution | accepted | 2026-11-04 |
| [0006](0006-plaintext-backends-unreachable.md) | Transport Follows the Backend's Declared Trust Strategy | accepted | 2026-11-05 |
