# Redis Sentinel 插件使用说明（实现定稿）

> `redis_sentinel` 插件已经实现；本文基于
> [`design.md`](./design.md) 整理，用于配置、联调和运维使用。

## 概述

`redis_sentinel` 插件用于监控和诊断 Redis Sentinel 本身。

它的目标是 Sentinel 进程，通常为 `host:26379`，而不是 Redis 数据节点
的 `6379` 端口。

这个插件和现有 `redis` 插件的职责分工明确：

- `redis` 插件负责 Redis 数据面节点
- `redis_sentinel` 插件负责 Sentinel 控制面节点

## 设计依据与最佳实践

本设计对齐以下业界共识：

- `SENTINEL CKQUORUM <master>` 应作为 Sentinel 的一等健康检查
- 默认监控既要覆盖 Sentinel 节点本身，也要覆盖它对 master 的判断
- 周期采集必须保持 pull 型、轻量、可边界控制
- 依赖拓扑规模和运维策略的检查默认关闭
- 显式配置的 `masters` 应视为期望状态，发现结果视为观测状态

文档同时默认这些运维事实成立，但插件本身不强制校验：

- 生产环境通常至少有 3 个 Sentinel 节点
- Sentinel 节点应尽量分布在不同故障域
- NAT、DNS、`announce-ip` 配置问题会直接影响 peer 可见性和 master
  地址解析

## 默认监控策略

这个插件遵循 catpaw 一贯的策略：

- 默认检查尽量低成本、低误报
- 周期采集不做订阅型、持续型或重枚举型操作
- 与具体部署策略强相关的检查默认关闭

### 默认开启的检查

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::masters_overview`
- `redis_sentinel::ckquorum`
- `redis_sentinel::master_sdown`
- `redis_sentinel::master_odown`
- `redis_sentinel::master_addr_resolution`

### 默认关闭的检查

- `redis_sentinel::peer_count`
- `redis_sentinel::known_replicas`
- `redis_sentinel::known_sentinels`
- `redis_sentinel::failover_in_progress`
- `redis_sentinel::tilt`

原因很简单：这些检查往往依赖 Sentinel 数量、Replica 标准数、维护窗口和
运维策略，不适合开箱即用就强制告警。

## 事件模型

### 节点级事件

适用于：

- `redis_sentinel::connectivity`
- `redis_sentinel::role`
- `redis_sentinel::masters_overview`
- `redis_sentinel::tilt`

标签：

- `check`
- `target`

### 按 master 维度事件

适用于：

- `redis_sentinel::ckquorum`
- `redis_sentinel::master_sdown`
- `redis_sentinel::master_odown`
- `redis_sentinel::master_addr_resolution`
- `redis_sentinel::peer_count`
- `redis_sentinel::known_replicas`
- `redis_sentinel::known_sentinels`
- `redis_sentinel::failover_in_progress`

标签：

- `check`
- `target`
- `master_name`

重要约定：

- `ckquorum` 明确是按 `master` 维度出事件，不做多 master 聚合
- 一个 Sentinel target 在一次 `Gather()` 中可以产生多个 per-master 事件

## `masters` 的语义

这是实现里必须写死的约定。

### 1. 显式配置优先

如果配置了 `[[instances.masters]]`，那么这组名字就是该 instance 的期望状态。

含义是：

- 周期采集中的 per-master 检查只对这些名字执行
- Sentinel 当前发现了别的 master，也不会默认为它们产出 per-master 事件
- 但 `masters_overview` 仍然可以汇报实际发现到的 master 总数

### 2. 未配置 `masters` 时不做 per-master 告警

如果没有配置 `masters`，插件仍然会执行 node 级检查，但不会产出任何
per-master 告警事件。

这样做是为了：

- 保证 `master_name` 来源稳定，避免 AlertKey 随 discovery 漂移
- 确保恢复事件一定能和历史告警用同一个 AlertKey 闭环

在这种模式下，`SENTINEL MASTERS` 只用于：

- `masters_overview`
- diagnose / inspect 阶段的上下文展示

### 3. 显式配置但 Sentinel 看不到时按异常处理

如果配置了某个 `master`，但该 Sentinel 的 `SENTINEL MASTERS` 里没有它：

- `ckquorum` 应继承 `ckquorum.severity`
- `master_sdown` 应输出 `Critical`
- `master_odown` 应输出 `Critical`
- `master_addr_resolution` 应输出 `Critical`

建议描述：

- `configured master mymaster is not present in SENTINEL MASTERS`

原因是这通常代表以下问题之一：

- Sentinel 配置漂移
- Sentinel 已经失去对期望 master 的监控
- 目标接错了环境

## 配置模型

### 实例级字段

建议支持这些字段：

- `targets`
- `concurrency`
- `timeout`
- `read_timeout`
- `username`
- `password`
- `labels`
- TLS client config
- `masters`

明确不支持这些 Redis data-plane 字段：

- `db`
- `mode`
- `cluster_name`

### target 规范化

实现约定：

- `host:port` 原样使用
- bare `host` 自动补成 `host:26379`
- 规范化后重复的 target 应在初始化时报错

### 传输与鉴权

实现应复用 `plugins/redis` 的连接模式：

- TCP/TLS 建连
- ACL `AUTH`
- timeout / read_timeout
- 有边界的 RESP 解析

但不要执行 `SELECT`，因为 Sentinel 不是逻辑 DB 端点。

## 推荐配置示例

### 最小可用配置

```toml
[[instances]]
targets = ["10.0.0.10"]
password = "${SENTINEL_PASSWORD}"

[[instances.masters]]
name = "mymaster"
```

这里 `10.0.0.10` 会自动规范化为 `10.0.0.10:26379`。

### 多 Sentinel 节点

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

### 只做 node 级检查的最小配置

```toml
[[instances]]
targets = ["10.0.0.10:26379"]
password = "${SENTINEL_PASSWORD}"
```

这种配置可以跑，但只会产出 node 级事件；如果你希望有 `ckquorum`、
`master_sdown`、`master_odown`、`master_addr_resolution` 这类 per-master
告警，必须显式配置 `masters`。

## 检查项与语义

### `redis_sentinel::connectivity`

- 建连后执行 `PING`
- 默认严重级别：`Critical`

### `redis_sentinel::role`

- 执行 `ROLE`
- 期望第一个 token 是 `sentinel`
- 如果不是 `sentinel`，直接 `Critical`

### `redis_sentinel::masters_overview`

- 执行 `SENTINEL MASTERS`
- 如果返回 0 个 master，默认输出 `Warning`
- 可通过配置把空结果升级为 `Critical`
- 正常时输出 `Ok`，并携带 master 数量摘要

### `redis_sentinel::ckquorum`

- 对每个有效 master 执行 `SENTINEL CKQUORUM <master>`
- 成功返回 `Ok`
- 如果返回 `NOQUORUM`、`NOGOODSLAVE` 等错误，输出 `Critical`
- 如果显式配置的 master 不存在于 `SENTINEL MASTERS`，输出 `Critical`

这是 Sentinel 默认监控里最关键的控制面健康检查之一。

### `redis_sentinel::master_sdown`

- 从 `SENTINEL MASTERS` 的 `flags` 判断
- 包含 `s_down` 时输出 `Warning`
- 否则输出 `Ok`
- 如果显式配置的 master 缺失，输出 `Critical`

### `redis_sentinel::master_odown`

- 从 `SENTINEL MASTERS` 的 `flags` 判断
- 包含 `o_down` 时输出 `Critical`
- 否则输出 `Ok`
- 如果显式配置的 master 缺失，输出 `Critical`

### `redis_sentinel::master_addr_resolution`

- 执行 `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`
- 如果解析不到地址或格式非法，输出 `Critical`
- 正常时输出 `Ok`

## 默认关闭检查的配置约定

### 统一配置形态

为了让实现和 partial merge 更可控，建议把检查配置收敛成三类。

### 1. `enabled + severity`

适用于：

- `role`
- `ckquorum`
- `master_sdown`
- `master_odown`
- `master_addr_resolution`
- `failover_in_progress`
- `tilt`

示例：

```toml
[instances.tilt]
enabled = true
severity = "Warning"
```

### 2. `enabled + empty_severity`

适用于：

- `masters_overview`

示例：

```toml
[instances.masters_overview]
enabled = true
empty_severity = "Critical"
```

### 3. `enabled + warn_lt + critical_lt`

适用于：

- `peer_count`
- `known_replicas`
- `known_sentinels`

示例：

```toml
[instances.peer_count]
enabled = true
warn_lt = 2
critical_lt = 1
```

规则：

- 这类检查默认关闭
- 启用时，`warn_lt` 和 `critical_lt` 至少要设置一个
- 如果两个都设置，要求 `critical_lt < warn_lt`

## 周期采集流程

每个 target 的建议采集顺序：

1. 建连、可选 TLS、可选 ACL 认证
2. `PING`
3. `ROLE`
4. `SENTINEL MASTERS`
5. 产出 `masters_overview`
6. 计算本次有效 master 集合
7. 对每个有效 master 运行：
   - `SENTINEL CKQUORUM <master>`
   - 基于 `SENTINEL MASTERS` flags 做 `sdown/odown`
   - `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`
8. 只有在显式开启高级检查时，才补拉 `REPLICAS` / `SENTINELS` / `INFO`

并发、hung gather 检测、timeout 预算可以直接参考 `plugins/redis`。

## 周期采集的开销约束

周期 `Gather()` 只应允许这些命令：

- `PING`
- `ROLE`
- `SENTINEL MASTERS`
- `SENTINEL CKQUORUM <master>`
- `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`

以下命令更适合默认关闭或只用于诊断：

- `SENTINEL SENTINELS <master>`
- `SENTINEL REPLICAS <master>`
- `SENTINEL MASTER <master>`
- `INFO`
- Pub/Sub 订阅

## 诊断工具

建议实现以下只读诊断工具。

核心原则不是“一条 Redis 命令一个 tool”，而是“让 LLM 一轮拿到足够多的
高信号信息”。

- `sentinel_overview`
- `sentinel_master_health`
- `sentinel_replicas`
- `sentinel_sentinels`
- `sentinel_info`

这些工具都只用于诊断，不进入周期采集。

### 推荐颗粒度

#### `sentinel_overview`

- target 维度总览工具
- 返回 `ROLE` 和 `SENTINEL MASTERS` 的紧凑摘要
- 适合第一轮判断“这个 Sentinel 自己是否正常、它在监控哪些 master”

#### `sentinel_master_health`

- master 维度聚合工具
- 合并 `SENTINEL MASTER <master>`、`SENTINEL CKQUORUM <master>`、
  `SENTINEL GET-MASTER-ADDR-BY-NAME <master>`，并附带 replicas / peers
  的紧凑计数
- 适合 quorum、master down、地址解析等大多数 master 相关告警

#### `sentinel_replicas`

- 只在需要详细看 replica 列表时调用
- 不放进第一轮默认调用，避免输出过大

#### `sentinel_sentinels`

- 只在需要详细看 peer Sentinel 列表时调用
- 主要用于 quorum 异常或 Sentinel 视图不一致的深入排查

#### `sentinel_info`

- 高级兜底工具
- 只在前面几类工具不足以解释现象时调用

## 诊断预采集

建议只预采集：

- `ROLE`
- `SENTINEL MASTERS`

不要在预采集里默认把所有 `REPLICAS` / `SENTINELS` 都拉一遍，否则会扩大
token 和采集开销。

## 推荐的诊断路线

- 通用或上下文不明的 Sentinel 告警：先调 `sentinel_overview`
- quorum 异常：先调 `sentinel_master_health`，必要时再调 `sentinel_sentinels`
- master 下线：先调 `sentinel_master_health`
- replica 可见性问题：先调 `sentinel_master_health`，必要时再调 `sentinel_replicas`
- Sentinel 之间视图不一致：先调 `sentinel_overview`，必要时再调 `sentinel_sentinels`

首轮原则：

- 如果告警已经带了 `master_name`，优先调 `sentinel_master_health`
- 如果告警只说 Sentinel 异常，没有明确 master，优先调 `sentinel_overview`
- 不要第一轮就把多个命令型工具串着调

## 测试环境建议

建议单独准备一套 Docker Compose 测试环境：

- 1 个 Redis master
- 2 个 Redis replica
- 3 个 Sentinel 节点

重点验证：

- quorum 正常
- `ROLE == sentinel`
- `masters_overview` 正常
- master 地址解析正常
- 停掉 master 后 Sentinel 的 `sdown` / `odown` / failover 表现
- 显式配置的 master 缺失时的 `Critical` 事件
- `announce-ip` / 解析异常场景

## Docker 联调环境

仓库里已经提供了一套可直接启动的 Sentinel 联调环境：

- [`testdata/sentinel/docker-compose.yml`](./testdata/sentinel/docker-compose.yml)

启动方式：

```bash
docker compose -f plugins/redis_sentinel/testdata/sentinel/docker-compose.yml up -d
```

默认暴露端口：

- Sentinel: `127.0.0.1:26379`
- Redis master: `127.0.0.1:6379`

跑 integration 测试：

```bash
REDIS_SENTINEL_TARGET=127.0.0.1:26379 \
REDIS_SENTINEL_MASTER=mymaster \
go test -tags integration ./plugins/redis_sentinel
```

常用手工检查：

```bash
docker compose -f plugins/redis_sentinel/testdata/sentinel/docker-compose.yml exec sentinel-tools \
  redis-cli -h sentinel-1 -p 26379 SENTINEL masters

docker compose -f plugins/redis_sentinel/testdata/sentinel/docker-compose.yml exec sentinel-tools \
  redis-cli -h sentinel-1 -p 26379 SENTINEL ckquorum mymaster
```

## 实现前结论

按本文约定，文档已经收敛到可以直接写代码的程度。实现阶段只需要继续沿用
`plugins/redis` 的模式，把以下点落成代码即可：

- 独立的 Sentinel config struct
- Sentinel accessor
- 轻量 gather 路径
- per-master 事件生成
- 只读 diagnose tools
- fake RESP 测试和 Docker 集成测试

## 相关文档

- [`design.md`](./design.md)
