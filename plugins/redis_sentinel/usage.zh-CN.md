# Redis Sentinel 插件使用说明（设计草案）

> 当前 `redis_sentinel` 插件尚未实现；本文是基于
> [`design.md`](./design.md) 的中文版使用草案，用于指导后续实现、
> 配置模板和测试环境设计。

## 概述

`redis_sentinel` 插件用于监控和诊断 Redis Sentinel 本身。

它的监控目标是 Sentinel 进程，通常是 `host:26379`，而不是 Redis
数据节点的 `6379` 端口。

这个插件和现有 `redis` 插件的职责分工应该明确：

- `redis` 插件负责 Redis 数据节点
- `redis_sentinel` 插件负责 Sentinel 控制平面

## 为什么 Sentinel 要单独做插件

Sentinel 和 Redis 数据节点虽然都使用 RESP 协议，但运维语义完全不同：

- 目标进程不同：Sentinel 不是 Redis data node
- 默认端口不同：通常为 `26379`
- 命令集合不同：核心是 `SENTINEL *`、`ROLE`
- 健康语义不同：重点是 quorum、peer 发现、主节点解析、failover 状态

如果继续往 `redis` 插件里塞 Sentinel 逻辑，会导致：

- 用户不清楚 target 到底是 Redis 还是 Sentinel
- 默认告警规则混合数据面和控制面语义
- 配置文件和文档变复杂，违背 catpaw 的“清晰、开箱即用”原则

因此推荐把 Sentinel 作为独立插件实现。

## 默认监控策略

这个插件应遵循 catpaw 一贯的策略：

- 默认检查尽量低成本、低误报
- workload 或拓扑策略相关的检查默认关闭
- 周期采集不做订阅型、持续型或重枚举型操作

### 默认开启的检查

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::ckquorum`
- `redis_sentinel::masters_overview`
- `redis_sentinel::master_sdown`
- `redis_sentinel::master_odown`
- `redis_sentinel::master_addr_resolution`

### 默认关闭的检查

- `redis_sentinel::peer_count`
- `redis_sentinel::known_replicas`
- `redis_sentinel::known_sentinels`
- `redis_sentinel::failover_in_progress`
- `redis_sentinel::tilt`

原因很简单：这些检查往往依赖你的部署策略、Sentinel 数量、master 的标准副本数，
不适合开箱即用就强行告警。

## 支持的检查项

### 节点级检查

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::ckquorum`
- `redis_sentinel::masters_overview`
- `redis_sentinel::peer_count`
- `redis_sentinel::tilt`

### 按 master 维度的检查

- `redis_sentinel::master_sdown`
- `redis_sentinel::master_odown`
- `redis_sentinel::master_addr_resolution`
- `redis_sentinel::known_replicas`
- `redis_sentinel::known_sentinels`
- `redis_sentinel::failover_in_progress`

## 诊断工具

设计中的只读诊断工具包括：

- `sentinel_masters`
- `sentinel_master`
- `sentinel_replicas`
- `sentinel_sentinels`
- `sentinel_ckquorum`
- `sentinel_get_master_addr_by_name`
- `sentinel_info`

这些工具都只用于诊断、`inspect` 或 AI 分析，不进入周期采集。

## 快速开始（目标形态）

下面是未来建议支持的最小配置形态：

```toml
[[instances]]
targets = ["10.0.0.10:26379", "10.0.0.11:26379", "10.0.0.12:26379"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"
```

这个配置应当默认启用：

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::ckquorum`
- `redis_sentinel::masters_overview`
- 针对 `mymaster` 的 `master_sdown` / `master_odown` / `master_addr_resolution`

## 推荐的配置模型

### 实例级字段

建议支持这些常用字段：

- `targets`：Sentinel 地址，支持 `host` 或 `host:port`
- `concurrency`：单个 instance 的并发检查数
- `timeout`：建连和写超时
- `read_timeout`：读超时
- `username`：Sentinel ACL 用户名
- `password`：Sentinel 密码
- `interval`
- `labels`

### `masters`

推荐显式配置要关注的 master 名称：

```toml
[[instances.masters]]
name = "mymaster"

[[instances.masters]]
name = "cache-master"
```

这么做的好处是：

- 配置语义清楚
- `CKQUORUM` 和主节点解析都能稳定执行
- 事件可以自然带上 `master_name`
- 不会因为 Sentinel 端临时发现或未发现某个 master 而让行为变得不可预测

## 常见配置示例

### 1. 最小可用 Sentinel 监控

```toml
[[instances]]
targets = ["10.0.0.10:26379"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"
```

### 2. 多 Sentinel 节点监控

```toml
[[instances]]
targets = [
  "10.0.0.10:26379",
  "10.0.0.11:26379",
  "10.0.0.12:26379",
]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"
```

### 3. 显式开启 peer 数量检查

```toml
[[instances]]
targets = ["10.0.0.10:26379"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"

[instances.peer_count]
warn_lt = 2
critical_lt = 1
```

### 4. 显式开启已知 replica 数量检查

```toml
[[instances]]
targets = ["10.0.0.10:26379"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"

[instances.known_replicas]
warn_lt = 2
critical_lt = 1
```

## 默认检查项说明

### `redis_sentinel::connectivity`

- 建连后执行 `PING`
- 默认严重级别建议为 `Critical`

### `redis_sentinel::role`

- 执行 `ROLE`
- 期望返回角色为 `sentinel`
- 如果不是 `sentinel`，说明目标不是 Sentinel，直接 `Critical`

### `redis_sentinel::ckquorum`

- 对每个配置的 master 执行 `SENTINEL CKQUORUM <master>`
- 成功返回 `Ok`
- 若返回 `NOQUORUM`、`NOGOODSLAVE` 等错误，则直接 `Critical`

这是 Sentinel 最值得默认开启的控制面健康检查之一。

### `redis_sentinel::masters_overview`

- 执行 `SENTINEL MASTERS`
- 如果一个 master 都没有返回，应视为异常
- 正常情况下输出 master 数量摘要

### `redis_sentinel::master_sdown`

- 从 `SENTINEL MASTERS` 的 flags 判断
- 如果包含 `s_down`，表示这个 Sentinel 主观认为 master 不可用
- 建议默认告警级别为 `Warning`

### `redis_sentinel::master_odown`

- 从 `SENTINEL MASTERS` 的 flags 判断
- 如果包含 `o_down`，表示已达到客观下线条件
- 建议默认告警级别为 `Critical`

### `redis_sentinel::master_addr_resolution`

- 执行 `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`
- 如果解析不到当前主节点地址，应视为严重问题

## 为什么某些检查默认关闭

### `peer_count`

Sentinel 节点数量和 peer 可见数量很依赖部署方式。对某些环境来说，少一个 peer
是事故；对另一些环境来说只是临时维护窗口。因此默认关闭更稳妥。

### `known_replicas`

master 应该有几个 replica，取决于你的 Redis 拓扑策略，不适合做成统一默认。

### `failover_in_progress`

failover 本身不一定是坏事。如果默认直接告警，很容易制造噪音。

### `tilt`

这是更偏专家级运维信号，建议只在明确需要时开启。

## 周期采集的开销约束

周期 `Gather()` 建议只允许使用这些命令：

- `PING`
- `ROLE`
- `SENTINEL MASTERS`
- `SENTINEL CKQUORUM <master>`
- `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`

这些命令足够覆盖默认健康检查，而且成本可控。

以下命令更适合默认关闭或只用于诊断：

- `SENTINEL SENTINELS <master>`
- `SENTINEL REPLICAS <master>`
- `INFO`
- Pub/Sub 订阅

## 诊断建议

下面是推荐的诊断路线：

- quorum 异常：`sentinel_ckquorum` + `sentinel_sentinels`
- master 下线：`sentinel_master` + `sentinel_get_master_addr_by_name`
- replica 可见性问题：`sentinel_replicas`
- Sentinel 之间视图不一致：`sentinel_masters` + `sentinel_sentinels`

诊断预采集建议只做：

- `ROLE`
- `SENTINEL MASTERS`

不要在预采集里默认把所有 `REPLICAS` / `SENTINELS` 都拉一遍，否则很容易扩大
token 和采集开销。

## 测试环境建议

建议单独准备一套 Docker Compose 测试环境：

- 1 个 Redis master
- 2 个 Redis replica
- 3 个 Sentinel 节点

重点验证：

- quorum 正常
- `ROLE == sentinel`
- master 地址解析正常
- 停掉 master 后 Sentinel 的 `sdown` / `odown` / failover 表现

## 相关文档

- [`design.md`](./design.md)
