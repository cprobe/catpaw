# Redis Sentinel Plugin Usage Guide

> The `redis_sentinel` plugin is now documented at implementation level. This
> guide is intended to drive coding, configuration templates, and test cases.

## Overview

`redis_sentinel` monitors Redis Sentinel itself.

Its target is a Sentinel process, usually `host:26379`, not a Redis data node
on port `6379`.

Responsibility split:

- `redis` monitors Redis data-plane nodes
- `redis_sentinel` monitors Sentinel control-plane nodes

## Design Basis And Best Practices

This plugin follows common Sentinel monitoring practice:

- `SENTINEL CKQUORUM <master>` is treated as a first-class health check
- default monitoring covers both Sentinel node health and Sentinel's view of
  monitored masters
- periodic collection stays lightweight, bounded, and pull-based
- topology-policy-dependent checks remain off by default
- explicitly configured `masters` are treated as desired state; discovered
  masters are observed state

Operational assumptions the plugin documents but does not enforce:

- production deployments normally run at least three Sentinel nodes
- Sentinel nodes should ideally span failure domains
- NAT, DNS, and `announce-ip` mistakes can break peer discovery and master
  address resolution

## Default Monitoring Strategy

Default-on checks:

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::masters_overview`
- `redis_sentinel::ckquorum`
- `redis_sentinel::master_sdown`
- `redis_sentinel::master_odown`
- `redis_sentinel::master_addr_resolution`

Default-off checks:

- `redis_sentinel::peer_count`
- `redis_sentinel::known_replicas`
- `redis_sentinel::known_sentinels`
- `redis_sentinel::failover_in_progress`
- `redis_sentinel::tilt`

## Event Model

Node-scoped events:

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::masters_overview`
- `redis_sentinel::tilt`

Labels:

- `check`
- `target`

Master-scoped events:

- `redis_sentinel::ckquorum`
- `redis_sentinel::master_sdown`
- `redis_sentinel::master_odown`
- `redis_sentinel::master_addr_resolution`
- `redis_sentinel::peer_count`
- `redis_sentinel::known_replicas`
- `redis_sentinel::known_sentinels`
- `redis_sentinel::failover_in_progress`

Labels:

- `check`
- `target`
- `master_name`

Important rule:

- `ckquorum` is explicitly a per-master event, not a node-aggregate event

## `masters` Semantics

### Explicit config wins

If `[[instances.masters]]` is configured, that list is the desired state for
per-master checks.

Meaning:

- per-master checks run only for configured names
- newly discovered but unconfigured masters do not automatically get
  per-master events
- `masters_overview` still reports what the Sentinel currently sees

### No `masters` means no per-master alerts

If `masters` is empty, the plugin still runs node-scoped checks, but it does
not emit any per-master alert events.

In that mode, `SENTINEL MASTERS` is used only for:

- `masters_overview`
- diagnosis / inspection context

### Missing configured masters are treated as failures

If a configured master is absent from `SENTINEL MASTERS`, these checks should
fail as follows:

- `ckquorum` uses configured `ckquorum.severity`
- `master_sdown`
- `master_odown`
- `master_addr_resolution`

Recommended description:

- `configured master mymaster is not present in SENTINEL MASTERS`

## Configuration Model

Supported instance fields:

- `targets`
- `concurrency`
- `timeout`
- `read_timeout`
- `username`
- `password`
- `labels`
- TLS client config
- `masters`

Not supported:

- `db`
- `mode`
- `cluster_name`

### Target normalization

- `host:port` is used as-is
- bare `host` is normalized to `host:26379`
- duplicate normalized targets are rejected

### Transport and auth

The plugin reuses the Redis plugin's connection model:

- TCP/TLS dialing
- ACL `AUTH`
- timeout / read timeout
- bounded RESP parsing

It does not issue `SELECT`.

## Recommended Config Examples

### Minimal explicit-master config

```toml
[[instances]]
targets = ["10.0.0.10"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"
```

### Multi-Sentinel config

```toml
[[instances]]
targets = [
  "10.0.0.10:26379",
  "10.0.0.11:26379",
  "10.0.0.12:26379",
]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"
```

### Node-only config

```toml
[[instances]]
targets = ["10.0.0.10:26379"]
password = "${SENTINEL_PASSWORD}"
```

This works, but it only produces node-scoped events. If you want per-master
alerts such as `ckquorum`, `master_sdown`, `master_odown`, and
`master_addr_resolution`, you must configure `masters` explicitly.

## Check Semantics

### `redis_sentinel::connectivity`

- connect and run `PING`
- default severity: `Critical`

### `redis_sentinel::role`

- run `ROLE`
- expect first token `sentinel`
- emit `Critical` if not `sentinel`

### `redis_sentinel::masters_overview`

- run `SENTINEL MASTERS`
- emit `Warning` by default when zero masters are returned
- allow operators to raise empty result to `Critical`
- emit `Ok` with summary attrs otherwise

### `redis_sentinel::ckquorum`

- run `SENTINEL CKQUORUM <master>` for each effective master
- emit `Ok` on success
- emit `Critical` for `NOQUORUM`, `NOGOODSLAVE`, or missing configured master

### `redis_sentinel::master_sdown`

- inspect `flags` from `SENTINEL MASTERS`
- emit `Warning` when `s_down` is present
- emit `Critical` if a configured master is missing

### `redis_sentinel::master_odown`

- inspect `flags` from `SENTINEL MASTERS`
- emit `Critical` when `o_down` is present
- emit `Critical` if a configured master is missing

### `redis_sentinel::master_addr_resolution`

- run `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`
- emit `Critical` when resolution is empty or malformed
- emit `Ok` when resolution succeeds

## Optional Check Config Shapes

### `enabled + severity`

Used by:

- `role`
- `ckquorum`
- `master_sdown`
- `master_odown`
- `master_addr_resolution`
- `failover_in_progress`
- `tilt`

### `enabled + empty_severity`

Used by:

- `masters_overview`

### `enabled + warn_lt + critical_lt`

Used by:

- `peer_count`
- `known_replicas`
- `known_sentinels`

Rules:

- these checks default to disabled
- at least one of `warn_lt` or `critical_lt` must be set when enabled
- if both are set, `critical_lt` must be lower than `warn_lt`

## Periodic Gather Budget

Allowed in `Gather()`:

- `PING`
- `ROLE`
- `SENTINEL MASTERS`
- `SENTINEL CKQUORUM <master>`
- `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`

Diagnosis-only or default-off:

- `SENTINEL SENTINELS <master>`
- `SENTINEL REPLICAS <master>`
- `SENTINEL MASTER <master>`
- `INFO`
- Pub/Sub subscriptions

## Diagnosis Tools

The plugin intentionally exposes task-shaped tools for LLM efficiency, not one
tool per Redis command:

- `sentinel_overview`
- `sentinel_master_health`
- `sentinel_replicas`
- `sentinel_sentinels`
- `sentinel_info`

Recommended first-round use:

- generic Sentinel alert: `sentinel_overview`
- master-specific alert: `sentinel_master_health`

Second-round drill-down:

- replica visibility issue: `sentinel_replicas`
- quorum or peer-view issue: `sentinel_sentinels`

## Diagnosis Pre-Collection

Pre-collect only:

- `ROLE`
- `SENTINEL MASTERS`

Do not pre-collect full `REPLICAS` or `SENTINELS` data for every master.

## Recommended Diagnosis Routes

- generic Sentinel alert: `sentinel_overview`
- quorum alert: `sentinel_master_health`, then `sentinel_sentinels` if needed
- master down alert: `sentinel_master_health`
- replica visibility issue: `sentinel_master_health`, then `sentinel_replicas`
- topology disagreement: `sentinel_overview`, then `sentinel_sentinels`

## Test Environment Recommendation

Recommended Docker topology:

- 1 Redis master
- 2 Redis replicas
- 3 Sentinel nodes

Validate at least:

- healthy quorum
- `ROLE == sentinel`
- non-empty `masters_overview`
- correct master address resolution
- `sdown` / `odown` / failover behavior after master shutdown
- `Critical` events for missing configured masters
- `announce-ip` / address resolution mismatch scenarios

## Docker Validation Environment

This repo includes a ready-to-use Docker validation stack:

- [`testdata/sentinel/docker-compose.yml`](./testdata/sentinel/docker-compose.yml)

Start it with:

```bash
docker compose -f plugins/redis_sentinel/testdata/sentinel/docker-compose.yml up -d
```

Default exposed endpoints:

- Sentinel: `127.0.0.1:26379`
- Redis master: `127.0.0.1:6379`

Run integration tests:

```bash
REDIS_SENTINEL_TARGET=127.0.0.1:26379 \
REDIS_SENTINEL_MASTER=mymaster \
go test -tags integration ./plugins/redis_sentinel
```

Useful manual checks:

```bash
docker compose -f plugins/redis_sentinel/testdata/sentinel/docker-compose.yml exec sentinel-tools \
  redis-cli -h sentinel-1 -p 26379 SENTINEL masters

docker compose -f plugins/redis_sentinel/testdata/sentinel/docker-compose.yml exec sentinel-tools \
  redis-cli -h sentinel-1 -p 26379 SENTINEL ckquorum mymaster
```
