# catpaw：一个为 AI 编程而生的轻量监控项目

> **TL;DR**：catpaw 是一个轻量级事件监控工具，27 个插件覆盖服务器核心风险点，单二进制零依赖。更重要的是——它可能是目前最适合验证 AI 编程能力的开源项目之一。来试试，用 AI 写一个属于你自己的监控插件。

---

## 凌晨 3 点，你被叫醒了

磁盘满了。证书过期了。conntrack 表被打满，线上静默丢包。某个进程悄悄挂了，直到用户投诉才发现。

事后复盘，每一个问题都是"早该监控上的"。虽然你也部署了 Node-Exporter，也部署了 Prometheus，但是很多指标的含义你并不清楚，有些 promql 也难以理解，所以迟迟没有配置。

更尴尬的是：**如果你的监控系统本身挂了，谁来告诉你？**

这就是 catpaw 要解决的问题。

---

## catpaw 是什么

catpaw（猫爪）是一个**轻量的事件监控工具**。它只做一件事：

> **探测异常，产出标准事件。**

它不是又一个指标采集器（不对标 Node-Exporter / Telegraf），不产出时序数据，不画图表。它的工作模式极简：

```
┌─────────────────────────────────────────────────────────┐
│                        catpaw                           │
│                                                         │
│  ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐         │
│  │ disk │ │ cpu  │ │ cert │ │ http │ │ ...  │  插件层  │
│  └──┬───┘ └──┬───┘ └──┬───┘ └──┬───┘ └──┬───┘         │
│     │        │        │        │        │              │
│     ▼        ▼        ▼        ▼        ▼              │
│  ┌─────────────────────────────────────────────┐       │
│  │              事件引擎 (engine)                │       │
│  │  去重 → 抑制 → 恢复检测 → 重复通知控制        │       │
│  └──────────────────┬──────────────────────────┘       │
│                     │                                   │
└─────────────────────┼───────────────────────────────────┘
                      ▼
          ┌───────────────────────┐
          │  Flashduty / Webhook  │
          │    告警分发 & 通知      │
          └───────────────────────┘
```

**核心特点：**

- **单二进制**，无 DB、无 MQ、无 agent 集群，下载即用
- **插件化架构**，27 个插件按需启用，配置项注释详尽，开箱即用
- **事件驱动**，不存储指标，只关心"有没有问题"
- **自监控友好**，适合给现有监控体系做旁路兜底

---

## 27 个插件，覆盖哪些风险

catpaw 目前内置 27 个插件（几乎全是 AI 写成的），按场景可以分为几大类：

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
| `journaltail` | journalctl 日志匹配，监控 OS 异常、硬件故障、OOM 等关键信号 |
| `exec` | 执行脚本，按输出产生事件，支持 Nagios 插件脚本 |
| `scriptfilter` | 脚本输出行过滤告警 |

---

## 5 分钟快速体验

### 第 1 步：下载

从 [GitHub Releases](https://github.com/cprobe/catpaw/releases) 下载对应平台的二进制文件。

### 第 2 步：启动测试

解压缩。

配置文件：`conf.d/config.toml`，可以先维持默认配置，后面再说。各个插件配置文件：`conf.d/p.<plugin>/<plugin>.toml`，比如：`conf.d/p.disk/disk.toml`，`conf.d/p.cert/cert.toml`，等等，也先维持默认。

启动测试：

```bash
# 测试模式：告警事件直接输出到终端
./catpaw -test
...
{"level":"info","ts":"2026-03-01T17:24:22+08:00","caller":"agent/agent.go:93","msg":"loading plugin","plugin":"secmod"}
{"level":"info","ts":"2026-03-01T17:24:22+08:00","caller":"agent/agent.go:93","msg":"loading plugin","plugin":"http"}
{"level":"info","ts":"2026-03-01T17:24:22+08:00","caller":"agent/agent.go:93","msg":"loading plugin","plugin":"cpu"}
...
```

如果一切正常，终端仅会输出一堆 info 日志，表示插件加载成功——**没消息就是好消息**。只有检测到异常时，才会打印告警事件，格式类似：

```
1740820800 03:00:00 a1b2c3d4 Critical check=cert::expiry,from_plugin=cert,target=example.com:443 certificate expires in 3 days
1740820800 03:00:00 e5f6a7b8 Warning  check=disk::space_usage,from_plugin=disk,target=/ disk usage 92.3% >= warn threshold 90.0%
```

**就这么简单。** 没有 DB，没有 Dashboard，没有复杂的配置。哲学就是：**没问题不打扰，有问题立刻告诉你。**

> **想立刻看到告警？** 打开 `conf.d/p.mem/mem.toml`，把内存使用率的告警阈值改小（比如 `warn_ge = 0.01`），然后运行 `./catpaw -test`，你的机器内存使用率大概率超过 0.01%，一分钟之后（因为默认配置 `for_duration = "60s"` 才会发送告警）就能在终端看到告警事件输出。验证完毕后记得改回来。

### 关于告警发送

catpaw 默认通过 [Flashduty](https://flashcat.cloud/product/flashduty/) 发送告警（配置 `conf.d/config.toml` 中的 webhook URL 即可）。如果你想把告警发到其他地方——钉钉、飞书、Slack、Telegram、自建系统——完全没问题。catpaw 的转发逻辑在 `engine/engine.go` 里，结构很清晰，**让 AI 帮你加一个新的发送通道，通常几分钟就能搞定**。这也是一个很好的 AI 编程练手机会。

> Flashduty 在线体验地址：https://console.flashcat.cloud/ 注册之后，在【集成中心】创建一个【标准告警事件】的集成，把集成的 webhook URL 填入 `conf.d/config.toml` 中的 `flashduty.url` 字段即可。告警之后就会发给 Flashduty， 在 Flashduty 里做进一步处理（告警降噪、上下文丰富、分派给值班人员、电话短信通知等）。

---

## 为什么说 catpaw 是 AI 编程的最佳实验田

这是我特别想聊的一个话题。

AI 编程（Cursor、Copilot、Claude 等）已经不是新鲜事了，catpaw 的代码框架搭建好了之后，各个插件的代码基本都是 AI 写成的，只需要你告诉它你想检测什么，它就能照着模式写出来。catpaw 可能是目前最适合验证 AI 编程能力的开源项目之一。

### 1. 插件高度独立，上下文小

每个插件就是一个独立的 Go package。输入是 TOML 配置，输出是事件。没有复杂的依赖关系，没有全局状态，AI 不需要理解整个项目就能写好一个插件。

```
plugins/
├── disk/          ← 独立 package
│   └── disk.go
├── cpu/           ← 独立 package
│   └── cpu.go
├── cert/          ← 独立 package
│   ├── cert.go
│   └── tls.go
└── ...
```

### 2. 问题域具体明确

不是模糊的"做一个 XX 系统"，而是非常具象的任务：

- "检测 conntrack 表是否快满了"
- "检查 TLS 证书还有几天过期"
- "监控 CLOSE_WAIT 连接数是否异常"

AI 擅长这类有**明确验收标准**的工作。

### 3. 有现成的模式可参考

27 个插件就是 27 个范例。插件实现遵循统一模式：

```go
// 1. 定义结构体
type Instance struct {
    config.InternalConfig
    // 你的配置字段
}

type YourPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}

// 2. 注册
func init() {
    plugins.Add("your_plugin", func() plugins.Plugin {
        return &YourPlugin{}
    })
}

// 3. 实现采集
func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
    // 检测逻辑
    event := types.BuildEvent(map[string]string{
        "check":  "your_plugin::your_check",
        "target": "something",
    })

    if something_is_wrong {
        q.PushFront(event.SetEventStatus(types.EventStatusCritical).
            SetDescription("what went wrong"))
        return
    }

    q.PushFront(event.SetDescription("everything is ok"))
}
```

让 AI 读几个已有插件的代码，再告诉它你想检测什么，它就能照着模式写出来。具体编写之前建议：

1. 让 AI 阅读项目 principles.md 文件，理解 catpaw 的设计原则
2. 让 AI 针对你想写的插件生成一个设计文档，放到插件代码目录下的 design.md 文件中，其他插件都是这么干的
3. review 设计文档，没问题的话就可以编码了，可以让多个 ai agent 互相 review

### 4. 验收闭环极短

写完代码 → `go build` → 改个 toml → `./catpaw -test` → 看终端输出。

整个过程不超过几分钟，不需要搭建复杂环境（只需要 go 开发环境，不依赖任何外部工具）。**这种快速反馈循环对 AI 辅助开发特别友好**——出了问题可以立刻迭代。

### 5. Go 语言对 AI 友好

静态类型、编译检查、标准库丰富。AI 生成的 Go 代码质量相对可控，编译器会帮你兜底大部分低级错误。

### 试试这个 prompt

打开你的 AI 编程工具，给它下面这段指令：

> 参考 catpaw 的 disk 插件实现，帮我写一个新插件 `psi`，检测 Linux 系统的 Pressure Stall Information（读取 `/proc/pressure/cpu`、`/proc/pressure/io`、`/proc/pressure/memory`），当 OS 资源压力过大时产出告警事件。

然后看看 AI 能不能一次性给你一个可工作的插件。**这就是最真实的 AI 编程能力评测。**

---

## 参与贡献：不需要你是专家

**你不需要是监控专家，甚至不需要是 Go 专家。** 你只需要遇到过一个"早该监控上的"问题。

### 参与路径

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│  Level 1  ──→  用 catpaw，遇到 bug 提 issue               │
│                                                             │
│  Level 2  ──→  用 AI 写一个自己需要的插件，fork 自己用       │
│                                                             │
│  Level 3  ──→  插件解决的是通用问题？提 PR 贡献上游          │
│                                                             │
│  Level 4  ──→  优化现有插件、补充测试、完善文档              │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

每一级都有价值。**尤其是 Level 2**——用 AI 写一个解决你自己问题的插件，这本身就是一次有趣的实践。如果它解决的是通用问题，那就更好了，欢迎贡献上游，我们一起 review。

### 待认领插件

以下插件还在规划中，如果你感兴趣，可以直接认领：

| 插件 | 说明 | 参考 |
| --- | --- | --- |
| `psi` | Pressure Stall Information（CPU/IO/Memory 压力） | 读 `/proc/pressure/*`，Linux 4.20+ |
| `smart` | 磁盘 S.M.A.R.T 健康状态，预测硬盘故障 | Nagios `check_smart` |
| `raid` | 硬件/软件 RAID 阵列状态 | Nagios `check_raid` |
| `mailq` | 邮件队列积压检测 | Nagios `check_mailq` |
| **你的场景** | 你遇到的"早该监控上的"问题 | 提前与 AI 探讨解法和必要性 |

### 为什么值得参与

1. **真实项目，不是玩具**。catpaw 已经在生产环境运行，解决的都是实际问题
2. **AI 编程练手场**。插件边界清晰、模式统一，是用 AI 做真实开发的绝佳场景
3. **Go 语言实战**。如果你在学 Go，这是一个复杂度适中、结构清晰的实战项目
4. **代码量可控**。一个插件通常只有 100-300 行，一个小时就能完成

---

## 架构一览

```
catpaw/
├── main.go                     程序入口
├── agent/                      插件加载与调度
│   ├── agent.go                Agent 核心
│   └── runner.go               PluginRunner，每个 instance 一个 goroutine
├── config/                     全局配置解析
├── engine/                     事件处理引擎（去重、抑制、恢复、推送）
├── types/                      Event 等核心类型
├── plugins/                    插件目录
│   ├── plugins.go              Plugin 接口定义与注册
│   ├── disk/                   磁盘检查
│   ├── cpu/                    CPU 检查
│   ├── cert/                   证书检查
│   ├── conntrack/              conntrack 表监控
│   ├── ...                     （共 27 个插件）
│   └── design.d/               插件规划
├── conf.d/                     配置目录
│   ├── config.toml             全局配置
│   └── p.<plugin>/             各插件 TOML 配置
├── docs/                       文档
└── docker/                     Docker 构建
```

**数据流：**

```
         配置加载                    定时采集                事件处理
conf.d/*.toml ──→ Agent.Start() ──→ Gather(queue) ──→ engine.PushRawEvents()
                                         │                      │
                                         ▼                      ▼
                                   types.Event            去重 / 抑制 / 恢复
                                                                │
                                                                ▼
                                                    HTTP POST → Flashduty
                                                              / Webhook
```

---

## 写在最后

catpaw 的名字取自"猫爪"——轻轻一抓就能抓出问题。

它不是要替代 Prometheus 或 Zabbix，而是做那个**轻轻部署一下就能安心不少**的补充。特别适合：

- 快速补上那些"早该监控"的检查项
- 给现有监控系统做旁路自监控
- 用 AI 快速开发你自己的监控插件

如果你对以下任何一点感兴趣，欢迎参与进来：

- **用它**：下载试试，反馈问题
- **改它**：用 AI 写个新插件，解决你自己的问题
- **一起做**：贡献代码，让 catpaw 覆盖更多场景

---

**GitHub**：[https://github.com/cprobe/catpaw](https://github.com/cprobe/catpaw)

**微信交流群**：加 `picobyte`，备注 `catpaw`
