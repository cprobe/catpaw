# Redis Plugin Docs

This directory contains the Redis plugin implementation, configuration examples,
and validation assets for both standalone Redis and Redis Cluster.

## Document Map

- [`docs/usage.md`](./docs/usage.md): English user guide
- [`docs/usage.zh-CN.md`](./docs/usage.zh-CN.md): 中文使用说明
- [`docs/design.md`](./docs/design.md): design notes, scope, defaults, and implementation model
- [`docs/test-plan.md`](./docs/test-plan.md): master/replica test plan
- [`docs/test-report.md`](./docs/test-report.md): master/replica test report
- [`docs/test-report.zh-CN.md`](./docs/test-report.zh-CN.md): 中文 master/replica 测试报告
- [`docs/cluster-test-plan.md`](./docs/cluster-test-plan.md): Redis Cluster test plan
- [`docs/cluster-test-report.md`](./docs/cluster-test-report.md): Redis Cluster test report
- [`testdata/master-replica/docker-compose.yml`](./testdata/master-replica/docker-compose.yml): master/replica test environment
- [`testdata/cluster/docker-compose.yml`](./testdata/cluster/docker-compose.yml): Redis Cluster test environment

## Code Structure

| File | Purpose |
| --- | --- |
| [`redis.go`](./redis.go) | package entry, plugin registration |
| [`types.go`](./types.go) | constants and Plugin / Instance / Partial structs |
| [`config.go`](./config.go) | partial merge, Init validation, normalization |
| [`gather.go`](./gather.go) | multi-target gather, per-target flow, hung handling |
| [`checks.go`](./checks.go) | node-level checks such as role, memory, persistence |
| [`cluster.go`](./cluster.go) | cluster-specific checks and topology parsing |
| [`accessor.go`](./accessor.go) | Redis connection, RESP protocol, INFO parsing |
| [`diagnose.go`](./diagnose.go) | AI diagnosis tools, pre-collector, hints |
| [`redis_test.go`](./redis_test.go) | unit tests with fake Redis server |
| [`diagnose_test.go`](./diagnose_test.go) | diagnosis tool registration and behavior tests |
| [`redis_integration_test.go`](./redis_integration_test.go) | integration tests behind the `integration` build tag |

## What The Plugin Covers

- Standalone Redis and master/replica deployments
- Redis Cluster hard-failure checks with conservative defaults
- Optional workload-specific checks such as replication lag and maxmemory usage percentage
- On-demand diagnosis tools for cluster topology and big keys

## What The Plugin Intentionally Does Not Do

- It does not replace `redis-exporter`
- It does not expose Prometheus metrics
- It does not run periodic heavy scans such as `SCAN` or `MEMORY USAGE`
- It does not assume topology policy such as required replica count by default
