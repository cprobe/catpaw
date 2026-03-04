# catpaw：会自己看病的监控 Agent

> **TL;DR**：catpaw 是一个轻量级监控 Agent，27 个插件覆盖服务器核心风险点，单二进制零依赖。与众不同的是——告警触发后，它会自动调用 70+ 诊断工具分析根因，把诊断报告和告警一起推给你。登录机器后还可以用 `catpaw chat` 和 AI 对话排障，不用记命令。

## 凌晨 3 点，你被叫醒了

磁盘满了。证书过期了。conntrack 表被打满，线上静默丢包。

你睡眼惺忪地打开电脑，看到告警消息写着：

> **[Critical] disk::space_usage / 磁盘使用率 97.2% >= 95%**

然后呢？

你开始回忆命令：`df -h` 还是 `du -sh /*`？大文件在哪？是日志没轮转还是 core dump 堆积？要不要看看 inode？

传统的监控工具到这里就结束了——它告诉你**出了什么问题**，但不告诉你**为什么**。诊断这件事，留给了凌晨 3 点半脑子不太清醒的你。

**如果监控工具不仅能发现问题，还能自己分析一遍，把初步结论告诉你呢？**

这就是 catpaw 想做的事。

## catpaw 是什么

catpaw（猫爪）是一个带 AI 大脑的轻量监控 Agent。它做两件事：

> **1. 探测异常，产出告警事件**
> **2. 告警触发后，AI 自动诊断根因**

你收到的不再只是一条干巴巴的"磁盘满了"，而是一份诊断报告：

```markdown
## 诊断报告

### 问题概要
/ 分区使用率 97.2%，超过 Critical 阈值 95%。

### 根因分析
- /var/log/app/access.log 占用 45GB，最近 24h 增长 12GB
- 日志轮转配置缺失，logrotate 中未配置该路径
- /tmp 下存在 3 个 core dump 文件，共 8.2GB

### 建议措施
1. 清理 core dump：rm /tmp/core.*
2. 配置 logrotate 轮转 /var/log/app/
3. 考虑扩容或迁移日志到独立分区
```

**凌晨 3 点收到这样的消息，和收到一行 "disk usage 97.2%" 的感受，是完全不同的。**

## 架构一览

```text
┌─────────────────────────────────────────────────────────────────┐
│                        catpaw agent                             │
│                                                                 │
│  ┌─────────────┐   告警     ┌──────────────┐    AI + 工具      │
│  │  27 个检查  │ ────────── │  AI 诊断     │ ──────────────┐   │
│  │    插件     │   触发     │    引擎      │               │   │
│  └──────┬──────┘            └──────────────┘               │   │
│         │                                                  ▼   │
│         │ 事件      ┌──────────────┐         ┌───────────────┐ │
│         └────────── │   通知渠道   │         │  70+ 诊断    │ │
│                     │  （多选）    │         │     工具     │ │
│                     └──────────────┘         └───────┬───────┘ │
│                                                      │         │
│  ┌─────────────┐                            ┌────────┴───────┐ │
│  │  AI Chat    │ ───── 自然语言排障 ──────── │  MCP 外部     │ │
│  │  (命令行)   │                            │  数据源       │ │
│  └─────────────┘                            └────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

**完整链路：** 插件检测异常 → 事件引擎去重/抑制 → 推送告警 → AI 自动诊断 → 诊断报告跟随告警一起推送。

## 三种 AI 能力

### 🤖 自动诊断：告警触发，AI 自己查

这是最核心的能力。你不需要做任何事——配置好 AI 之后，告警触发时 catpaw 会自动：

1. 聚合同一目标的多个告警（5s 窗口）
2. 构建告警上下文（指标值、阈值、检查维度）
3. AI 多轮调用诊断工具（CPU Top、内存分析、日志搜索、网络状态……）
4. 生成结构化诊断报告
5. 把报告作为新事件推送到你的通知渠道

你在手机上看到告警的同时，就能看到 AI 的分析结论。

### 💬 交互 Chat：登录机器，和 AI 对话

```bash
./catpaw chat
```

凌晨 3 点登录故障机器，你不用记命令了：

```
You: 最近有 OOM 吗？
AI: [调用 oom_history 工具]
    发现 2 次 OOM 事件：
    - 03:12 java (PID 8847) 被 OOM Killer 终止，RSS 7.8GB
    - 02:45 python3 (PID 9123) 被 OOM Killer 终止，RSS 3.2GB
    建议检查这两个进程的内存限制配置。

You: 看看 java 进程现在的内存情况
AI: [调用 process_detail, mem_top 工具]
    PID 12305 (java) 当前 RSS 6.1GB，持续增长中...
```

AI 会调用 70+ 内置诊断工具，必要时还可以执行 shell 命令（会先征得你的同意）。**不需要记 `ss -tlnp` 还是 `netstat -an`，不需要记 `awk` 的复杂语法，用自然语言描述你想查什么就行。**

### 🩺 主动巡检：没告警也能查

```bash
./catpaw inspect cpu
./catpaw inspect disk /data
```

不需要等到告警才看，主动让 AI 给某个维度做一次全面体检。

## 27 个检查插件

catpaw 不是指标采集器（不与 Prometheus + Node-Exporter 重叠），它只关心**有没有问题**。

### 基础资源

| 插件 | 一句话说明 |
| --- | --- |
| `cpu` | CPU 使用率、归一化 Load Average |
| `mem` | 内存 / Swap 使用率 |
| `disk` | 磁盘空间、inode、可写性 |
| `uptime` | 异常重启检测 |

### 网络与连通性

| 插件 | 一句话说明 |
| --- | --- |
| `ping` | ICMP 可达性、丢包率、时延 |
| `net` | TCP/UDP 连通性与响应时间 |
| `http` | HTTP 可用性、状态码、响应体 |
| `dns` | DNS 解析检查 |
| `cert` | TLS 证书有效期（远程 + 本地） |

### Linux 内核暗坑

这部分是 catpaw 最有价值的地方——**很多问题你不监控就永远不知道：**

| 插件 | 故事 |
| --- | --- |
| `conntrack` | K8s 集群莫名丢包？排查两天，发现 nf_conntrack 表满了 |
| `neigh` | 新 Pod 无法通信？ARP 邻居表满了，大规模 K8s 经典暗坑 |
| `sysctl` | 精心调优的内核参数，内核升级后静默回到默认值 |
| `filefd` | 系统级 fd 耗尽，所有服务同时报错 |
| `tcpstate` | CLOSE_WAIT 堆积到上万，连接泄漏 |
| `sockstat` | TCP listen 队列溢出，请求被静默丢弃 |
| `netif` | 网卡错误包 / 丢包持续增长 |
| `ntp` | 时钟偏移导致日志乱序、token 失效 |

### 进程与服务

| 插件 | 一句话说明 |
| --- | --- |
| `procnum` | 进程存活检查 |
| `procfd` | 单进程 fd 使用率，预防 too many open files |
| `zombie` | 僵尸进程检测 |
| `systemd` | systemd 服务状态 |
| `docker` | 容器运行状态、频繁重启、健康检查 |

### 日志与脚本

| 插件 | 一句话说明 |
| --- | --- |
| `logfile` | 纯文本日志关键字匹配（支持轮转、glob、多编码） |
| `journaltail` | journalctl 日志匹配，监控 OS 异常、硬件故障、OOM |
| `exec` | 执行脚本，按输出产生事件，支持 Nagios 插件脚本 |
| `scriptfilter` | 脚本输出行过滤告警 |

## 70+ 诊断工具

告警触发后，AI 不是在胡猜——它有一整套工具箱：

| 领域 | 工具举例 |
| --- | --- |
| ⚙️ 系统 | CPU Top、内存分布、OOM 历史、cgroup 限制、PSI 压力 |
| 🌐 网络 | ping、traceroute、TCP 连接状态、Socket RTT/cwnd、重传率、防火墙规则 |
| 💾 存储 | 磁盘 I/O 延迟、块设备拓扑、LVM 状态 |
| 🔐 内核 | dmesg、中断分布、conntrack 统计、热区温度、sysctl 快照 |
| 📜 日志 | 日志 tail、日志 grep、journald 查询 |
| 🐳 服务 | systemd 状态、Docker ps/inspect |

还可以通过 **MCP（Model Context Protocol）** 接入 Prometheus、Jaeger 等外部数据源，让 AI 查询历史指标趋势和链路追踪。

## 5 分钟快速体验

### 第 1 步：下载

从 [https://github.com/cprobe/catpaw/releases](https://github.com/cprobe/catpaw/releases) 下载对应平台的二进制文件。

### 第 2 步：启动

解压后直接运行：

```bash
./catpaw run
```

默认配置已开启 `[notify.console]`，告警事件会直接打印到终端——无需任何外部服务。

**没消息就是好消息。** 只有检测到异常时才会输出，类似：

```text
🔴 Critical  disk::space_usage  target=/  disk usage 97.2% >= critical threshold 95.0%
🟡 Warning   mem::memory_usage  target=memory  memory usage 82.1% >= warn threshold 80.0%
```

> **想立刻看到告警？** 打开 `conf.d/p.mem/mem.toml`，把内存告警阈值改小（比如 `warn_ge = 0.01`），运行 `./catpaw run`，一分钟后就能在终端看到告警事件。验证完改回来就好。

### 第 3 步：开启 AI（可选）

在 `conf.d/config.toml` 中添加：

```toml
[ai]
enabled = true
model_priority = ["default"]

[ai.models.default]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_API_KEY}"
model = "gpt-4o"
```

现在告警触发时，AI 会自动诊断并把报告推送到你的通知渠道。默认 notify 只有 console，通过日志查看生成的告警，后续您可以对接 FlashDuty、PagerDuty 等 On-call 平台。

试试交互模式：

```bash
./catpaw chat
```

问它任何你想知道的：「磁盘空间被什么占了」「网络延迟高的原因」「最近有没有 OOM」——AI 会调用工具帮你查。

### 告警发到哪

catpaw 支持多种通知渠道，可以同时开启：

| 渠道 | 说明 |
| --- | --- |
| **Console** | 终端输出（默认开启，快速验证） |
| **WebAPI** | 推送到任意 HTTP 端点 |
| **Flashduty** | 对接 [Flashduty](https://flashcat.cloud/product/flashduty/) 告警平台 |
| **PagerDuty** | 对接 [PagerDuty](https://www.pagerduty.com/) |

## 为什么说 catpaw 适合练手 AI 编程

catpaw 可能是目前最适合验证 AI 编程能力的开源项目之一。整个项目几乎全部由 AI 编写（框架搭好之后，插件、诊断工具、通知渠道的代码都是 AI 写的），这不是噱头，而是因为项目结构天然对 AI 友好。

### 1. 模块高度独立，上下文小

每个插件是独立的 Go package，每个诊断工具是独立的函数。AI 不需要理解整个项目就能写好一个模块。

```text
plugins/disk/      ← 检查插件，独立 package
plugins/cpu/       ← 检查插件，独立 package
diagnose/tools/    ← 诊断工具，独立函数
notify/webapi.go   ← 通知后端，独立实现
```

### 2. 问题域具体明确

不是模糊的"做一个 XX 系统"，而是非常具象的任务：

- 「写一个插件检测 conntrack 表是否快满了」
- 「写一个诊断工具查看进程的打开文件列表」
- 「写一个通知后端把事件推到钉钉 webhook」

AI 擅长这类有**明确验收标准**的工作。

### 3. 有 27 个范例可参考

27 个插件 + 70 多个诊断工具 = 近 100 个同模式的实现范例。告诉 AI 参考哪个已有实现，再描述你想做什么，它就能照着模式写出来。

### 4. 验收闭环极短

写完 → `go build` → 改个 toml → `./catpaw run` → 看终端输出。整个过程不超过几分钟，不需要搭建复杂环境。**这种快速反馈循环对 AI 辅助开发特别友好。**

### 试试这些 prompt

打开你的 AI 编程工具，试试：

> 参考 catpaw 的 disk 插件实现，帮我写一个新插件 `psi`，检测 Linux 系统的 Pressure Stall Information（读取 `/proc/pressure/cpu`、`/proc/pressure/io`、`/proc/pressure/memory`），当资源压力过大时产出告警事件。

或者更有挑战的：

> 参考 catpaw 现有的诊断工具，帮我写一个新的诊断工具 `gpu_usage`，读取 NVIDIA GPU 的利用率和显存占用，让 AI 在诊断时能分析 GPU 负载。

看看 AI 能不能一次性给你一个可工作的模块。**这就是最真实的 AI 编程能力评测。**

## 参与贡献

你不需要是监控专家，甚至不需要是 Go 专家。你只需要遇到过一个"早该监控上的"问题，或者想体验一下 AI 编程的快感。

### 参与路径

```text
Level 1  →  下载试用，遇到问题提 issue
Level 2  →  用 AI 写一个自己需要的插件或诊断工具
Level 3  →  解决的是通用问题？提 PR 贡献上游
Level 4  →  优化 AI 提示词、改进诊断准确率
Level 5  →  接入新的 MCP 数据源，扩展 AI 的诊断视野
```

每一级都有价值。**尤其是 Level 2**——用 AI 写一个解决你自己问题的模块，这本身就是一次很爽的实践。

### 待认领方向

| 方向 | 说明 |
| --- | --- |
| `psi` 插件 | Linux Pressure Stall Information 监控 |
| `smart` 插件 | 磁盘 S.M.A.R.T 健康状态预测 |
| `raid` 插件 | 硬件/软件 RAID 阵列状态 |
| GPU 诊断工具 | NVIDIA GPU 利用率/显存/温度 |
| 更多 MCP 适配 | ClickHouse、Elasticsearch、Jaeger 等 |
| **你的场景** | 你遇到的"早该监控上的"问题 |

### 举个例子

例子1：比如你想开发 raid 插件，大概过程是：

- 让 AI 熟悉现在的项目情况
- 跟 AI 探讨 raid 插件的实施必要性，如果违反了 catpaw 的项目定位就不要加进来
- 跟 AI 探讨插件设计思路
- 让 AI 写代码，并至少做两轮 review，AI 自己写的代码也可以自己 review 出问题
- 让 AI 提供生产环境真实测试方法

> 注意：你可以随意让 AI 在你自己的环境增加能力。但是不要轻易提交 pr。只有你真正使用生产环境测试验证的插件再提交。提交之前一定先建立 issue，描述清楚你想解决的问题和解法思路。

例子2：比如你想接入钉钉通知。大概过程是：

> catpaw 初始对接了四个通知方式，都是结构化的，其中 FlashDuty 和 PagerDuty 都是强大的 On-call 中心，支持告警统一降噪、排班、认领、升级、分析等。钉钉、飞书这类通知媒介其实不够结构化，不适合让 catpaw 直接发告警给钉钉、飞书。但你为了测试，当然可以在自己的环境里加入这个能力。AI 时代，这简直不要太容易。

- 让 AI 熟悉现在的 notify 模块的逻辑
- 让 AI 搜索并理解钉钉的机器人通知机制和接口设计
- 让 AI 参照 notify 模块，给出对接钉钉通知渠道的思路
- 你来 review 这个设计，和 AI 多轮对话捋清楚设计
- 让 AI 实现代码，并至少做两轮 review，AI 自己写的代码也可以自己 review 出问题

## 写在最后

catpaw 的名字取自"猫爪"——轻轻一抓就能抓出问题，然后告诉你为什么。

它不是要替代 Prometheus 或 Zabbix，而是做那个**部署一下就能安心不少**的补充：

- **发现问题**：27 个插件覆盖服务器核心风险点
- **诊断问题**：AI 自动分析根因，凌晨 3 点你也能看懂
- **排查问题**：Chat 模式用自然语言排障，不用记命令

如果你对以下任何一点感兴趣：

- **用它**：下载试试，5 分钟跑起来
- **和它聊**：`catpaw chat`，体验用 AI 排障的感觉
- **改它**：用 AI 写个新模块，感受快速反馈的爽感
- **一起做**：贡献代码，让 catpaw 覆盖更多场景

---

**GitHub**：[https://github.com/cprobe/catpaw](https://github.com/cprobe/catpaw)

**微信交流群**：加 `picobyte`，备注 `catpaw`
