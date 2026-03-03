# hostident 插件设计

## 概述

检测机器 hostname 和 IP 地址在 catpaw 进程生命周期内是否发生变化。变化时产出告警，提醒用户重启 catpaw 以刷新全局 labels 中的 `${HOSTNAME}` / `${IP}` 值。

**背景**：catpaw 在启动时将 `${HOSTNAME}` 和 `${IP}` 解析为实际值并写入全局 labels，全程不再更新。如果运行期间 hostname 或 IP 发生变化，告警事件中的 `from_hostname` / `from_hostip` 会与实际不符，用户无法正确关联告警来源。此外，labels 变化会导致 AlertKey（MD5）改变，引发虚假恢复和虚假新告警。

**定位**：用 catpaw 自身的监控能力解决自身的元问题——检测影响告警正确性的系统身份变更，让用户主动决定是否重启。

**参考**：Prometheus node_exporter、Telegraf 均在启动时确定 hostname，进程生命周期内不变。hostident 补充了"变化感知"这一缺失环节。

## 检查维度

| 维度 | check label | 默认级别 | 说明 |
| --- | --- | --- | --- |
| hostname 变化 | `hostident::hostname_changed` | Warning | hostname 变化影响 AlertKey 正确性 |
| IP 变化 | `hostident::ip_changed` | Warning | IP 变化影响 labels 准确性 |

- **target label** 为 `"system"`（与 uptime 插件保持一致，系统级唯一属性）
- 每个 check 的 severity 可由用户自定义（Critical / Warning / Info），默认 Warning

## 数据来源

| 数据 | 来源 | 跨平台 |
| --- | --- | --- |
| hostname | `os.Hostname()` | Go 标准库，Linux/macOS/Windows 全支持 |
| IP | `config.DetectIP()` | 复用 config 包已有函数，与 labels `${IP}` 使用同一检测路径 |

### IP 检测一致性

hostident 使用 `config.DetectIP()` 而非自行实现 IP 检测。这保证了插件检测到的 IP 与 `InitConfig` 中写入 labels 的 `${IP}` 值来自完全相同的检测逻辑（网关发现 → UDP dial → 接口枚举 fallback），避免因检测方式不同产生假阳性。

### 无新增依赖

仅使用 Go 标准库 `os` 和项目已有的 `config.DetectIP()`。

## 基准值机制

插件在 `Init()` 时记录 hostname 和 IP 的基准值，`Gather()` 每轮获取当前值与基准值对比，不一致则按配置的 severity 告警，一致则产出 OK 事件。基准值在进程生命周期内不变，重启 catpaw 后基准值自然刷新为新值。

## 告警生命周期

```
t=0:    catpaw 启动，Init() 记录 baseHostname="host-A", baseIP="10.0.0.1"
t=5m:   Gather: hostname="host-A", IP="10.0.0.1" → 两个 OK 事件
t=10m:  运维修改 hostname 为 "host-B"
t=15m:  Gather: hostname="host-B" != "host-A" → Warning（或用户配置的级别）
                IP="10.0.0.1" → OK
t=20m:  用户重启 catpaw
t=20m:  Init() 记录 baseHostname="host-B", baseIP="10.0.0.1"
t=25m:  Gather: hostname="host-B" → OK（新基准）
```

特殊情况：如果 hostname 又变回原值（例如运维撤回操作），`Gather()` 会自动产出 OK 事件恢复告警。

### 关于重启后恢复通知

重启后旧进程的告警状态丢失，新进程不会为旧告警发送恢复通知。这是 catpaw 告警引擎的通用特性（所有插件都受此影响），不在 hostident 插件层解决。下游平台（FlashDuty、PagerDuty）的超时自动关闭机制可覆盖此场景。

## 结构体设计

```go
type CheckConfig struct {
    Enabled  bool   `toml:"enabled"`
    Severity string `toml:"severity"`
}

type Instance struct {
    config.InternalConfig

    HostnameChanged CheckConfig `toml:"hostname_changed"`
    IPChanged       CheckConfig `toml:"ip_changed"`

    baseHostname string
    baseIP       string
    hostSeverity string  // normalized from HostnameChanged.Severity
    ipSeverity   string  // normalized from IPChanged.Severity
}

type HostidentPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

`CheckConfig` 是两个 check 共用的配置结构，`Severity` 支持 Critical / Warning / Info（大小写不敏感），为空时默认 Warning。`Init()` 阶段 `parseSeverity` 校验并规范化为标准值。

不需要：
- `Targets` — 系统级唯一属性，无需指定目标
- `Concurrency` — 单次系统调用，无并发需求
- `Timeout` — `os.Hostname()` 和 `config.DetectIP()` 均为本地操作，毫秒级完成
- `Partials` — 配置极简（两个 bool），无模板复用场景

## Attrs（SetAttrs 设置）

| 属性 | 示例值 | 说明 |
| --- | --- | --- |
| `baseline` | `host-A` / `10.0.0.1` | Init 时记录的基准值 |
| `current` | `host-B` / `10.0.0.2` | 当前采集到的值 |

OK 事件也携带 attrs，便于巡检确认当前身份信息。

## Init() 校验

每个 check 独立处理：只有 `Enabled=true` 的 check 才会校验 severity 和获取基准值。两个 check 都 disable 时 `Init()` 直接返回 nil，`Gather()` 也不产出任何事件。Init 失败意味着基准值无法建立，会阻止实例启动。

## Gather() 行为

- 单次 Gather 最多产出两个事件（hostname 和 IP 各一个），互相独立
- 获取失败或值变化时使用用户配置的 severity（默认 Warning），不静默跳过
- 值变回原值时自动产出 OK 事件恢复告警
- 无需并发，同步单次调用

## Description 示例

- hostname 变化：`hostname changed from "host-A" to "host-B" since catpaw started, consider restarting catpaw`
- IP 变化：`IP changed from 10.0.0.1 to 10.0.0.2 since catpaw started, consider restarting catpaw`
- 一切正常（Ok）：`hostname "host-A" unchanged` / `IP 10.0.0.1 unchanged`
- hostname 获取失败：`failed to get hostname: <error>`
- IP 获取失败：`failed to detect current IP address`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| hostname_changed.enabled | `true` | 默认启用，hostname 变化影响 AlertKey |
| hostname_changed.severity | `"Warning"` | 默认 Warning，用户可改为 Critical 或 Info |
| ip_changed.enabled | `true` | 默认启用，IP 变化影响 labels 准确性 |
| ip_changed.severity | `"Warning"` | 默认 Warning，用户可改为 Critical 或 Info |
| interval | `"5m"` | 低频事件，5 分钟检测一次足够 |
| for_duration | `0` | 身份变化是确定性事件，无需持续确认 |
| repeat_interval | `"1h"` | 持续提醒用户重启 |
| repeat_number | `0` | 不限制，直到用户重启 catpaw |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 检测 hostname/IP 变化 | **hostident** | 系统身份变更感知 |
| 检测整机重启 | uptime | 系统 uptime 变化 |
| 检测网络连通性 | ping / net | 网络层探测 |
| 检测 DNS 解析异常 | dns | DNS 查询检测 |

hostident 与 uptime 互补：重启后 uptime 检测到低 uptime，hostident 检测到身份一致（因为重启后基准值刷新）；不重启但 hostname 变化时，uptime 无感知，hostident 能捕获。

## 跨平台兼容性

| 平台 | hostname | IP |
| --- | --- | --- |
| Linux | `os.Hostname()` — 读取 `/proc/sys/kernel/hostname` | `config.DetectIP()` — UDP dial + 接口枚举 |
| macOS | `os.Hostname()` — `gethostname(2)` 系统调用 | 同上 |
| Windows | `os.Hostname()` — `GetComputerNameEx` | 同上 |

全部由 Go 标准库和 config 包封装，无需平台特定代码，无需 build tags。

## 文件结构

```
plugins/hostident/
    design.md             # 本文档
    hostident.go          # 主逻辑
    hostident_test.go     # 测试

conf.d/p.hostident/
    hostident.toml        # 默认配置
```

## 默认配置文件

```toml
[[instances]]
## 检测 hostname 变化
## severity: 变化时的告警级别（Critical / Warning / Info，默认 Warning）
[instances.hostname_changed]
enabled = true
# severity = "Warning"

## 检测 IP 变化
[instances.ip_changed]
enabled = true
# severity = "Warning"

## 低频事件，5 分钟检测一次即可
interval = "5m"

[instances.alerting]
for_duration = 0
repeat_interval = "1h"
repeat_number = 0
# disabled = false
# disable_recovery_notification = false
```
