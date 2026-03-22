# Redis Sentinel Plugin Design

## Scope

The `redis_sentinel` plugin monitors and diagnoses Redis Sentinel itself.

Its monitoring target is a Sentinel process, typically `host:26379`, not a
Redis data node on `6379`.

This plugin is intentionally separate from the existing `redis` plugin.

## Why A Separate Plugin

Redis Sentinel is operationally different from both standalone Redis and Redis
Cluster:

- target process is Sentinel, not a Redis data node
- default port is different (`26379`)
- command surface is different (`SENTINEL *`, `ROLE`, optional `INFO`)
- health semantics are different (quorum, peer discovery, master address
  resolution, failover state)

Keeping Sentinel inside the existing `redis` plugin would create ambiguous
configuration and poor defaults:

- users would have to tell whether a target is Redis or Sentinel inside one
  plugin
- check names and defaults would mix data-plane and control-plane concerns
- documentation would become harder to understand

The recommended design is:

- `redis` plugin monitors Redis data nodes
- `redis_sentinel` plugin monitors Sentinel control-plane nodes

Implementation should still reuse RESP transport style and selected parsing
helpers from `plugins/redis`, but product semantics must remain separate.

## Industry-Aligned Operating Assumptions

This design follows Redis Sentinel official command semantics and common
exporter / observability practice:

- `SENTINEL CKQUORUM <master>` is treated as a first-class monitoring check
- health is evaluated from both Sentinel-node and monitored-master viewpoints
- default periodic collection stays bounded and pull-only
- topology-size-dependent checks remain opt-in
- the configured master list is treated as desired state when present
- Sentinel ACL should be minimal and read-only

Operational assumptions the plugin should document, but not enforce:

- production deployments normally run at least three Sentinel nodes
- Sentinel nodes should live in separate failure domains when possible
- NAT / hostname / `announce-ip` mistakes can break peer visibility and master
  resolution even when TCP connectivity is fine

## Requirements Summary

### Functional requirements

- monitor a Sentinel node via RESP
- emit node-level health events
- emit per-master health events
- support explicit master configuration for per-master alert identity
- use discovery only for overview and diagnosis context
- provide read-only diagnosis tools for quorum and failover troubleshooting
- support ACL auth and TLS in the same way as existing remote plugins

### Non-functional requirements

- keep `Gather()` lightweight and bounded
- avoid default checks that depend on site-specific topology policy
- keep configuration explicit and predictable
- reuse existing remote plugin patterns for concurrency, timeouts, and
  diagnosis
- make failure messages directly actionable

## High-Level Architecture

```text
instance
  -> normalize targets and check config
  -> for each target
       -> connect/auth/tls
       -> PING
       -> ROLE
       -> SENTINEL MASTERS
       -> derive effective master set
       -> run per-master lightweight checks
       -> emit node + master scoped events

diagnosis
  -> reuse accessor
  -> pre-collect ROLE + SENTINEL MASTERS
  -> fetch REPLICAS / SENTINELS / CKQUORUM / GET-MASTER-ADDR-BY-NAME on demand
```

## Monitoring Model

The unit of collection is still one target: one Sentinel node `host:port`.

The plugin must support:

- node-level checks against the Sentinel process itself
- per-master checks derived from Sentinel's view of monitored masters

One gather cycle against one Sentinel target may emit:

- node-level events such as connectivity, role, and masters overview
- per-master events such as quorum health, down state, and master address
  resolution

## Key Design Decisions

### ADR-1: `redis_sentinel` is a dedicated plugin

Decision:

- build a separate plugin instead of extending `redis`

Why:

- Sentinel semantics are control-plane specific
- safe defaults differ from Redis data-node defaults
- user-facing configuration is clearer

Trade-off:

- some RESP and config code will be duplicated or lightly wrapped

### ADR-2: `ckquorum` emits per-master events

Decision:

- `redis_sentinel::ckquorum` is a per-master check, not a node-aggregate check

Why:

- the command input is `master`
- the result is only meaningful for one master at a time
- per-master events preserve alert routing and diagnosis context

Trade-off:

- one target can emit multiple quorum events in one gather cycle

### ADR-3: explicit `masters` are the only source of per-master alert identity

Decision:

- if `instances.masters` is configured, that list is authoritative for
  per-master checks
- if `instances.masters` is empty, skip all per-master checks
- discovery from `SENTINEL MASTERS` is used only for overview and diagnosis
  context

Why:

- explicit configuration gives stable behavior and predictable alerts
- recovery events require stable labels and therefore stable `master_name`
- discovery data is too volatile to serve as AlertKey identity

Trade-off:

- users who want per-master alerting must model intended masters explicitly
- explicit configuration can still produce critical events for masters that
  Sentinel no longer reports; this is intentional because it usually indicates
  drift, misconfiguration, or monitoring blindness

### ADR-4: default-on checks must cover both node and master health

Decision:

- default-on checks are:
  - `redis_sentinel::connectivity`
  - `redis_sentinel::role`
  - `redis_sentinel::masters_overview`
  - `redis_sentinel::ckquorum`
  - `redis_sentinel::master_sdown`
  - `redis_sentinel::master_odown`
  - `redis_sentinel::master_addr_resolution`

Why:

- Sentinel is valuable only if it both runs and correctly tracks masters
- these checks are lightweight and high-signal
- they do not require deployment-specific thresholds

Trade-off:

- per-master event volume is higher than a node-only default set

### ADR-5: transport behavior reuses Redis patterns, not Redis config surface

Decision:

- reuse RESP transport, ACL auth, TLS, timeout, and concurrency patterns from
  `plugins/redis`
- do not reuse Redis-only config such as `db`, `mode`, or `cluster_name`

Why:

- transport behavior is the same class of problem
- Sentinel-facing product semantics are different

Trade-off:

- a dedicated Sentinel config model must still be maintained

### ADR-6: topology-count checks stay opt-in

Decision:

- `peer_count`, `known_replicas`, `known_sentinels`, `failover_in_progress`,
  and `tilt` are disabled by default

Why:

- these checks depend heavily on operator policy and deployment shape
- defaulting them on would create noisy cross-environment behavior

Trade-off:

- deeper topology guarantees require explicit operator intent

## Default Strategy

The plugin should follow catpaw's existing principles:

- default configuration should be useful without manual tuning
- default checks should be low-cost and low-false-positive
- workload-specific or topology-policy-specific checks should default to off

## Check Set

| Check | Scope | Default | Source | Notes |
| --- | --- | --- | --- | --- |
| `redis_sentinel::connectivity` | node | on | `PING` | basic reachability |
| `redis_sentinel::role` | node | on | `ROLE` | must report `sentinel` |
| `redis_sentinel::masters_overview` | node | on | `SENTINEL MASTERS` | Sentinel is monitoring masters |
| `redis_sentinel::ckquorum` | master | on | `SENTINEL CKQUORUM <master>` | primary quorum health signal |
| `redis_sentinel::master_sdown` | master | on | `SENTINEL MASTERS` | subjective down according to this Sentinel |
| `redis_sentinel::master_odown` | master | on | `SENTINEL MASTERS` | objective down / quorum reached |
| `redis_sentinel::master_addr_resolution` | master | on | `SENTINEL GET-MASTER-ADDR-BY-NAME <master>` | resolve active master |
| `redis_sentinel::peer_count` | master | off | `SENTINEL SENTINELS <master>` | policy-dependent |
| `redis_sentinel::known_replicas` | master | off | `SENTINEL REPLICAS <master>` | policy-dependent |
| `redis_sentinel::known_sentinels` | master | off | `SENTINEL SENTINELS <master>` | policy-dependent |
| `redis_sentinel::failover_in_progress` | master | off | `SENTINEL MASTER <master>` | noisy unless explicitly desired |
| `redis_sentinel::tilt` | node | off | `INFO` | advanced operational signal |

## Recommended Default Semantics

### `redis_sentinel::connectivity`

- connect, optional auth, `PING`
- default severity: `Critical`

### `redis_sentinel::role`

- execute `ROLE`
- expected first token: `sentinel`
- if not `sentinel`, emit `Critical`

This prevents accidental use of the Sentinel plugin against a Redis data node.

### `redis_sentinel::masters_overview`

- execute `SENTINEL MASTERS`
- if zero masters are returned, emit `Warning` by default
- operators may raise empty-master severity to `Critical`
- otherwise emit `Ok` with summary attrs

This is useful because a reachable Sentinel with no monitored masters is often
misconfigured or not useful.

### `redis_sentinel::ckquorum`

- for each effective master, execute `SENTINEL CKQUORUM <master>`
- success => `Ok`
- error text such as `NOQUORUM` or `NOGOODSLAVE` => `Critical`
- if the configured master is unknown to this Sentinel, emit configured
  `ckquorum.severity`

This should be one of the most important default checks.

### `redis_sentinel::master_sdown`

Per effective master:

- read master flags from `SENTINEL MASTERS`
- if flags contain `s_down`, emit `Warning`
- otherwise `Ok`
- if an explicitly configured master is absent from `SENTINEL MASTERS`, emit
  `Critical`

### `redis_sentinel::master_odown`

Per effective master:

- read master flags from `SENTINEL MASTERS`
- if flags contain `o_down`, emit `Critical`
- otherwise `Ok`
- if an explicitly configured master is absent from `SENTINEL MASTERS`, emit
  `Critical`

### `redis_sentinel::master_addr_resolution`

Per effective master:

- run `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`
- if address is missing or malformed, emit `Critical`
- otherwise `Ok`

### Default-off checks

Recommended default meanings when explicitly enabled:

- `peer_count`: compare number of returned peers against operator thresholds
- `known_replicas`: compare number of returned replicas against operator
  thresholds
- `known_sentinels`: compare number of returned peer Sentinels against operator
  thresholds
- `failover_in_progress`: emit `Warning` when master flags indicate failover
  state
- `tilt`: emit configured severity when `sentinel_tilt:1`

## Configuration Model

Key instance fields:

- `targets`
- `username` / `password`
- `timeout`
- `read_timeout`
- `concurrency`
- `masters`
- `labels`
- TLS client config fields reused from `plugins/redis`

Not applicable:

- `db`
- `mode`
- `cluster_name`

### Target normalization

- `host:port` is accepted as-is
- bare `host` is normalized to `host:26379`
- duplicate normalized targets should be rejected during validation

### Transport and auth reuse

The Sentinel accessor should reuse the Redis accessor approach for:

- TCP/TLS dialing
- ACL `AUTH`
- timeouts
- bounded RESP parsing

It must not issue `SELECT`, because Sentinel is not a logical DB endpoint.

### Masters config

Suggested config style:

```toml
[[instances]]
targets = ["10.0.0.10:26379", "10.0.0.11:26379", "10.0.0.12:26379"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"

[[instances.masters]]
name = "cache-master"
```

Rules:

- `name` is required and must be unique within one instance
- if `masters` is empty, the plugin skips all per-master checks
- if `masters` is non-empty, that list is authoritative for per-master checks
- discovered masters not listed in config may still appear in
  `masters_overview`, but do not get per-master checks

### Effective master set

For one target in one gather cycle:

1. fetch `SENTINEL MASTERS`
2. parse the discovered masters into a map keyed by name
3. if `instances.masters` is empty, stop at node-level checks
4. otherwise run per-master checks only for configured names

This preserves stable alert identity and guarantees that recovery events can
reuse the same AlertKey.

### Check config schema

To stay consistent with existing plugin patterns, Sentinel checks should use a
small number of config shapes.

#### Severity-only checks

Used for:

- `connectivity`
- `role`
- `ckquorum`
- `master_sdown`
- `master_odown`
- `master_addr_resolution`
- `failover_in_progress`
- `tilt`

Suggested fields:

```toml
[instances.role]
enabled = true
severity = "Critical"
```

Rules:

- `enabled` defaults according to the check default
- `severity` defaults to the documented default for that check

#### Empty-result check

Used for:

- `masters_overview`

Suggested fields:

```toml
[instances.masters_overview]
enabled = true
empty_severity = "Warning"
```

Rules:

- `empty_severity` controls the status when zero masters are returned
- non-empty results remain `Ok` unless parsing fails

#### Minimum-count threshold checks

Used for:

- `peer_count`
- `known_replicas`
- `known_sentinels`

Suggested fields:

```toml
[instances.peer_count]
enabled = true
warn_lt = 2
critical_lt = 1
```

Rules:

- these checks default to disabled
- at least one of `warn_lt` or `critical_lt` must be > 0 when enabled
- if both are set, `critical_lt` must be less than `warn_lt`

### Partial config support

The plugin should support partial/template reuse in the same style as
`plugins/redis`:

- shared connection options
- shared TLS config
- shared check defaults

This reduces duplication without pulling in Redis-only fields.

## Gather Algorithm

For each target:

1. connect with timeout, optional TLS, optional ACL auth
2. `PING`
3. `ROLE`
4. `SENTINEL MASTERS`
5. emit `masters_overview`
6. if `instances.masters` is empty, stop at node-level checks
7. otherwise for each configured master:
   - `SENTINEL CKQUORUM <master>`
   - inspect flags from `SENTINEL MASTERS`
   - `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`
8. if optional checks are enabled, fetch additional per-master detail on demand

Concurrency, hung-gather detection, and timeout budgeting should follow the
same patterns already used in `plugins/redis`.

## Event Model

### Node-level labels

- `check`
- `target`

### Per-master labels

- `check`
- `target`
- `master_name`

### Recommended attrs

Node-level attrs:

- `masters_total`
- `discovered_masters`
- `response_time`
- `threshold_desc`

Per-master attrs:

- `flags`
- `resolved_master`
- `known_peers`
- `known_replicas`
- `threshold_desc`

Description style should stay consistent with catpaw:

- pure text
- actual state first, then expected state or threshold

Examples:

- `sentinel role is sentinel, everything is ok`
- `sentinel ckquorum for master mymaster failed: NOQUORUM 2 usable Sentinels`
- `sentinel master mymaster is objectively down`
- `sentinel resolved master mymaster to 10.0.0.20:6379`
- `configured master mymaster is not present in SENTINEL MASTERS`

## Collection Cost Budget

The periodic `Gather()` path must remain lightweight.

### Allowed commands in `Gather()`

- `PING`
- `ROLE`
- `SENTINEL MASTERS`
- `SENTINEL CKQUORUM <master>`
- `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`

### Commands that should be default-off or diagnosis-only

- `SENTINEL SENTINELS <master>`
- `SENTINEL REPLICAS <master>`
- `SENTINEL MASTER <master>`
- `INFO`
- Pub/Sub subscriptions

The plugin should avoid:

- persistent subscriptions during periodic collection
- large unbounded outputs in `Gather()`
- repeated expensive topology calls when cheaper summary commands are enough

## Diagnosis Model

The Sentinel plugin should register read-only diagnosis tools.

### Granularity principles

Diagnosis tools should be shaped for LLM interaction, not for one-to-one Redis
command mapping.

Target shape:

- one first-round overview tool for target-level context
- one first-round master-focused tool for most master-related alerts
- detailed topology tools only when the first-round result is insufficient
- one advanced fallback tool for low-frequency deep inspection

The plugin should avoid:

- exposing one tool per Redis command when those commands are usually consumed
  together
- building one giant catch-all tool that returns too much irrelevant data

### Core tools

- `sentinel_overview`
- `sentinel_master_health`
- `sentinel_replicas`
- `sentinel_sentinels`
- `sentinel_info`

Recommended semantics:

- `sentinel_overview`
  - target-scoped snapshot
  - returns `ROLE` plus a compact `SENTINEL MASTERS` summary
  - first call when the alert context does not yet identify one master
- `sentinel_master_health`
  - master-scoped snapshot
  - combines `SENTINEL MASTER <master>`, `SENTINEL CKQUORUM <master>`,
    `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`, and compact counts for
    replicas / peer Sentinels
  - first call for quorum, down-state, or address-resolution alerts
- `sentinel_replicas`
  - detailed replica list for one master
  - only used when replica visibility or topology depth matters
- `sentinel_sentinels`
  - detailed peer-Sentinel list for one master
  - only used for quorum or view-disagreement drill-down
- `sentinel_info`
  - advanced fallback
  - returns bounded `INFO` output for expert troubleshooting

### Optional advanced tools

- `sentinel_events_recent` if a bounded event implementation becomes useful

## PreCollector Strategy

For diagnosis, pre-collect only compact high-signal data:

- `ROLE`
- `SENTINEL MASTERS`

Do not pre-collect:

- all `SENTINEL REPLICAS` for every master
- all `SENTINEL SENTINELS` for every master
- live Pub/Sub streams

Those are better fetched on demand by diagnosis tools.

## Diagnose Hints

Suggested routes:

- unknown / generic Sentinel alert -> `sentinel_overview`
- quorum alert -> `sentinel_master_health` + `sentinel_sentinels` if needed
- master down alert -> `sentinel_master_health`
- replica visibility issue -> `sentinel_master_health` + `sentinel_replicas`
- topology disagreement -> `sentinel_overview` + `sentinel_sentinels`
- first round should prefer one overview tool or one master-health tool, not
  multiple command-shaped tools

## Parsing Notes

Sentinel commands often return nested arrays of alternating key/value items.

Implementation should:

- parse those into structured maps
- validate odd/even field count
- tolerate missing optional fields
- normalize common fields such as `name`, `ip`, `port`, `flags`
- limit output size before sending to AI

## Security Considerations

- use least-privilege Sentinel ACL accounts
- never log raw passwords
- redact sensitive config or auth material in diagnosis output
- keep diagnosis tools read-only

## Validation Plan

The plugin should be validated at three levels:

1. unit tests with fake RESP replies
2. fake-server tests for protocol shape and edge cases
3. real Docker Compose integration tests with multiple Sentinel nodes and one
   monitored Redis master/replica set

## Docker Test Environment

Recommended test stack:

- 1 Redis master
- 2 Redis replicas
- 3 Sentinel nodes

The environment should validate:

- healthy quorum
- master discovery
- `ROLE == sentinel`
- master failover after shutting down the original master

## Failure Injection Scenarios

The most valuable integration scenarios are:

- one Sentinel down, quorum still healthy
- quorum broken
- master `s_down`
- master `o_down`
- successful failover and new master resolution
- Sentinel node reachable but monitoring zero masters
- explicit configured master missing from `SENTINEL MASTERS`
- bad `announce-ip` / address resolution behavior

## Risks And Mitigations

- Sentinel can be reachable while logically blind to peers or masters
  - mitigate with `ckquorum`, `masters_overview`, and address-resolution checks
- topology-dependent checks are noisy across environments
  - mitigate by keeping them opt-in
- NAT / DNS mistakes can distort Sentinel discovery
  - mitigate with diagnosis tools and explicit operator documentation
- per-master event count grows with monitored masters
  - mitigate by keeping commands lightweight and avoiding default heavy calls

## Suggested Directory Layout

```text
plugins/redis_sentinel/
  design.md
  sentinel.go
  accessor.go
  diagnose.go
  sentinel_test.go
  diagnose_test.go
  docker-compose.yml
  test-plan.md
  test-report.md
  usage.md
  usage.zh-CN.md
```

## Implementation Recommendation

Build `redis_sentinel` as a new plugin.

Reuse:

- RESP transport patterns from `plugins/redis`
- timeout / concurrency / alerting patterns from existing remote plugins
- diagnose accessor and pre-collector registration patterns from
  `plugins/redis`

Do not reuse the existing `redis` plugin config surface directly. The control
plane is different enough that a dedicated config model will be clearer and
safer.
