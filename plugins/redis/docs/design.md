# Redis Plugin Design

## Scope

The Redis plugin provides Redis-aware monitoring and diagnosis on top of
catpaw's standard event model.

It is designed for:

- standalone Redis
- master/replica Redis
- Redis Cluster

It is not designed to replace `redis-exporter` or continuous metrics
collection. The plugin focuses on anomaly detection and on-demand diagnosis.

## Design Goals

- keep the plugin lightweight and safe for overloaded Redis targets
- provide useful defaults without forcing users to tune many thresholds
- support both node-level and minimal cluster-level health detection
- reuse the same Redis accessor for alerting and diagnosis

## Monitoring Model

The unit of collection is still a single Redis target: `host:port`.

This is true even in Redis Cluster mode. Cluster support is added by making the
plugin cluster-aware after it connects to a target.

### Per-target checks

| Check | Default | Notes |
| --- | --- | --- |
| `redis::connectivity` | on | `PING` after optional `AUTH` / `SELECT` |
| `redis::response_time` | off | threshold-driven |
| `redis::role` | off | expected `master` / `slave` |
| `redis::repl_lag` | off | workload-specific, byte offset lag |
| `redis::connected_clients` | off | threshold-driven |
| `redis::blocked_clients` | off | threshold-driven |
| `redis::used_memory` | off | absolute bytes |
| `redis::used_memory_pct` | off | requires `maxmemory > 0` |
| `redis::rejected_connections` | off | delta per interval |
| `redis::master_link_status` | off | replica-side check |
| `redis::connected_slaves` | off | master-side check |
| `redis::evicted_keys` | off | delta per interval |
| `redis::expired_keys` | off | delta per interval |
| `redis::instantaneous_ops_per_sec` | off | threshold-driven |
| `redis::persistence` | off | RDB / AOF health |

### Cluster-aware checks

| Check | Default | Notes |
| --- | --- | --- |
| `redis::cluster_state` | on in cluster mode | conservative hard-failure check |
| `redis::cluster_topology` | on in cluster mode | conservative topology hard-failure check |

## Default Strategy

The Redis plugin follows catpaw's "out-of-the-box" and "monitoring must not
become a burden" principles.

### Default-on checks

- `redis::connectivity`
- `redis::cluster_state` when `INFO server` reports `redis_mode=cluster`
- `redis::cluster_topology` when `INFO server` reports `redis_mode=cluster`

### Default-off checks

Everything threshold-driven or workload-specific remains off by default:

- response time
- role expectations
- replication lag
- client counts
- memory thresholds
- delta counters
- persistence

This keeps the default config predictable and avoids false positives across
different Redis deployment styles.

## Collection Cost Budget

The periodic `Gather()` path is intentionally narrow.

### Commands used in normal collection

- `PING`
- `INFO server`
- `INFO replication`
- `INFO clients`
- `INFO memory`
- `INFO stats`
- `INFO persistence`
- `CLUSTER INFO`
- `CLUSTER NODES` only when cluster topology check is enabled

### Commands intentionally excluded from `Gather()`

- `SCAN`
- `MEMORY USAGE`
- `CLIENT LIST`
- `SLOWLOG GET`
- any large enumeration command

Those commands may still be used in diagnosis, but not in periodic monitoring.

## Configuration Model

Key fields:

- `targets`
- `username` / `password`
- `db`
- `mode = auto | standalone | cluster`
- `cluster_name`
- standard timeout / concurrency / alerting settings

### Redis mode

- `auto`: detect cluster mode through `INFO server`
- `standalone`: never run cluster checks
- `cluster`: require cluster mode; emit a clear error event otherwise

### Cluster name

`cluster_name` is only an event label for grouping and alert readability. It
does not affect connection behavior.

## Cluster Semantics

### `redis::cluster_state`

Uses `CLUSTER INFO`.

Default rule:

- `cluster_state != ok` -> `Critical`

### `redis::cluster_topology`

Uses `CLUSTER INFO` plus `CLUSTER NODES`.

Default rule set is intentionally conservative:

- `fail` node exists -> `Critical`
- `cluster_slots_fail > 0` -> `Critical`
- `cluster_slots_assigned != 16384` -> `Critical`
- `pfail` node exists -> `Warning`

It intentionally does not assume:

- required replica count
- balanced slot distribution
- balanced traffic or memory

Those are real concerns, but they are not safe universal defaults.

## Threshold Semantics

### `redis::repl_lag`

- unit: byte offset lag
- replica view: `master_repl_offset - slave_repl_offset`
- master view: max lag across parsed `slaveN:offset=...`
- default: off

### `redis::used_memory_pct`

- unit: percentage of `maxmemory`
- only meaningful when `maxmemory > 0`
- when `maxmemory = 0`, the plugin emits `Ok` with a skip explanation
- default: off

### Delta counters

`rejected_connections`, `evicted_keys`, and `expired_keys` are evaluated as
interval deltas, not process lifetime totals.

The first successful gather establishes baseline and emits `Ok`.

## Diagnosis Model

The Redis plugin exposes read-only diagnosis tools.

### Low-cost tools

- `redis_info`
- `redis_config_get`
- `redis_cluster_info`
- `redis_latency`
- `redis_slowlog`
- `redis_client_list`
- `redis_memory_analysis`

### Heavy but bounded tool

- `redis_bigkeys_scan`

`redis_bigkeys_scan` is diagnosis-only. It uses bounded `SCAN` sampling and
`MEMORY USAGE` to find large keys on the current Redis node. It is never used
in periodic `Gather()`.

## PreCollector Strategy

The diagnosis pre-collector gathers:

- `INFO all`
- plus `CLUSTER INFO` when the target is in cluster mode

It intentionally does not pre-collect `CLUSTER NODES`, because that output is
larger and better fetched on demand by `redis_cluster_info`.

## Validation Assets

The Redis plugin is validated at multiple levels:

- unit tests in [`../redis_test.go`](../redis_test.go)
- diagnosis tests in [`../diagnose_test.go`](../diagnose_test.go)
- standalone/master-replica Docker validation in [`../testdata/master-replica/docker-compose.yml`](../testdata/master-replica/docker-compose.yml)
- Redis Cluster Docker validation in [`../testdata/cluster/docker-compose.yml`](../testdata/cluster/docker-compose.yml)
- integration tests behind the `integration` build tag in [`../redis_integration_test.go`](../redis_integration_test.go)

## Non-Goals

- exporter-style metrics collection
- Sentinel-specific orchestration logic
- periodic big key scans
- periodic client list or slowlog sweeps
- assuming topology policy by default

## Suggested Next Work

The plugin is already feature-complete for catpaw's current Redis scope.

Future work should be driven by real user demand, not feature accumulation. The
most reasonable follow-up items are:

- more structured cluster topology summaries
- more failure-injection integration tests
- top-level README documentation updates
