# tcpstate 插件设计

## 概述

监控系统级 TCP 连接状态分布，当 CLOSE_WAIT 或 TIME_WAIT 连接数异常堆积时产出告警事件。

**核心场景**：

1. **CLOSE_WAIT 泄漏**：应用代码未正确关闭 socket（忘了 `Close()`），导致 CLOSE_WAIT 持续积累，最终 fd 耗尽（too many open files）。这是最常见的应用层连接泄漏 bug
2. **TIME_WAIT 爆满**：高并发短连接场景（如反向代理、API 网关），大量 TIME_WAIT 耗尽**同一目标 IP:Port 对应的**临时端口（默认范围 28232 个），新连接 `connect: cannot assign requested address`。注意：不同目标 IP:Port 的 TIME_WAIT 互不冲突，因此系统总 TIME_WAIT 数是上界估计，实际端口耗尽取决于连接目标的集中程度
3. **静默积累**：以上两种问题都是渐进式恶化，不看数据根本感知不到，直到临界点爆发

**与现有插件的关系**：

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| TCP 连接状态异常 | **tcpstate** | CLOSE_WAIT/TIME_WAIT 堆积 |
| TCP listen 队列溢出 | sockstat | 应用未及时 accept |
| 网卡错误/丢包 | netif | 物理层异常 |
| fd 使用率 | procfd / filefd | fd 耗尽的直接监控 |
| 远端连通性 | net / ping | 应用层可达性 |

**定位**：TCP 连接生命周期异常检测。与 sockstat（listen 队列）互补——sockstat 看"进不来"，tcpstate 看"出不去"。

**参考**：Prometheus node_exporter `tcpstat` collector（Netlink 实现）、Nagios `check_tcp_states`、`ss -s`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| CLOSE_WAIT 数量 | `tcpstate::close_wait` | system | 系统级 CLOSE_WAIT 连接数 |
| TIME_WAIT 数量 | `tcpstate::time_wait` | system | 系统级 TIME_WAIT 连接数 |

- **系统级聚合**——不按进程/端口拆分，因为连接状态异常通常需要从系统全局角度判断
- **target 固定为 `"system"`**——一个 instance 只产出最多 2 个事件
- **默认 title_rule** 为 `"[check]"`

### 为什么只关注 CLOSE_WAIT 和 TIME_WAIT

TCP 有 11 种状态，但只有这两种会"堆积"成问题：

| 状态 | 堆积风险 | 说明 |
| --- | --- | --- |
| ESTABLISHED | 低 | 正常连接，数量多但受控 |
| **CLOSE_WAIT** | **高** | 应用 bug 导致，不会自动清理，永久泄漏 |
| **TIME_WAIT** | **高** | 正常机制但可耗尽端口，默认持续 60s |
| FIN_WAIT_1/2 | 低 | 通常秒级过渡，罕见堆积 |
| SYN_RECV | 低 | SYN flood 场景，但内核 syncookies 已能防护 |
| 其他 | 极低 | LISTEN/SYN_SENT/CLOSING/LAST_ACK 极少成为瓶颈 |

其他状态如果确实需要，后续可扩展，当前聚焦最高价值的两个。

### 为什么拆成两个 check label

- **根因不同**：CLOSE_WAIT = 应用 bug（需要修代码），TIME_WAIT = 流量模式问题（需要调参数或用连接池）
- **阈值差异大**：CLOSE_WAIT 100 就值得警惕，TIME_WAIT 5000 可能才开始关注
- **运维响应不同**：CLOSE_WAIT 需要找应用负责人，TIME_WAIT 需要调 sysctl

## 数据来源

### 方案选型

在连接数量很大的服务器上（负载均衡/API 网关可达 10 万+连接），数据源的选择直接影响采集性能：

| 数据源 | TIME_WAIT | CLOSE_WAIT | 性能 | IPv6 |
| --- | --- | --- | --- | --- |
| **Netlink (`NETLINK_INET_DIAG`)** | 有 | 有 | **O(n) 但内核态聚合，极快** | 两次调用（AF_INET + AF_INET6） |
| `/proc/net/sockstat` | 有（`tw` 字段） | 无 | O(1)，< 1ms | 仅 IPv4 |
| `/proc/net/tcp` + `tcp6` | 有 | 有 | O(n)，10 万连接 ~100ms | 需额外扫描 tcp6 |
| `ss -tna` 命令 | 有 | 有 | O(n)，多进程开销 | 有 |

### 采用 Netlink 作为主数据源

Netlink `NETLINK_INET_DIAG`（即 `ss` 命令底层使用的内核接口）通过 sock_diag 子系统直接从内核获取 socket 信息，不经过 procfs 的文本序列化/反序列化。Prometheus node_exporter 的 `tcpstat` collector 已采用此方案。

**相比 `/proc/net/tcp` 的优势**：

1. **更快**——无需内核将每条连接序列化为文本行，再由用户态逐行解析。内核直接以二进制结构体返回，省去了双向序列化开销
2. **内存更省**——`/proc/net/tcp` 需要 bufio 流式读取大文件（500K 连接 ~75MB），Netlink 以消息批量返回，无需在用户态维护大缓冲区
3. **天然支持 IPv4/IPv6**——通过 `AF_INET` / `AF_INET6` 两次请求即可，无需处理文件存在性
4. **扩展性好**——如果未来需要按进程/端口细分，Netlink 可以返回完整的 socket 详情，而无需增加额外的 `/proc` 解析

**性能对比**（估算，10 万连接）：

| 方案 | 耗时 | 内存 | 说明 |
| --- | --- | --- | --- |
| `/proc/net/tcp` | ~50ms | ~15MB（文件大小） | 需 bufio 流式读取 |
| Netlink | ~10ms | ~几 MB（消息缓冲区） | 内核二进制结构体 |
| `/proc/net/sockstat` | < 1ms | ~1KB | 仅 TIME_WAIT，O(1) |

### 混合策略：Netlink + sockstat 快速路径

```
如果只配了 time_wait（未配 close_wait）:
    → 读 /proc/net/sockstat，O(1)，即使百万连接也毫秒级

如果配了 close_wait（无论是否同时配 time_wait）:
    → Netlink 查询，一次遍历同时得到所有状态计数
```

**好处**：
- 只关心 TIME_WAIT 的用户（最常见场景）自动走 sockstat 快速路径，零性能顾虑
- 需要 CLOSE_WAIT 时走 Netlink，比 `/proc/net/tcp` 快数倍且内存更省
- 对用户完全透明，无需配置

### Netlink `INET_DIAG` 协议

#### 请求结构

```go
type InetDiagReqV2 struct {
    Family   uint8          // AF_INET 或 AF_INET6
    Protocol uint8          // syscall.IPPROTO_TCP
    Ext      uint8          // 扩展信息标志
    Pad      uint8
    States   uint32         // 状态过滤位图
    ID       InetDiagSockID // 可选的 socket 过滤条件（全零表示不过滤）
}
```

通过 `SOCK_DIAG_BY_FAMILY`（消息类型 20）发送 dump 请求，设置 `NLM_F_REQUEST | NLM_F_DUMP` 标志。

**关键优化——精准状态过滤**：`States` 字段设为 `(1 << ESTABLISHED) | (1 << TIME_WAIT) | (1 << CLOSE_WAIT)` 而非 `TCPFAll(0xFFF)`。这让内核只返回我们关心的 3 种状态的 socket，跳过 LISTEN、SYN_SENT 等无关状态。在一台 ESTABLISHED 10 万 + CLOSE_WAIT 100 的服务器上，与 node_exporter 的 `TCPFAll` 方案相比传输量不变（ESTABLISHED 仍需计数），但在 LISTEN 端口极多的场景下可减少无效数据。更重要的是，语义上明确了"只要这三种"。

#### 响应结构

每条响应消息包含一个 `InetDiagMsg`：

```go
type InetDiagMsg struct {
    Family  uint8           // 地址族
    State   uint8           // TCP 状态（与 /proc/net/tcp 的 st 字段含义相同）
    Timer   uint8
    Retrans uint8
    ID      InetDiagSockID
    Expires uint32
    RQueue  uint32          // 接收队列字节数
    WQueue  uint32          // 发送队列字节数
    UID     uint32
    Inode   uint32
}
```

只需读取 `State` 字段并计数，无需解析其他字段。

#### 状态码映射

| 值 | 状态 | 说明 |
| --- | --- | --- |
| 1 | ESTABLISHED | 正常连接 |
| 6 | TIME_WAIT | 等待 2MSL 超时 |
| 8 | CLOSE_WAIT | 等待应用关闭 |

与 `/proc/net/tcp` 的十六进制 `st` 字段完全一致（`0x01`、`0x06`、`0x08`）。

### `/proc/net/sockstat` 格式（快速路径）

```
sockets: used 1234
TCP: inuse 500 orphan 10 tw 3000 alloc 600 mem 50
UDP: inuse 20 mem 5
```

`tw` 字段即为 TIME_WAIT 连接数。内核维护的计数器，读取不涉及连接遍历。

### 依赖

**无第三方依赖**——直接使用标准库 `syscall` 操作 Netlink socket（`syscall.Socket`、`syscall.Sendto`、`syscall.Recvfrom`、`syscall.ParseNetlinkMessage`）。

Prometheus node_exporter 使用 `github.com/mdlayher/netlink` 库，但 catpaw 的需求很简单（dump + 计数，不需要属性过滤、Netlink 多播等高级功能），原始 syscall 足够且更符合"轻量无重依赖"的项目理念。整个 Netlink 实现约 130 行代码。

## 结构体设计

```go
type StateCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    CloseWait StateCheck `toml:"close_wait"`
    TimeWait  StateCheck `toml:"time_wait"`

    hasCloseWaitCheck bool
    hasTimeWaitCheck  bool
}
```

不需要：
- `Timeout` — Netlink 和 `/proc` 读取都是纯内核操作，不会 hang
- `Concurrency` — 单次查询，串行即可
- `prevCounters` — 不做增量检查，直接检查当前连接数
- `IPv6` 配置项 — Netlink 天然分族查询，自动检测 IPv6 可用性（见 Gather 逻辑）

### 为什么不做增量而是绝对值

与 netif/sockstat 不同，CLOSE_WAIT 和 TIME_WAIT 的数量本身就是问题指标（不是累计计数器），且会自然回落：
- CLOSE_WAIT：应用修复 bug 后会逐渐清零
- TIME_WAIT：默认 60s 后自动消失

所以直接检查"当前数量"最符合直觉。

### 为什么移除了 `ipv6` 配置项

旧设计中 `ipv6` 字段用于控制是否扫描 `/proc/net/tcp6`，主要动机是减半扫描耗时。切换到 Netlink 后：

1. IPv6 查询只是一次额外的 Netlink 请求，开销极小（毫秒级），不值得让用户操心
2. 自动检测更可靠——尝试 `AF_INET6` 请求，如果内核未启用 IPv6 则静默跳过（返回空结果或 `ENOENT`），无需用户配置

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_count` | `3500` | 当前状态的连接数 |
| `_attr_established` | `12000` | ESTABLISHED 连接数（提供背景参考） |

`_attr_established` 在 Netlink 路径下零额外成本（遍历消息时顺便计数），帮助用户判断异常连接的相对比例——"3500 CLOSE_WAIT + 12000 ESTABLISHED"比单独看 3500 更有信息量。

仅在 Netlink 路径（配了 close_wait）时携带 `_attr_established`；sockstat 快速路径不携带（sockstat 文件中无此信息）。

## Init() 校验

```
Init():
    1. if runtime.GOOS != "linux":
           return error: "tcpstate plugin only supports linux"

    2. hasCloseWaitCheck = close_wait.warn_ge > 0 || close_wait.critical_ge > 0
       hasTimeWaitCheck  = time_wait.warn_ge > 0 || time_wait.critical_ge > 0

       if !hasCloseWaitCheck && !hasTimeWaitCheck:
           return error: "at least one check must be configured"

    3. validate close_wait thresholds: >= 0, warn < critical (if both > 0)
    4. validate time_wait thresholds: 同上
```

## Gather() 逻辑

```
type stateCounts struct {
    established uint64
    closeWait   uint64
    timeWait    uint64
}


Gather(q):
    if hasCloseWaitCheck:
        // Netlink 路径：一次查询得到所有状态计数
        counts, err = queryNetlinkTcpStates()
        if err:
            emit Critical "failed to query TCP states via netlink: ..."
            return

        emitStateEvent(q, "tcpstate::close_wait", counts.closeWait, close_wait, counts.established)

        if hasTimeWaitCheck:
            emitStateEvent(q, "tcpstate::time_wait", counts.timeWait, time_wait, counts.established)

    else if hasTimeWaitCheck:
        // 快速路径：只读 /proc/net/sockstat
        timeWaitCount, err = readSockstatTimeWait()
        if err:
            emit Critical "failed to read sockstat: ..."
            return

        emitStateEvent(q, "tcpstate::time_wait", timeWaitCount, time_wait, -1)


emitStateEvent(q, check, count, config, establishedCount):
    tr = config.titleRule or "[check]"
    event = BuildEvent(check=check, target="system")
    event._attr_count = count
    if establishedCount >= 0:
        event._attr_established = establishedCount
    status = EvaluateGeThreshold(count, config.warn_ge, config.critical_ge)

    switch status:
        Critical: "N CLOSE_WAIT connections, above critical threshold M"
        Warning:  "N CLOSE_WAIT connections, above warning threshold M"
        Ok:       "N CLOSE_WAIT connections, everything is ok"

    q.PushFront(event)


queryNetlinkTcpStates() (*stateCounts, error):
    fd = syscall.Socket(AF_NETLINK, SOCK_DGRAM|SOCK_CLOEXEC, NETLINK_SOCK_DIAG)
    syscall.Bind(fd, &SockaddrNetlink{Family: AF_NETLINK})
    syscall.SetsockoptTimeval(fd, SOL_SOCKET, SO_RCVTIMEO, 5s)  // 防止 Recv 永久阻塞

    counts = &stateCounts{}

    for _, family in [AF_INET, AF_INET6]:
        req = buildDiagRequest(family)  // States = (1<<ESTABLISHED)|(1<<TIME_WAIT)|(1<<CLOSE_WAIT)
        syscall.Sendto(fd, req, 0, addr)

        for:
            n = syscall.Recvfrom(fd, buf)
            msgs = syscall.ParseNetlinkMessage(buf[:n])

            for each msg:
                if NLMSG_DONE: break
                if NLMSG_ERROR: return parseNlError(msg.Data)
                state = msg.Data[1]  // InetDiagMsg.State 在偏移量 1
                switch state:
                    case ESTABLISHED: counts.established++
                    case CLOSE_WAIT:  counts.closeWait++
                    case TIME_WAIT:   counts.timeWait++

        if family == AF_INET6 && isIPv6Unavailable(err):
            continue  // IPv6 未启用，静默跳过

    return counts, nil


readSockstatTimeWait() (uint64, error):
    data, err = os.ReadFile("/proc/net/sockstat")
    if err:
        return 0, fmt.Errorf("read /proc/net/sockstat: %w", err)

    return parseSockstatTimeWait(data)  // 纯解析函数，独立于 sockstat.go


parseSockstatTimeWait(data []byte) (uint64, error):
    for each line in data:
        if hasPrefix "TCP:":
            fields = strings.Fields(line)
            for i, f in fields:
                if f == "tw" && i+1 < len(fields):
                    return parseUint(fields[i+1])

    return 0, fmt.Errorf("tw field not found in /proc/net/sockstat")
```

### 关键行为

1. **Netlink 为主，sockstat 为辅**——配了 close_wait 时走 Netlink（内核二进制接口，比 `/proc/net/tcp` 文本解析快数倍且内存更省）；只配 time_wait 时走 sockstat O(1) 快速路径。对用户完全透明。
2. **精准内核状态过滤**——请求的 `States` 位图只包含 ESTABLISHED、TIME_WAIT、CLOSE_WAIT 三种状态，内核跳过 LISTEN/SYN_SENT 等无关 socket，减少无效数据传输。
3. **零外部依赖**——直接使用标准库 `syscall` 操作 Netlink socket，不引入 `mdlayher/netlink` 等第三方包。
4. **IPv6 自动检测**——依次查询 `AF_INET` 和 `AF_INET6`，IPv6 不可用时检测 `ENOENT`/`EAFNOSUPPORT`/`EPROTONOSUPPORT` 三种错误码，静默跳过。
5. **安全防护**——`SOCK_CLOEXEC` 防止 fd 泄漏给子进程；`SO_RCVTIMEO = 5s` 防止 Recv 永久阻塞。
6. **`readSockstatTimeWait` 错误分层**——文件读取失败和解析失败分别返回不同的错误信息，便于定位问题。`parseSockstatTimeWait` 为独立纯函数，放在无 build tag 文件中，全平台可测试。
7. **不做增量**——CLOSE_WAIT 和 TIME_WAIT 的绝对数量就是问题指标，不需要与上次比较。
8. **可测试性**——`queryStatesFn` 和 `readTimeWaitFn` 为包级函数变量，测试中通过 mock 替换即可，无需接口抽象。

### Netlink 性能特征

| 连接数 | Netlink 耗时（估算） | `/proc/net/tcp` 耗时 | 说明 |
| --- | --- | --- | --- |
| 1,000 | < 1ms | < 1ms | 差异不明显 |
| 10,000 | ~2ms | ~5ms | Netlink 优势开始体现 |
| 100,000 | ~10ms | ~50ms | 5 倍差距 |
| 500,000 | ~50ms | ~250ms | 省去文本序列化/反序列化开销 |

Netlink 的优势在高连接数时更显著，因为 `/proc/net/tcp` 的瓶颈在于内核将每条连接序列化为 ~150 字节的文本行，而 Netlink 直接传递固定大小的二进制结构体。

## Description 示例

### close_wait

- 正常：`0 CLOSE_WAIT connections, everything is ok`
- 告警：`350 CLOSE_WAIT connections, above warning threshold 100`
- 严重：`2000 CLOSE_WAIT connections, above critical threshold 1000`

### time_wait

- 正常：`1200 TIME_WAIT connections, everything is ok`
- 告警：`8000 TIME_WAIT connections, above warning threshold 5000`
- 严重：`25000 TIME_WAIT connections, above critical threshold 20000`

### 读取失败

- `failed to query TCP states via netlink: netlink dial: socket: protocol not supported`
- `failed to read sockstat: read /proc/net/sockstat: permission denied`
- `failed to read sockstat: tw field not found in /proc/net/sockstat`

## 默认配置建议

| 决策 | 值 | 理由 |
| --- | --- | --- |
| close_wait.warn_ge | `100` | 正常应用几乎不应该有 CLOSE_WAIT |
| close_wait.critical_ge | `1000` | 大量 CLOSE_WAIT 暗示严重泄漏 |
| time_wait.warn_ge | `5000` | 高并发场景下 TIME_WAIT 常见但需要关注 |
| time_wait.critical_ge | `20000` | 接近默认端口范围（28232）的 70% |
| interval | `"60s"` | 平衡灵敏度和性能 |
| for_duration | `"2m"` | TIME_WAIT 随流量波动，2 分钟过滤瞬时尖峰；CLOSE_WAIT 持续 2 分钟基本确认泄漏 |
| repeat_interval | `"30m"` | 持续性问题 |
| repeat_number | `3` | 防止噪音 |

### 阈值选择依据

**CLOSE_WAIT**：

正常运行的应用，CLOSE_WAIT 数量应接近 0。少量 CLOSE_WAIT（< 10）可能是请求正在处理中的正常过渡态。超过 100 通常意味着存在连接泄漏 bug。

**TIME_WAIT**：

TIME_WAIT 是 TCP 正常关闭流程的一部分（持续 2×MSL，Linux 默认 60s）。数量与 QPS 和连接模式相关：
- 短连接 QPS 1000 → 稳态约 60000 个 TIME_WAIT
- 使用连接池/Keep-Alive → TIME_WAIT 极少

默认临时端口范围 `net.ipv4.ip_local_port_range = 32768 60999`（共 28232 个）。TIME_WAIT 达到 20000 时，可用端口所剩无几。注意：TIME_WAIT 的端口耗尽是按**目标 IP:Port** 维度计算的（即 4 元组中源端口不能重复），因此系统级总数是上界估计，实际风险取决于连接目标的集中程度。

### 为什么 `for_duration` 默认 `"2m"` 而非 `0`

- **TIME_WAIT** 天然有 60 秒生命周期，数量随流量模式波动。一次短暂的流量尖峰可能让 TIME_WAIT 瞬间冲高，60 秒后自然回落。`for_duration = 0` 会对这类瞬时尖峰告警，产生噪音
- **CLOSE_WAIT** 一旦泄漏不会自动消失，持续 2 分钟基本可以确认是真正的泄漏而非请求处理中的正常过渡态
- `"2m"` 是两者的平衡点——既过滤了 TIME_WAIT 的瞬时波动，又不会延迟 CLOSE_WAIT 泄漏的发现
- 符合原则 6（宁可漏报，不可误报）

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | Netlink `INET_DIAG` + `/proc/net/sockstat` |
| macOS | 不支持 | Init 返回错误 |
| Windows | 不支持 | Init 返回错误 |

Netlink 是 Linux 特有的内核接口，其他平台无对等替代。对于 catpaw 的定位（Linux 生产环境监控），这不是问题。

## 文件结构

```
plugins/tcpstate/
    design.md          # 本文档
    tcpstate.go        # 插件框架 + 业务逻辑（Init/Gather/emitStateEvent）
    diag_linux.go      # Linux Netlink 实现 + sockstat 读取（build tag: linux）
    diag_other.go      # 非 Linux 平台 stub（build tag: !linux）
    sockstat.go        # sockstat 纯解析函数（无 build tag，全平台可测试）
    tcpstate_test.go   # 测试

conf.d/p.tcpstate/
    tcpstate.toml      # 默认配置
```

**文件拆分理由**：
- `diag_linux.go` / `diag_other.go` 通过 build tag 隔离平台特有代码
- `sockstat.go` 的 `parseSockstatTimeWait` 是纯函数（输入 `[]byte`，输出数值），不依赖任何 Linux 特有 API，放在无 build tag 文件中使其可在 macOS/Windows 上跑测试
- `tcpstate.go` 通过包级函数变量（`queryStatesFn`、`readTimeWaitFn`）解耦平台实现，测试中可 mock

## 默认配置文件示例

```toml
[[instances]]
## ===== TCP 连接状态监控（60 秒跑起来）=====
## CLOSE_WAIT：应用未关闭 socket，连接泄漏。正常应接近 0
##   排查：ss -tnp state close-wait，找到泄漏进程，检查代码中未 Close() 的连接
## TIME_WAIT：TCP 正常关闭后的等待态（60s），高并发短连接场景会大量积累
##   排查：ss -s 查看 timewait 计数，考虑启用连接池/Keep-Alive 或调整
##         net.ipv4.tcp_tw_reuse / net.ipv4.ip_local_port_range

interval = "60s"

## CLOSE_WAIT 连接数阈值
## 正常应用几乎不应有 CLOSE_WAIT，100 就值得调查
[instances.close_wait]
warn_ge = 100
critical_ge = 1000
# title_rule = "[check]"

## TIME_WAIT 连接数阈值
## 与 QPS 和连接模式相关，高并发短连接场景常见
## 默认端口范围 28232 个，20000 时可用端口已不多
[instances.time_wait]
warn_ge = 5000
critical_ge = 20000
# title_rule = "[check]"

[instances.alerting]
for_duration = "2m"
repeat_interval = "30m"
repeat_number = 3
# disabled = false
# disable_recovery_notification = false
```

## 常见排查场景

| 告警 | 可能原因 | 排查命令 |
| --- | --- | --- |
| CLOSE_WAIT 堆积 | 应用未关闭连接（HTTP client 未 Close Body、DB 连接未释放） | `ss -tnp state close-wait` 定位进程 |
| TIME_WAIT 过多 | 大量短连接、未使用连接池 | `ss -s` 查看 timewait，检查 `net.ipv4.tcp_tw_reuse` |
| TIME_WAIT 导致端口耗尽 | 对同一目标的短连接过多，临时端口被占满 | `ss -tn state time-wait dst <ip>:<port> \| wc -l` 按目标统计，`sysctl net.ipv4.ip_local_port_range` 扩大范围或启用 `tcp_tw_reuse` |

## 与 sockstat 插件的协同

sockstat 监控的是 **listen 队列溢出**（服务端 accept 不及时），tcpstate 监控的是 **连接状态异常**（客户端/应用侧问题）。两者从不同角度覆盖 TCP 健康：

```
客户端 ──connect──→ [SYN_SENT → ESTABLISHED → CLOSE_WAIT?] ← tcpstate 关注
服务端 ──accept───→ [LISTEN → SYN_RECV → ESTABLISHED]       ← sockstat 关注
                     ↑ listen queue overflow
```

建议同时启用两个插件，全面覆盖 TCP 栈。
