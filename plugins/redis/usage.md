# Redis Plugin Usage Guide

## Overview

The `redis` plugin monitors Redis availability, latency, replication state, client pressure, memory pressure, delta counters, and persistence health.

Supported checks:

- `redis::connectivity`
- `redis::response_time`
- `redis::role`
- `redis::connected_clients`
- `redis::blocked_clients`
- `redis::used_memory`
- `redis::rejected_connections`
- `redis::master_link_status`
- `redis::connected_slaves`
- `redis::evicted_keys`
- `redis::expired_keys`
- `redis::instantaneous_ops_per_sec`
- `redis::persistence`

## Quick Start

Create a config file under `conf.d/p.redis/redis.toml`:

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"

[instances.role]
expect = "master"

[instances.used_memory]
warn_ge = "512MB"
critical_ge = "1GB"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 3
```

Run in test mode:

```bash
./catpaw -test -plugins redis
```

## Configuration Basics

Each Redis instance supports:

- `targets`: Redis addresses, `host` or `host:port`
- `concurrency`: concurrent checks per instance, default `10`
- `timeout`: dial/write timeout, default `3s`
- `read_timeout`: read timeout, default `2s`
- `username`: Redis ACL username
- `password`: Redis password
- `db`: Redis database index, default `0`
- `use_tls` and related TLS fields
- `interval`: collection interval
- `labels`: custom event labels

If a target does not include a port, `:6379` is added automatically.

## Authentication

Password-only auth:

```toml
[[instances]]
targets = ["redis.example.com:6379"]
password = "your-password"
```

ACL auth:

```toml
[[instances]]
targets = ["redis.example.com:6379"]
username = "monitor"
password = "your-password"
```

Select a non-default DB:

```toml
[[instances]]
targets = ["redis.example.com:6379"]
password = "your-password"
db = 2
```

## TLS

Example for native TLS or a Redis proxy:

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

## Common Examples

### 1. Basic Availability

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"
```

This enables `redis::connectivity`.

### 2. Master Health

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

### 3. Replica Health

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

### 4. Memory and Client Pressure

```toml
[[instances]]
targets = ["10.0.0.10:6379"]
password = "your-password"

[instances.connected_clients]
warn_ge = 500
critical_ge = 1000

[instances.blocked_clients]
warn_ge = 1
critical_ge = 5

[instances.used_memory]
warn_ge = "8GB"
critical_ge = "10GB"
```

### 5. Runtime Counters

```toml
[[instances]]
targets = ["10.0.0.10:6379"]
password = "your-password"

[instances.rejected_connections]
warn_ge = 1
critical_ge = 10

[instances.evicted_keys]
warn_ge = 10
critical_ge = 100

[instances.expired_keys]
warn_ge = 100
critical_ge = 1000

[instances.instantaneous_ops_per_sec]
warn_ge = 5000
critical_ge = 20000
```

Notes:

- `rejected_connections`, `evicted_keys`, and `expired_keys` all use delta within the collection interval, not Redis lifetime totals.
- The first collection cycle only establishes a baseline and does not produce alerts.

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

[partials.persistence]
enabled = true
severity = "Critical"

[[instances]]
targets = ["10.0.0.10:6379"]
partial = "prod"

[instances.role]
expect = "master"

[[instances]]
targets = ["10.0.0.11:6379"]
partial = "prod"

[instances.role]
expect = "slave"

[instances.master_link_status]
expect = "up"
```

## Check Reference

### `redis::connectivity`

- Always enabled
- Connects, optionally authenticates, optionally selects DB, then sends `PING`
- Default severity: `Critical`

### `redis::response_time`

- Measures total time from connect to `PONG`
- Enabled when `warn_ge` or `critical_ge` is configured

### `redis::role`

- Reads `INFO replication`
- Allowed `expect`: `master`, `slave`, `replica`
- `replica` is normalized to `slave`

### `redis::connected_clients`

- Reads `connected_clients` from `INFO clients`

### `redis::blocked_clients`

- Reads `blocked_clients` from `INFO clients`

### `redis::used_memory`

- Reads `used_memory` from `INFO memory`
- Adds `maxmemory` labels if Redis reports them

### `redis::rejected_connections`

- Reads `rejected_connections` from `INFO stats`
- Useful for `maxclients` exhaustion or resource pressure

### `redis::master_link_status`

- Replica-only semantic check
- Typical expected value: `up`

### `redis::connected_slaves`

- Master-only semantic check
- Uses `warn_lt` and `critical_lt`

### `redis::evicted_keys`

- Reads `evicted_keys` from `INFO stats`
- Evaluates per-interval delta
- First collection establishes baseline

### `redis::expired_keys`

- Reads `expired_keys` from `INFO stats`
- Evaluates per-interval delta
- First collection establishes baseline

### `redis::instantaneous_ops_per_sec`

- Reads `instantaneous_ops_per_sec` from `INFO stats`

### `redis::persistence`

- Reads `INFO persistence`
- Alerts when:
  - `loading = 1`
  - `rdb_last_bgsave_status != ok`
  - `aof_enabled = 1` and `aof_last_write_status != ok`

## Operational Recommendations

- Use `role` and `connected_slaves` on masters.
- Use `role` and `master_link_status` on replicas.
- Set `used_memory` thresholds relative to actual `maxmemory`.
- Treat `rejected_connections` as an operational problem, not just a warning counter.
- Tune `evicted_keys`, `expired_keys`, and `instantaneous_ops_per_sec` based on actual workload and interval.
- Enable `persistence` in environments where Redis durability matters.

## Troubleshooting

### Authentication failure

Symptoms:

- `redis::connectivity` reports `WRONGPASS`
- Redis is reachable but authentication fails

Check:

- `password`
- `username`
- ACL permissions for the monitoring user

### TLS failure

Symptoms:

- connectivity fails before `PING`

Check:

- `use_tls`
- CA / certificate files
- `tls_server_name`
- `insecure_skip_verify` usage

### Role mismatch

Symptoms:

- `redis::role` reports the actual role differs from the expected role

Check:

- failover state
- sentinel / orchestration changes
- whether the config still matches topology

### No `evicted_keys` or `expired_keys` alerts

This is not automatically a problem:

- first collection only establishes baseline
- `evicted_keys` needs real memory pressure
- `expired_keys` depends on actual TTL expiration during the collection interval

## Related Files

- [`design.md`](./design.md)
- [`docker-compose.yml`](./docker-compose.yml)
- [`test-plan.md`](./test-plan.md)
- [`test-report.md`](./test-report.md)
- [`test-report.zh-CN.md`](./test-report.zh-CN.md)
