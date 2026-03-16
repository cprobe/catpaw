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
- command surface is different (`SENTINEL *`, `ROLE`, Pub/Sub events)
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

Implementation may still reuse the same RESP transport style and some parsing
helpers, but product semantics should remain separate.

## Design Goals

- make Sentinel monitoring usable out of the box with low-noise defaults
- keep periodic collection lightweight
- monitor Sentinel as a control-plane service, not as a data node
- support both node-level Sentinel health and monitored-master health
- provide read-only diagnosis tools for failover and quorum troubleshooting

## Non-Goals

- not replacing exporter-style metrics collection
- not subscribing continuously to Pub/Sub channels in periodic `Gather()`
- not performing automated repair or failover actions
- not mixing Sentinel checks into the `redis` plugin
- not requiring the user to model the full Sentinel topology to get value

## Monitoring Model

The unit of collection is still a single target: one Sentinel node
`host:port`.

The plugin should support:

- node-level checks against the Sentinel process itself
- per-monitored-master checks derived from Sentinel's view of masters

This means one gather cycle against one Sentinel target may emit:

- one node-level event, such as connectivity or quorum health
- zero or more master-scoped events, one for each monitored master name

## Default Strategy

The plugin should follow catpaw's existing principles:

- default configuration should be useful without manual tuning
- default checks should be low-cost and low-false-positive
- workload-specific or topology-policy-specific checks should default to off

### Default-on checks

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::ckquorum`
- `redis_sentinel::masters_overview`

### Default-off checks

- peer count thresholds
- per-master replica count thresholds
- failover duration thresholds
- script queue / pending script thresholds
- tilt mode thresholds
- any check that assumes a specific deployment size or operational policy

## Check Set

### Node-level checks

| Check | Default | Source | Notes |
| --- | --- | --- | --- |
| `redis_sentinel::connectivity` | on | `PING` | basic reachability |
| `redis_sentinel::role` | on | `ROLE` | should report `sentinel` |
| `redis_sentinel::ckquorum` | on | `SENTINEL CKQUORUM <master>` | low-noise quorum health |
| `redis_sentinel::masters_overview` | on | `SENTINEL MASTERS` | Sentinel is actually monitoring masters |
| `redis_sentinel::peer_count` | off | `SENTINEL SENTINELS <master>` | policy-dependent |
| `redis_sentinel::tilt` | off | `INFO` / Sentinel state | advanced operational signal |

### Per-master checks

| Check | Default | Source | Notes |
| --- | --- | --- | --- |
| `redis_sentinel::master_sdown` | on | `SENTINEL MASTERS` | subjectively down according to this Sentinel |
| `redis_sentinel::master_odown` | on | `SENTINEL MASTERS` | objectively down / quorum reached |
| `redis_sentinel::master_addr_resolution` | on | `SENTINEL GET-MASTER-ADDR-BY-NAME` | Sentinel can resolve active master |
| `redis_sentinel::known_replicas` | off | `SENTINEL REPLICAS <master>` | topology-policy-specific |
| `redis_sentinel::known_sentinels` | off | `SENTINEL SENTINELS <master>` | topology-policy-specific |
| `redis_sentinel::failover_in_progress` | off | `SENTINEL MASTER <master>` flags/state | noisy unless explicitly wanted |

## Recommended Default Semantics

### `redis_sentinel::connectivity`

- connect, optional auth, `PING`
- default severity: `Critical`

### `redis_sentinel::role`

- execute `ROLE`
- expected first token: `sentinel`
- if not `sentinel`, emit `Critical`

This prevents accidental use of the Sentinel plugin against a Redis data node.

### `redis_sentinel::ckquorum`

This should be one of the most important default checks.

Behavior:

- for each configured master name, execute `SENTINEL CKQUORUM <master>`
- success => `Ok`
- failure text such as "NOQUORUM" or "NOGOODSLAVE" => `Critical`

This is a control-plane health check and aligns well with Sentinel's own
operational model.

### `redis_sentinel::masters_overview`

Behavior:

- execute `SENTINEL MASTERS`
- if zero masters are returned, emit `Warning` or `Critical` depending on
  configuration
- otherwise emit `Ok` with summary attrs

This is useful because a reachable Sentinel with no monitored masters is often
misconfigured or not useful.

### `redis_sentinel::master_sdown`

Per monitored master:

- if flags contain `s_down`, emit `Warning`
- otherwise `Ok`

### `redis_sentinel::master_odown`

Per monitored master:

- if flags contain `o_down`, emit `Critical`
- otherwise `Ok`

### `redis_sentinel::master_addr_resolution`

Per monitored master:

- run `SENTINEL GET-MASTER-ADDR-BY-NAME`
- if address is missing or malformed, emit `Critical`
- otherwise `Ok`

## Configuration Model

Key fields should be:

- `targets`
- `username` / `password`
- `timeout`
- `read_timeout`
- `masters`
- `cluster_name` is not applicable here; use Sentinel-specific naming instead
- `labels`

Suggested master config style:

```toml
[[instances]]
targets = ["10.0.0.10:26379", "10.0.0.11:26379", "10.0.0.12:26379"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"

[[instances.masters]]
name = "cache-master"

[instances.ckquorum]
enabled = true
severity = "Critical"
```

### Why `masters` should be explicit

Sentinel commands such as `CKQUORUM`, `MASTER`, `REPLICAS`, `SENTINELS`, and
`GET-MASTER-ADDR-BY-NAME` are keyed by master name.

An explicit `masters` list gives:

- clear configuration semantics
- predictable default behavior
- simple mapping between events and monitored masters

The plugin may still use `SENTINEL MASTERS` as a discovery source, but explicit
master names should remain the primary configuration for diagnosis and quorum
checks.

## Event Model

### Node-level labels

- `check`
- `target`

### Per-master labels

- `check`
- `target`
- `master_name`

Description style should stay consistent with catpaw:

- pure text
- actual state first, then expected state or threshold

Examples:

- `sentinel role is sentinel, everything is ok`
- `sentinel ckquorum for master mymaster failed: NOQUORUM 2 usable Sentinels`
- `sentinel master mymaster is objectively down`
- `sentinel resolved master mymaster to 10.0.0.20:6379`

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
- `INFO`
- Pub/Sub subscriptions

The plugin should avoid:

- persistent subscriptions during periodic collection
- large unbounded outputs in `Gather()`
- repeated expensive topology calls when cheaper summary commands are enough

## Diagnosis Model

The Sentinel plugin should register read-only diagnosis tools.

### Core tools

- `sentinel_masters`
- `sentinel_master`
- `sentinel_replicas`
- `sentinel_sentinels`
- `sentinel_ckquorum`
- `sentinel_get_master_addr_by_name`

### Optional advanced tools

- `sentinel_info`
- `sentinel_pubsub_events_recent` if a bounded implementation becomes useful

## PreCollector Strategy

For diagnosis, pre-collect only compact high-signal data:

- `ROLE`
- `SENTINEL MASTERS`

Do not pre-collect:

- all `SENTINEL REPLICAS` for every master
- all `SENTINEL SENTINELS` for every master
- live Pub/Sub streams

Those are better fetched on demand by diagnosis tools.

## Diagnosis Hints

Suggested routes:

- quorum alert -> `sentinel_ckquorum` + `sentinel_sentinels`
- master down alert -> `sentinel_master` + `sentinel_get_master_addr_by_name`
- replica visibility issue -> `sentinel_replicas`
- topology disagreement -> `sentinel_masters` + `sentinel_sentinels`

## Parsing Notes

Sentinel commands often return nested arrays of alternating key/value items.

Implementation should:

- parse those into structured maps
- validate odd/even field count
- tolerate missing optional fields
- limit output size before sending to AI

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

Do not reuse the existing `redis` plugin config surface directly. The control
plane is different enough that a dedicated config model will be clearer and
safer.
