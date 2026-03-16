# Redis 插件设计说明

## 适用范围

Redis 插件在 catpaw 标准事件模型之上，提供 Redis 语义感知的监控与诊断能力。

它面向以下部署形态：

- 单机 Redis
- 主从 Redis
- Redis 集群

它的目标不是替代 `redis-exporter` 或持续型指标采集系统。这个插件更关注异常发现，以及异常发生后的按需诊断。

## 设计目标

- 保持插件轻量，在 Redis 已经过载时也尽量安全
- 提供有用的默认行为，不强迫用户调很多阈值
- 支持节点级健康检查，并提供最小但实用的集群级健康识别
- 在告警与诊断阶段尽量复用同一个 Redis 访问器

## 监控模型

采集的基本单位仍然是单个 Redis 目标：`host:port`。

即使在 Redis 集群模式下也是如此。集群支持的方式是在连接到目标后，让插件具备集群感知能力。

### 单目标检查项

| 检查项 | 默认状态 | 说明 |
| --- | --- | --- |
| `redis::connectivity` | 开启 | 可选 `AUTH` / `SELECT` 之后执行 `PING` |
| `redis::response_time` | 关闭 | 基于阈值判定 |
| `redis::role` | 关闭 | 检查期望角色是否为 `master` / `slave` |
| `redis::repl_lag` | 关闭 | 与业务负载相关，单位为字节偏移差 |
| `redis::connected_clients` | 关闭 | 基于阈值判定 |
| `redis::blocked_clients` | 关闭 | 基于阈值判定 |
| `redis::used_memory` | 关闭 | 绝对内存字节数 |
| `redis::used_memory_pct` | 关闭 | 需要 `maxmemory > 0` |
| `redis::rejected_connections` | 关闭 | 按采集周期计算增量 |
| `redis::master_link_status` | 关闭 | 副本节点视角检查 |
| `redis::connected_slaves` | 关闭 | 主节点视角检查 |
| `redis::evicted_keys` | 关闭 | 按采集周期计算增量 |
| `redis::expired_keys` | 关闭 | 按采集周期计算增量 |
| `redis::instantaneous_ops_per_sec` | 关闭 | 基于阈值判定 |
| `redis::persistence` | 关闭 | 检查 RDB / AOF 健康状态 |

### 集群感知检查项

| 检查项 | 默认状态 | 说明 |
| --- | --- | --- |
| `redis::cluster_state` | 集群模式下开启 | 保守的硬故障检查 |
| `redis::cluster_topology` | 集群模式下开启 | 保守的拓扑硬故障检查 |

## 默认策略

Redis 插件遵循 catpaw 的两个原则：

- 开箱即用
- 监控本身不能成为负担

### 默认开启的检查项

- `redis::connectivity`
- 当 `INFO server` 返回 `redis_mode=cluster` 时，自动开启 `redis::cluster_state`
- 当 `INFO server` 返回 `redis_mode=cluster` 时，自动开启 `redis::cluster_topology`

### 默认关闭的检查项

所有依赖阈值、或者强依赖业务负载特征的检查项，都保持默认关闭：

- 响应时间
- 角色期望
- 复制延迟
- 客户端数量
- 内存阈值
- 增量型计数器
- 持久化状态

这样可以让默认配置更可预测，并避免在不同 Redis 部署风格下产生误报。

## 采集成本预算

周期性的 `Gather()` 路径会刻意控制得很窄。

### 正常采集路径使用的命令

- `PING`
- `INFO server`
- `INFO replication`
- `INFO clients`
- `INFO memory`
- `INFO stats`
- `INFO persistence`
- `CLUSTER INFO`
- 仅当启用了集群拓扑检查时，才执行 `CLUSTER NODES`

### 明确不放入 `Gather()` 的命令

- `SCAN`
- `MEMORY USAGE`
- `CLIENT LIST`
- `SLOWLOG GET`
- 任何大规模枚举类命令

这些命令仍然可以用于诊断，但不会进入周期监控路径。

## 配置模型

关键字段包括：

- `targets`
- `username` / `password`
- `db`
- `mode = auto | standalone | cluster`
- `cluster_name`
- 标准的超时、并发和告警设置

### Redis 模式

- `auto`：通过 `INFO server` 自动识别是否为集群模式
- `standalone`：绝不执行集群检查
- `cluster`：要求目标必须工作在集群模式，否则产出明确错误事件

### 集群名称

`cluster_name` 只是事件标签，用于聚合和提升告警可读性，不影响连接行为。

## 集群语义

### `redis::cluster_state`

使用 `CLUSTER INFO`。

默认规则：

- `cluster_state != ok` 时，记为 `Critical`

### `redis::cluster_topology`

使用 `CLUSTER INFO` 配合 `CLUSTER NODES`。

默认规则集刻意保持保守：

- 存在 `fail` 节点时，记为 `Critical`
- `cluster_slots_fail > 0` 时，记为 `Critical`
- `cluster_slots_assigned != 16384` 时，记为 `Critical`
- 存在 `pfail` 节点时，记为 `Warning`

它不会默认假设以下条件一定成立：

- 副本数量必须满足某个固定值
- slot 分布必须完全均衡
- 流量和内存必须均衡

这些问题确实重要，但不适合作为通用默认规则。

## 阈值语义

### `redis::repl_lag`

- 单位是字节偏移差
- 副本视角：`master_repl_offset - slave_repl_offset`
- 主节点视角：解析所有 `slaveN:offset=...` 后取最大 lag
- 默认关闭

### `redis::used_memory_pct`

- 单位是 `maxmemory` 的百分比
- 只有在 `maxmemory > 0` 时才有意义
- 当 `maxmemory = 0` 时，插件输出 `Ok`，并附带跳过原因说明
- 默认关闭

### 增量型计数器

`rejected_connections`、`evicted_keys` 和 `expired_keys` 都按采集周期增量判断，而不是使用进程生命周期累计值。

第一次成功采集只建立基线，并输出 `Ok`。

## 诊断模型

Redis 插件暴露的诊断工具全部为只读工具。

### 低成本工具

- `redis_info`
- `redis_config_get`
- `redis_cluster_info`
- `redis_latency`
- `redis_slowlog`
- `redis_client_list`
- `redis_memory_analysis`

### 较重但有边界的工具

- `redis_bigkeys_scan`

`redis_bigkeys_scan` 仅用于诊断。它会使用有边界的 `SCAN` 采样和 `MEMORY USAGE`，在当前 Redis 节点上识别大 key。它永远不会进入周期性的 `Gather()`。

## 预采集策略

诊断阶段的 pre-collector 会采集：

- `INFO all`
- 如果目标处于集群模式，再额外采集 `CLUSTER INFO`

它刻意不预采集 `CLUSTER NODES`，因为这部分输出更大，更适合由 `redis_cluster_info` 在需要时按需获取。

## 校验资产

Redis 插件通过多层校验来验证：

- 单元测试：[`../redis_test.go`](../redis_test.go)
- 诊断测试：[`../diagnose_test.go`](../diagnose_test.go)
- 单机 / 主从 Docker 验证：[`../testdata/master-replica/docker-compose.yml`](../testdata/master-replica/docker-compose.yml)
- Redis 集群 Docker 验证：[`../testdata/cluster/docker-compose.yml`](../testdata/cluster/docker-compose.yml)
- 通过 `integration` 构建标签启用的集成测试：[`../redis_integration_test.go`](../redis_integration_test.go)

## 非目标

- exporter 风格的指标采集
- Sentinel 专用编排逻辑
- 周期性大 key 扫描
- 周期性客户端列表或 slowlog 扫描
- 默认假设固定的拓扑策略

## 建议的后续工作

以 catpaw 当前对 Redis 的支持范围来看，这个插件的功能已经基本完整。

后续演进应由真实用户需求驱动，而不是为了堆功能而堆功能。比较合理的下一步包括：

- 输出更结构化的集群拓扑摘要
- 增加更多故障注入型集成测试
- 更新顶层 `README` 文档
