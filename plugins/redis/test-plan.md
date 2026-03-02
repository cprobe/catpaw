# Redis Plugin Test Plan

## Scope

Validate the Redis plugin against the Docker Compose environment in [`docker-compose.yml`](./docker-compose.yml) with real Redis master/replica instances.

Covered checks:

- `redis::connectivity`
- `redis::response_time`
- `redis::role`
- `redis::master_link_status`
- `redis::connected_slaves`
- `redis::used_memory`
- `redis::rejected_connections`
- `redis::evicted_keys`
- `redis::expired_keys`
- `redis::instantaneous_ops_per_sec`
- `redis::persistence`

## Environment

- Docker Compose stack under `plugins/redis/`
- `redis-master` exposed on `127.0.0.1:6379`
- `redis-replica` exposed on `127.0.0.1:6380`
- password: `catpaw-test`

## Test Strategy

### 1. Baseline environment check

Verify:

- master and replica containers are running
- both instances answer `PING`
- replica reports `role=slave`
- master reports `connected_slaves >= 1`

Evidence:

- `docker compose ps`
- `redis-cli INFO replication`

### 2. Catpaw master check

Run Catpaw in `-test` mode with a temporary config that targets the master and enables:

- connectivity
- response time
- role=`master`
- connected_slaves minimum threshold
- used_memory threshold
- persistence

Expected:

- `redis::connectivity` => `Ok`
- `redis::role` => `Ok`
- `redis::connected_slaves` => `Ok`
- `redis::persistence` => `Ok`
- response/memory may be `Ok` or thresholded depending on runtime values

### 3. Catpaw replica check

Run Catpaw in `-test` mode with a temporary config that targets the replica and enables:

- connectivity
- role=`slave`
- master_link_status=`up`

Expected:

- `redis::connectivity` => `Ok`
- `redis::role` => `Ok`
- `redis::master_link_status` => `Ok`

### 4. Runtime load and counter check

Generate activity on master:

- `redis-benchmark` to raise `instantaneous_ops_per_sec`
- bulk writes with short TTL to raise `expired_keys`
- memory pressure writes to try to raise `evicted_keys`

Run Catpaw twice against the same master config:

- first run establishes baseline for delta counters
- second run validates delta-based checks

Expected:

- `redis::instantaneous_ops_per_sec` should become `Warning`/`Critical` if load is sufficient
- `redis::expired_keys` should increase after TTL expiry
- `redis::evicted_keys` should increase only if maxmemory eviction is actually triggered

Note:

- `evicted_keys` is environment-sensitive; if eviction does not happen, record as "not triggered" rather than forcing a failure

## Pass Criteria

- Core semantic checks pass: connectivity, role, master_link_status, connected_slaves, persistence
- Delta counters behave as designed: first run baseline, second run reports delta
- Test report records all commands, observed events, and any non-triggered checks with explanation
