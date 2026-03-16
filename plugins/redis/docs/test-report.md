# Redis Plugin Test Report

## Test Date

- 2026-03-02

## Related Files

- [`test-plan.md`](./test-plan.md)
- [`../testdata/master-replica/docker-compose.yml`](../testdata/master-replica/docker-compose.yml)
- [`../redis.go`](../redis.go)
- [`../redis_test.go`](../redis_test.go)

## Environment

- Docker Compose stack from `plugins/redis/testdata/master-replica/docker-compose.yml`
- `redis-master`: `127.0.0.1:6379`
- `redis-replica`: `127.0.0.1:6380`
- password: `catpaw-test`

Baseline evidence from Redis itself:

- master `INFO replication`: `role=master`, `connected_slaves=1`
- replica `INFO replication`: `role=slave`, `master_link_status=up`
- master `INFO persistence`: `loading=0`, `rdb_last_bgsave_status=ok`, `aof_last_write_status=ok`

## Test Method

Two validation paths were used:

1. Real integration checks against the Docker Redis environment through Catpaw CLI and temporary Go helpers that call the Redis plugin `Gather` method directly.
2. Plugin unit tests with `GOCACHE=/tmp/catpaw-go-cache go test ./plugins/redis`.

Why both were needed:

- `catpaw -test` prints alert and recovery events, but it does not print ordinary healthy events.
- For healthy-path verification, a temporary helper invoked the plugin directly and printed all returned events.

## Commands Executed

Environment and unit tests:

```bash
docker compose ps
docker compose exec -T redis-master redis-cli -a catpaw-test INFO replication
docker compose exec -T redis-replica redis-cli -a catpaw-test INFO replication
docker compose exec -T redis-master redis-cli -a catpaw-test INFO persistence
GOCACHE=/tmp/catpaw-go-cache go test ./plugins/redis
```

CLI alert-path verification:

```bash
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-master -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-bad-auth -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-response -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-replica-mismatch -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-counters -plugins redis -loglevel error
```

Runtime stimulation:

```bash
docker compose exec -T redis-master redis-cli -a catpaw-test CONFIG SET maxclients 10
docker compose exec -T redis-master redis-benchmark -a catpaw-test -n 500 -c 50 -t ping
docker compose exec -T redis-master redis-cli -a catpaw-test CONFIG SET maxclients 10000

docker compose exec -T redis-master sh -lc '... SETEX ...'
docker compose exec -T redis-master redis-benchmark -a catpaw-test -n 3000 -c 10 -d 131072 -r 1000000 -t set
```

Healthy-path helpers:

```bash
GOCACHE=/tmp/catpaw-go-cache go run /tmp/redis_plugin_semantic_helper.go
GOCACHE=/tmp/catpaw-go-cache go run /tmp/redis_plugin_expired_helper.go
GOCACHE=/tmp/catpaw-go-cache go run /tmp/redis_plugin_evicted_helper.go
```

## Results

| Check | Evidence | Result |
| --- | --- | --- |
| `redis::connectivity` | healthy event: `redis ping ok`; bad password event: `redis ping failed: WRONGPASS...` | Pass |
| `redis::response_time` | warning event with forced threshold: `redis response time 1.855404ms >= warning threshold 1µs` | Pass |
| `redis::role` | healthy master/slave events from helper; mismatch event: `redis role is slave, expected master` | Pass |
| `redis::master_link_status` | healthy replica event: `redis master link status is up, matches expectation`; mismatch event: `redis master link status is up, expected down` | Pass |
| `redis::connected_slaves` | healthy event: `redis connected slaves 1, everything is ok`; warning event with stricter threshold: `redis connected slaves 1 < warning threshold 2` | Pass |
| `redis::used_memory` | warning event: `redis used memory 1.0 MiB >= warning threshold 1.0MB` | Pass |
| `redis::rejected_connections` | Redis `INFO stats` showed `rejected_connections:41`; plugin warning event: `redis rejected connections 41 >= warning threshold 1` | Pass |
| `redis::instantaneous_ops_per_sec` | warning event: `redis instantaneous ops per second 1 >= warning threshold 1`; same process later emitted recovery `Ok` when value returned to `0` | Pass |
| `redis::expired_keys` | helper round1 baseline: `baseline established (total: 600)`; helper round2 warning: `redis expired keys delta 3 >= warning threshold 1` | Pass |
| `redis::evicted_keys` | helper round1 baseline: `baseline established (total: 0)`; helper round2 warning: `redis evicted keys delta 1961 >= warning threshold 1` | Pass |
| `redis::persistence` | healthy event: `redis persistence status is healthy`; Redis `INFO persistence` also showed `loading=0`, `rdb_last_bgsave_status=ok`, `aof_last_write_status=ok` | Pass |

## Key Observations

- The Redis plugin worked correctly against a real master/replica deployment.
- Delta-based checks behaved as designed:
  - first gather established baseline
  - second gather reported delta instead of Redis lifetime total
- `instantaneous_ops_per_sec` emitted a warning and then a recovery in the same process, which confirms both threshold evaluation and recovery behavior.
- `rejected_connections` was reproducible by temporarily lowering `maxclients` and generating concurrent client load.
- `evicted_keys` required random keys plus large payloads. Earlier benchmark runs without `-r` did not create enough retained data to trigger eviction.

## Final Redis State After Testing

Observed from master near the end of testing:

- `rejected_connections: 41`
- `expired_keys: 900`
- `evicted_keys: 2613`
- `used_memory_human: 62.39M`
- `maxmemory_human: 64.00M`

## Conclusion

The Redis plugin passed functional verification in the Docker Compose test environment for connectivity, semantic role checks, memory/replication thresholds, persistence health, cumulative counters, and delta counters.

## Notes

- This test changed runtime counters and memory usage in the Redis test environment.
- If you want to reset the environment before another round, recreate the stack or clear Redis data explicitly.
