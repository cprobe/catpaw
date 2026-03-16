# Redis Plugin Usage Guide

## Overview

The `redis` plugin monitors Redis-aware health signals instead of generic TCP
reachability only.

It supports:

- standalone Redis
- master/replica Redis
- Redis Cluster

The plugin is designed around two rules:

- useful defaults should work without much tuning
- periodic monitoring must stay lightweight

For that reason, only a small set of low-cost checks is enabled by default.

## Supported Checks

### Default checks

- `redis::connectivity`
- `redis::cluster_state` when the target is detected as a Redis Cluster node
- `redis::cluster_topology` when the target is detected as a Redis Cluster node

### Optional checks

- `redis::response_time`
- `redis::role`
- `redis::repl_lag`
- `redis::connected_clients`
- `redis::blocked_clients`
- `redis::used_memory`
- `redis::used_memory_pct`
- `redis::rejected_connections`
- `redis::master_link_status`
- `redis::connected_slaves`
- `redis::evicted_keys`
- `redis::expired_keys`
- `redis::instantaneous_ops_per_sec`
- `redis::persistence`

## Diagnosis Tools

The Redis plugin also registers diagnosis tools for AI diagnosis and `inspect`.

Available tools:

- `redis_info`
- `redis_cluster_info`
- `redis_slowlog`
- `redis_client_list`
- `redis_config_get`
- `redis_latency`
- `redis_memory_analysis`
- `redis_bigkeys_scan`

`redis_bigkeys_scan` is diagnosis-only and intentionally does not run during
periodic monitoring.

## Quick Start

Minimal config:

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"
```

This enables:

- `redis::connectivity`
- and, if the target is a Redis Cluster node, the default cluster checks

Run in test mode:

```bash
./catpaw -test -plugins redis
```

## Configuration Basics

Common fields:

- `targets`: Redis addresses, `host` or `host:port`
- `concurrency`: concurrent checks per instance, default `10`
- `timeout`: dial/write timeout, default `3s`
- `read_timeout`: read timeout, default `2s`
- `username`: Redis ACL username
- `password`: Redis password
- `db`: Redis database index, default `0`
- `mode`: `auto` / `standalone` / `cluster`, default `auto`
- `cluster_name`: optional cluster label for event grouping
- `use_tls` and related TLS fields
- `interval`
- `labels`

If a target omits the port, `:6379` is added automatically.

## Cluster Behavior

### `mode = "auto"`

This is the default and recommended mode.

Behavior:

- query `INFO server`
- if `redis_mode=cluster`, automatically enable:
  - `redis::cluster_state`
  - `redis::cluster_topology`
- if not cluster, skip cluster checks cleanly

### `mode = "standalone"`

Use this when the target is definitely not a cluster node and you want to avoid
even cluster detection.

### `mode = "cluster"`

Use this when the target must be a cluster node. If the target is not running
in cluster mode, catpaw emits a clear error event.

## Why Some Checks Are Off By Default

The following checks depend heavily on workload or topology policy and therefore
stay off by default:

- `redis::repl_lag`
- `redis::used_memory`
- `redis::used_memory_pct`
- `redis::connected_clients`
- `redis::connected_slaves`

This avoids noisy defaults and keeps the plugin usable across very different
Redis deployments.

## Common Config Examples

### 1. Standalone or simple availability

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"
```

### 2. Master health

```toml
[[instances]]
targets = ["10.0.0.10:6379"]
password = "your-password"

[instances.role]
expect = "master"
severity = "Warning"

[instances.connected_slaves]
warn_lt = 2
critical_lt = 1

[instances.persistence]
enabled = true
severity = "Critical"
```

### 3. Replica health

```toml
[[instances]]
targets = ["10.0.0.11:6379"]
password = "your-password"

[instances.role]
expect = "slave"
severity = "Warning"

[instances.master_link_status]
expect = "up"
severity = "Warning"
```

### 4. Cluster node with default hard-failure checks

```toml
[[instances]]
targets = ["10.0.0.20:6379"]
password = "your-password"
mode = "auto"
cluster_name = "prod-cache"
```

### 5. Cluster node with optional workload-specific checks

```toml
[[instances]]
targets = ["10.0.0.20:6379"]
password = "your-password"
mode = "auto"
cluster_name = "prod-cache"

[instances.repl_lag]
warn_ge = "1MB"
critical_ge = "10MB"

[instances.used_memory_pct]
warn_ge = 80
critical_ge = 90
```

## Check Notes

### `redis::repl_lag`

- unit: byte offset lag, not time
- replica view: `master_repl_offset - slave_repl_offset`
- master view: max lag across known replicas

### `redis::used_memory_pct`

- only meaningful when `maxmemory > 0`
- if `maxmemory = 0`, the plugin emits `Ok` with a skip explanation

### Delta counters

These checks use interval delta rather than process lifetime total:

- `redis::rejected_connections`
- `redis::evicted_keys`
- `redis::expired_keys`

The first gather establishes baseline and does not alert.

## TLS

Example:

```toml
[[instances]]
targets = ["redis.example.com:6380"]
password = "your-password"
use_tls = true
tls_ca = "/etc/catpaw/ca.pem"
tls_server_name = "redis.example.com"
```

Available TLS fields:

- `use_tls`
- `tls_ca`
- `tls_cert`
- `tls_key`
- `tls_key_pwd`
- `tls_server_name`
- `insecure_skip_verify`
- `tls_min_version`
- `tls_max_version`

## Partials

Use `partials` to share auth, TLS, timeouts, and common thresholds:

```toml
[[partials]]
id = "prod"
password = "your-password"
timeout = "3s"
read_timeout = "2s"

[partials.connectivity]
severity = "Critical"

[partials.cluster_state]
severity = "Critical"

[[instances]]
targets = ["10.0.0.20:6379"]
partial = "prod"
cluster_name = "prod-cache"
```

## Related Docs

- [`../README.md`](../README.md)
- [`design.md`](./design.md)
- [`test-plan.md`](./test-plan.md)
- [`cluster-test-plan.md`](./cluster-test-plan.md)
