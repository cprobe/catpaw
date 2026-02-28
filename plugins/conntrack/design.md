# conntrack 插件设计

## 概述

监控 Linux 内核连接跟踪表（nf_conntrack）的使用率：当 `nf_conntrack_count / nf_conntrack_max` 超过阈值时产出告警事件。

连接跟踪表满时新连接被内核静默丢弃（`nf_conntrack: table full, dropping packet`），表现为随机连接失败，是最难排查的网络问题之一——应用层看不到任何错误，tcpdump 能看到 SYN 但无 SYN-ACK，只有 `dmesg` 或 `journalctl` 里有内核日志。

**定位**：纯 Linux 内核级监控。与 net 插件（检查端口连通性）互补——net 检查的是"能否建连"，conntrack 检查的是"为什么突然建连失败"。

**参考**：Prometheus `node_exporter` 的 conntrack collector。node_exporter 采集指标值，catpaw 关注异常——当使用率逼近上限时主动告警，而非等到丢包后排查。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 连接跟踪表使用率 | `conntrack::conntrack_usage` | count/max 百分比超阈值告警 |

- **target label** 为 `"system"`（连接跟踪表是系统级唯一资源）
- **默认 title_rule** 为 `"[check]"`

## 数据来源

读取 `/proc/sys/net/netfilter/` 下的两个文件：

| 文件 | 含义 | 示例值 |
| --- | --- | --- |
| `nf_conntrack_count` | 当前跟踪的连接数 | `12345` |
| `nf_conntrack_max` | 连接跟踪表上限 | `262144` |

### 文件路径优先级

| 优先级 | 路径前缀 | 适用内核 |
| --- | --- | --- |
| 1（优先） | `/proc/sys/net/netfilter/nf_conntrack_*` | 2.6.18+（所有现代发行版） |
| 2（回退） | `/proc/sys/net/ipv4/netfilter/ip_conntrack_*` | 2.6.14 – 2.6.17（极罕见） |

Gather 时先尝试优先路径，若 `os.ErrNotExist` 则尝试回退路径，均不存在则视为模块未加载。

### nf_conntrack 模块未加载

很多服务器没有 iptables/nftables 规则需要连接跟踪，`nf_conntrack` 模块不会被加载，上述 proc 文件不存在。

**处理策略**：Gather 静默返回（不产出任何事件）。理由：
- 模块未加载 = 无连接跟踪表 = 无耗尽风险 = 没有需要告警的对象
- 每次 Gather 都发 Ok 事件是噪音——用户配置了插件但系统不需要 conntrack
- 模块后续被加载（如添加 iptables 规则）时，下一轮 Gather 自动检测到文件并开始监控

区分"模块未加载"和"真实读取失败"：
- `os.ErrNotExist`（两组路径均不存在）→ 模块未加载 → 静默返回
- 其他错误（权限不足等）→ 产出 Critical 事件（原则 7：自身故障可感知）

### 无新增依赖

仅读取 proc 文件，使用标准库 `os.ReadFile` + `strconv.ParseUint`，无需任何第三方依赖。

## 阈值设计

### 使用率百分比

`conntrack_usage = count / max * 100`

| 阈值 | 含义 |
| --- | --- |
| `warn_ge = 75.0` | 使用率 ≥ 75% 时 Warning（还有缓冲，但需关注） |
| `critical_ge = 90.0` | 使用率 ≥ 90% 时 Critical（即将耗尽，需立即处理） |

### 为什么默认阈值偏保守

连接跟踪表满的后果极为严重（静默丢包），且从 90% 到 100% 可能在秒级发生（突发流量）。75% Warning 给运维留出调整 `nf_conntrack_max` 或排查连接泄漏的时间窗口。

### 处置建议（供 Description 参考）

用户收到告警后典型处置：
1. `sysctl -w net.netfilter.nf_conntrack_max=524288`（临时翻倍）
2. 排查是否有连接泄漏（`conntrack -L | awk '{print $4}' | sort | uniq -c | sort -rn`）
3. 持久化：写入 `/etc/sysctl.d/99-conntrack.conf`

## 结构体设计

```go
type ConntrackUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    ConntrackUsage ConntrackUsageCheck `toml:"conntrack_usage"`
}

type ConntrackPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

不需要：
- `Timeout` — 读取 proc 文件是本地操作，微秒级完成
- `Concurrency` — 仅读两个文件，无并发需求
- `inFlight` / `GatherTimeout` — 不涉及网络或阻塞操作
- `Targets` — 连接跟踪表是系统级唯一资源

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_count` | `12345` | 当前连接数 |
| `_attr_max` | `262144` | 上限值 |
| `_attr_usage_percent` | `4.7%` | 格式化的使用率 |

Ok 事件也携带完整 `_attr_` 标签，便于巡检时确认连接跟踪表容量和当前水位。

## Init() 校验

Init() 只校验配置**合法性**，不校验"是否启用"——阈值全为 0 时不报错，Gather 静默跳过即可。与 cpu、mem、uptime 等插件保持一致（原则 11：一次学会，处处适用）。

```
Init():
    1. if runtime.GOOS != "linux":
        return error: "conntrack plugin only supports linux (current: <os>)"

    2. if warn_ge > 0 && critical_ge > 0 && warn_ge >= critical_ge:
        return error: "conntrack_usage.warn_ge must be less than critical_ge"
```

默认配置文件已提供 `warn_ge = 75.0` / `critical_ge = 90.0` 作为未注释的值——使用默认配置的用户开箱即用；自定义配置不写阈值的用户得到静默跳过。

## Gather() 逻辑

```
Gather(q):
    // 阈值全为 0 时静默跳过
    if ins.ConntrackUsage.WarnGe == 0 && ins.ConntrackUsage.CriticalGe == 0:
        return

    count, max, err = readConntrackFiles()

    if err == errModuleNotLoaded:
        return   // 模块未加载，静默跳过

    if err != nil:
        event = buildEvent("conntrack::conntrack_usage", "system")
        event → Critical: "failed to read conntrack data: <error>"
        q.PushFront(event)
        return

    if max == 0:
        event = buildEvent("conntrack::conntrack_usage", "system")
        event → Critical: "nf_conntrack_max is 0, cannot calculate usage"
        q.PushFront(event)
        return

    usagePercent = float64(count) / float64(max) * 100

    event = buildEvent("conntrack::conntrack_usage", "system")
    event._attr_count = strconv.FormatUint(count, 10)
    event._attr_max = strconv.FormatUint(max, 10)
    event._attr_usage_percent = fmt.Sprintf("%.1f%%", usagePercent)

    status = EvaluateGeThreshold(usagePercent, warn_ge, critical_ge)
    event.SetEventStatus(status)

    switch status:
        Critical: "conntrack usage 94.2% (12345/13107), above critical threshold 90%"
        Warning:  "conntrack usage 78.5% (12345/15728), above warning threshold 75%"
        Ok:       "conntrack usage 4.7% (12345/262144), everything is ok"

    q.PushFront(event)
```

### readConntrackFiles() 伪代码

```
readConntrackFiles() (count uint64, max uint64, err error):
    paths = [
        ("/proc/sys/net/netfilter/nf_conntrack_count", "/proc/sys/net/netfilter/nf_conntrack_max"),
        ("/proc/sys/net/ipv4/netfilter/ip_conntrack_count", "/proc/sys/net/ipv4/netfilter/ip_conntrack_max"),
    ]

    for each (countPath, maxPath) in paths:
        countBytes, err1 = os.ReadFile(countPath)
        maxBytes, err2 = os.ReadFile(maxPath)

        if err1 == nil && err2 == nil:
            count, parseErr1 = strconv.ParseUint(trim(countBytes), 10, 64)
            if parseErr1 != nil:
                return 0, 0, fmt.Errorf("parse %s: %v", countPath, parseErr1)
            max, parseErr2 = strconv.ParseUint(trim(maxBytes), 10, 64)
            if parseErr2 != nil:
                return 0, 0, fmt.Errorf("parse %s: %v", maxPath, parseErr2)
            return count, max, nil

        if !os.IsNotExist(err1) || !os.IsNotExist(err2):
            // 文件存在但读取失败（权限等），返回具体错误
            if err1 != nil:
                return 0, 0, fmt.Errorf("read %s: %v", countPath, err1)
            return 0, 0, fmt.Errorf("read %s: %v", maxPath, err2)

    // 所有路径均不存在 → 模块未加载
    return 0, 0, errModuleNotLoaded
```

### 关键行为

1. **模块未加载时静默跳过**（不产出事件，无噪音）
2. **模块加载后自动激活**（每轮 Gather 重新尝试读文件，无需重启 catpaw）
3. **文件读取失败产出 Critical 事件**（原则 7：自身故障可感知）
4. **`nf_conntrack_max = 0` 产出 Critical 事件**（防止除零，且 max=0 本身是异常配置）
5. **单次 Gather 产出 0 或 1 个事件**（模块未加载时 0 个，其他情况 1 个）
6. **无需并发、无需 goroutine**——同步读两个小文件（原则 8：采集开销可控）
7. **优先尝试 nf_conntrack 路径**，仅在不存在时回退 ip_conntrack（原则 10：向后兼容）

## Description 示例

- 使用率正常（Ok）：`conntrack usage 4.7% (12345/262144), everything is ok`
- 使用率偏高（Warning）：`conntrack usage 78.5% (205783/262144), above warning threshold 75%`
- 即将耗尽（Critical）：`conntrack usage 94.2% (246887/262144), above critical threshold 90%`
- 读取失败（Critical）：`failed to read conntrack data: read /proc/sys/net/netfilter/nf_conntrack_count: permission denied`
- max 异常（Critical）：`nf_conntrack_max is 0, cannot calculate usage`
- 解析失败（Critical）：`failed to read conntrack data: parse /proc/sys/net/netfilter/nf_conntrack_count: strconv.ParseUint: parsing "abc": invalid syntax`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| warn_ge | `75.0` | 连接跟踪表满后果极严重（静默丢包），75% 留出缓冲 |
| critical_ge | `90.0` | 90% 到 100% 可能瞬间发生，需要立即行动 |
| interval | `"30s"` | 连接数变化可能很快（突发流量），30 秒足够及时 |
| for_duration | `0` | conntrack 使用率波动不大（不像 CPU），单次超阈即需关注 |
| repeat_interval | `"5m"` | 持续高位时定期提醒 |
| repeat_number | `0` | 不限制，直到使用率下降 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 连接跟踪表即将耗尽 | **conntrack** | 在丢包发生前预警 |
| 连接跟踪表满导致丢包（内核日志） | journaltail / dmesg | 已发生丢包，捕获 `nf_conntrack: table full` 日志 |
| 端口连通性检测 | net | 检查是否能建连（可能因 conntrack 满而失败） |
| 网络延迟/丢包 | ping | 检查网络质量 |

conntrack 与 journaltail 互补：conntrack 是**预警**（使用率逼近上限），journaltail 是**事后确认**（已经发生丢包）。理想情况下 conntrack 告警在 journaltail 之前触发，让运维有时间处置。

## 跨平台兼容性

| 平台 | 支持 | 处理方式 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 proc 文件 |
| macOS | 不支持 | Init 返回错误，插件不加载 |
| Windows | 不支持 | Init 返回错误，插件不加载 |

连接跟踪（nf_conntrack）是 Linux Netfilter 框架的组成部分，macOS（pf）和 Windows（WFP）有各自的状态跟踪机制但无统一接口，且无用户可见的"表满"问题。

### Linux 发行版兼容性

| 发行版 | 内核版本 | conntrack 路径 |
| --- | --- | --- |
| RHEL/CentOS 7+ | 3.10+ | `/proc/sys/net/netfilter/nf_conntrack_*` |
| RHEL/CentOS 6 | 2.6.32 | `/proc/sys/net/netfilter/nf_conntrack_*` |
| Ubuntu 14.04+ | 3.13+ | `/proc/sys/net/netfilter/nf_conntrack_*` |
| Debian 8+ | 3.16+ | `/proc/sys/net/netfilter/nf_conntrack_*` |
| SUSE 12+ | 3.12+ | `/proc/sys/net/netfilter/nf_conntrack_*` |
| 极旧系统（< 2.6.18） | 2.6.14 – 2.6.17 | `/proc/sys/net/ipv4/netfilter/ip_conntrack_*`（回退路径） |

实际上 2006 年后的所有 Linux 发行版均使用 `nf_conntrack_*` 路径。回退路径仅为极端情况兜底。

### 容器环境

在容器内读取 `/proc/sys/net/netfilter/nf_conntrack_*` 获取的是**宿主机**的值（默认 namespace 下 proc 不隔离）。若 catpaw 运行在容器内，监控的是宿主机的连接跟踪表。这通常是期望的行为——连接跟踪表是宿主机级别资源，在容器内监控正好覆盖"宿主机 conntrack 满导致容器网络异常"的场景。

## 文件结构

```
plugins/conntrack/
    design.md             # 本文档
    conntrack.go          # 主逻辑（仅 Linux）
    conntrack_test.go     # 测试

conf.d/p.conntrack/
    conntrack.toml        # 默认配置
```

不需要 build tags 文件（`_linux.go` / `_notlinux.go`）——通过 `runtime.GOOS` 在 Init 中检查即可（与 ntp 插件一致），保持文件结构简洁。

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 监控 Linux 连接跟踪表（nf_conntrack）使用率
## 表满时新连接被内核静默丢弃，是最难排查的网络问题之一
## nf_conntrack 模块未加载时自动跳过（不告警）

## 连接跟踪表使用率阈值（count / max 百分比）
## 表满后果极为严重，默认阈值偏保守
[instances.conntrack_usage]
warn_ge = 75.0
critical_ge = 90.0
# title_rule = "[check]"

## 采集间隔
interval = "30s"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 0
# disabled = false
# disable_recovery_notification = false
```
