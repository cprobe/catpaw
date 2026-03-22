# Redis 出问题时，你到底该先看什么？

> **TL;DR**：Redis 出问题时，很多团队第一反应还是盯 QPS、CPU、内存曲线。但真正决定故障是否能被尽快定位的，往往是另一层语义更强的信号：角色是否正确、主从链路是否健康、`maxmemory` 是否逼近上限、`rejected_connections` 是否在增长、集群 slot 是否丢失、某个节点是不是已经 fail 了。catpaw 的 Redis 插件做的不是替代 `redis-exporter`，而是把这些面向故障的检查和按需诊断补上。

## Redis 故障，最容易踩的坑不是“没指标”，而是“看错指标”

很多 Redis 故障，表面上都长得差不多：

- 请求变慢了
- 超时变多了
- hit ratio 掉了
- 应用日志里开始刷 `READONLY`、`LOADING`、`OOM command not allowed`
- 集群里有一两个节点看起来“不太对”

这时候最常见的动作是打开 dashboard，看几条熟悉的曲线：

- QPS
- CPU
- used_memory
- 网络流量
- keyspace hits / misses

这些当然重要。

但问题在于，它们更适合回答：

> Redis 最近忙不忙？

而不是：

> Redis 现在到底是不是已经出故障了？如果出了，根因更可能在哪一层？

你会发现，值班真正需要的其实是下面这些问题的答案：

- 这个节点现在还能不能正常响应？
- 它到底是 master 还是 replica？
- replica 和 master 的复制链路还在不在？
- `maxmemory` 已经压到多高了？
- 被拒绝连接是不是在持续增长？
- 集群是不是已经有 fail / pfail 节点？
- slot 有没有丢失？
- 慢查询、大 key、客户端风暴是不是正在发生？

这就是 Redis 监控最容易被忽略的点：

**不是没有数据，而是缺少一套面向 Redis 语义和故障处理流程的视角。**

## 先把 Redis 监控分成两层：周期检查 和 按需诊断

我觉得 Redis 这类组件最好的监控方式，不是“能查的都周期扫一遍”，而是把动作分层。

### 第一层：周期检查

这层要回答的是：

- 有没有明确异常
- 现在要不要告警
- 对 Redis 本身的负担是否足够低

所以这层应该尽量轻，只跑低成本、强信号的动作。

### 第二层：按需诊断

这层要回答的是：

- 为什么异常会发生
- 是不是某个节点、某类 key、某类客户端出了问题
- 下一步该往慢查询、内存、集群拓扑还是客户端方向深挖

这层可以重一些，但应该只在告警触发或人工巡检时才执行。

catpaw 的 Redis 插件就是按这个思路设计的：

- 周期采集路径只做轻量检查
- `SCAN`、`MEMORY USAGE`、`SLOWLOG GET`、`CLIENT LIST` 这类更重的动作，不进周期采集
- 这些动作留到诊断阶段，由 AI 或 `inspect` 按需触发

这个边界非常关键。

因为 Redis 已经过载的时候，最不该做的就是监控系统自己再去给它加压。

## catpaw 对 Redis 的定位，非常明确

先把最容易误解的一点说清楚：

**catpaw 的 Redis 插件不是 `redis-exporter` 的替代品。**

它更关注三件事：

1. Redis 语义层的异常发现  
2. 告警时的标准化事件输出  
3. 故障发生后的只读诊断工具

也就是说，如果你要的是：

- 全量指标采集
- 长周期趋势分析
- Grafana 仪表盘

那还是 Prometheus + `redis-exporter` 更对路。

如果你要的是：

- 这个 Redis 节点是否已经明显异常
- 主从链路是不是断了
- cluster 状态是不是已经坏了
- 告警之后能不能顺手看 slowlog、clients、memory、bigkeys

那 catpaw 更适合补这一层。

## Redis 最值得盯的 4 类信号

如果你不想把 Redis 监控搞成一大堆低价值指标，最值得优先看的通常是这 4 类。

### 1. 连通性与基本可用性

这是最底层、也最先该确认的。

至少要知道：

- 这个节点能不能连接
- 认证是否正常
- `PING` 能不能过
- 响应时间是否已经异常抬高

catpaw 默认一定会做 `redis::connectivity`，这是最小闭环。

如果你只是想先知道 Redis 有没有明显挂掉，最小配置其实很短：

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"
```

这个配置至少会告诉你：

- Redis 还能不能连
- 如果它是 cluster 节点，还会自动做集群级硬故障检查

### 2. 角色与复制链路

很多 Redis 故障不是“进程挂了”，而是“角色和复制关系已经不对了”。

最常见的就是：

- 你以为它是 master，其实已经切成 replica 了
- replica 还活着，但 `master_link_status` 已经不再是 `up`
- 主节点还活着，但 `connected_slaves` 变少了
- 复制延迟在持续拉大

这类问题如果只盯 CPU/QPS，很容易看不出来。

catpaw 这层提供的检查比较实用：

- `redis::role`
- `redis::master_link_status`
- `redis::connected_slaves`
- `redis::repl_lag`

但这里也有一个非常正确的取舍：

**这些检查默认并不全部开启。**

因为复制延迟、连接副本数、角色期望，本身都和你的拓扑设计强相关，开箱即用地硬开，很容易误报。

例如主节点场景可以这么配：

```toml
[[instances]]
targets = ["10.0.0.10:6379"]
password = "your-password"

[instances.role]
expect = "master"
severity = "Warning"

[instances.connected_slaves]
warn_lt = 2
critical_lt = 1

[instances.persistence]
enabled = true
severity = "Critical"
```

副本节点则更关心链路状态：

```toml
[[instances]]
targets = ["10.0.0.11:6379"]
password = "your-password"

[instances.role]
expect = "slave"
severity = "Warning"

[instances.master_link_status]
expect = "up"
severity = "Warning"
```

### 3. 内存与容量边界

Redis 的很多事故，本质上是容量事故。

但真正值得监控的，不是“内存曲线高不高”这么简单，而是：

- `used_memory` 绝对值是否异常
- `used_memory_pct` 是否逼近 `maxmemory`
- `evicted_keys` 是否在持续增长
- `rejected_connections` 是否开始冒出来

这里有个很容易忽略的细节：

`used_memory_pct` 只有在 `maxmemory > 0` 时才有意义。

如果你的 Redis 根本没配 `maxmemory`，那这个百分比检查本来就不该乱报。

catpaw 在这点上做得比较克制：

- 如果 `maxmemory = 0`，不会硬报错
- 会输出 `Ok` 并说明这个检查被跳过

这比很多“见到指标就开规则”的做法要靠谱。

可以按需开启：

```toml
[[instances]]
targets = ["10.0.0.20:6379"]
password = "your-password"

[instances.used_memory_pct]
warn_ge = 80
critical_ge = 90

[instances.rejected_connections]
warn_ge = 1
critical_ge = 10

[instances.evicted_keys]
warn_ge = 1
critical_ge = 10
```

这里 `rejected_connections`、`evicted_keys`、`expired_keys` 还用了一个很重要的策略：

**按采集周期做 delta 判断，而不是拿 Redis 生命周期累计值直接比阈值。**

否则你会得到一堆没有值班意义的数字。

### 4. 集群状态与拓扑硬故障

如果你跑的是 Redis Cluster，最值得默认盯住的，往往不是“内存是不是均衡”，而是那些真正构成硬故障的信号：

- `cluster_state` 是否还是 `ok`
- 有没有 `fail` 节点
- 有没有 `pfail` 节点
- `cluster_slots_assigned` 是否还是 16384
- `cluster_slots_fail` 是否大于 0

catpaw 在这个点上有一个很好的设计选择：

**默认规则刻意保持保守。**

也就是它不会默认假设：

- 每个 master 一定要有固定数量 replica
- slot 一定要绝对均匀
- 各节点流量和内存一定要均衡

这些不是不重要，而是太依赖业务拓扑，拿来做通用默认规则很容易误导。

所以它默认只盯“硬故障”。

最小 cluster 配置也很简单：

```toml
[[instances]]
targets = ["10.0.0.20:6379"]
password = "your-password"
mode = "auto"
cluster_name = "prod-cache"
```

`mode = "auto"` 也是一个很实用的默认值：

- 如果目标是 standalone，就不跑 cluster 检查
- 如果目标是 cluster 节点，就自动启用 `cluster_state` 和 `cluster_topology`

这让 Redis 插件更像一个“能感知上下文”的检查器，而不是死板地套一套模板。

## 为什么有些动作绝对不该放进周期采集

如果你平时经常排 Redis，看到下面这些词一定不陌生：

- `SLOWLOG GET`
- `CLIENT LIST`
- `MEMORY USAGE`
- `SCAN`
- bigkeys

这些动作在诊断阶段都很有价值。

但有价值，不代表适合周期跑。

原因很简单：

- 它们更重
- 它们更依赖上下文
- 它们对线上实例的影响更需要谨慎

所以 catpaw 在 Redis 这块的边界非常合理：

### 周期采集里会跑的

- `PING`
- `INFO server`
- `INFO replication`
- `INFO clients`
- `INFO memory`
- `INFO stats`
- `INFO persistence`
- `CLUSTER INFO`
- 在需要拓扑检查时才跑 `CLUSTER NODES`

### 周期采集里明确不跑的

- `SCAN`
- `MEMORY USAGE`
- `CLIENT LIST`
- `SLOWLOG GET`
- 任何大规模枚举类命令

这也是为什么我觉得它更适合生产环境。

因为真正成熟的监控系统，不是“什么都能查”，而是“知道什么该周期查，什么只该按需查”。

## 真出故障时，Redis 更需要的是诊断工具，而不是再多一条曲线

当 Redis 告警真的打出来时，值班人通常最需要的是下面这些动作：

- 看一眼 `INFO`
- 看 cluster 快照
- 看 slowlog
- 看客户端列表
- 看关键配置
- 看内存分析
- 必要时做一次有边界的 bigkeys 检查

catpaw Redis 插件已经把这套工具注册成只读诊断工具：

- `redis_info`
- `redis_cluster_info`
- `redis_slowlog`
- `redis_client_list`
- `redis_config_get`
- `redis_latency`
- `redis_memory_analysis`
- `redis_bigkeys_scan`

这里最值得提的是两个点。

### 1. `redis_bigkeys_scan` 只在诊断时用

大 key 确实是线上很常见的问题。

但“大 key 扫描”绝不该被塞进周期采集。

catpaw 这里把它明确限定在诊断阶段，这个边界是对的。

### 2. 集群诊断不是只看当前节点

Redis Cluster 的真实问题，经常不在你现在连上的这个节点本身，而在它的 peer 上。

所以 `redis_cluster_info` 并不是只吐一段 `CLUSTER NODES` 文本就算完事。

它更适合做成一张集群快照：

- 集群总体状态
- fail / pfail 节点
- peer 连通性
- 每个 peer 的关键健康摘要

这样 AI 或值班人一轮就能大致判断：

- 是节点挂了
- 是网络分区
- 是认证不一致
- 还是某个 peer 单点过载

这比逐个节点人工点查要高效得多。

## 一篇文章带走的最小落地方案

如果你现在就想在生产环境里给 Redis 补一层真正有故障意义的监控，我建议按下面这个顺序上。

### 场景 1：你只有单机 Redis

先做：

- `connectivity`
- 可选 `response_time`
- 如果有 `maxmemory`，再开 `used_memory_pct`
- 如果你关心拒绝连接，再开 `rejected_connections`

### 场景 2：你跑主从 Redis

在单机场景基础上，再补：

- `role`
- `master_link_status`
- `connected_slaves`
- `persistence`
- 视业务决定是否开 `repl_lag`

### 场景 3：你跑 Redis Cluster

先保守地把这几项盯住：

- `cluster_state`
- `cluster_topology`
- 重要节点的 `used_memory_pct`
- 必要时再按业务启 `repl_lag`

不要一上来就默认把所有阈值都开了。

Redis 这类组件最怕的不是“少监控两个数”，而是“默认把一堆不懂业务含义的规则全开，最后谁都不信告警”。

## 这类文章最后一定要把定位说清楚

如果你只记住一句话，我希望是这句：

**Redis 的难点从来不只是采指标，而是把 Redis 的语义翻译成故障判断。**

你当然可以继续用 `redis-exporter` 做全量指标。

你也应该继续用 Prometheus / Grafana 看趋势和历史。

但如果你还缺下面这些能力：

- 轻量的周期异常检查
- 更贴近 Redis 语义的标准化事件
- 告警时能顺手查 `INFO`、slowlog、client list、cluster 快照、bigkeys

那 catpaw Redis 插件补的，就是这一层。

不是替代，而是分工。

不是再来一堆指标，而是更接近值班动作的检查和诊断。
