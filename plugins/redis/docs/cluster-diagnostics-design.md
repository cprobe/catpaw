# 集群组件诊断设计

## 背景

catpaw agent 部署在每台机器上，天然以单节点为视角。但 Redis、MySQL、Kafka、Elasticsearch、MongoDB、PostgreSQL 等组件普遍以集群方式部署，故障的根因往往不在当前节点，而在集群拓扑的其他位置。

单节点视角的诊断会面临两个核心问题：

1. **信息不完整**：当前节点的 `CLUSTER NODES` 报告 peer 为 `fail`，但不知道 peer 是"进程挂了"还是"网络不通"还是"认证变更"
2. **AI 轮次浪费**：AI 每多一次 tool call 就是一次完整的 API 往返（1-3s + token 消耗），如果需要逐个探测 peer 才能做判断，轮次成本线性增长

本文档定义 catpaw 在集群场景下的诊断策略，适用于所有集群化部署的 remote 插件。

## 核心原则

### 原则一：Agent 做"协议可达的"，Brain 做"需要全局协调的"

Agent 通过组件自身的集群协议获取集群信息，不需要 Brain 介入：

```
Agent 连 Redis node-1
  → CLUSTER INFO   → 集群健康状态
  → CLUSTER NODES  → 全部节点的地址、角色、slot 分布、故障标记
  → 连 peer node-3 → INFO / SLOWLOG / MEMORY
```

Brain 负责跨 Agent 的协调和因果推断：

```
Agent-A 报告 Redis node-1 高延迟
Agent-B 报告 Redis node-2 复制延迟增大
Agent-C 报告 Redis node-3 宕机
  → Brain 综合判断：node-3 宕机是根因，node-1/node-2 的异常是故障传播
  → Brain 可让 Agent-C 检查 node-3 机器的 dmesg、OOM 日志
```

**划分依据**：如果一条 TCP 连接 + 几条协议命令就能拿到的信息，Agent 自己做；如果需要多台机器的 Agent 各自收集再汇总，Brain 来协调。

### 原则二：诊断工具做 enrichment，减少 AI 决策轮次

诊断工具不是越原子越好，而是要把 **AI 大概率需要的关联信息打包返回**。

**反面示例**（5 轮 AI 调用）：

```
Round 1: AI 调 redis_cluster_info → 拿到 CLUSTER NODES 文本
Round 2: AI 决定检查 node-3 → 调 redis_query_peer(node-3, ping)
Round 3: AI 发现 node-3 连不上 → 调 redis_query_peer(node-5, info) 看 slave 状态
Round 4: AI 再查 node-6 → 调 redis_query_peer(node-6, info)
Round 5: AI 综合判断，输出报告
```

**正面示例**（2 轮 AI 调用）：

```
Round 1: AI 调 redis_cluster_info → 拿到 CLUSTER NODES + 全部 peer 连通性 + 关键指标快照
Round 2: AI 综合判断，输出报告（或针对性深查某个 peer 的 SLOWLOG）
```

每省一轮 AI 调用 = 省 1-3 秒延迟 + 数千 token 成本。

### 原则三：try-with-same-auth + 优雅降级

连接 peer 节点时，使用当前 Instance 的认证信息。连接失败不是错误，而是**诊断数据**。

| 失败类型 | 含义 | AI 可推断 |
|---|---|---|
| `connection refused` | 进程未运行或端口未监听 | 节点宕机 |
| `connection timeout` | 网络不通或防火墙拦截 | 网络分区 |
| `auth failed` | 认证信息与当前节点不同 | 配置不一致（运维问题，非故障） |
| `i/o timeout` | 连上了但响应极慢 | 节点过载 |

**不需要** per-peer 认证配置。认证不同的节点在 catpaw 中应配置为不同的 Instance。

### 原则四：集群探测有边界

peer 探测只用于**诊断**，不用于**监控**。Gather 路径不连 peer。

| 维度 | Gather（监控） | Diagnose（诊断） |
|---|---|---|
| 目标 | 仅当前 target | 当前 target + peer 节点 |
| 频率 | 每 interval 周期执行 | 事件触发，低频 |
| 开销预算 | 严格受控（< 100ms） | 可适当放宽（总超时内即可） |
| peer 连接 | 不做 | 并发探测，短超时，快速释放 |

## 集群快照工具设计

### 工具定位

集群快照工具是集群场景下的核心诊断工具。它在返回集群拓扑信息的同时，**自动并发探测所有 peer 节点**，将"拓扑 + 连通性 + 关键指标"打包为一个原子结果返回。

### 输出结构

分为三个区块，逐层递进：

```
[CLUSTER SUMMARY]             ← 一眼可判断的核心状态
cluster_state: ok
cluster_known_nodes: 6
fail_nodes: 1
pfail_nodes: 0

[CLUSTER INFO / TOPOLOGY]     ← 协议原始输出，供深度分析
...原有协议输出...

[PEER CONNECTIVITY]            ← 自动探测结果，省掉后续轮次
node_id   address          role    reachable  latency  snapshot
a1b2c3    10.0.0.1:6379    master  ✓          1ms      mem=2.1G/4G, clients=142, ops/s=12340
d4e5f6    10.0.0.2:6379    master  ✓          2ms      mem=1.8G/4G, clients=98, ops/s=9870
g7h8i9    10.0.0.3:6379    master  ✗          -        connection refused
j0k1l2    10.0.0.4:6379    slave   ✓          1ms      mem=2.0G/4G, clients=45, repl_lag=0
m3n4o5    10.0.0.5:6379    slave   ✓          3ms      mem=1.7G/4G, clients=38, repl_lag=128
p6q7r8    10.0.0.6:6379    slave   ✓          1ms      mem=1.9G/4G, clients=41, repl_lag=0
```

AI 一轮就拿到了：谁挂了、谁是它的 slave、其他节点是否健康。

### 探测实现约束

| 约束 | 值 | 原因 |
|---|---|---|
| 最大探测节点数 | 20 | 超大集群（100+ 节点）不逐一探测，只探测异常节点 + 采样 |
| 单节点探测超时 | 2-3s | fail 节点不能拖慢整个诊断 |
| 探测并发度 | 10 | 避免瞬间大量连接，对目标网络段产生压力 |
| 采集内容 | PING + 关键指标子集 | 够用且不重；不跑 INFO all，只取 server/memory/clients/replication 的核心字段 |
| 探测失败处理 | 记录错误类型 | 错误本身就是诊断数据 |
| 跳过自身 | 是 | 不重复探测当前已连接的 target |

### 超大集群降级策略

当 `cluster_known_nodes > max_probe_peers` 时，优先探测：

1. 带 `fail` 或 `pfail` 标记的节点（最可能是问题节点）
2. 当前节点的 master（如果自身是 slave）或 slave（如果自身是 master）
3. 其他节点按 node-id 排序取前 N 个

并在输出中标注 `[PARTIAL PROBE: 20/100 nodes probed, priority: fail/pfail nodes + related replicas]`。

## 按需深查 peer 工具设计

集群快照工具提供了 peer 的连通性和关键指标快照。当 AI 需要对特定 peer 做**深度诊断**（如查 SLOWLOG、大 Key 分析、内存详情）时，使用独立的 peer 查询工具。

### 工具定义

```
名称：{plugin}_query_peer
参数：
  - peer: string, required — peer 地址（从集群拓扑输出中获取）
  - command: string, required — 要执行的命令（白名单内）
  - args: string, optional — 命令参数
```

### 命令白名单

只允许只读诊断命令。以 Redis 为例：

| 命令 | 说明 |
|---|---|
| `info [section]` | 节点信息 |
| `slowlog [count]` | 慢查询 |
| `client_list` | 客户端列表 |
| `cluster_info` | 集群信息 |
| `memory_doctor` | 内存诊断 |
| `memory_stats` | 内存统计 |
| `latency` | 延迟事件 |
| `config_get [pattern]` | 配置查询（敏感字段脱敏） |

禁止任何写入、删除、修改操作。

### 连接生命周期

peer 连接是**临时的**：创建 → 执行命令 → 立即关闭。不复用 session 的主 Accessor。

```
session.Accessor  →  主 target 的持久连接（整个诊断会话生命周期）
peer 连接          →  临时连接（单次 tool call 内创建、使用、关闭）
```

## 各组件适用性

### 通用集群协议能力矩阵

| 组件 | 从单节点可获取的集群信息 | 快照工具应采集的 peer 指标 |
|---|---|---|
| **Redis Cluster** | `CLUSTER INFO` + `CLUSTER NODES`：全部节点地址、角色、slot、fail 标记 | PING + mem/clients/ops/repl_lag |
| **Redis 主从** | `INFO replication`：master 看 slaveN 列表，slave 看 master_link_status | PING + repl_offset/lag + mem |
| **MySQL 主从** | `SHOW REPLICA STATUS`：slave 看 master 连接和延迟 | PING + threads_running + repl_lag |
| **MySQL MGR** | `performance_schema.replication_group_members`：全组成员和状态 | PING + transactions_behind + member_state |
| **Elasticsearch** | `_cluster/health` + `_cat/nodes`：全节点状态 | PING + heap_pct/disk_pct/shard_count |
| **MongoDB 副本集** | `rs.status()`：全成员状态和 oplog 位点 | PING + oplog_lag + state |
| **MongoDB 分片** | `sh.status()`：全分片拓扑 | 仅 mongos 可查，不探测单个 shard |
| **Kafka** | `AdminClient.DescribeCluster()`：全 broker 列表和 controller | PING + partition_count/under_replicated |
| **PostgreSQL 主从** | `pg_stat_replication`（主）/ `pg_is_in_recovery()`（从） | PING + replay_lag + connections |

### 各组件实现指引

每个插件实现集群诊断时，遵循以下步骤：

**步骤 1：识别集群拓扑发现命令**

确定从当前节点获取全部 peer 地址的协议命令。这是集群快照工具的基础。

**步骤 2：定义 peer 快照指标**

选择 3-5 个最能反映 peer 健康状态的指标。标准：
- 一条命令就能拿到（不做多轮交互）
- 对故障定位有直接价值（内存、延迟、复制状态）
- 量级可控（不返回大文本）

**步骤 3：实现集群快照工具**

在现有的集群信息工具基础上，增加 `[PEER CONNECTIVITY]` 区块。遵循本文档的探测约束。

**步骤 4：实现 peer 深查工具**

注册 `{plugin}_query_peer` 工具，定义该组件的只读命令白名单。

**步骤 5：更新 DiagnoseHints**

在诊断路线图中增加集群场景的指引，例如：

```
- Cluster 告警 → 先调 redis_cluster_info 获取集群快照（含 peer 连通性），
  根据 PEER CONNECTIVITY 中的异常节点决定是否用 redis_query_peer 深查
```

## Agent 与 Brain 的职责边界

### Agent 负责

| 职责 | 说明 |
|---|---|
| 集群协议状态 | 通过协议命令获取集群健康、拓扑、角色信息 |
| Peer 连通性探测 | 并发 PING peer，报告连通性和关键指标 |
| Peer 按需深查 | AI 决定后，连 peer 执行只读诊断命令 |
| 单集群范围内的根因判断 | "node-3 挂了导致 cluster_state=fail" |

### Brain 负责

| 职责 | 说明 |
|---|---|
| 多 Agent 告警关联 | 同一集群不同节点的 Agent 告警合并为一个 Incident |
| 跨层因果推断 | Redis 慢 → 是 Redis 问题还是所在机器的磁盘/内存问题？ |
| 跨 Agent 诊断编排 | 让 node-3 的 Agent 查机器级别信息（dmesg、OOM 日志等） |
| 拓扑驱动的影响分析 | 基于 OTel 数据，判断故障对上下游服务的影响范围 |

### 交互模型

```
                    Brain
                      │
          ┌───────────┼───────────┐
          │           │           │
       Agent-A     Agent-B     Agent-C
          │           │           │
      Redis-1     Redis-2     Redis-3
          │
          ├── CLUSTER NODES → 拿到全拓扑
          ├── 探测 Redis-2 ✓ → 关键指标
          ├── 探测 Redis-3 ✗ → connection refused
          └── 报告：Redis-3 宕机导致 cluster_state=fail

      Brain 收到 Agent-A 报告 + Agent-C 的机器级告警
        → 综合判断：Redis-3 宕机原因是 OOM killer（Agent-C 的 dmesg 显示）
        → 影响范围：依赖 Redis-3 所在 slot 的服务 X、Y（Topo 数据）
```

## 认证信息处理

### 同集群认证一致性

| 组件 | 认证是否强制一致 | 原因 |
|---|---|---|
| Redis Cluster | **强制一致** | 节点间通信要求相同 ACL/requirepass |
| Redis 主从 | 通常一致 | 非强制，但运维实践上 99% 相同 |
| MySQL 主从/MGR | **可能不同** | 监控用户各节点独立创建 |
| Kafka | 通常一致 | SASL 认证 broker 级统一配置 |
| Elasticsearch | **强制一致** | 集群安全配置全局统一 |
| MongoDB 副本集 | **强制一致** | 用户数据全集群同步 |
| PostgreSQL 主从 | **可能不同** | pg_hba.conf 各节点独立 |

### 处理策略

**不引入 per-peer 认证配置**，遵循以下逻辑：

1. 使用当前 Instance 的认证信息连接 peer
2. 认证失败时，在探测结果中标注 `auth_failed`
3. AI 可据此判断"认证不一致"并在报告中提示运维关注
4. 认证确实不同的节点，用户应配置为独立的 Instance

理由：
- 集群认证不一致本身是一个运维问题，值得被诊断发现
- per-peer 认证配置会显著增加配置复杂度，违背"开箱即用"原则
- catpaw 的定位是诊断 agent，不是集群管理工具

## 设计决策记录

### 决策 1：peer 探测内嵌在集群信息工具中，而非独立工具

**选项 A**：集群信息工具只返回协议输出，peer 探测作为独立工具

**选项 B**（采用）：集群信息工具自动包含 peer 探测结果

**理由**：AI 在看到集群拓扑后，100% 会想知道异常节点是否可达。将这个"必然的下一步"内嵌到工具中，省掉 1-2 轮 AI 调用。这是 tool enrichment 原则的直接应用。

### 决策 2：peer 连接是临时的，不复用 session Accessor

**选项 A**：session 支持多 Accessor（主 + N 个 peer）

**选项 B**（采用）：peer 连接在单次 tool call 内创建和销毁

**理由**：
- peer 深查是低频操作，复用连接收益极小
- 多 Accessor 管理增加 session 复杂度和资源泄漏风险
- 临时连接模型更简单、更安全

### 决策 3：超大集群部分探测，而非全量探测

**选项 A**：无论集群多大，探测所有节点

**选项 B**（采用）：超过阈值时优先探测异常节点 + 关联副本

**理由**：100 节点集群全量探测需要 10+ 秒，超出诊断工具的合理执行时间。优先探测 fail/pfail 节点覆盖了绝大多数故障场景。

### 决策 4：Gather 路径不连 peer

**选项 A**：Gather 时也探测 peer，丰富告警信息

**选项 B**（采用）：Gather 仅检查当前 target，peer 探测只在诊断时做

**理由**：
- Gather 每 interval 执行一次，peer 探测会显著增加采集开销
- 违背"监控不能成为负担"原则
- Gather 的职责是发现异常，不是分析根因——后者交给诊断
