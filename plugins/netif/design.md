# netif 插件设计

## 概述

监控 Linux 网络接口的链路状态、收发错误和丢包。当接口链路断开、出现新的错误或丢包时产出告警事件。

**核心场景**：

1. **网线松动/交换机端口故障**：物理网卡 link down，但 IP 仍配在接口上，上层应用超时后才感知——已经来不及了
2. **网卡/驱动缺陷导致静默丢包**：`rx_errors` / `rx_dropped` 持续增长，但因为不看计数器所以没人发现，表现为偶发的延迟抖动或丢包重传
3. **虚拟化/容器网络异常**：bond 备链切换、bridge 口 flap，错误计数器是最早暴露问题的信号

**与现有插件的关系**：

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 网卡链路/错误/丢包 | **netif** | 物理层和驱动层异常 |
| TCP listen 队列溢出 | sockstat | 传输层（应用未及时 accept） |
| ARP 邻居表满 | neigh | 三层邻居发现 |
| 远端连通性 | ping / net | 应用层可达性 |

**定位**：网络接口物理层 + 驱动层健康检查。与 sockstat（传输层）、neigh（邻居层）、ping/net（应用层）互补，从底向上覆盖网络栈。

**参考**：Nagios `check_ifstatus`、Prometheus `node_exporter` (netclass collector)、`ethtool -S`、`/proc/net/dev`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 链路状态 | `netif::link` | 接口名 | 指定接口是否处于 up 状态 |
| 收发错误 | `netif::errors` | 接口名 | 上次检查以来的新增 rx_errors + tx_errors |
| 收发丢包 | `netif::drops` | 接口名 | 上次检查以来的新增 rx_dropped + tx_dropped |

- **每个接口 × 每个维度独立产出事件**——eth0 的 errors 事件不影响 eth1 的 drops 事件
- **target label** 为接口名（如 `"eth0"`、`"bond0"`）
- **默认 title_rule** 为 `"[check] [target]"`

### 为什么将 errors 和 drops 拆成两个 check label

| 方案 | 优点 | 缺点 |
| --- | --- | --- |
| 合并为 `netif::health` | 事件数量少 | errors 和 drops 含义不同，合并后无法设不同阈值 |
| **拆分 errors + drops** | 语义清晰；errors = 硬件/驱动缺陷，drops = 拥塞/软件丢弃；可独立配阈值 | 每接口 2 个事件（或 3 个加 link） |

选择**拆分方案**：errors 通常暗示硬件问题（网卡故障、线缆差），drops 通常暗示拥塞（ring buffer 满、iptables/nftables 丢弃）。运维排查方向完全不同，合并会丢失关键信息。

### 为什么将 rx + tx 合并到同一个事件

rx_errors 和 tx_errors 通常由同一个根因触发（网卡故障、线缆问题同时影响收发）。拆成 4 个维度（rx_errors、tx_errors、rx_dropped、tx_dropped）会导致事件数量爆炸。`_attr_` 标签中保留 rx/tx 明细，供排查时区分方向。

## 数据来源

### `/sys/class/net/<iface>/`

```
/sys/class/net/eth0/
    operstate           # "up", "down", "unknown", "dormant", ...
    statistics/
        rx_errors       # 累计计数器
        tx_errors
        rx_dropped
        tx_dropped
        rx_packets      # 参考，不告警
        tx_packets
```

**为什么读 `/sys/class/net/` 而非 `/proc/net/dev`**：

- `/sys/class/net/` 每个计数器是独立文件，按需读取，不用解析整行
- 枚举接口时天然得到接口列表（目录名）
- 与 `operstate` 在同一目录下，读取一致
- `/proc/net/dev` 需要解析固定宽度的表格，容易因格式变化出错

**读取不会 hang**：`/sys/class/net/` 是 sysfs，由内核在内存中维护，不涉及对实际网络设备的 I/O。

### operstate 值

| 值 | 含义 |
| --- | --- |
| `up` | 链路正常 |
| `down` | 链路断开 |
| `unknown` | 驱动未报告状态（常见于虚拟接口） |
| `dormant` | 物理连接但 802.1X 等协议未完成 |
| `lowerlayerdown` | 下层接口（如 bond 的 slave）断开 |
| `notpresent` | 接口已注册但硬件不在位 |
| `testing` | 测试中 |

link 检查只在 operstate != "up" 时告警（`unknown` 也视为正常——许多虚拟接口永远报 unknown）。

## 接口发现与过滤

### 自动发现（errors / drops 检查）

自动扫描 `/sys/class/net/` 下所有接口，通过 `exclude` 列表排除不需要监控的接口。

```toml
exclude = ["lo", "docker*", "veth*", "br-*", "virbr*", "cali*", "flannel*", "cni*", "tunl*", "kube-*", "lxc*", "tap*", "dummy*"]
```

- `exclude` 使用 glob 模式（`filepath.Match` 语法）
- 可选 `include`：如果非空，**只监控** include 中匹配的接口（先 include 后 exclude）
- 默认 exclude 覆盖常见虚拟/容器接口

### 手动指定（link 检查）

link 检查必须手动指定接口，因为"哪个接口应该 up"取决于具体环境：

```toml
[[instances.link_up]]
interface = "eth0"
severity = "Critical"
```

### 为什么 errors/drops 自动发现而 link 手动指定

- **errors/drops**：任何接口出现新错误都值得关注，无需事先知道接口列表
- **link status**：不是所有接口都应该 up（备用网卡、管理口可能正常 down），误报代价高

## 结构体设计

```go
type DeltaCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type LinkSpec struct {
    Interface string `toml:"interface"`
    Severity  string `toml:"severity"`
    TitleRule string `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    Include []string   `toml:"include"`
    Exclude []string   `toml:"exclude"`

    Errors DeltaCheck `toml:"errors"`
    Drops  DeltaCheck `toml:"drops"`
    LinkUp []LinkSpec `toml:"link_up"`

    prevCounters map[string]*ifCounters
    initialized  bool
}
```

内部数据结构：

```go
type ifCounters struct {
    rxErrors  uint64
    txErrors  uint64
    rxDropped uint64
    txDropped uint64
}
```

不需要：
- `Timeout` — sysfs 读取是纯内存操作
- `Concurrency` — 接口数量有限（几十个上限），串行遍历

## _attr_ 标签

### errors / drops 事件

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_delta` | `5` | 本次检查周期内的增量（rx + tx 合计） |
| `_attr_rx` | `3` | 本次增量中 rx 部分 |
| `_attr_tx` | `2` | 本次增量中 tx 部分 |
| `_attr_total` | `12345` | 系统启动以来的累计值 |

### link 事件

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_operstate` | `down` | 当前 operstate 值 |
| `_attr_expect` | `up` | 期望状态（始终为 up） |

## Init() 校验

```
Init():
    1. if runtime.GOOS != "linux":
           return error: "netif plugin only supports linux"

    2. hasErrorCheck  = errors.warn_ge > 0 || errors.critical_ge > 0
       hasDropCheck   = drops.warn_ge > 0 || drops.critical_ge > 0
       hasLinkCheck   = len(link_up) > 0

       if !hasErrorCheck && !hasDropCheck && !hasLinkCheck:
           return error: "at least one check must be configured"

    3. if errors 有配置:
       a. warn_ge 和 critical_ge 必须 >= 0
       b. if 同时配了 warn 和 critical: warn < critical

    4. if drops 有配置: 同上

    5. for each link_up:
       a. interface = TrimSpace(interface)
       b. if interface is empty: return error
       c. normalize severity: default "Critical"
       d. 检查 interface 唯一性

    6. 验证 include / exclude 是否为合法 glob 模式

    7. 初始化 prevCounters = empty map
```

### exclude 默认值的考量

默认 exclude 列表采用保守策略，排除已知的虚拟/容器接口前缀。用户可以通过配置覆盖。

如果用户同时配了 include 和 exclude，处理顺序：先 include 筛选 → 再 exclude 排除。这样 `include = ["eth*"]` + `exclude = ["eth1"]` 表示"监控除 eth1 外的所有 eth 口"。

## Gather() 逻辑

```
Gather(q):
    // 1. 枚举接口
    allIfaces = readdir("/sys/class/net/")
    matchedIfaces = applyFilter(allIfaces, include, exclude)

    // 2. 读取计数器（接口可能在枚举后消失，如容器销毁）
    currentCounters = map[string]*ifCounters{}
    for each iface in matchedIfaces:
        counters, err = readCounters(iface)
        if counters == nil:
            // 接口已消失（os.IsNotExist）→ 静默跳过
            // 其他 error → 记日志，跳过该接口
            if err != nil: logger.Warnw(...)
            continue
        currentCounters[iface] = counters

    // 3. errors/drops 增量检查
    if !initialized:
        // 首次采集仅记录 baseline，不产出 errors/drops 事件（静默启动）
        // 与 sockstat 插件行为一致，避免启动时涌入大量 Ok 事件
        prevCounters = currentCounters
        initialized = true
        // 不 return——继续执行下面的 link 检查（link 不依赖 baseline）
    else:
        for each iface in currentCounters:
            prev = prevCounters[iface]
            if prev == nil:
                // 新接口，记录 baseline
                continue

            // errors delta
            if hasErrorCheck:
                rxDelta = safeDelta(current.rxErrors, prev.rxErrors)
                txDelta = safeDelta(current.txErrors, prev.txErrors)
                delta = rxDelta + txDelta
                status = EvaluateGeThreshold(delta, errors.warn_ge, errors.critical_ge)
                emit event check="netif::errors" target=iface

            // drops delta
            if hasDropCheck:
                rxDelta = safeDelta(current.rxDropped, prev.rxDropped)
                txDelta = safeDelta(current.txDropped, prev.txDropped)
                delta = rxDelta + txDelta
                status = EvaluateGeThreshold(delta, drops.warn_ge, drops.critical_ge)
                emit event check="netif::drops" target=iface

        // 清理已消失的接口
        prevCounters = currentCounters

    // 4. link 检查（独立于 errors/drops）
    for each spec in link_up:
        operstate, err = readOperstate(spec.interface)
        if err:
            emit Critical "failed to read eth0 operstate: ..."
            continue
        if operstate == "not_found":
            emit spec.severity "eth0 interface not found"
        else if operstate == "up" || operstate == "unknown":
            emit Ok check="netif::link" target=spec.interface
        else:
            emit spec.severity check="netif::link" target=spec.interface


safeDelta(current, prev):
    if current >= prev:
        return current - prev
    // 计数器溢出（32 位内核）或接口重建：视为 0，跳过本次
    return 0


readCounters(iface) (*ifCounters, error):
    read /sys/class/net/<iface>/statistics/rx_errors
    // 任何文件 os.IsNotExist → return nil, nil（接口已消失，静默跳过）
    // 其他 error → return nil, err（记日志，跳过该接口）
    read /sys/class/net/<iface>/statistics/tx_errors
    read /sys/class/net/<iface>/statistics/rx_dropped
    read /sys/class/net/<iface>/statistics/tx_dropped
    return &ifCounters{...}, nil


readOperstate(iface) (string, error):
    data, err = readFile(/sys/class/net/<iface>/operstate)
    if os.IsNotExist(err):
        return "not_found", nil    // 接口不存在，比 link down 更严重
    if err:
        return "", err
    return TrimSpace(data), nil
```

### 关键行为

1. **第一次采集静默建立 baseline**——不产出 errors/drops 事件（既不告警也不 Ok），避免启动时事件涌入。link 检查不依赖 baseline，首次即执行。与 sockstat 插件的处理方式一致。
2. **每个接口 × 每个维度独立事件**——eth0 的 errors 告警不影响 eth1 的 drops 事件。
3. **计数器溢出安全**——`safeDelta` 处理 current < prev 的场景（32 位内核溢出或接口重建），视为 0。
4. **接口动态增减**——新出现的接口（如新建 Docker 容器）自动纳入监控，已消失的接口自动清理。
5. **link 检查不依赖计数器**——独立读取 operstate，即使 errors/drops 检查未配置，link 检查也能独立工作。
6. **`unknown` operstate 视为正常**——loopback、tun、tap、veth 等虚拟接口通常报 unknown，不应告警。
7. **接口不存在比 link down 更严重**——`/sys/class/net/<iface>/operstate` 不存在意味着内核都看不到这块网卡，直接以配置的 severity 告警。
8. **`dormant` 状态与 `for_duration`**——`dormant` 表示物理连接已建立但 802.1X 认证未完成，在企业网络中是开机后的短暂过渡态（几秒到十几秒）。当前设计中 `dormant` 会触发告警，但用户可通过 `for_duration = "30s"` 消除这类瞬态误报。

## Description 示例

### errors

- 正常：`eth0 no new errors (total: 123)`
- 告警：`eth0 has 15 new errors since last check (rx: 12, tx: 3, total: 138), above warning threshold 1`

### drops

- 正常：`eth0 no new drops (total: 456)`
- 告警：`eth0 has 200 new drops since last check (rx: 180, tx: 20, total: 656), above critical threshold 100`

### link

- 正常：`eth0 link is up`
- 链路断开：`eth0 link is down (operstate: down)`
- 接口不存在：`eth0 interface not found`
- dormant：`eth0 link is not ready (operstate: dormant)`

### 读取失败

- `failed to read eth0 operstate: permission denied`

## 默认配置建议

| 决策 | 值 | 理由 |
| --- | --- | --- |
| errors.warn_ge | `1` | 任何新 error 都值得关注 |
| errors.critical_ge | `100` | 大量 error 暗示硬件故障 |
| drops.warn_ge | `1` | 任何新 drop 都值得关注 |
| drops.critical_ge | `100` | 大量 drop 暗示严重拥塞 |
| link severity | `"Critical"` | 链路断开通常是紧急故障 |
| interval | `"60s"` | 平衡检测灵敏度和系统开销 |
| for_duration | `0` | 错误/丢包是确定性的，不需要持续确认 |
| repeat_interval | `"30m"` | 持续性问题，适度提醒 |
| repeat_number | `3` | 防止噪音 |

### errors / drops 的阈值选择

`warn_ge = 1` 是一个激进但合理的默认值：

- 在正常网络环境中，errors 和 drops 的增量应该为 0
- 任何非零增量都值得调查
- 如果用户环境中有"合理的"低频 drop（如高负载服务器偶尔 ring buffer 满），可以调高阈值

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/sys/class/net/` |
| macOS | 不支持 | Init 返回错误。macOS 用 `netstat -ib`，格式不同 |
| Windows | 不支持 | Init 返回错误。Windows 用 WMI/perfcounters |

## 文件结构

```
plugins/netif/
    design.md        # 本文档
    netif.go         # 主逻辑（仅 Linux）
    netif_test.go    # 测试

conf.d/p.netif/
    netif.toml       # 默认配置
```

通过 `runtime.GOOS` 在 Init 中限制为 Linux，无需 build tags。

## 默认配置文件示例

```toml
[[instances]]
## ===== 网络接口健康检查（60 秒跑起来）=====
## 自动发现网络接口，监控错误和丢包增量
## errors = 网卡/驱动/线缆故障（rx_errors + tx_errors）
## drops  = 拥塞/ring buffer 满/iptables 丢弃（rx_dropped + tx_dropped）
## 任何非零增量都值得关注，生产环境建议 warn_ge = 1

interval = "60s"

## 接口过滤（应用于 errors / drops 自动发现）
## include：如果非空，只监控匹配的接口（glob 模式）
## exclude：排除匹配的接口（glob 模式）
## 处理顺序：先 include 筛选 → 再 exclude 排除
# include = []
exclude = ["lo", "docker*", "veth*", "br-*", "virbr*", "cali*", "flannel*", "cni*", "tunl*", "kube-*", "lxc*", "tap*", "dummy*"]

## 错误增量检查（rx_errors + tx_errors 合计增量）
## warn_ge / critical_ge 为 0 表示不检查
[instances.errors]
warn_ge = 1
critical_ge = 100
# title_rule = "[check] [target]"

## 丢包增量检查（rx_dropped + tx_dropped 合计增量）
[instances.drops]
warn_ge = 1
critical_ge = 100
# title_rule = "[check] [target]"

## 链路状态检查（手动指定期望 up 的接口）
## 不是所有接口都需要 up（备用网卡、管理口可能正常 down），所以需要手动列出
## severity 默认 Critical（链路断开通常是紧急故障）

# [[instances.link_up]]
# interface = "eth0"
# severity = "Critical"
# # title_rule = "[check] [target]"

# [[instances.link_up]]
# interface = "bond0"
# severity = "Critical"

[instances.alerting]
for_duration = 0
repeat_interval = "30m"
repeat_number = 3
# disabled = false
# disable_recovery_notification = false
```

## 与 ethtool 的对比

`ethtool -S <iface>` 提供更细粒度的驱动级计数器（如 `rx_crc_errors`、`rx_frame_errors`、`tx_carrier_errors`），但：

1. 需要 root 权限（`CAP_NET_ADMIN`）
2. 输出格式因驱动而异，难以通用解析
3. catpaw 不是 metrics 采集系统，粗粒度的 errors/drops 足以触发告警

如果用户需要细粒度 NIC 计数器，应使用专门的 metrics exporter（如 Prometheus node_exporter）。catpaw 的定位是"发现问题并告警"，不是"展示详细 metrics"。

## 常见排查场景

| 告警 | 可能原因 | 排查命令 |
| --- | --- | --- |
| errors 增长 | 网线/光模块故障、网卡硬件缺陷、驱动 bug | `ethtool -S <iface>`, `dmesg \| grep <iface>` |
| drops 增长（rx） | ring buffer 满、softirq 处理不及时 | `ethtool -g <iface>`, `cat /proc/net/softnet_stat` |
| drops 增长（tx） | 限速（tc/cgroup）、队列满 | `tc -s qdisc show dev <iface>` |
| link down | 网线松动、交换机端口 down、bond failover | `ethtool <iface>`, `ip link show <iface>` |
