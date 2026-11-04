# 0005 — HRPAuth Presence and Runtime Path Resolution

- **Status**: accepted
- **Date**: 2026-11-04 (presence renewal fixed in `9dceaf6`; path
  resolution fixed in `3b46aac`, both 2026-11-04)

## Context

Two deployment realities shape this entry. First, Cascade is a
self-registering microservice in the HRPAuth ecosystem: it must announce
itself to HRPAuth Core for discovery and gateway relay, using only what the
HRPAuth contract offers. Second, the same binary runs both from a source
checkout (`go run`, cwd = repo root) and as a standalone binary started from
an arbitrary working directory (e.g. a systemd unit); before the fixes
recorded here, the cwd assumptions caused silent failures: a presence record
that expired and vanished from discovery, and a standalone binary that
started without any database tables because it could not find the migrations
directory.

## Decision

**Presence registration and renewal.**

- At startup, when HRPAuth is configured, the service registers itself
  (delayed 2 s, non-blocking): `POST /services/presence` with
  `{name, ttl_seconds, security_level: 0}`, then relay rules, an internal
  key refresh, and the embedded SDK package upload
  (`internal/app/app.go:113-124,149-178`;
  `internal/auth/client.go:310-317,321-342`).
- A renewal loop re-posts the same presence record at **half the TTL**
  (`internal/app/presence.go:25-30,46-67`), so a renewal lands well before
  the record expires and one missed renewal is tolerable. Failed renewals
  retry with exponential backoff, 5 s start capped at 60 s, logged at error
  level (`presence.go:12-14,33-39`).
- `ttl_seconds <= 0` declares a never-expiring record per the HRPAuth
  contract and disables the loop (`presence.go:25-28`; HA-Contract
  `hrpauth-business.yaml`, `PresenceRequest.ttl_seconds`). The default TTL
  is 120 s (`internal/config/config.go:199`), configurable via
  `presence_ttl_sec` / `RWGIN_PRESENCE_TTL_SEC`.
- **There is no deregistration.** The HRPAuth contract exposes only
  `POST /services/presence` (register or heartbeat); no deregister endpoint
  exists (HA-Contract `hrpauth-business.yaml`, `/services/presence`).
  Consequently nothing is sent on shutdown; the record simply expires after
  its TTL (`presence.go:41-45`).

**Runtime path resolution.**

- Relative program-owned paths are resolved against the working directory
  first (preserves `go run` behavior) and the executable's directory second
  (`internal/paths/paths.go:24-33`), via `paths.FirstExisting`
  (`paths.go:38-45`). This applies to:
  - `config.yaml` — an existing file is read from either location; a
    brand-new file is always created in the working directory
    (`internal/config/config.go:133-142`).
  - the SQLite `db_path` — an existing database found next to the
    executable wins over creating a fresh one in the cwd
    (`internal/app/app.go:44-57`).
  - the migrations directory
    (`internal/database/migrate.go:51-58`).
- A missing migrations directory is **fatal**: startup aborts with
  `ErrMigrationsDirNotFound` listing every candidate path tried
  (`migrate.go:20-23,55-58`). Silently skipping migrations would leave the
  database without tables (historically surfacing later as "no such table:
  player_bindings" during identity binding — commit `3b46aac`). Standalone
  deployments must therefore ship `internal/database/migrations`
  (`README.md:38`).

## Consequences

Costs and limitations:

- A stopped instance stays visible in HRPAuth discovery for up to one TTL
  (120 s by default); there is no way to remove it faster, because the
  contract has no deregister endpoint. Gateway routing may briefly point at
  a dead instance after a crash or restart.
- Renewal traffic is proportional to 2/TTL per instance; very small TTLs
  amplify both traffic and the blast radius of a Core outage (records lapse
  after one TTL without renewal).
- cwd-first resolution means a stray `config.yaml` or database in the
  working directory shadows the files next to the binary; explicit failure
  on missing migrations trades "always starts" for "never starts
  table-less".

Benefits:

- Half-TTL renewal gives one free retry before expiry; exponential backoff
  keeps a failing Core from being hammered.
- The same binary and the same relative paths work from a source checkout
  and from a foreign cwd, and misdeployment is loud (startup abort) instead
  of silent (empty database).

Why the default TTL is exactly 120 s: the author no longer recalls the
original rationale (2026-11-04), and a contract check settles the question.
The HRPAuth contract prescribes **no default** for `ttl_seconds`: the field
is optional, and `<= 0` or omission means "never expires" (HA-Contract
`hrpauth-business.yaml:1217-1219`; `microservices.md:64`). **120 s therefore
comes from this repository's own historical implementation — the value
already used by the original one-shot registration (`9dceaf6`); it is not a
contract-mandated default and not an ecosystem convention.** The only
numerical data point in the contract repository is the registration example
in `microservices.md:46`, which happens to use `"ttl_seconds": 120` — an
illustrative example, not a prescribed value.

## Evidence

- `internal/app/app.go:113-124` (startup registration goroutine + renewal
  loop), `:149-178` (`registerService`: presence, relay, internal key, SDK
  upload), `:44-57` (sqlite `db_path` resolution).
- `internal/app/presence.go:12-14` (backoff bounds), `:25-30` (half-TTL
  interval; `<=0` disables), `:33-39` (backoff doubling), `:41-67` (loop;
  comment documenting the missing deregister endpoint).
- `internal/auth/client.go:310-317` (`RegisterPresence` body incl.
  `ttl_seconds`, `security_level: 0`), `:321-342` (`RegisterRelay`).
- `internal/paths/paths.go:24-33` (`Candidates`: cwd then executable dir),
  `:38-45` (`FirstExisting`).
- `internal/config/config.go:133-142` (config lookup/creation), `:199`
  (`PresenceTTLSec` default 120).
- `internal/database/migrate.go:20-23` (`ErrMigrationsDirNotFound`),
  `:51-58` (resolution + fatal on missing), `:87-97` (per-db-type
  migrations dirs under `internal/database/migrations/`).
- HA-Contract repository, `docs/api/openapi/hrpauth-business.yaml`:
  `/services/presence` at `:706-716` (POST only — register or heartbeat;
  re-verified 2026-11-04 against the spec's complete path list: no
  deregister or DELETE operation for presence exists anywhere) and
  `PresenceRequest.ttl_seconds` at `:1217-1219` ("Self-declared lifetime in
  seconds; <=0 or omitted means never expires"). No default value for
  `ttl_seconds` is prescribed anywhere in the spec.
- HA-Contract repository, `docs/dev/HRPAuth/microservices.md:43-59`
  (registration example using `"ttl_seconds": 120`), `:64` (`ttl_seconds`
  semantics: optional; `<= 0` or omitted = never expires), `:88` (repeated
  heartbeats keep `first_seen`, refresh `last_seen`; expired records are
  cleaned lazily on access).
- Commits: `9dceaf6` "fix(auth): renew HRPAuth presence before its TTL
  expires" (documents the original one-shot bug, the half-TTL cadence,
  backoff, and the contract check for the TTL unit); `3b46aac` "fix(db):
  resolve runtime paths next to the executable and fail loudly when
  migrations are missing" (documents the silent table-less startup bug).
- `README.md:38` — operator-facing path-resolution and migrations-shipping
  statement.
