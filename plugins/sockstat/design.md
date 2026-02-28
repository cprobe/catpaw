# sockstat 插件设计

## 概述

监控 Linux TCP listen 队列溢出：当 `ListenOverflows` 计数器在两次采集之间出现增量时产出告警事件。

listen 队列（accept backlog）满时，内核静默丢弃新到达的 SYN 包——客户端看到的是连接超时，服务端的应用日志里没有任何记录。这与 conntrack 表满的症状**几乎一模一样**（tcpdump 看到 SYN 但无 SYN-ACK），但根因和处置方式完全不同。

**定位**：TCP 连接层监控。与 conntrack（连接跟踪表耗尽）和 neigh（ARP 表溢出）互补——三者满时的症状都是"新连接失败"，但根因分别在不同的内核子系统：

| 插件 | 根因层级 | 关键字 |
| --- | --- | --- |
| neigh | L2 地址解析 | ARP 表满，无法解析新 IP 的 MAC |
| conntrack | L3/L4 连接跟踪 | Netfilter 跟踪表满，新连接被丢弃 |
| **sockstat** | L4 应用层 accept | listen backlog 满，应用 accept() 太慢 |

**参考**：Prometheus `node_exporter` 采集 `node_netstat_TcpExt_ListenOverflows` 和 `node_netstat_TcpExt_ListenDrops` 指标（累计计数器），用户需自己在 PromQL 里做 `rate()` 和告警规则。catpaw 内置增量检测和阈值判断，开箱即用。

## 与前三个插件的关键差异

| 对比项 | conntrack / filefd / neigh | sockstat |
| --- | --- | --- |
| 指标类型 | **Gauge**（瞬时值百分比） | **Counter**（累计计数器增量） |
| 阈值含义 | 使用率百分比 | 两次采集间的新增溢出次数 |
| 状态管理 | 无状态 | 需存储上次计数器值 |
| 首次 Gather | 直接计算并产出事件 | 建立基线，产出 Ok（delta=0） |
| 计数器重置 | 不适用 | 系统重启时自然重置（catpaw 同步重启） |

这是 catpaw 第一个**增量检测**模式的系统级插件。docker 插件的 `restart_detected` 维度采用类似模式（跟踪 RestartCount 增量），但作用于容器粒度。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| TCP listen 队列溢出 | `sockstat::listen_overflow` | ListenOverflows 增量超阈值告警 |

- **target label** 为 `"system"`（ListenOverflows 是系统级全局计数器）
- **默认 title_rule** 为 `"[check]"`

## 数据来源

### `/proc/net/netstat`

该文件按 "header 行 + value 行" 成对出现，我们需要 `TcpExt:` 段：

```
TcpExt: SyncookiesSent SyncookiesRecv ... ListenOverflows ListenDrops ...
TcpExt: 0 0 ... 789 790 ...
```

| 字段 | 含义 | 类型 |
| --- | --- | --- |
| `ListenOverflows` | accept 队列满导致的 SYN 丢弃次数（累计） | 单调递增计数器 |
| `ListenDrops` | 所有 listen 相关的连接丢弃次数（累计，⊇ ListenOverflows） | 单调递增计数器 |

### ListenOverflows vs ListenDrops

- `ListenOverflows`：**精确**表示 accept backlog 满导致的丢弃
- `ListenDrops`：包含 ListenOverflows + 其他 listen 相关丢弃（如半连接队列满）
- `ListenDrops >= ListenOverflows` 恒成立

本插件以 `ListenOverflows` 为阈值判断依据（更精确），同时将 `ListenDrops` 作为 `_attr_` 标签提供上下文。

### 解析方式

```
1. 读取 /proc/net/netstat
2. 找到以 "TcpExt:" 开头的两行（第 1 行为 header，第 2 行为 value）
3. 按空格分割 header 行，定位 "ListenOverflows" 和 "ListenDrops" 的列索引
4. 从 value 行按相同索引提取对应值
```

不硬编码列位置——不同内核版本的 TcpExt 字段顺序可能不同，按 header 名查找更健壮。

### 无新增依赖

仅读取 proc 文件，使用标准库 `os.ReadFile` + `strings` + `strconv.ParseUint`，无需任何第三方依赖。

## 阈值设计

### 增量阈值

`delta = 当前 ListenOverflows - 上次 ListenOverflows`

| 阈值 | 含义 |
| --- | --- |
| `warn_ge = 1` | 两次采集间有 ≥ 1 次溢出 → Warning |
| `critical_ge = 100` | 两次采集间有 ≥ 100 次溢出 → Critical |

### 为什么 warn_ge = 1

每一次 ListenOverflow 都意味着一个真实的客户端连接被丢弃。在生产环境中，这可能导致：
- 用户请求超时
- 微服务间 RPC 失败
- 健康检查失败触发 Pod 重启

不同于百分比类指标（80% 还有缓冲），溢出是**已经发生的损害**。`warn_ge = 1` 确保任何溢出都被捕获。

### 为什么默认 for_duration = "1m" 而非 0

与 conntrack/filefd/neigh（`for_duration = 0`）不同，sockstat 默认 `for_duration = "1m"`。理由：
- 单次瞬间溢出可能是正常流量尖峰（如滚动更新期间），不一定需要告警
- `for_duration = "1m"` 意味着连续两个采集周期（30s × 2）都有溢出才触发通知
- 持续溢出 = 系统性问题（应用 accept() 太慢或 backlog 太小），值得告警
- 瞬间溢出 = 可能是正常波动，不告警减少噪音

如果用户希望对任何溢出都立即告警，可将 `for_duration` 设为 0。

### 处置建议（供 Description 参考）

用户收到告警后典型处置：
1. `cat /proc/net/netstat | grep -A1 TcpExt | grep -oP 'ListenOverflows \K\d+'`（确认累计值）
2. `ss -lnt`（查看哪些监听端口的 Recv-Q 接近 Send-Q，即 accept 队列接近 backlog）
3. 常见原因及对策：
   - **应用 accept() 太慢**：优化应用性能或增加 worker 线程
   - **backlog 太小**：`sysctl -w net.core.somaxconn=65535` + 应用层调整 `listen(fd, backlog)`
   - **SYN flood 攻击**：启用 SYN cookies（`sysctl -w net.ipv4.tcp_syncookies=1`）
4. 持久化：写入 `/etc/sysctl.d/99-somaxconn.conf`

## 结构体设计

```go
type ListenOverflowCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    ListenOverflow ListenOverflowCheck `toml:"listen_overflow"`

    // 增量检测状态（不序列化）
    prevOverflows uint64
    prevDrops     uint64
    initialized   bool
}

type SockstatPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

状态字段说明：
- `prevOverflows`：上次 Gather 时的 ListenOverflows 累计值
- `prevDrops`：上次 Gather 时的 ListenDrops 累计值（仅用于 _attr_）
- `initialized`：是否已建立基线（首次 Gather 后设为 true）

不需要：
- `Timeout` — 读取 proc 文件是本地操作
- `Concurrency` — 仅读一个文件
- `Targets` — ListenOverflows 是系统级计数器

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_delta` | `5` | 本次采集间隔内的新增溢出次数（阈值判断依据） |
| `_attr_total_overflows` | `789` | ListenOverflows 累计值（自系统启动） |
| `_attr_total_drops` | `790` | ListenDrops 累计值（⊇ overflows，上下文参考） |

Ok 事件也携带完整 `_attr_` 标签，便于巡检时确认 listen 溢出的历史规模。

## Init() 校验

```
Init():
    1. if runtime.GOOS != "linux":
        return error: "sockstat plugin only supports linux (current: <os>)"

    2. if warn_ge < 0 || critical_ge < 0:
        return error: "listen_overflow thresholds must be >= 0"

    3. if warn_ge > 0 && critical_ge > 0 && warn_ge >= critical_ge:
        return error: "listen_overflow.warn_ge must be less than critical_ge"
```

注意：增量阈值**不限制上界 100**（不是百分比，理论上单次采集间隔可以有成千上万次溢出），只校验非负。

## Gather() 逻辑

```
Gather(q):
    // 阈值全为 0 时静默跳过
    if ins.ListenOverflow.WarnGe == 0 && ins.ListenOverflow.CriticalGe == 0:
        return

    overflows, drops, err = readListenStats()

    if err != nil:
        event = buildEvent("sockstat::listen_overflow", "system")
        event → Critical: "failed to read netstat data: <error>"
        q.PushFront(event)
        return

    // 首次 Gather：建立基线
    if !ins.initialized:
        ins.prevOverflows = overflows
        ins.prevDrops = drops
        ins.initialized = true
        // 产出 Ok 事件（delta=0），附带当前累计值供参考
        event = buildEvent(...)
        event._attr_delta = "0"
        event._attr_total_overflows = overflows
        event._attr_total_drops = drops
        event → Ok: "listen overflow baseline established (total overflows: 789)"
        q.PushFront(event)
        return

    // 计算增量
    delta = overflows - ins.prevOverflows
    if overflows < ins.prevOverflows:
        // 计数器回绕或系统重启后 catpaw 未重启（极罕见），重置基线
        delta = 0

    dropsDelta = drops - ins.prevDrops
    if drops < ins.prevDrops:
        dropsDelta = 0

    // 更新基线
    ins.prevOverflows = overflows
    ins.prevDrops = drops

    deltaStr = strconv.FormatUint(delta, 10)
    event = buildEvent("sockstat::listen_overflow", "system")
    event._attr_delta = deltaStr
    event._attr_total_overflows = strconv.FormatUint(overflows, 10)
    event._attr_total_drops = strconv.FormatUint(drops, 10)

    status = EvaluateGeThreshold(float64(delta), warn_ge, critical_ge)
    event.SetEventStatus(status)

    switch status:
        Critical: "150 new listen overflows since last check (total: 789), above critical threshold 100"
        Warning:  "5 new listen overflows since last check (total: 789), above warning threshold 1"
        Ok:       "no new listen overflows (total: 789), everything is ok"

    q.PushFront(event)
```

### readListenStats() 伪代码

```
readListenStats() (overflows uint64, drops uint64, err error):
    data, err = os.ReadFile("/proc/net/netstat")
    if err != nil:
        return 0, 0, fmt.Errorf("read /proc/net/netstat: %v", err)

    lines = strings.Split(string(data), "\n")

    // 找到 TcpExt header 行和 value 行
    var headerLine, valueLine string
    for i, line in lines:
        if strings.HasPrefix(line, "TcpExt:"):
            if headerLine == "":
                headerLine = line
            else:
                valueLine = line
                break

    if headerLine == "" || valueLine == "":
        return 0, 0, fmt.Errorf("TcpExt section not found in /proc/net/netstat")

    headers = strings.Fields(headerLine)
    values = strings.Fields(valueLine)
    if len(headers) != len(values):
        return 0, 0, fmt.Errorf("TcpExt header/value count mismatch (%d vs %d)", len(headers), len(values))

    // 按 header 名查找列索引
    overflowIdx = -1
    dropsIdx = -1
    for i, h in headers:
        if h == "ListenOverflows": overflowIdx = i
        if h == "ListenDrops": dropsIdx = i

    if overflowIdx < 0:
        return 0, 0, fmt.Errorf("ListenOverflows not found in TcpExt")

    overflows, err = strconv.ParseUint(values[overflowIdx], 10, 64)
    if err != nil:
        return 0, 0, fmt.Errorf("parse ListenOverflows: %v", err)

    if dropsIdx >= 0:
        drops, _ = strconv.ParseUint(values[dropsIdx], 10, 64)

    return overflows, drops, nil
```

### 关键行为

1. **按 header 名查找列索引**，不硬编码列位置——内核版本间字段顺序可能不同
2. **首次 Gather 建立基线**，产出 Ok 事件（delta=0），不产生误报
3. **计数器回绕保护**：若当前值 < 上次值（极罕见，如系统重启后 catpaw 未重启），delta 设为 0 并重置基线
4. **ListenDrops 仅作为 _attr_ 参考**，不参与阈值判断（它是 ListenOverflows 的超集，缺乏精确性）
5. **`ListenOverflows` 不存在时产出 Critical 事件**——极老内核可能没有此字段
6. **`ListenDrops` 不存在时静默忽略**（仅影响 _attr_，不影响核心逻辑）
7. **单次 Gather 产出 0 或 1 个事件**
8. **无需并发、无需 goroutine** — 同步读一个文件

## Description 示例

- 基线建立（Ok）：`listen overflow baseline established (total overflows: 789)`
- 无新增溢出（Ok）：`no new listen overflows (total: 789), everything is ok`
- 有新增溢出（Warning）：`5 new listen overflows since last check (total: 794), above warning threshold 1`
- 大量溢出（Critical）：`150 new listen overflows since last check (total: 939), above critical threshold 100`
- 读取失败（Critical）：`failed to read netstat data: read /proc/net/netstat: permission denied`
- 格式异常（Critical）：`failed to read netstat data: TcpExt section not found in /proc/net/netstat`
- 字段缺失（Critical）：`failed to read netstat data: ListenOverflows not found in TcpExt`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| warn_ge | `1` | 每次溢出都是一个真实连接被丢弃，值得关注 |
| critical_ge | `100` | 30 秒内 100+ 次溢出表明系统性问题 |
| interval | `"30s"` | 与其他系统级插件一致 |
| for_duration | `"1m"` | **与其他插件不同**：过滤瞬间溢出噪音，持续 1 分钟才告警 |
| repeat_interval | `"5m"` | 持续溢出时定期提醒 |
| repeat_number | `0` | 不限制，直到溢出停止 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| TCP listen 队列溢出 | **sockstat** | accept backlog 满，应用处理不过来 |
| 连接跟踪表耗尽 | conntrack | Netfilter 层面的连接跟踪表满 |
| ARP 表溢出 | neigh | 新 IP 无法解析 MAC 地址 |
| 端口连通性检测 | net | 检查是否能建连 |

**三者鉴别**：当 "新连接失败" 时，同时查看 conntrack、neigh、sockstat 的告警状态：

| conntrack | neigh | sockstat | 最可能的根因 |
| --- | --- | --- | --- |
| ⚠️ | ✅ | ✅ | 连接跟踪表满（流量太大或规则太多） |
| ✅ | ⚠️ | ✅ | ARP 表满（新 IP 太多，如容器扩缩容） |
| ✅ | ✅ | ⚠️ | 应用 accept() 太慢或 backlog 太小 |
| ⚠️ | ✅ | ⚠️ | 整体过载（连接跟踪 + 应用层双瓶颈） |

这张鉴别表是三个插件联合使用的核心价值——单看任何一个只能说"连接有问题"，三个一起看能精确定位问题层级。

## 跨平台兼容性

| 平台 | 支持 | 处理方式 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/proc/net/netstat` |
| macOS | 不支持 | Init 返回错误，插件不加载 |
| Windows | 不支持 | Init 返回错误，插件不加载 |

`/proc/net/netstat` 是 Linux 特有的，macOS 和 Windows 的 TCP 栈没有等价的 ListenOverflows 计数器。

### 容器环境

在容器内读取 `/proc/net/netstat` 获取的是**容器自身 network namespace** 的值（与 conntrack/neigh/filefd 不同——那些读到的是宿主机的值）。

- 如果容器使用 `hostNetwork: true`，读到的是宿主机的值
- 如果容器使用独立 network namespace（默认），读到的是该 namespace 的值

这意味着 catpaw 以 DaemonSet 部署时：
- **hostNetwork: true**（推荐）：监控整个节点的 listen overflow
- **hostNetwork: false**：仅监控 catpaw 自身 namespace 的 listen overflow（通常无意义）

## 文件结构

```
plugins/sockstat/
    design.md             # 本文档
    sockstat.go           # 主逻辑（仅 Linux）
    sockstat_test.go      # 测试

conf.d/p.sockstat/
    sockstat.toml         # 默认配置
```

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 监控 TCP listen 队列溢出（ListenOverflows）
## accept backlog 满时 SYN 被静默丢弃，客户端看到连接超时
## 与 conntrack 满的症状几乎一样，但根因不同（应用层 vs 内核层）

## listen 溢出增量阈值（两次采集间新增溢出次数）
## warn_ge = 1 表示任何溢出都告警，critical_ge = 100 表示大规模溢出
[instances.listen_overflow]
warn_ge = 1
critical_ge = 100
# title_rule = "[check]"

## 采集间隔
interval = "30s"

[instances.alerting]
## 持续 1 分钟有溢出才告警（过滤瞬间尖峰）
for_duration = "1m"
repeat_interval = "5m"
repeat_number = 0
# disabled = false
# disable_recovery_notification = false
```
