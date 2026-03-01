# ntp 插件设计

## 概述

检查系统 NTP 时间同步状态、时钟偏移和时间源层级（stratum）。自动探测本机使用的 NTP 工具（chrony / ntpd / timedatectl），无需手动指定。

**核心场景**：

1. **时钟漂移**：NTP 服务故障导致系统时间偏移，引发证书验证失败、日志时间线混乱、分布式系统数据不一致
2. **NTP 服务停止**：chrony/ntpd 挂了但无人发现，时钟慢慢偏移
3. **时间源质量差**：stratum 过高意味着经过了太多跳数，时间精度不可靠

**参考**：Nagios `check_ntp_time` / `check_ntp_peer`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 同步状态 | `ntp::sync` | ntp | NTP 是否处于同步状态 |
| 时钟偏移 | `ntp::offset` | ntp | 本地时钟与 NTP 源的偏差（绝对值） |
| 时间源层级 | `ntp::stratum` | ntp | NTP 源的 stratum 值 |

- **target 固定为 `"ntp"`**——系统级检查，每个 instance 最多 3 个事件
- offset 和 stratum 仅在同步状态下检查（不同步时这两个值无意义）

## 数据来源

### 自动探测（默认 `mode = "auto"`）

按优先级依次尝试：
1. **chronyc** → `chronyc -n tracking`（解析 System time、Stratum、Leap status 等）
2. **ntpq** → `ntpq -pn`（解析 `*` 标记的活跃 peer 行）
3. **timedatectl** → `timedatectl show`（仅能获取同步状态，无 offset/stratum）

### 各后端能力对比

| 能力 | chrony | ntpd | timedatectl |
| --- | --- | --- | --- |
| 同步状态 | Leap status == Normal | 有 `*` 标记的 peer | NTPSynchronized == yes |
| offset | System time 字段 | peer offset 字段（ms） | 不支持 |
| stratum | Stratum 字段 | peer st 字段 | 不支持 |

`timedatectl` 能力最弱，但作为 fallback 可以覆盖没装 chrony/ntpd 的 systemd-timesyncd 场景。

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Mode    string          // "auto"（默认）/ "chrony" / "ntpd" / "timedatectl"
    Timeout config.Duration // 命令执行超时，默认 10s
    Sync    SyncCheck       // 同步状态检查，默认 severity = Critical
    Offset  OffsetCheck     // 偏移阈值（warn_ge / critical_ge）
    Stratum StratumCheck    // stratum 阈值（warn_ge / critical_ge）
}
```

## Init() 校验

1. 仅 Linux 支持
2. `mode = auto` 时按 chrony → ntpd → timedatectl 顺序探测
3. timedatectl 模式下如果配了 offset/stratum 阈值，记录 warn 日志（无法获取数据）
4. 阈值校验：warn < critical

## Gather() 逻辑

1. 调用对应后端命令，解析为统一的 `ntpResult` 结构
2. **sync 检查**：不同步 → severity 告警
3. **offset 检查**（chrony/ntpd）：|offset| 超过阈值 → 告警
4. **stratum 检查**（chrony/ntpd）：stratum 超过阈值 → 告警

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 自动探测 chrony/ntpd/timedatectl |
| macOS | 不支持 | Init 返回错误 |
| Windows | 不支持 | Init 返回错误 |
