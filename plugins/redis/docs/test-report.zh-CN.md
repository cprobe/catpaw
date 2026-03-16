# Redis 插件测试报告

## 测试日期

- 2026-03-02

## 相关文件

- [`test-plan.md`](./test-plan.md)
- [`test-report.md`](./test-report.md)
- [`../testdata/master-replica/docker-compose.yml`](../testdata/master-replica/docker-compose.yml)
- [`../redis.go`](../redis.go)
- [`../redis_test.go`](../redis_test.go)

## 测试环境

- 使用 `plugins/redis/testdata/master-replica/docker-compose.yml` 启动测试环境
- `redis-master`：`127.0.0.1:6379`
- `redis-replica`：`127.0.0.1:6380`
- Redis 密码：`catpaw-test`

从 Redis 实例本身取得的基线信息如下：

- master 的 `INFO replication`：`role=master`，`connected_slaves=1`
- replica 的 `INFO replication`：`role=slave`，`master_link_status=up`
- master 的 `INFO persistence`：`loading=0`，`rdb_last_bgsave_status=ok`，`aof_last_write_status=ok`

## 测试方法

本次验证分为两条路径：

1. 通过 Catpaw CLI 和临时 Go helper，直接连接 Docker Compose 中的真实 Redis 环境进行集成验证。
2. 通过 `GOCACHE=/tmp/catpaw-go-cache go test ./plugins/redis` 执行插件单元测试。

同时使用两条路径的原因是：

- `catpaw -test` 默认只输出告警事件和恢复事件，不会输出普通的健康事件。
- 对于健康路径验证，额外使用了临时 helper 直接调用 Redis 插件的 `Gather`，打印全部事件结果。

## 执行过的命令

环境确认与单元测试：

```bash
docker compose ps
docker compose exec -T redis-master redis-cli -a catpaw-test INFO replication
docker compose exec -T redis-replica redis-cli -a catpaw-test INFO replication
docker compose exec -T redis-master redis-cli -a catpaw-test INFO persistence
GOCACHE=/tmp/catpaw-go-cache go test ./plugins/redis
```

CLI 告警路径验证：

```bash
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-master -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-bad-auth -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-response -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-replica-mismatch -plugins redis -loglevel error
timeout 5s /tmp/catpaw-redis-test -test -configs /tmp/catpaw-redis-counters -plugins redis -loglevel error
```

运行时压力与计数器触发：

```bash
docker compose exec -T redis-master redis-cli -a catpaw-test CONFIG SET maxclients 10
docker compose exec -T redis-master redis-benchmark -a catpaw-test -n 500 -c 50 -t ping
docker compose exec -T redis-master redis-cli -a catpaw-test CONFIG SET maxclients 10000

docker compose exec -T redis-master sh -lc '... SETEX ...'
docker compose exec -T redis-master redis-benchmark -a catpaw-test -n 3000 -c 10 -d 131072 -r 1000000 -t set
```

健康路径 helper：

```bash
GOCACHE=/tmp/catpaw-go-cache go run /tmp/redis_plugin_semantic_helper.go
GOCACHE=/tmp/catpaw-go-cache go run /tmp/redis_plugin_expired_helper.go
GOCACHE=/tmp/catpaw-go-cache go run /tmp/redis_plugin_evicted_helper.go
```

## 测试结果

| 检查项 | 证据 | 结果 |
| --- | --- | --- |
| `redis::connectivity` | 健康事件：`redis ping ok`；错误密码事件：`redis ping failed: WRONGPASS...` | 通过 |
| `redis::response_time` | 通过极低阈值强制触发告警：`redis response time 1.855404ms >= warning threshold 1µs` | 通过 |
| `redis::role` | helper 输出 master/slave 健康事件；错误期望场景输出：`redis role is slave, expected master` | 通过 |
| `redis::master_link_status` | helper 输出 replica 健康事件：`redis master link status is up, matches expectation`；错误期望场景输出：`redis master link status is up, expected down` | 通过 |
| `redis::connected_slaves` | 健康事件：`redis connected slaves 1, everything is ok`；严格阈值下输出：`redis connected slaves 1 < warning threshold 2` | 通过 |
| `redis::used_memory` | 输出告警：`redis used memory 1.0 MiB >= warning threshold 1.0MB` | 通过 |
| `redis::rejected_connections` | Redis `INFO stats` 显示 `rejected_connections:41`；插件输出：`redis rejected connections 41 >= warning threshold 1` | 通过 |
| `redis::instantaneous_ops_per_sec` | 输出告警：`redis instantaneous ops per second 1 >= warning threshold 1`；同一进程随后输出恢复 `Ok` | 通过 |
| `redis::expired_keys` | helper 第一次采集输出基线：`baseline established (total: 600)`；第二次采集输出：`redis expired keys delta 3 >= warning threshold 1` | 通过 |
| `redis::evicted_keys` | helper 第一次采集输出基线：`baseline established (total: 0)`；第二次采集输出：`redis evicted keys delta 1961 >= warning threshold 1` | 通过 |
| `redis::persistence` | 健康事件：`redis persistence status is healthy`；Redis `INFO persistence` 同时显示 `loading=0`、`rdb_last_bgsave_status=ok`、`aof_last_write_status=ok` | 通过 |

## 关键观察

- Redis 插件已经在真实的 master/replica 环境中完成验证。
- delta 型检查项行为符合设计：
  - 第一次采集建立 baseline
  - 第二次采集基于增量值告警，而不是直接使用 Redis 启动以来的累计值
- `instantaneous_ops_per_sec` 在同一进程中先告警后恢复，说明阈值判断和恢复通知逻辑都正常。
- `rejected_connections` 可以通过临时下调 `maxclients` 并制造并发连接稳定触发。
- `evicted_keys` 需要使用随机 key 和大 value 才能稳定触发；早期不带 `-r` 的 benchmark 因 key 被覆盖，不能形成足够的内存压力。

## 测试结束时 Redis 状态

在测试结束阶段，从 master 观察到：

- `rejected_connections: 41`
- `expired_keys: 900`
- `evicted_keys: 2613`
- `used_memory_human: 62.39M`
- `maxmemory_human: 64.00M`

## 结论

Redis 插件在当前 Docker Compose 测试环境中已经通过功能验证，覆盖了连通性、角色语义、主从链路、内存与副本阈值、持久化健康、累计计数器以及 delta 计数器等核心能力。

## 说明

- 本次测试会修改 Redis 测试环境中的运行期计数器和内存状态。
- 如果后续要重新做一轮干净测试，建议重建 compose 环境，或者显式清空 Redis 数据。
