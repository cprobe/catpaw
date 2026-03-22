# 监控你的监控系统：Prometheus 挂了之后，谁来发现？

> **TL;DR**：很多团队把 Prometheus、Nightingale、Alertmanager 当作监控体系的“地基”，却没有给这层地基再加一层独立哨兵。结果就是，真正可怕的不是业务告警太多，而是某一天告警突然全都没了。catpaw 很适合补这一层：它不依赖 Prometheus 自己活着，能从主机、进程、HTTP、磁盘、日志和时间同步这些角度，盯住你的监控系统本身。

## 最危险的时刻，往往不是告警响了，而是告警突然不响了

值班久了之后，你会慢慢意识到一件事：

真正让人不安的，不一定是手机一直在响。

更糟糕的情况其实是：

- 业务明明有波动
- 某些系统明明最近不太稳定
- 但告警渠道异常安静

这个时候你最该警惕的，不是“今天真平静”，而是：

> 监控系统自己是不是已经失声了？

这类事故特别容易被低估。

因为当 Prometheus、Nightingale、Alertmanager 这类系统出问题时，最先失去的，恰恰就是你平时用来发现问题的能力。

常见的失效方式包括：

- Prometheus 进程还在，但 TSDB 磁盘已经快满
- Alertmanager 还在监听端口，但通知路由已经卡住
- Nightingale 页面能打开，但采集链路或规则引擎已经不健康
- exporter 正常，Prometheus 也正常，但宿主机时钟偏移让规则、日志、证书都开始错位
- 服务进程还活着，但 systemd 已经在反复拉起，监控平台本身处于半坏状态

这类问题的共同点是：

- 你不能只靠“它自己上报的指标”来发现
- 因为它一旦坏得更深，那套指标链路本身就可能不可信

所以“监控你的监控系统”这件事，本质上是在做一件很朴素的事：

**给监控系统本身，再加一层独立的外部视角。**

## 为什么监控系统不能只靠自监控

Prometheus 当然可以监控 Prometheus 自己。

Nightingale 当然也可以暴露自己的健康指标。

Alertmanager 当然也有自己的状态页面和内部 metrics。

这些都应该有，而且很有价值。

但如果你把全部希望都押在“系统自己监控自己”上，会有一个天然盲区：

> 一旦这套系统在最关键的地方失效，它也就不再可靠地告诉你自己失效了。

这不是 Prometheus 的问题，也不是 Nightingale 的问题。

而是任何复杂系统都会有的“自引用脆弱性”。

所以更稳妥的方式，通常是两层：

### 第一层：系统内监控

依赖它自己的 metrics、rule、dashboard。

适合回答：

- 查询量是否升高
- TSDB 压力是否变大
- rule eval 延迟有没有抬升
- 通知吞吐是否异常

### 第二层：系统外哨兵

由一个更轻、更独立的 Agent 从宿主机和网络侧观察它。

适合回答：

- 进程还在吗
- systemd 状态对不对
- 端口和 HTTP 还通吗
- 磁盘是不是要爆了
- 日志里是不是已经刷出致命错误
- 时间是不是已经漂了

这就是 catpaw 很适合出现的位置。

它不是来替代 Prometheus，而是来盯住 Prometheus 自己。

## “监控监控系统”最值得先做的，不是 fancy 指标，而是 6 个基本面

如果你问我，Prometheus / Nightingale / Alertmanager 这类系统最该优先盯什么，我不会先从一堆内部 metrics 开始。

我会先从最容易真正导致“监控失声”的 6 个面开始。

### 1. 进程和 systemd 状态

这是最基础的一层。

因为很多时候，监控系统不是“完全死掉”，而是：

- systemd 在反复拉起
- 进程名还在，但已经不是你以为的那个实例
- 进程活着，但服务状态已经异常

最简单的做法就是用 `procnum` 和 `systemd` 两条线一起盯。

如果你的 Prometheus 是 systemd 管理的，最小配置可以先这么来：

```toml
[[instances]]
units = ["prometheus", "alertmanager"]

[instances.state]
severity = "Critical"
```

配合进程存活检查：

```toml
[[instances]]
search_exec_name = "prometheus"

[instances.process_count]
critical_lt = 1
```

这两者放在一起的意义是：

- `systemd` 看的是服务状态语义
- `procnum` 看的是进程存在性

两者都很简单，但在值班里非常有效。

### 2. HTTP / 端口健康

很多监控系统就算进程还在，也不代表外部请求路径正常。

你真正关心的通常是：

- UI 能不能打开
- `/healthz` 或 `/-/ready` 能不能返回预期状态
- 关键接口是不是还在 2xx
- HTTPS 证书是不是快到期了

这层用 `http` 插件通常最直接。

例如：

```toml
[[instances]]
targets = [
  "http://127.0.0.1:9090/-/ready",
  "http://127.0.0.1:9093/-/ready",
]

[instances.connectivity]
severity = "Critical"

[instances.status_code]
expect = ["20*"]
severity = "Warning"
```

如果你是通过域名暴露监控平台，还可以顺手把证书有效期也盯上：

```toml
[instances.cert_expiry]
warn_within = "72h"
critical_within = "24h"
```

这一步的核心不是“能不能采到更多信息”，而是：

**从用户真正访问的入口去看，它现在到底是不是健康的。**

### 3. 磁盘空间和可写性

Prometheus 这类系统最经典的故障之一，就是磁盘问题。

比如：

- TSDB 持续增长把盘写满
- inode 异常增长
- 某个挂载点掉了，数据写回根分区
- 文件系统变成只读

这些问题如果不提前盯住，监控平台通常会先表现成：

- 查询越来越慢
- 写入失败
- 重启后恢复异常
- 数据保留期突然缩短

这时候 `disk` 插件非常实用。

最小配置甚至不用太多改：

```toml
[[instances]]

[instances.space_usage]
warn_ge = 90.0
critical_ge = 99.0

[instances.inode_usage]
warn_ge = 90.0
critical_ge = 99.0

[instances.diagnose]
enabled = true
```

如果监控系统依赖特定数据盘，也应该和 `mount` 插件一起用。

因为“盘满了”和“盘根本没挂上”是两类完全不同的问题。

### 4. 日志里的致命错误

只盯进程和 HTTP，还不够。

因为很多时候，系统正在进入故障前夜，而最早的证据躺在日志里。

比如：

- OOM kill
- WAL / TSDB 写入错误
- 文件系统只读
- 内核 panic、segfault
- 某些异常不断重试

如果你的监控系统主要由 systemd 管理，`journaltail` 很适合用来盯这类“已经很危险”的日志。

它的价值不是替代日志平台，而是把少数高价值、强故障信号的日志直接变成事件。

比如默认模板里已经包含一些非常典型的系统级异常：

- `*Out of memory*`
- `*kernel panic*`
- `*segfault*`
- `*I/O error*`
- `*Read-only file system*`

这对监控系统主机尤其有用。

因为 Prometheus / Alertmanager 这类组件一旦碰上磁盘、内存、文件系统问题，常常比普通业务更脆。

### 5. 时间同步

这一项经常被忽略，但对监控系统来说非常关键。

因为时间一旦漂了，影响的不是一个组件，而是一整条观测链路：

- 规则计算窗口会错位
- 告警时间线会变乱
- 日志和指标对不上
- TLS / token 相关认证可能开始异常

所以我会非常建议把 `ntp` 也放进“监控你的监控系统”的最小清单里：

```toml
[[instances]]

[instances.sync]
severity = "Critical"

[instances.offset]
warn_ge = "100ms"
critical_ge = "500ms"
```

这类检查看起来不起眼，但真的会在你最意想不到的时候救场。

### 6. 变更之后的主动巡检

最后这一项不是“持续监控”，而是操作习惯。

监控系统最容易出问题的时候，往往是：

- 升级版本之后
- 调 retention 之后
- 换存储盘之后
- 改 systemd unit 之后
- 调反向代理或证书之后

这类场景不该只靠等告警。

更好的方式是变更后主动跑一轮检查。

这时 `catpaw inspect` 就很好用：

```bash
./catpaw inspect systemd prometheus
./catpaw inspect disk /data
```

它相当于把“上线后我手动验一遍”的动作也标准化了。

## 如果把 Prometheus 当作被监控对象，该怎么落一套最小方案

如果你现在就想给 Prometheus 主机加一层独立哨兵，我建议从这几类开始：

### 基础存活

- `systemd`：看 `prometheus.service` 是否 active
- `procnum`：看 prometheus 进程是否存在

### 外部可访问

- `http`：看 `/-/ready` 是否 2xx
- 如果走 HTTPS，再加证书有效期

### 宿主机资源

- `disk`：重点盯数据盘空间和 inode
- `mount`：重点盯 Prometheus 数据盘挂载是否存在

### 失败前兆

- `journaltail`：盯 OOM、I/O error、read-only、segfault
- `ntp`：盯同步状态和偏移量

这套东西并不依赖 Prometheus 自己活着。

这就是它的最大价值。

## Nightingale、Alertmanager 也是同样的逻辑

这个主题如果只写 Prometheus，其实有点太窄了。

Nightingale、Alertmanager、甚至你自己的告警聚合服务，原则都一样：

- 先把它们当成普通关键服务看
- 再把它们当成“影响整个监控体系可信度”的关键基础设施看

也就是说，你要盯的从来不只是“它能不能提供功能”，还包括：

- 它挂了之后，会不会让整个团队进入盲飞

所以 Nightingale / Alertmanager 的最小方案，思路几乎完全一样：

- `systemd`
- `procnum`
- `http`
- `disk`
- `journaltail`
- `ntp`

只不过目标路径、service 名和端口不同而已。

## catpaw 为什么特别适合做这层独立哨兵

如果只讲这个主题，不讲为什么 catpaw 合适，文章其实不完整。

我觉得它适合的原因很简单：

### 1. 它足够独立

catpaw 是单二进制、轻量、check-first 的 Agent。

这意味着它很适合作为监控地基之外的另一层独立观察者，而不是把自己绑进原有监控链路里。

### 2. 它更关心“有没有问题”，而不是“采多少指标”

监控监控系统这件事，最需要的不是再来一堆 metrics。

而是优先回答：

- 这层基础设施现在有没有明显失效
- 如果失效了，最可能是哪一层

这正是 check 型 Agent 的长处。

### 3. 它出告警后还能顺手做一轮本机诊断

比如 Prometheus 主机一旦因为磁盘、OOM、systemd 异常触发告警，catpaw 可以直接继续查：

- 磁盘目录占用
- journald 日志
- OOM 历史
- systemd unit 状态
- 文件系统挂载

这比只有一条“Prometheus down”要强得多。

## 如果 Prometheus 还活着，MCP 还能把“监控系统内部视角”接回来

这里还有一个非常实用的补充：

catpaw 并不是只能看本机。

如果你把 Prometheus、Nightingale 等系统通过 MCP 接进来，AI 在诊断时还能顺手查这类历史上下文：

- 最近某个实例的趋势变化
- 某个告警规则是否持续抖动
- 某类指标是不是在一段时间里逐渐恶化

配置形态大概是这样：

```toml
[ai.mcp]
enabled = true

[[ai.mcp.servers]]
name = "prometheus"
command = "/usr/local/bin/mcp-prometheus"
args = ["serve"]
identity = 'instance="${IP}:9100"'
[ai.mcp.servers.env]
PROMETHEUS_URL = "http://127.0.0.1:9090"
```

然后可以先用：

```bash
./catpaw mcptest
```

确认 MCP 连接没问题。

这层能力的意义在于：

- catpaw 负责外部哨兵
- MCP 再把监控系统内部的历史上下文接回来

两层结合，比单靠任意一边都更稳。

## 这件事真正解决的，其实是“盲飞”

我觉得“监控你的监控系统”这个主题，真正该打中的不是技术细节本身，而是值班心理。

因为一旦监控系统自己坏了，团队最容易进入的状态就是：

> 我现在既不知道业务是否正常，也不知道我的告警体系是否正常。

这就是盲飞。

而你给 Prometheus / Nightingale / Alertmanager 外面再套一层独立哨兵，本质上是在降低这种盲飞风险。

哪怕这层哨兵只做最朴素的事情：

- 看 systemd
- 看进程
- 看 HTTP
- 看磁盘
- 看日志
- 看时间

它也已经足够把很多“监控系统已经失声”的问题提前暴露出来。

## 最后给一份最小自监控清单

如果你想今天就开始，我建议把下面这份清单当作第一版：

- `systemd`：Prometheus / Alertmanager / Nightingale 的 unit 状态
- `procnum`：关键进程是否存在
- `http`：关键健康接口是否 2xx
- `disk`：数据盘空间和 inode
- `mount`：监控数据盘是否还挂在预期路径
- `journaltail`：OOM、I/O error、read-only、segfault
- `ntp`：同步状态和偏移量

如果这几项都还没有，那你最该担心的不是业务系统有没有监控够，而是你的监控系统自己可能还没人盯。

**监控系统不是上帝视角。**

**它也需要被监控。**
