# Redis Master/Replica Test Plan

## Scope

Validate the Redis plugin against the master/replica Docker environment in
[`../testdata/master-replica/docker-compose.yml`](../testdata/master-replica/docker-compose.yml).

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

- `redis-master` on `127.0.0.1:6379`
- `redis-replica` on `127.0.0.1:6380`
- password: `catpaw-test`

## Test Strategy

### 1. Baseline environment check

Verify:

- both containers are running
- both nodes answer `PING`
- replica reports `role=slave`
- master reports `connected_slaves >= 1`

Evidence:

- `docker compose ps`
- `docker compose exec -T redis-master redis-cli -a catpaw-test INFO replication`
- `docker compose exec -T redis-replica redis-cli -a catpaw-test INFO replication`

### 2. Master-path validation

Run Catpaw or a helper against the master with:

- connectivity
- response time
- role expectation
- connected slaves threshold
- used memory threshold
- persistence check

Expected:

- `redis::connectivity` => `Ok`
- `redis::role` => `Ok`
- `redis::connected_slaves` => `Ok`
- `redis::persistence` => `Ok`

### 3. Replica-path validation

Run Catpaw or a helper against the replica with:

- connectivity
- role expectation
- master link expectation

Expected:

- `redis::connectivity` => `Ok`
- `redis::role` => `Ok`
- `redis::master_link_status` => `Ok`

### 4. Runtime counter validation

Generate activity on master:

- `redis-benchmark` for `instantaneous_ops_per_sec`
- short-TTL writes for `expired_keys`
- large retained writes for `evicted_keys`
- connection pressure for `rejected_connections`

Run Catpaw twice:

- round 1 establishes baseline
- round 2 validates delta-based behavior

Expected:

- `redis::rejected_connections`, `redis::expired_keys`, and `redis::evicted_keys`
  alert on interval delta rather than process lifetime total

## Pass Criteria

- semantic master/replica checks pass
- persistence check passes in healthy state
- delta checks behave as designed
- test report records both healthy-path and alert-path evidence
