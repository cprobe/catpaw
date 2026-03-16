# Redis Cluster Test Report

## Test Date

- 2026-03-16

## Related Files

- [`../testdata/cluster/docker-compose.yml`](../testdata/cluster/docker-compose.yml)
- [`cluster-test-plan.md`](./cluster-test-plan.md)
- [`../redis.go`](../redis.go)
- [`../diagnose.go`](../diagnose.go)
- [`../redis_integration_test.go`](../redis_integration_test.go)

## Environment

- 6-node Redis Cluster from `plugins/redis/testdata/cluster/docker-compose.yml`
- 3 masters + 3 replicas
- password: `catpaw-test`
- validated target inside Docker network: `192.168.97.6:7000`

## Commands Executed

Cluster bootstrap:

```bash
docker compose -f docker-compose.cluster.yml up -d
docker compose -f docker-compose.cluster.yml ps
docker compose -f docker-compose.cluster.yml exec -T redis-cluster-node-1 redis-cli -p 7000 -a catpaw-test CLUSTER INFO
docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' catpaw-redis-cluster-node-1
```

Unit tests:

```bash
GOCACHE=/tmp/catpaw-go-cache go test ./plugins/redis/...
```

Integration tests:

```bash
GOCACHE=/tmp/catpaw-go-cache REDIS_CLUSTER_TARGET=192.168.97.6:7000 REDIS_CLUSTER_PASSWORD=catpaw-test \
  go test -tags integration ./plugins/redis -run 'TestRedisClusterIntegration(Gather|ClusterInfoTool)'
```

Bigkeys data seeding and validation:

```bash
docker compose -f docker-compose.cluster.yml exec -T redis-cluster-node-1 sh -lc "<seed big keys via redis-cli -c>"
GOCACHE=/tmp/catpaw-go-cache REDIS_CLUSTER_TARGET=192.168.97.6:7000 REDIS_CLUSTER_PASSWORD=catpaw-test \
  REDIS_CLUSTER_BIGKEYS_READY=1 go test -tags integration ./plugins/redis -run TestRedisClusterIntegrationBigkeysTool
```

## Results

| Area | Evidence | Result |
| --- | --- | --- |
| Cluster bootstrap | `CLUSTER INFO` reported `cluster_state:ok` and `cluster_slots_assigned:16384` | Pass |
| Default monitoring | `TestRedisClusterIntegrationGather` passed against real cluster node | Pass |
| Cluster diagnosis | `TestRedisClusterIntegrationClusterInfoTool` passed against real cluster node | Pass |
| Bigkeys diagnosis | Seeded real cluster keys and `TestRedisClusterIntegrationBigkeysTool` passed | Pass |
| Unit coverage | `go test ./plugins/redis/...` passed | Pass |

## Key Observations

- The new default Cluster checks worked against a real Redis Cluster node.
- `redis_cluster_info` worked end-to-end in the integration environment.
- `redis_bigkeys_scan` worked against real seeded data after constraining the test to keys owned by the selected master.
- Host-mapped ports on this desktop environment were not reliable for the Go integration test path. Direct testing against the container IP inside the Docker network was stable and passed.

## Conclusion

Redis Cluster support is now validated at three levels:

- unit tests for parsing, thresholds, and tool behavior
- fake-server tests for protocol-level behavior
- real Docker Compose integration tests for cluster monitoring and diagnosis

The current implementation now covers:

- default low-cost Cluster monitoring
- optional `repl_lag` and `used_memory_pct`
- on-demand `redis_cluster_info`
- on-demand `redis_bigkeys_scan`
