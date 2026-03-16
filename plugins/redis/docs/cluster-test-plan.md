# Redis Cluster Test Plan

## Scope

Validate Redis Cluster monitoring and diagnosis against
[`../testdata/cluster/docker-compose.yml`](../testdata/cluster/docker-compose.yml).

Covered monitoring checks:

- `redis::connectivity`
- `redis::cluster_state`
- `redis::cluster_topology`
- `redis::repl_lag` when explicitly enabled
- `redis::used_memory_pct` when explicitly enabled

Covered diagnosis tools:

- `redis_cluster_info`
- `redis_bigkeys_scan`

## Environment

- 6-node Redis Cluster
- 3 masters + 3 replicas
- password: `catpaw-test`

Notes:

- host ports `7000`-`7005` are published
- for automated Go integration tests on this desktop environment, using the
  container IP inside the Docker network is more reliable than using the host
  mapped port

## Test Strategy

### 1. Cluster bootstrap

Verify:

- all six nodes are running
- cluster creation completed
- `CLUSTER INFO` reports `cluster_state:ok`
- slot assignment is complete (`cluster_slots_assigned:16384`)

Evidence:

- `docker compose -f docker-compose.cluster.yml ps`
- `docker compose -f docker-compose.cluster.yml exec -T redis-cluster-node-1 redis-cli -p 7000 -a catpaw-test CLUSTER INFO`
- `docker compose -f docker-compose.cluster.yml exec -T redis-cluster-node-1 redis-cli -p 7000 -a catpaw-test CLUSTER NODES`

### 2. Default cluster checks

Run Catpaw or the integration test entry against one cluster node with default
Redis config.

Expected:

- `redis::connectivity` => `Ok`
- `redis::cluster_state` => `Ok`
- `redis::cluster_topology` => `Ok`

### 3. Optional threshold checks

Enable explicitly:

- `repl_lag`
- `used_memory_pct`

Expected:

- checks execute only when configured
- status reflects actual cluster runtime state

### 4. Diagnosis tools

Validate:

- `redis_cluster_info` returns cluster summary plus topology
- `redis_bigkeys_scan` returns bounded sampled output after synthetic writes

### 5. Runtime stimulation

Generate cluster traffic using `redis-cli -c` and `redis-benchmark`:

- normal reads/writes
- large keys on known slot owners for `redis_bigkeys_scan`

## Pass Criteria

- default cluster checks work with no extra tuning
- optional checks stay opt-in
- diagnosis tools return useful bounded output
- no heavy scan behavior leaks into periodic `Gather()`
