# redis 插件设计

## 概述

监控 Redis 服务的可用性、响应时间、主从复制状态、客户端负载、吞吐和持久化健康。

**核心场景**：

1. **Redis 不可达**：进程挂了、端口不通、TLS/认证配置错误
2. **Redis 变慢**：实例负载升高或网络抖动导致 PING 响应时间增加
3. **主从角色漂移**：预期 master 的节点变成 replica，或反之
4. **客户端积压**：连接数过高或 blocked clients 增长，通常意味着应用或 Lua/事务阻塞
5. **内存压力**：`used_memory` 接近容量上限，可能触发淘汰或 OOM
6. **拒绝连接**：`rejected_connections` 增长，通常意味着 `maxclients` 或资源耗尽
7. **复制链路异常**：replica 的 `master_link_status` 不为 `up`
8. **持久化异常**：RDB 最近一次 bgsave 失败或 AOF 最近一次写入失败
9. **副本数量异常**：master 的 `connected_slaves` 数量低于预期
10. **淘汰和过期激增**：单个采集周期内 `evicted_keys` / `expired_keys` 突增
11. **瞬时吞吐升高**：`instantaneous_ops_per_sec` 超过阈值

**与现有插件的关系**：`net` 只能做通用 TCP send/expect，无法理解 Redis 的 AUTH、SELECT、INFO 和角色/客户端指标。`redis` 插件在保持轻量实现的前提下补上了 Redis 语义层检查。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 连通性 | `redis::connectivity` | host:port | 建连、可选 AUTH/SELECT 后执行 `PING` |
| 响应时间 | `redis::response_time` | host:port | 从建连到收到 `PONG` 的总耗时 |
| 角色 | `redis::role` | host:port | `INFO replication` 的 `role` 是否符合预期 |
| 客户端连接数 | `redis::connected_clients` | host:port | `INFO clients` 的连接数是否超阈值 |
| 阻塞客户端数 | `redis::blocked_clients` | host:port | `INFO clients` 的阻塞客户端数是否超阈值 |
| 内存使用量 | `redis::used_memory` | host:port | `INFO memory` 的 `used_memory` 是否超阈值 |
| 拒绝连接数 | `redis::rejected_connections` | host:port | `INFO stats` 的 `rejected_connections` 是否超阈值 |
| 主从链路状态 | `redis::master_link_status` | host:port | replica 的 `master_link_status` 是否符合预期 |
| 已连接副本数 | `redis::connected_slaves` | host:port | master 的 `connected_slaves` 是否低于最小副本数 |
| 淘汰键数量 | `redis::evicted_keys` | host:port | `INFO stats` 的 `evicted_keys` 周期增量是否超阈值 |
| 过期键数量 | `redis::expired_keys` | host:port | `INFO stats` 的 `expired_keys` 周期增量是否超阈值 |
| 瞬时吞吐 | `redis::instantaneous_ops_per_sec` | host:port | `INFO stats` 的瞬时 ops 是否超阈值 |
| 持久化状态 | `redis::persistence` | host:port | `loading`、`rdb_last_bgsave_status`、`aof_last_write_status` 是否健康 |

- 每个 target 独立事件
- 支持并发检查（默认 10）
- 支持 `partials` 复用认证、TLS、超时和阈值配置

## 配置设计

```go
type Instance struct {
    config.InternalConfig
    Targets      []string
    Concurrency  int
    Timeout      config.Duration
    ReadTimeout  config.Duration
    Username     string
    Password     string
    DB           int
    tls.ClientConfig

    Connectivity    ConnectivityCheck
    ResponseTime    ResponseTimeCheck
    Role            RoleCheck
    ConnectedClients CountCheck
    BlockedClients   CountCheck
    UsedMemory       MemoryUsageCheck
    RejectedConn     CountCheck
    MasterLink       MasterLinkCheck
    ConnectedSlaves  MinCountCheck
    EvictedKeys      CountCheck
    ExpiredKeys      CountCheck
    OpsPerSecond     OpsPerSecondCheck
    Persistence      PersistenceCheck
}
```

### 认证与数据库

- `password` 非空时执行 `AUTH`
- `username + password` 同时配置时走 Redis ACL 模式 `AUTH user pass`
- `db > 0` 时执行 `SELECT <db>`

### TLS

复用项目现有 `tls.ClientConfig`：

- `use_tls`
- `tls_ca`
- `tls_cert`
- `tls_key`
- `tls_server_name`
- `insecure_skip_verify`

适用于 Redis 原生 TLS、stunnel、云 Redis 代理等场景。

## Init() 校验

1. `targets` 允许 `host` 或 `host:port`
2. 未显式指定端口时自动补 `:6379`
3. `response_time.warn_ge < critical_ge`
4. `role.expect` 只允许 `master`、`slave`、`replica`（内部统一成 `slave`）
5. `connected_clients` / `blocked_clients` 阈值必须非负，且 `warn < critical`
6. `db >= 0`

## Gather() 逻辑

对每个 target：

1. 建立 TCP/TLS 连接
2. 如有需要执行 `AUTH`
3. 如有需要执行 `SELECT`
4. 执行 `PING`
5. 产出 `connectivity` 和 `response_time`
6. 如启用 `role` / `master_link_status`，执行 `INFO replication`
7. 如启用 `connected_clients` / `blocked_clients`，执行 `INFO clients`
8. 如启用 `used_memory`，执行 `INFO memory`
9. 如启用 `rejected_connections` / `evicted_keys` / `expired_keys` / `instantaneous_ops_per_sec`，执行 `INFO stats`
10. 如启用 `connected_slaves`，复用 `INFO replication`
11. 如启用 `persistence`，执行 `INFO persistence`

### 失败处理

- 任一步骤失败，`connectivity` 直接告警
- `INFO` 查询失败时，对对应维度产出 Critical，避免静默漏报

### 持久化检查规则

- `loading = 1` 直接告警
- `rdb_last_bgsave_status != ok` 告警
- `aof_enabled = 1` 且 `aof_last_write_status != ok` 告警
- 其他情况为 Ok

### 增量计数器检查规则

- `evicted_keys`、`expired_keys` 和 `rejected_connections` 使用两个采集周期之间的 delta，不直接使用 Redis 启动以来累计值
- 首次采集只建立 baseline，产出 Ok 事件，delta=0
- 如果计数器回绕或 Redis 重启导致值变小，本周期按 delta=0 处理

### connected_slaves 检查规则

- 使用 `warn_lt` / `critical_lt`
- 适用于 master 节点
- 示例：`warn_lt = 2`, `critical_lt = 1` 表示副本数少于 2 预警，少于 1 严重告警

## 默认策略

- `connectivity.severity = "Critical"`
- `role.severity = "Warning"`，因为角色漂移是否致命依赖拓扑；用户可提升为 Critical
- `master_link_status.severity = "Warning"`，复制拓扑问题通常先预警
- `persistence.severity = "Critical"`，RDB/AOF 失败通常意味着数据安全风险
- `connected_slaves` 没有单独 severity，按阈值走 Warning/Critical
- `evicted_keys` / `expired_keys` 因为是 delta 阈值，建议结合业务流量设置，不建议照抄固定值
- 其他阈值默认关闭，由用户按容量和业务模型设置

## 未来规划

### 监控检查维度扩展

| 优先级 | 维度 | check label | 数据来源 | 说明 |
| --- | --- | --- | --- | --- |
| P0 | 复制延迟 | `redis::repl_lag` | `INFO replication` | `master_repl_offset - slave_repl_offset`（replica 侧）或 `slave0:offset` 差值（master 侧）。复制延迟是 Redis 运维最高频的告警需求之一，阈值单位为字节偏移量 |
| P0 | 内存碎片率 | `redis::mem_frag_ratio` | `INFO memory` | `mem_fragmentation_ratio`。> 1.5 表示碎片严重（RSS 远大于实际数据），< 1.0 表示 swap 可能介入。与 `used_memory` 互补 |
| P1 | maxmemory 使用百分比 | `redis::used_memory_pct` | `INFO memory` | `used_memory / maxmemory * 100`。比绝对值更通用——不同实例 maxmemory 不同，百分比是统一语言。`maxmemory = 0` 时跳过 |
| P2 | Cluster 状态 | `redis::cluster_state` | `CLUSTER INFO` | `cluster_state != ok` 即告警。仅在 Redis 开启 Cluster 模式时生效（`INFO server` 的 `redis_mode` 为 `cluster`），否则静默跳过 |
| P3 | Keyspace 命中率 | `redis::keyspace_hitrate` | `INFO stats` | `keyspace_hits / (keyspace_hits + keyspace_misses) * 100`。业务相关性强，默认关闭，由用户按场景开启 |

实现要点：
- `repl_lag` 需要区分 master 和 replica 两个视角：replica 直接读 `master_repl_offset` 和 `slave_repl_offset`；master 需解析 `slaveN:` 行
- `mem_frag_ratio` 的阈值方向特殊：既有 `warn_ge`（碎片高）又有 `warn_lt`（可能 swap），需要新的阈值类型 `RangeCheck`
- `used_memory_pct` 在 `maxmemory = 0`（无限制）时不产出事件，避免除零和误报
- `cluster_state` 需先探测 Redis 是否运行在 Cluster 模式，非 Cluster 模式跳过，不报错

### 诊断工具扩展

| 优先级 | 工具 | Redis 命令 | 说明 |
| --- | --- | --- | --- |
| P0 | `redis_bigkeys_scan` | `SCAN` + `MEMORY USAGE` | 内存告警时 AI 最需要的能力——定位大 key。使用 `SCAN` 采样（默认 1000 个 key）+ `MEMORY USAGE` 估算大小，按类型分组输出 Top N。比 `redis-cli --bigkeys` 更可控（可限制采样数量和耗时），且不阻塞 Redis。需要 Redis 4.0+ |
| P1 | `redis_cluster_info` | `CLUSTER INFO` + `CLUSTER NODES` | Cluster 模式诊断必备。输出 cluster_state、slots 分配、节点拓扑、故障节点。仅在 Cluster 模式下可用 |

实现要点：
- `redis_bigkeys_scan` 必须有采样上限（默认 `max_keys=1000`）和总耗时上限（per-tool timeout 保护），避免在大实例上卡住
- `redis_bigkeys_scan` 的 `MEMORY USAGE` 对大 key 本身可能较慢（O(N) 对集合类型），对超过阈值的 key 只报告类型和元素数，不调用 `MEMORY USAGE`
- `redis_cluster_info` 需处理非 Cluster 模式的优雅降级（返回提示而非报错）

### 已完成的 LLM 工具调用优化

针对 LLM 驱动诊断的特点（每轮 tool call 都是一次完整 API 往返），对诊断工具做了以下优化：

1. **PreCollector 预注入**：诊断触发时自动执行 `INFO all`，结果注入 AI 系统提示。LLM 无需第一轮调 `redis_info` 就能直接开始分析，省掉最昂贵的信息收集轮次
2. **合并 `redis_memory_doctor` + `redis_memory_stats` → `redis_memory_analysis`**：内存诊断时这两个命令几乎永远被一起调用，合并后省掉一轮 tool call
3. **删除 `redis_dbsize`**：`redis_info section=keyspace` 已包含每个 db 的 keys/expires/avg_ttl，比 DBSIZE 更有信息量
4. **取消 `redis_keyspace_stats`**：与 `redis_info section=keyspace` 完全重复，不再单独实现
5. **诊断路线图**：在 prompt 中为不同告警类型提供推荐的工具调用路径，引导 LLM 首轮并行调用正确的工具组合
6. **优化 `redis_info` description**：明确列出每个 section 的内容，减少 LLM 选错 section 的概率

当前 Redis 工具清单（6 个）：`redis_info`, `redis_slowlog`, `redis_client_list`, `redis_config_get`, `redis_latency`, `redis_memory_analysis`

### 明确不做

- `redis_debug_object` — 需要 DEBUG 权限，安全风险高
- `redis_rdb_analysis` — 分析 RDB 文件过重，不适合 agent 侧实时执行
- `redis_keyspace_stats` — 与 `redis_info section=keyspace` 重复，不单独实现
- Keyspace notification 监听 — 持续订阅违背"按需诊断"边界
- Sentinel 深度集成 — Sentinel 是独立组件，catpaw 只关注单实例健康

## 参考

- Redis `PING`
- Redis `AUTH`
- Redis `SELECT`
- Redis `INFO replication`
- Redis `INFO clients`
- Redis `INFO memory`
- Redis `INFO stats`
- Redis `INFO persistence`
- Redis `CLUSTER INFO` / `CLUSTER NODES`
- Redis `SCAN` / `MEMORY USAGE`（4.0+）
- Redis `LATENCY LATEST`
- Redis `MEMORY DOCTOR` / `MEMORY STATS`
