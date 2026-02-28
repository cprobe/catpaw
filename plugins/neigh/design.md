# neigh 插件设计

## 概述

监控 Linux 内核 ARP/邻居表（neighbour table）使用率：当 `entries / gc_thresh3` 超过阈值时产出告警事件。

邻居表满时内核无法为新 IP 地址解析 MAC 地址，导致与**新 IP** 的通信静默失败——已缓存的邻居正常工作，只有首次通信的目标受影响，表现为间歇性、随机性的网络故障。这是容器/Kubernetes 环境中最常见的"沉默杀手"之一。

**定位**：Linux 内核网络子系统监控。与 conntrack（连接跟踪表耗尽）互补——conntrack 检查的是"已建连的跟踪表"，neigh 检查的是"建连前的地址解析表"。两者满时的症状几乎一样（新连接失败），但根因和处置方式完全不同。

**参考**：Prometheus `node_exporter` 不直接采集邻居表使用率。这是一个被广泛低估的监控盲区——大多数监控系统只有在 `dmesg` 出现 `neighbour table overflow` 后才能事后发现。

### 命名说明

todo 中暂定 `nf_neigh`，但 `nf_` 前缀表示 Netfilter（`nf_conntrack` 属于 Netfilter 框架），而邻居表属于内核网络子系统（neighbour subsystem），与 Netfilter 无关。最终命名为 `neigh`，与内核路径 `/proc/sys/net/ipv4/neigh/` 一致，也符合项目命名惯例（conntrack、cpu、mem 均以概念命名）。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 邻居表使用率 | `neigh::neigh_usage` | entries/gc_thresh3 百分比超阈值告警 |

- **target label** 为 `"system"`（邻居表是系统级共享资源）
- **默认 title_rule** 为 `"[check]"`

## 数据来源

需要读取两类数据：当前条目数（count）和硬上限（max）。

### 当前条目数：`/proc/net/arp`

```
IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         00:11:22:33:44:55     *        eth0
10.244.0.3       0x1         0x2         aa:bb:cc:dd:ee:ff     *        cni0
```

- 第 1 行为固定表头，数据行从第 2 行开始
- 每行代表一个邻居表条目
- **条目数 = 总行数 - 1**（减去表头）

### 硬上限：`/proc/sys/net/ipv4/neigh/default/gc_thresh3`

| gc 阈值 | 含义 | 默认值 |
| --- | --- | --- |
| `gc_thresh1` | 低于此值时 GC 不运行 | `128` |
| `gc_thresh2` | 超过此值时 GC 变得积极（5 秒超时） | `512` |
| `gc_thresh3` | **硬上限**，超过时内核拒绝新条目 | `1024` |

我们只关注 `gc_thresh3`——这是触发 `neighbour table overflow` 的阈值。

### 关于 /proc/net/arp 的计数精度

`/proc/net/arp` 显示的是已完成 ARP 解析的条目（Flags 含 `ATF_COM`）。内核邻居表还可能包含 INCOMPLETE（正在解析）和 FAILED（解析失败）状态的条目，这些条目同样占用 gc_thresh3 配额但不一定出现在 `/proc/net/arp` 中。

实际影响很小：INCOMPLETE 条目存在时间极短（3 次重试 × 1 秒超时 ≈ 3 秒），FAILED 条目会被快速垃圾回收。在绝大多数场景下，`/proc/net/arp` 行数与实际邻居表大小的差异可忽略。

如需精确计数，需通过 Netlink（`RTM_GETNEIGH`），但会引入额外复杂度和依赖。对于监控预警场景，/proc/net/arp 的精度完全足够。

### IPv4 vs IPv6

| 协议 | 邻居表 | gc_thresh3 路径 | 条目查看 |
| --- | --- | --- | --- |
| IPv4 | ARP 表 | `/proc/sys/net/ipv4/neigh/default/gc_thresh3` | `/proc/net/arp` |
| IPv6 | NDP 表 | `/proc/sys/net/ipv6/neigh/default/gc_thresh3` | 需 Netlink 或 `ip -6 neigh` |

本插件 v1 聚焦 **IPv4**。理由：
- 绝大多数 `neighbour table overflow` 事故发生在 IPv4 ARP 表
- IPv4 条目可通过 `/proc/net/arp` 零依赖获取
- IPv6 NDP 条目没有对应的 proc 文件，需 Netlink，增加复杂度
- IPv6 邻居表溢出在实际生产中极为罕见（即使在 dual-stack Kubernetes 中）

IPv6 支持可作为后续迭代——需求出现时再加，避免过度设计。

### 无新增依赖

仅读取 proc 文件，使用标准库 `os.ReadFile` + `strings` + `strconv.ParseUint`，无需任何第三方依赖。

## 阈值设计

### 使用率百分比

`neigh_usage = entries / gc_thresh3 * 100`

| 阈值 | 含义 |
| --- | --- |
| `warn_ge = 75.0` | 使用率 ≥ 75% 时 Warning |
| `critical_ge = 90.0` | 使用率 ≥ 90% 时 Critical |

### 为什么与 conntrack 相同的 75/90 而非 filefd 的 80/90

邻居表增长模式与 conntrack 类似——可以在短时间内急剧增长：
- Kubernetes 节点滚动更新：大量新 Pod IP 同时出现
- 服务网格 sidecar 重启：瞬间建立到多个新 IP 的连接
- 网络扫描或服务发现：短时间内探测大量 IP

且默认 `gc_thresh3 = 1024` 非常小，75% = 768 条目，在容器密集型节点上很容易触及。75% Warning 提供了必要的预警缓冲。

### 为什么 Kubernetes 环境是重灾区

| 因素 | 说明 |
| --- | --- |
| 默认 gc_thresh3 太小 | 1024 条目，但一个节点可能有数百个 Pod |
| Pod IP 高频变化 | 滚动更新、扩缩容导致新 ARP 条目不断产生 |
| 跨节点通信 | 每个 Pod 可能与其他节点的多个 Pod 通信 |
| STALE 条目堆积 | GC 在 gc_thresh2 以下不积极清理，条目存活时间长 |

典型症状：节点运行一段时间后，部分新建的 Pod 无法通信，但重启网络或手动清理 ARP 表后恢复。

### 处置建议（供 Description 参考）

用户收到告警后典型处置：
1. `cat /proc/net/arp | wc -l` 确认当前条目数
2. `sysctl net.ipv4.neigh.default.gc_thresh3` 确认当前上限
3. 临时扩容：`sysctl -w net.ipv4.neigh.default.gc_thresh3=8192`（同步调整 gc_thresh1/gc_thresh2）
4. 排查是否有异常大量的邻居条目（`arp -n | awk '{print $5}' | sort | uniq -c | sort -rn`，按网卡分组查看）
5. 持久化：写入 `/etc/sysctl.d/99-neigh.conf`

推荐同步调整三个阈值保持比例：
```
net.ipv4.neigh.default.gc_thresh1 = 1024
net.ipv4.neigh.default.gc_thresh2 = 4096
net.ipv4.neigh.default.gc_thresh3 = 8192
```

## 结构体设计

```go
type NeighUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    NeighUsage NeighUsageCheck `toml:"neigh_usage"`
}

type NeighPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

不需要：
- `Timeout` — 读取 proc 文件是本地操作
- `Concurrency` — 仅读两个文件，无并发需求
- `Targets` — 邻居表是系统级唯一资源（IPv4）

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_entries` | `768` | 当前邻居表条目数 |
| `_attr_gc_thresh3` | `1024` | 硬上限 |
| `_attr_usage_percent` | `75.0%` | 格式化的使用率 |

Ok 事件也携带完整 `_attr_` 标签，便于巡检时确认邻居表容量和当前水位。

## Init() 校验

Init() 只校验配置**合法性**，不校验"是否启用"——阈值全为 0 时不报错，Gather 静默跳过即可。与 cpu、mem、conntrack、filefd 等插件保持一致。

```
Init():
    1. if runtime.GOOS != "linux":
        return error: "neigh plugin only supports linux (current: <os>)"

    2. if warn_ge < 0 || warn_ge > 100 || critical_ge < 0 || critical_ge > 100:
        return error: "neigh_usage thresholds must be between 0 and 100"

    3. if warn_ge > 0 && critical_ge > 0 && warn_ge >= critical_ge:
        return error: "neigh_usage.warn_ge must be less than critical_ge"
```

## Gather() 逻辑

```
Gather(q):
    // 阈值全为 0 时静默跳过
    if ins.NeighUsage.WarnGe == 0 && ins.NeighUsage.CriticalGe == 0:
        return

    entries, gcThresh3, err = readNeighData()

    if err != nil:
        event = buildEvent("neigh::neigh_usage", "system")
        event → Critical: "failed to read neigh data: <error>"
        q.PushFront(event)
        return

    if gcThresh3 == 0:
        event = buildEvent("neigh::neigh_usage", "system")
        event → Critical: "gc_thresh3 is 0, cannot calculate usage"
        q.PushFront(event)
        return

    usagePercent = float64(entries) / float64(gcThresh3) * 100
    entriesStr = strconv.FormatUint(entries, 10)
    gcThresh3Str = strconv.FormatUint(gcThresh3, 10)

    event = buildEvent("neigh::neigh_usage", "system")
    event._attr_entries = entriesStr
    event._attr_gc_thresh3 = gcThresh3Str
    event._attr_usage_percent = fmt.Sprintf("%.1f%%", usagePercent)

    status = EvaluateGeThreshold(usagePercent, warn_ge, critical_ge)
    event.SetEventStatus(status)

    switch status:
        Critical: "neigh table usage 92.3% (946/1024), above critical threshold 90%"
        Warning:  "neigh table usage 78.1% (800/1024), above warning threshold 75%"
        Ok:       "neigh table usage 12.5% (128/1024), everything is ok"

    q.PushFront(event)
```

### readNeighData() 伪代码

```
readNeighData() (entries uint64, gcThresh3 uint64, err error):
    // 1. 读取 gc_thresh3（硬上限）
    thresh3Data, err = os.ReadFile("/proc/sys/net/ipv4/neigh/default/gc_thresh3")
    if err != nil:
        return 0, 0, fmt.Errorf("read gc_thresh3: %v", err)

    gcThresh3, err = strconv.ParseUint(strings.TrimSpace(string(thresh3Data)), 10, 64)
    if err != nil:
        return 0, 0, fmt.Errorf("parse gc_thresh3: %v", err)

    // 2. 读取 /proc/net/arp 并计数
    arpData, err = os.ReadFile("/proc/net/arp")
    if err != nil:
        return 0, 0, fmt.Errorf("read /proc/net/arp: %v", err)

    // 按行分割，减去表头
    content = strings.TrimSpace(string(arpData))
    if content == "":
        return 0, gcThresh3, nil

    lines = strings.Split(content, "\n")
    if len(lines) <= 1:
        return 0, gcThresh3, nil  // 只有表头，无条目

    entries = uint64(len(lines) - 1)
    return entries, gcThresh3, nil
```

### 关键行为

1. **无"模块未加载"场景** — 邻居子系统是内核网络栈的基础组件，`/proc/net/arp` 和 `gc_thresh3` 在所有 Linux 系统上始终存在
2. **先读 gc_thresh3 再读 arp 表** — 如果 gc_thresh3 读取失败，直接报错，无需继续读 arp 表
3. **空 ARP 表（entries=0）是合法状态** — 不产出错误，正常计算使用率为 0%
4. **文件读取/解析失败产出 Critical 事件**（原则 7：自身故障可感知）
5. **`gc_thresh3 = 0` 产出 Critical 事件**（防止除零，且该值为 0 是异常配置）
6. **单次 Gather 产出 0 或 1 个事件**（阈值全为 0 时 0 个，其他情况 1 个）
7. **无需并发、无需 goroutine** — 同步读两个小文件（原则 8：采集开销可控）

### /proc/net/arp 在大规模环境下的性能

当邻居表有数千条目时，`/proc/net/arp` 文件可能达到数百 KB。读取和计数行仍然是微秒级操作（proc 是内存文件系统），不会成为性能瓶颈。我们只需要计数行数，不需要解析每一行的字段。

## Description 示例

- 使用率正常（Ok）：`neigh table usage 12.5% (128/1024), everything is ok`
- 使用率偏高（Warning）：`neigh table usage 78.1% (800/1024), above warning threshold 75%`
- 即将耗尽（Critical）：`neigh table usage 92.3% (946/1024), above critical threshold 90%`
- 读取失败（Critical）：`failed to read neigh data: read gc_thresh3: open /proc/sys/net/ipv4/neigh/default/gc_thresh3: permission denied`
- 解析失败（Critical）：`failed to read neigh data: parse gc_thresh3: strconv.ParseUint: parsing "abc": invalid syntax`
- gc_thresh3 异常（Critical）：`gc_thresh3 is 0, cannot calculate usage`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| warn_ge | `75.0` | 默认 gc_thresh3=1024 很小，75%=768 条目在容器环境易触及，需要预警缓冲 |
| critical_ge | `90.0` | 90%=921 条目，距离溢出仅剩约 100 个位置，需立即处理 |
| interval | `"30s"` | 容器 IP 变化可能很快，30 秒粒度足够及时 |
| for_duration | `0` | 邻居表使用率高于阈值即需关注，单次超阈即告警 |
| repeat_interval | `"5m"` | 持续高位时定期提醒 |
| repeat_number | `0` | 不限制，直到使用率下降 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 邻居表即将溢出 | **neigh** | 在 ARP 解析失败前预警 |
| 邻居表溢出后的内核日志 | journaltail / dmesg | 捕获 `neighbour table overflow` 日志 |
| 连接跟踪表耗尽 | conntrack | 另一个网络"沉默杀手"，症状相似但根因不同 |
| 端口连通性检测 | net | 检查是否能建连（可能因 neigh 满而失败） |

**neigh 与 conntrack 的鉴别价值**：当用户同时告警 neigh + conntrack，说明是流量/连接数整体过大；只告警 neigh 不告警 conntrack，说明是新 IP 过多（如容器扩缩容）；只告警 conntrack 不告警 neigh，说明是长连接过多。

## 跨平台兼容性

| 平台 | 支持 | 处理方式 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/proc/net/arp` + `/proc/sys/net/ipv4/neigh/default/gc_thresh3` |
| macOS | 不支持 | Init 返回错误，插件不加载 |
| Windows | 不支持 | Init 返回错误，插件不加载 |

macOS 和 Windows 有各自的 ARP 机制，但没有类似的"表满静默丢包"问题——它们的 ARP 表实现采用动态扩展策略，不存在硬上限。

### 容器环境

在容器内读取 `/proc/net/arp` 和 `/proc/sys/net/ipv4/neigh/default/gc_thresh3` 获取的是**宿主机**的值。若 catpaw 运行在容器内，监控的是宿主机的邻居表。这是期望的行为——邻居表是宿主机级别资源，正好覆盖"宿主机 ARP 表满导致容器网络异常"的场景。

**注意**：Kubernetes 场景下，catpaw 通常以 DaemonSet 部署在每个节点，刚好一个 catpaw 实例监控一个节点的邻居表。

## 文件结构

```
plugins/neigh/
    design.md             # 本文档
    neigh.go              # 主逻辑（仅 Linux）
    neigh_test.go         # 测试

conf.d/p.neigh/
    neigh.toml            # 默认配置
```

不需要 build tags 文件——通过 `runtime.GOOS` 在 Init 中检查即可，保持文件结构简洁。

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 监控 Linux ARP/邻居表（neighbour table）使用率
## 表满时新 IP 的通信静默失败，已缓存的正常，极难排查
## Kubernetes / 容器密集型环境是重灾区（默认 gc_thresh3=1024 太小）

## 邻居表使用率阈值（entries / gc_thresh3 百分比）
[instances.neigh_usage]
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
