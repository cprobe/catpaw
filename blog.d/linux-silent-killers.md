# 那些你不知道自己需要监控的 Linux 暗坑

> **TL;DR**：conntrack 表满了、ARP 邻居表溢出、内核参数被静默重置、listen 队列丢包……这些 Linux 内核层的"沉默杀手"不会出现在你的 Grafana 大盘上，但能让你的线上服务在几秒内崩溃。本文拆解 8 个真实暗坑，每个都附带故障原理和监控方案。

## 故事：K8s 集群丢包两天，最后是 conntrack 的锅

某天凌晨，值班收到告警：部分 Pod 间歇性超时。

打开 Grafana——CPU 正常，内存正常，网络带宽正常，磁盘 IO 正常。四大金指标一片绿色。

开始排查：抓包发现 SYN 发出去了，但对端没回 SYN-ACK。换个 Pod 试试，好了。再试一次，又超时了。像是某种随机故障。

排查了两天。最后在 `dmesg` 里找到了一行不起眼的日志：

```
nf_conntrack: table full, dropping packet.
```

**conntrack 表满了。** 新连接被内核在网络栈底层静默丢弃，SYN 包根本没到达目标进程。

这条信息不在 Prometheus 的任何 exporter 里，不在 Grafana 的任何 dashboard 里，不在任何告警规则里。它安静地躺在 `dmesg` 的日志洪流中，等着某个老运维碰巧想到去看一眼。

**这就是"沉默杀手"——内核层的故障，没人监控，出事了也没人能一眼看出来。**

## 你的监控栈有一大片盲区

大多数团队的监控架构长这样：

```text
Prometheus + Node-Exporter  →  Grafana Dashboard  →  AlertManager  →  通知
```

这套体系非常优秀——用来做**指标采集和趋势展示**。

但它有一个根本性的盲区：**Node-Exporter 采的是指标（metrics），不是异常判定（checks）。**

什么意思？举个例子：

- Node-Exporter 会告诉你 `node_nf_conntrack_entries = 131072`，但它不知道 `nf_conntrack_max` 也是 `131072`——表已经满了
- 它会告诉你 `node_network_receive_errs_total = 15847`，但这是一个累计值，你需要自己算增量、设阈值、写 PromQL 告警规则
- 它采了 `node_sockstat_TCP_tw` 告诉你 TIME_WAIT 数量，但你需要自己判断"多少算多"

**指标在那里，但没人把它们变成告警。**

不是 Node-Exporter 的问题，而是"指标采集"和"异常检测"是两件不同的事。大多数团队做完了前者，跳过了后者——尤其是在内核层面。

接下来我们拆解 8 个最常见的 Linux "沉默杀手"。

## 暗坑 1：conntrack 表溢出——所有连接静默失败

### 故事

NAT 网关、K8s Node、高并发负载均衡器——只要启用了 iptables/nftables NAT 或 connection tracking，内核就会为每条连接维护一个 conntrack 条目。默认上限通常是 65536 或 131072。

表满之后的行为不是报错，不是拒绝连接，而是**静默丢包**。SYN 发出去了，没有任何响应。客户端看到的是连接超时，而服务端的进程对此完全无感——包根本没到达应用层。

### 原理

```text
新连接 → 内核网络栈 → nf_conntrack 尝试创建条目
         ↓
  表满 → 直接 DROP → 没有 RST，没有 ICMP unreachable
         ↓
  客户端等到超时 → 报 connection timeout
```

唯一的证据是 `dmesg` 中的 `nf_conntrack: table full, dropping packet`，以及 `/proc/net/stat/nf_conntrack` 中 `drop` 计数器的增长。

### 监控方案

catpaw 的 `conntrack` 插件读取 `/proc/sys/net/netfilter/nf_conntrack_count`（当前条目数）和 `nf_conntrack_max`（上限），计算使用率百分比：

```toml
# conf.d/p.conntrack/conntrack.toml
[[instances]]
[instances.conntrack_usage]
warn_ge = 75.0
critical_ge = 90.0
```

nf_conntrack 模块未加载时自动跳过，不会误报。

## 暗坑 2：ARP 邻居表满——新 IP 无法通信，旧 IP 正常

### 故事

大规模 K8s 集群，上千个 Pod 分布在同一个大二层网络。运行几天后，新创建的 Pod 无法和某些节点通信，但已经在运行的 Pod 之间通信正常。

重启 Pod？好了。过几分钟又不行了。

最后发现：**ARP 邻居表的 `gc_thresh3` 硬上限被打满了。** 内核默认值是 1024——一千个 IP 就到头了。已缓存的 ARP 条目正常工作，但新 IP 的 ARP 解析被拒绝。

### 原理

Linux 邻居表有三个阈值：
- `gc_thresh1`：正常回收的软下限
- `gc_thresh2`：超过后 5 秒内必须回收
- `gc_thresh3`：**硬上限**，超过后内核直接拒绝新条目

默认 `gc_thresh3 = 1024`。在容器密集型环境（每个 Pod 至少一个 IP），很容易突破。

症状极具迷惑性：**部分通信正常，部分失败，重启有时能修复**——因为重启会清理旧条目、添加新条目，但总量不变。

### 监控方案

```toml
# conf.d/p.neigh/neigh.toml
[[instances]]
[instances.neigh_usage]
warn_ge = 75.0
critical_ge = 90.0
```

插件读取 `/proc/net/arp` 统计条目数，除以 `/proc/sys/net/ipv4/neigh/default/gc_thresh3` 得到使用率。告警触发后，你需要做的就是调大 `gc_thresh3`（通常建议改到 4096 或 8192）。

## 暗坑 3：sysctl 参数静默漂移——你以为调好了，其实早就变回去了

### 故事

生产环境精心调优过一批内核参数：`somaxconn` 改到 65535，`tcp_tw_reuse` 打开，`swappiness` 设为 1。

三个月后升级了内核，然后"偶发"连接拒绝。排查半天——`somaxconn` 又变回 128 了。

### 原理

`sysctl` 参数的生效链条：

```text
sysctl.conf / sysctl.d/*.conf → systemd-sysctl.service → /proc/sys/
```

以下场景都会导致参数"回归默认"：

1. **内核升级重启**，但 sysctl.conf 里遗漏了某个参数
2. **模块重新加载**（如 `nf_conntrack` 模块重加载后，相关参数重置）
3. **配置管理工具覆盖**（Ansible/Puppet 推送了不同的配置版本）
4. **容器运行时修改了宿主机参数**

而且这类问题不会立刻暴露——只有在负载上来或特定条件触发时，你才会发现"参数不对"。

### 监控方案

catpaw 的 `sysctl` 插件是一个通用的参数基线检查器。你定义"期望值"，它定期比对：

```toml
# conf.d/p.sysctl/sysctl.toml
[[instances]]
[instances.param_check]
params = [
  { key = "net.core.somaxconn", op = "ge", value = "65535" },
  { key = "vm.swappiness", op = "le", value = "10" },
  { key = "net.ipv4.tcp_tw_reuse", value = "1" },
  { key = "fs.file-max", op = "ge", value = "1000000" },
]
```

支持 6 种比较操作符（`eq`、`ne`、`ge`、`le`、`gt`、`lt`），每个参数可独立设置告警级别。一旦参数偏离基线，立刻告警。

告警触发后，AI 诊断引擎还有一个 `sysctl_snapshot` 工具，一次性快照 24 个关键内核参数（`vm.swappiness`、`fs.file-max`、`net.core.somaxconn`、`net.ipv4.tcp_syncookies` 等），帮你快速定位全局参数状态。

## 暗坑 4：TCP listen 队列溢出——服务在线但连不上

### 故事

流量高峰期，用户报告"网站时好时坏"。服务进程正常运行，端口正常监听，healthcheck 也是绿色。但部分用户持续报连接超时。

### 原理

每个监听端口有一个 accept queue（backlog），大小由 `min(somaxconn, application_backlog)` 决定。当应用处理连接的速度跟不上新连接到达的速度时，队列满了，新的 SYN 被丢弃：

```text
客户端 SYN → 内核收到 → accept queue 满 → 静默丢弃
                                         ↓
                              客户端重试 SYN（指数退避）
                                         ↓
                              最终超时，报 connection timeout
```

与 conntrack 满的症状**几乎一样**（都是连接超时），但根因完全不同——一个是内核层的全局问题，一个是应用层的单服务问题。

证据藏在 `/proc/net/netstat` 的 `TcpExt` 段里：`ListenOverflows` 和 `ListenDrops` 两个累计计数器。

### 监控方案

catpaw 的 `sockstat` 插件监控两次采集之间 `ListenOverflows` 的**增量**——不是累计值，而是"最近这 30 秒又溢出了几次"：

```toml
# conf.d/p.sockstat/sockstat.toml
[[instances]]
[instances.listen_overflow]
warn_ge = 1
critical_ge = 100

[instances.alerting]
for_duration = "1m"
```

`warn_ge = 1` 意味着：**任何一次溢出都值得知道。** `for_duration = "1m"` 过滤掉瞬间尖峰，持续溢出才告警。

告警触发后，AI 会自动调用 `listen_overflow` 诊断工具（查看各 listen socket 的 backlog 配置和队列使用情况）和 `tcp_tuning_check`（检查 TCP 内核参数是否合理），给出具体的优化建议。

## 暗坑 5：CLOSE_WAIT 堆积——连接泄漏的慢性病

### 故事

一个 Java 服务运行了几周后，突然报 `Too many open files`。查看 fd 数量——几万个。大部分是 TCP socket，状态全是 CLOSE_WAIT。

### 原理

TCP 四次挥手中，当远端发了 FIN（我要关了），本端回了 ACK，连接进入 CLOSE_WAIT 状态，等待本端应用调用 `close()`。

如果应用代码有 bug——连接用完了没 close，或者 error path 上遗漏了 `defer conn.Close()`——这条连接就会**永远**停在 CLOSE_WAIT。没有超时，没有回收，就这么静静地占着一个 fd，直到进程 fd 耗尽。

正常的服务，CLOSE_WAIT 数量应该接近 0。如果看到上百个，基本就是连接泄漏。

### 监控方案

```toml
# conf.d/p.tcpstate/tcpstate.toml
[[instances]]
[instances.close_wait]
warn_ge = 100
critical_ge = 1000

[instances.time_wait]
warn_ge = 5000
critical_ge = 20000
```

catpaw 的 `tcpstate` 插件通过 Linux **Netlink SOCK_DIAG** 接口直接查询内核的 TCP socket 状态——不是遍历 `/proc/net/tcp`（慢），而是通过 netlink 只请求目标状态的 socket，效率高得多。同时支持 IPv4 和 IPv6。

同一个插件也监控 TIME_WAIT。高并发短连接场景下 TIME_WAIT 大量积累会耗尽本地端口范围（默认约 28000 个端口），导致 `Cannot assign requested address`。

告警触发后，AI 会调用 `tcp_state_distribution`（查看连接状态分布）和 `top_connections_by_port`（按端口分组 Top 20 连接数），精准定位是哪个服务、连哪个远端出了问题。

## 暗坑 6：系统级 fd 耗尽——所有服务同时崩溃

### 故事

凌晨 3 点，所有服务同时报错。Nginx 报 `accept() failed (24: Too many open files)`，MySQL 报 `Can't open file`，cron 报 `fork: Resource temporarily unavailable`。

看起来像是"系统崩了"，但 CPU/内存/磁盘都正常。

### 原理

Linux 有两层 fd 限制：

```text
进程级：ulimit -n（默认 1024 或 65535）→ 报错 EMFILE
系统级：/proc/sys/fs/file-max（整个系统的 fd 总量）→ 报错 ENFILE
```

大多数人只关注进程级的 `ulimit`，忘了系统还有一个全局上限。当系统级 fd 耗尽时，**所有进程的 open()、socket()、accept()** 同时失败，报错信息千差万别——因为每个程序对错误的包装方式不同。

这是一个典型的"症状与根因脱节"问题：看起来是 Nginx 的问题、MySQL 的问题、cron 的问题，其实是一个系统级的问题。

### 监控方案

```toml
# conf.d/p.filefd/filefd.toml
[[instances]]
[instances.filefd_usage]
warn_ge = 80.0
critical_ge = 90.0
```

插件读取 `/proc/sys/fs/file-nr`（三个字段：已分配数 / 空闲数 / 上限），计算 `已分配 / 上限 * 100%`。

AI 诊断触发时，还有一个 `filefd_top_procs` 工具，遍历 `/proc/<pid>/fd` 目录统计每个进程的 fd 数量，输出 Top N 排行——帮你一眼看出是哪个进程在"吃"fd。

## 暗坑 7：网卡错误和丢包持续增长——慢性出血

### 故事

偶尔有用户报告"网页加载慢"，但监控上看不出异常。带宽利用率不高，延迟指标也正常。

直到有一天查看 `ethtool -S eth0`，发现 `rx_crc_errors` 已经累积到几十万——网线在慢慢坏，每隔几秒丢几个包，TCP 重传掩盖了问题，但用户体验在持续劣化。

### 原理

网卡的 error 和 drop 计数器是累计值，记录在 `/sys/class/net/<iface>/statistics/` 下。增长原因包括：

- **rx_errors**：CRC 错误（线缆/光模块故障）、帧错误、over-run
- **tx_errors**：驱动 bug、硬件故障
- **rx_dropped**：ring buffer 满（`ethtool -g` 查看）、内核协议栈来不及处理
- **tx_dropped**：发送队列满、QoS 策略丢弃

少量的 error/drop 在高流量环境下可以接受，但**持续增长**就是信号。

### 监控方案

```toml
# conf.d/p.netif/netif.toml
[[instances]]
exclude = ["lo", "docker*", "veth*", "br-*"]

[instances.errors]
warn_ge = 1
critical_ge = 100

[instances.drops]
warn_ge = 1
critical_ge = 100
```

`warn_ge = 1` 表示任何非零增量都报告。`exclude` 过滤掉容器虚拟网卡的噪音。

插件还支持 **link_up 检查**——指定期望处于 up 状态的接口（如 `eth0`、`bond0`），链路断开时立刻告警：

```toml
[[instances.link_up]]
interface = "eth0"
severity = "Critical"
```

这在 bond 场景下尤其有用——bond 的成员网卡掉了一块，bond 接口本身还是 up 的，但你已经损失了一半带宽和冗余。

## 暗坑 8：挂载点漂移——数据写错地方了

### 故事

某次重启后，数据库服务恢复了，healthcheck 也通过了。但过了几小时，根分区磁盘满了。

原因：数据盘 `/data` 的 NFS 挂载在重启后没有成功恢复（NFS server 当时也在重启），数据库进程写到了空的 `/data` 目录——实际上是根分区。

### 原理

Linux 的挂载点是"覆盖"语义——如果 `/data` 没有挂载任何设备，它就是根分区上的一个普通目录。往里面写数据不会报错，只是写到了错误的位置。

类似的问题还有：
- fstab 条目写错了，重启后静默失败
- NFS server 不可达，挂载超时但服务已经启动
- CIS 安全基线要求 `/tmp` 带 `noexec` 选项，但实际挂载时遗漏了

### 监控方案

```toml
# conf.d/p.mount/mount.toml
[[instances]]

[[instances.mounts]]
path = "/data"
fstype = "ext4"
severity = "Critical"

[[instances.mounts]]
path = "/backup"
fstype = "nfs"
severity = "Critical"

[[instances.mounts]]
path = "/tmp"
options = ["noexec", "nosuid", "nodev"]
severity = "Warning"
```

还可以开启 fstab 自动检查——插件读取 `/etc/fstab`，逐条验证是否已挂载、文件系统类型是否匹配：

```toml
[instances.fstab]
enabled = true
severity = "Critical"
```

自动跳过 swap、`noauto`、以及 `tmpfs`/`devtmpfs`/`squashfs`/`overlay` 等虚拟文件系统。

## 为什么传统 metrics 监控容易漏掉这些？

回头看这 8 个暗坑，它们有几个共同特征：

**1. 不是指标趋势问题，而是阈值判定问题**

conntrack 使用率 80% 和 90% 在 Grafana 曲线上差别不大，但 100% 就是灾难。你需要的不是一条曲线，而是一个明确的"到了没有"的判定。

**2. 需要关联两个值才能判断**

conntrack 需要 `count / max`，ARP 需要 `entries / gc_thresh3`，fd 需要 `allocated / file-max`。Node-Exporter 采了这些原始值，但需要你自己写 PromQL 做除法、设阈值、配告警。大多数团队在配 Grafana dashboard 的时候做了前半截，但告警规则那一步跳过了。

**3. 增量比绝对值重要**

网卡错误和 listen 溢出是累计计数器，重要的是"最近有没有增长"，而不是绝对值是多少。这需要有状态的检查——记住上次的值，算增量。

**4. 症状和根因严重脱节**

fd 耗尽表现为所有服务同时报不同的错，ARP 满表现为"部分 Pod 通信失败"，sysctl 漂移表现为"偶发连接拒绝"。如果没有针对性的检查，你可能要排查很久才能定位到真正的根因。

## catpaw 的方式：check + AI 双层防护

catpaw 做这件事的方式和 Prometheus 体系互补，而不是重叠：

```text
Prometheus + Node-Exporter：   采指标 → 存时序 → 画图 → (手动配告警)
catpaw：                       做检查 → 判异常 → 发告警 → AI 自动诊断根因
```

**第一层：插件做异常判定。** 不是采指标然后等人写 PromQL，而是插件本身就包含了判定逻辑——读取内核数据，计算是否越过阈值，有状态地追踪增量，输出明确的"有问题/没问题"结论。

**第二层：AI 做根因分析。** 当告警触发时，AI 自动调用对应领域的诊断工具：

| 告警 | AI 会调用的工具 |
| --- | --- |
| conntrack 表 90% | `conntrack_stat` 查看表使用和内核统计 |
| ARP 邻居表溢出 | `arp_neigh` 查看邻居表摘要，与 gc_thresh3 对比 |
| sysctl 参数偏离 | `sysctl_snapshot` 快照 24 个关键参数 |
| listen 队列溢出 | `listen_overflow` 查队列 + `tcp_tuning_check` 查参数 |
| CLOSE_WAIT 堆积 | `tcp_state_distribution` + `top_connections_by_port` |
| 系统 fd 耗尽 | `filefd_usage` + `filefd_top_procs` 查 Top 进程 |
| 网卡错误增长 | `net_interface` 查每接口统计 |
| 挂载点异常 | `mount_info` 查当前挂载状态 |

你收到的不是一条"conntrack usage 92%"的干巴巴告警，而是一份分析报告：当前使用量多少、哪些连接在占用、哪个服务的连接数最多、建议怎么扩容。

## 自查清单：你的服务器上这些都在监控吗？

| # | 检查项 | 风险 | 你在监控吗？ |
| --- | --- | --- | --- |
| 1 | conntrack 表使用率 | 表满后所有新连接静默失败 | ☐ |
| 2 | ARP 邻居表使用率 | 表满后新 IP 无法通信 | ☐ |
| 3 | 内核参数基线 | 升级/重启后参数静默回滚 | ☐ |
| 4 | TCP listen 队列溢出 | 服务在线但连不上 | ☐ |
| 5 | CLOSE_WAIT / TIME_WAIT 数量 | 连接泄漏 / 端口耗尽 | ☐ |
| 6 | 系统级 fd 使用率 | 所有服务同时崩溃 | ☐ |
| 7 | 网卡 error/drop 增量 | 网络质量慢性劣化 | ☐ |
| 8 | 挂载点合规性 | 数据写错位置 | ☐ |
| 9 | NTP 时钟同步 | 证书失效/日志乱序/认证失败 | ☐ |

如果你发现有几项没有覆盖到——这很正常，大多数团队都是这样。

好消息是，catpaw 的默认配置已经覆盖了上面所有检查项。下载、解压、`./catpaw run`，开箱即用：

```bash
# 下载
wget https://github.com/cprobe/catpaw/releases/latest/download/catpaw-linux-amd64.tar.gz
tar xzf catpaw-linux-amd64.tar.gz
cd catpaw

# 启动，告警直接打印到终端
./catpaw run

# 只运行内核相关插件验证
./catpaw run --plugins conntrack:neigh:sysctl:sockstat:tcpstate:netif:filefd:mount:ntp
```

没消息就是好消息——只有检测到异常时才会输出。

如果你想让告警推送到 On-call 平台（Flashduty、PagerDuty），或者开启 AI 自动诊断，参考[第一篇博客](catpaw-intro.md)的配置指南。

## 写在最后

这 8 个暗坑有一个共同点：**你不知道自己需要监控它们，直到它们把你叫醒。**

conntrack 表满、ARP 邻居表溢出、listen 队列丢包——这些故障的排查过程往往是一样的：从应用层一路往下挖，挖到内核层，才恍然大悟。然后你会说："早该监控上的。"

catpaw 就是那个帮你"早该监控上"的工具。

它不替代 Prometheus——Prometheus 做指标采集和趋势分析无可替代。catpaw 补的是那块被忽视的拼图：**内核层的异常检测 + AI 辅助的根因分析。**

部署一个轻量 Agent，盯住这些沉默的杀手，让你的凌晨 3 点少一些不必要的意外。

---

**GitHub**：[https://github.com/cprobe/catpaw](https://github.com/cprobe/catpaw)

**微信交流群**：加 `picobyte`，备注 `catpaw`
