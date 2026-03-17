# Redis Sentinel Plugin Docs

This directory contains the Redis Sentinel plugin implementation, configuration
example, tests, and design documents.

## Documentation Index

- [`usage.md`](./usage.md): implementation-ready usage guide in English
- [`usage.zh-CN.md`](./usage.zh-CN.md): implementation-ready usage guide in Chinese
- [`design.md`](./design.md): design, defaults, diagnosis model, and testing plan
- [`../../conf.d/p.redis_sentinel/redis_sentinel.toml`](../../conf.d/p.redis_sentinel/redis_sentinel.toml): default config example
- [`testdata/sentinel/docker-compose.yml`](./testdata/sentinel/docker-compose.yml): Docker validation environment with 1 master, 2 replicas, and 3 Sentinels

## Code Structure

| File | Purpose |
| --- | --- |
| [`sentinel.go`](./sentinel.go) | package entry and plugin registration |
| [`types.go`](./types.go) | constants, config structs, plugin and instance types |
| [`config.go`](./config.go) | partial merge, defaults, validation, target normalization |
| [`accessor.go`](./accessor.go) | Sentinel connection, RESP handling, and reply parsing |
| [`gather.go`](./gather.go) | multi-target gather flow, event generation, hung-check handling |
| [`diagnose.go`](./diagnose.go) | diagnosis tools, accessor factory, pre-collector, diagnose hints |
| [`sentinel_test.go`](./sentinel_test.go) | fake Sentinel server and gather/config tests |
| [`diagnose_test.go`](./diagnose_test.go) | diagnosis tool registration and behavior tests |
| [`sentinel_integration_test.go`](./sentinel_integration_test.go) | `integration`-tag tests for the real Docker Sentinel environment |

## Scope

The plugin monitors Redis Sentinel as a control-plane service, not Redis data
nodes.

It covers:

- Sentinel node reachability and role validation
- monitored-master health from the Sentinel viewpoint
- low-cost default checks suitable for periodic collection
- LLM-friendly diagnosis tools for overview, master health, peer view, and
  replica view

It intentionally does not:

- replace exporter-style metrics collection
- subscribe to Sentinel Pub/Sub in periodic gather
- assume a fixed topology policy by default
- mix Sentinel semantics into the `redis` plugin

## Docker Validation

Start the test environment:

```bash
docker compose -f plugins/redis_sentinel/testdata/sentinel/docker-compose.yml up -d
```

Default exposed ports:

- Redis master: `127.0.0.1:6379`
- Sentinel nodes: `127.0.0.1:26379`, `127.0.0.1:26380`, `127.0.0.1:26381`

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
