# Prometheus 很强，但它不负责半夜帮你查机器

> **TL;DR**：Prometheus 很擅长做指标采集、时间序列存储、趋势分析和规则告警。catpaw 想补的不是这套能力，而是另一层东西：主机侧 check、监控盲区、告警后的第一轮根因初筛。两者不是替代关系，而是职责分层。

## 一个非常常见的问题

每次有人第一次看到 catpaw，几乎都会问同一句：

> 我已经有 Prometheus 和 Grafana 了，还需要它吗？

这个问题问得对。

因为如果连边界都讲不清，任何新工具都会看起来像"重复造轮子"。

我的结论很直接：

- 如果你要的是指标采集、趋势图、历史查询，Prometheus 非常强，而且已经是事实标准
- 如果你要的是"这台机器现在到底出了什么异常"、"告警之后该先查什么"，Prometheus 并不直接负责

这不是说 Prometheus 不行。

恰恰相反，是因为 Prometheus 已经把自己该做的事做得足够好了，所以更需要把边界讲清楚。

## Prometheus 擅长什么，不擅长什么

Prometheus 最擅长的，是把系统状态变成一组持续可查询的时间序列。

比如：

- CPU 使用率
- 内存占用
- 磁盘 IO
- QPS
- 错误率
- P99 延迟

然后你可以在 Grafana 上画图，在 PromQL 里做聚合，在 Alertmanager 里配置告警路由。

这套链路很适合回答下面这些问题：

- 最近一周 CPU 是不是持续走高
- 哪些实例的错误率在过去 15 分钟出现了抬升
- 某次发版之后，接口延迟曲线有没有明显变化
- 这个集群的资源使用率有没有长期逼近瓶颈

这些都是**趋势和历史问题**。

但值班时还经常有另一类问题：

- 为什么服务明明在线，连接还是超时
- 为什么大盘全绿，新的 Pod 却连不上
- 为什么 healthcheck 正常，但用户还是偶发失败
- 为什么 fd 突然耗尽，明明 CPU 和内存都没爆

这些问题不只是"看指标"，而是"判断异常"。

甚至更进一步，是：

> 确认这是不是已经构成故障，以及接下来该先查哪一层。

## 指标采集、异常检测、根因初筛，本来就是三层事

很多团队把这三件事混在一起，所以经常会出现一种错觉：

> 指标都采全了，监控应该就完整了吧。

其实不是。

更合理的拆法是这样：

| 层次 | 典型工具 | 解决的问题 |
| --- | --- | --- |
| Metrics | Prometheus / Node Exporter | 系统现在有哪些数字，趋势怎么变 |
| Alerting | Alertmanager / 告警规则 | 哪些变化值得通知，通知谁 |
| Check + RCA | catpaw | 哪些状态已经构成问题，告警后先查什么 |

注意这里最容易被忽略的一层是 `Check + RCA`。

因为很多时候，原始 metrics 明明在，但离"一条对值班人有帮助的异常判断"还差得很远。

比如：

- 有 `node_nf_conntrack_entries`，但没人帮你判断它是不是已经逼近上限
- 有网络错误累计值，但没人帮你算增量、设合理阈值、压掉瞬时抖动
- 有 TCP 状态数，但没人告诉你 CLOSE_WAIT 这么多基本就是连接泄漏
- 有 listen queue 相关计数器，但没人把它翻译成"服务在线，但请求可能已被静默丢弃"

**指标存在，不等于故障可见。**

这就是 check 层存在的意义。

## 为什么很多故障在 Grafana 上看不出来

如果你做过一段时间 On-call，大概率遇到过这种时刻：

- 用户说服务不稳定
- 应用进程还在
- CPU 正常
- 内存正常
- 磁盘 IO 正常
- Grafana 大盘一片绿色

然后你开始 SSH 上机查：

```bash
dmesg | grep -i conntrack
cat /proc/net/netstat
ss -ant | awk '{print $1}' | sort | uniq -c | sort -rn
sysctl net.core.somaxconn
lsof -p <pid> | wc -l
```

这说明什么？

说明你需要的信息，不在常规 dashboard 那一层。

更准确地说，是**不在已经被加工成图表和通用告警规则的那一层**。

有些问题属于典型的主机侧故障信号：

- conntrack 表满了
- 邻居表满了
- sysctl 参数漂移
- listen 队列溢出
- CLOSE_WAIT 堆积
- 系统级 fd 逼近上限

这些问题有的可以从 exporter 里拼出来，有的甚至根本不会直接出现在常规 dashboard 上。

但就算你能采到，也还要再做几层工作：

1. 从原始指标里选出真正有故障意义的项  
2. 把累计值变成增量，把数字变成判断  
3. 设计阈值，避免误报和漏报  
4. 把多个信号串起来，变成可执行的排查路径

这已经不是"把 metrics 存起来"的问题了。

## Node Exporter 的职责不是替你做故障判断

这里很容易对 Node Exporter 产生一种不公平的期待。

它的职责本来就是把宿主机上的一批状态暴露成指标。

它不是故障知识库，也不是值班教练。

比如：

- 它可以把 conntrack 当前数量暴露出来
- 可以把 sockstat 某些计数暴露出来
- 可以把 network errors 的累计值暴露出来
- 可以把各种内核统计信息暴露出来

但它不会替你回答：

- 对这个业务场景来说，多少算危险
- 哪些值应该组合着看
- 哪些短时尖峰该忽略，哪些持续信号必须告警
- 告警之后下一步该去磁盘、网络、内核还是进程层查

这不是 exporter 的问题，而是分工本来就不一样。

如果非要类比：

- Prometheus 像一个很强的数据仓库和查询引擎
- Grafana 像一个很强的可视化界面
- Alertmanager 像一个很强的通知编排器
- catpaw 更像一个**带故障知识和主机侧诊断能力的 check Agent**

## 一个最典型的例子：conntrack

这是我最喜欢拿来解释这个边界的场景。

假设一台 K8s Node 上出现了连接偶发超时。

你打开 Grafana，看到：

- CPU 正常
- 内存正常
- 网络带宽正常
- 磁盘正常

然后你会以为一切都没事。

但真正的问题可能是：

```text
nf_conntrack: table full, dropping packet.
```

这时候新的连接会被内核静默丢掉。

用户看到的是 timeout。

应用看到的是"请求怎么没来"。

而大盘上很可能没有一个特别刺眼的红块告诉你：

> 这不是应用抖，而是连接跟踪表已经满了。

为什么？

因为从"当前条目数"到"这是正在发生的故障"之间，还隔着至少两步：

- 你得知道上限是多少
- 你得知道这个比例一旦接近上限意味着什么

catpaw 在这一层做的事情就很直接：

- 它不先给你一堆 metrics
- 它直接检查 `nf_conntrack_count / nf_conntrack_max`
- 超过阈值就产出标准化异常事件

配置也很短：

```toml
[[instances]]
[instances.conntrack_usage]
warn_ge = 75.0
critical_ge = 90.0

interval = "30s"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 0
```

这就是 check 型 Agent 的价值。

它帮你把"原始信号"变成"面向故障的判断"。

## 再看一个场景：sysctl 漂移

这类问题更能说明"有指标"和"能发现问题"之间的差别。

比如你明明把下面这些参数调好了：

- `net.core.somaxconn = 65535`
- `vm.swappiness = 1`
- `fs.file-max = 1000000`

三个月后，内核升级了，或者某个配置管理任务覆盖了，参数悄悄漂回默认值。

你不会立刻发现。

直到某次流量上来，服务开始偶发超时，或者队列溢出，才意识到哪里不对。

Prometheus 能不能采这些参数？

能，或者至少能通过别的方式拿到一部分。

但关键问题是：**谁来维护这份基线，谁来持续比对，谁来把偏离基线这件事直接翻译成异常事件。**

这时 catpaw 的 `sysctl` 插件更接近一个真正的运维工具：

```toml
[[instances]]
[instances.param_check]
params = [
  { key = "net.core.somaxconn", op = "ge", value = "65535" },
  { key = "vm.swappiness", op = "le", value = "10" },
  { key = "fs.file-max", op = "ge", value = "1000000" },
]
```

这不是趋势分析，而是基线守护。

## catpaw 补的，不是 metrics，而是 check 和告警后的动作

如果把 catpaw 说得更准确一点，我会这样描述它：

### 1. 它是 check-first，不是 metrics-first

catpaw 的插件不是围绕"暴露多少指标"设计的，而是围绕"有哪些故障信号值得直接检查"设计的。

这也是为什么它天然适合这些场景：

- 磁盘空间 / inode / writable
- conntrack / neigh / sockstat
- tcpstate / procfd / filefd
- sysctl / mount / secmod
- systemd / docker / redis

### 2. 它输出的是标准化事件

不是一大坨原始指标，而是更接近值班语义的事件：

- 什么检查触发了
- 目标是谁
- 当前值是什么
- 阈值描述是什么
- 严重级别是什么

这意味着它天然适合直接接值班通知渠道，比如：

- console
- WebAPI
- Flashduty
- PagerDuty

### 3. 告警触发后，它还能继续做第一轮诊断

这点是它和大多数 check 工具拉开距离的地方。

告警送出去之后，catpaw 还可以根据上下文自动触发 AI 诊断：

- 聚合同一目标的多个关联告警
- 调用 70+ 本地诊断工具
- 必要时再接 Prometheus、Jaeger 等 MCP 外部工具
- 生成结构化报告，再次回到通知链路里

所以它补的不只是"发现异常"，还包括"帮你把异常推进到第一轮根因"。

## 更现实的做法不是替换，而是分层组合

我更推荐读者把监控栈理解成一个组合拳，而不是单选题。

一个更现实的架构大概是这样：

```text
Prometheus / Grafana
  - 指标采集
  - 历史趋势
  - 仪表盘
  - PromQL 查询

Alertmanager / On-call 平台
  - 告警路由
  - 值班升级
  - 通知编排

catpaw
  - 主机侧异常检测
  - 监控盲区补齐
  - 告警后第一轮根因初筛

MCP 外部数据源
  - 给 AI 诊断补历史上下文
  - 接 Prometheus / Jaeger / CMDB
```

这里每一层都很清楚：

- Prometheus 负责看全局趋势
- catpaw 负责盯那些会在主机上直接构成故障的点
- On-call 平台负责把异常送到正确的人
- MCP 负责把外部历史数据喂回诊断链路

这样组合起来，比争论"到底该选谁"更接近真实生产环境。

## 什么时候特别适合加 catpaw

如果你团队已经有 Prometheus，但仍经常遇到下面这些情况，那 catpaw 很可能是有意义的：

- 值班时还经常要 SSH 上机，临时拼命令排查
- 大盘常常是绿的，但故障还是发生了
- 团队里只有少数人熟 Linux 内核层排障
- exporter 指标很多，但真正有用的告警始终补不齐
- 希望告警发出去时，能顺手附上第一轮分析结论

尤其是下面这几类插件，通常非常适合先上：

- `conntrack`
- `tcpstate`
- `sockstat`
- `sysctl`
- `procfd`
- `filefd`

这些都是传统监控栈里最容易"知道有数字，但不知道怎么变成故障判断"的地方。

## 什么时候不必强推

反过来说，也没必要把 catpaw 说成所有团队都必须立刻接。

如果你当前处在下面这些阶段，优先级可能没那么高：

- 还没有把基础 metrics、日志、trace 搭起来
- 团队暂时没有正式的值班流程
- 业务规模很小，核心诉求只是 uptime 和基础资源监控
- 当前最大的痛点不是监控盲区，而是告警太乱、基础规范太差

这并不是 catpaw 无用，而是说明问题的主次顺序要对。

基础观测能力没打牢之前，任何更高级的排障闭环都很难发挥真正价值。

## 真正该替代的，不是 Prometheus，而是那段重复的人肉劳动

我觉得这篇文章最重要的一句话其实是：

**catpaw 不是来替代 Prometheus 的。**

它更像是在替代下面这段重复劳动：

1. 收到一条没有上下文的告警  
2. SSH 登录机器  
3. 回忆命令  
4. 拼 `/proc`、`ss`、`dmesg`、`sysctl`、`lsof` 输出  
5. 再把这些原始结果翻译给值班群里的人听

Prometheus 不该背这个锅。

它从来没承诺自己要负责这段工作。

而 catpaw 想做的，恰恰就是把这段劳动压缩掉一部分。

## 如果你已经有 Prometheus，可以怎么开始

最简单的切入方式不是全面替换，而是补最容易出事、最难靠大盘发现的几个点。

比如先从这 3 个插件开始：

### 1. `conntrack`

适合 K8s Node、NAT 网关、高并发入口机。

### 2. `tcpstate`

适合排查 CLOSE_WAIT / TIME_WAIT 积累、连接泄漏、端口耗尽。

### 3. `sysctl`

适合把关键内核参数做成长期基线，而不是靠人记。

然后保留默认 console 输出，或者直接接到你现有的 On-call 平台：

```toml
[notify.console]
enabled = true
```

如果想继续验证告警后的自动诊断，再打开 AI：

```toml
[ai]
enabled = true
model_priority = ["default"]

[ai.models.default]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_API_KEY}"
model = "gpt-4o"
```

这样你的体验就会很明确：

- Prometheus 继续负责大盘和趋势
- catpaw 开始负责主机侧异常和第一轮根因初筛

## 最后总结

如果你问我一句最短的结论，我会这么回答：

- Prometheus 擅长回答："最近发生了什么变化？"
- catpaw 更擅长回答："这台机器现在到底哪里不对？"

一个负责看趋势。

一个负责盯故障信号。

一个负责告诉你曲线怎么走。

一个负责在半夜提醒你：这不是普通波动，这已经是个该处理的问题，而且下一步建议先查这里。

所以真正成熟的监控栈，通常不是单一工具赢了，而是每一层都各司其职。

**Prometheus 很强。**

**但它不负责半夜帮你查机器。**
