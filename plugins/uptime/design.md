# uptime 插件设计

## 概述

检测系统异常重启：当系统运行时间（uptime）低于配置的阈值时产出告警事件，意味着系统在短时间内发生过重启。

uptime 自然增长会超过阈值，告警自动恢复——每次重启产出一次告警、一次恢复，形成完整的事件生命周期。

**定位**：补充 procnum（只关心进程是否存在）和 systemd（只关心服务状态）的不足，覆盖"整机重启"这一无法被进程级监控捕获的场景。

**参考**：Nagios `check_uptime`。Nagios 的 check_uptime 默认检查 uptime 是否**超过**阈值（用于检测需要重启打补丁的机器），catpaw 取反——检查 uptime 是否**低于**阈值（用于检测异常重启），更符合 On-call 场景的需要。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 异常重启检测 | `uptime::reboot_detected` | uptime 低于阈值时告警 |

- **target label** 为 `"system"`（uptime 是系统级属性，每台机器唯一）
- **默认 title_rule** 为 `"[check]"`

### 未来扩展维度（本版本不实现）

| 维度 | check label | 说明 |
| --- | --- | --- |
| 长时间未重启 | `uptime::stale_reboot` | uptime 超过阈值时告警（如 180 天未重启，可能需要打内核补丁） |

## 数据来源

`gopsutil/v3/host` 包（已是项目依赖）：

- `host.Uptime()` — 返回 uptime 秒数（`uint64`）
- `host.BootTime()` — 返回开机 Unix 时间戳（`uint64`），用于 `_attr_boot_time`

各平台实现：

| 平台 | Uptime 数据来源 | 特点 |
| --- | --- | --- |
| Linux | `/proc/uptime` | 单调时钟，不受 NTP/时钟调整影响 |
| macOS | `sysctl kern.boottime` | 系统调用 |
| Windows | `GetTickCount64` | 毫秒级精度 |

### 无新增依赖

`gopsutil/v3` 已是项目依赖（cpu、mem、disk 插件均在使用），`host` 子包无需额外引入。

## 阈值设计

### "lt" 反向阈值

使用 `warn_lt` / `critical_lt` 表示"uptime 低于多久时触发"：

| 阈值 | 含义 |
| --- | --- |
| `critical_lt = "10m"` | uptime < 10 分钟时 Critical（刚刚重启） |
| `warn_lt = "1h"` | uptime < 1 小时时 Warning（最近重启过） |

约束：如果两者都 > 0，`warn_lt` 必须 > `critical_lt`（warn 是外圈，critical 是更紧急的内圈）。

### 告警生命周期

```
t=0:   系统重启
t=1m:  第一次 Gather，uptime=1m < critical_lt(10m) → Critical
t=5m:  uptime=5m < critical_lt(10m) → Critical（持续中）
t=10m: uptime=10m >= critical_lt(10m)，但 < warn_lt(1h) → Warning
t=1h:  uptime=1h >= warn_lt(1h) → OK（自动恢复）
```

这是"自愈型"事件——不需要人工介入恢复，uptime 自然增长会使告警消失。`for_duration` 设为 0 即可，因为重启是确定性事件，无需持续确认。

### 默认阈值

| 阈值 | 默认值 | 理由 |
| --- | --- | --- |
| `critical_lt` | `"10m"` | 10 分钟内的 uptime 意味着系统刚刚重启，属紧急事件 |
| `warn_lt` | `0`（不启用） | 默认只做重启检测，不设 warning 层级；用户可按需开启 |

开箱即用：默认配置能检测到系统重启并在 10 分钟后自动恢复。

## 结构体设计

```go
type RebootDetectedCheck struct {
    WarnLt     config.Duration `toml:"warn_lt"`
    CriticalLt config.Duration `toml:"critical_lt"`
    TitleRule  string          `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    RebootDetected RebootDetectedCheck `toml:"reboot_detected"`
}

type UptimePlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

不需要：
- `Timeout` — 读取 uptime 是本地系统调用，毫秒级完成
- `Concurrency` — 单次调用，无并发
- `inFlight` / `GatherTimeout` — 不涉及网络或文件系统阻塞操作
- `Targets` — uptime 是系统级唯一值，无需指定目标

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_uptime` | `2d 5h 30m` | 人类可读的 uptime |
| `_attr_uptime_seconds` | `191400` | 原始秒数 |
| `_attr_boot_time` | `2026-02-24 08:30:00 CST` | 系统启动时间（本地时区，附带时区标识） |
| `_attr_critical_lt` | `10m` | 配置的 critical 阈值（humanDuration 格式，与 uptime 风格一致） |
| `_attr_warn_lt` | `1h` | 配置的 warn 阈值（仅配置时显示） |

OK 事件也携带完整 `_attr_` 标签，便于巡检时确认系统运行时长和启动时间。

## Init() 校验

Init() 只校验配置**合法性**，不校验"是否启用"——阈值全为 0 时不报错，Gather 静默跳过即可。

1. 如果 `warn_lt > 0 && critical_lt > 0 && warn_lt <= critical_lt`，报错：`warn_lt must be greater than critical_lt`
2. 无其他需要校验的字段——结构体极简，无需校验 targets、concurrency 等

## Gather() 逻辑

```
Gather(q):
    // 未配置任何阈值时静默跳过
    if ins.RebootDetected.CriticalLt == 0 && ins.RebootDetected.WarnLt == 0:
        return

    uptimeSec, err = host.Uptime()
    if err:
        event = buildEvent("uptime::reboot_detected", "system")
        event → Critical: "failed to get system uptime: <error>"
        q.PushFront(event)
        return

    bootTimeSec, _ = host.BootTime()   // non-fatal: 仅用于 _attr_

    uptimeDur = time.Duration(uptimeSec) * time.Second

    event = buildEvent("uptime::reboot_detected", "system")

    // 附加 _attr_ 标签
    event._attr_uptime = humanDuration(uptimeDur)
    event._attr_uptime_seconds = strconv.FormatUint(uptimeSec, 10)
    if bootTimeSec > 0:
        event._attr_boot_time = time.Unix(int64(bootTimeSec), 0).Format("2006-01-02 15:04:05 MST")
    event._attr_critical_lt = humanDuration(time.Duration(ins.RebootDetected.CriticalLt))
    if ins.RebootDetected.WarnLt > 0:
        event._attr_warn_lt = humanDuration(time.Duration(ins.RebootDetected.WarnLt))

    // 阈值判断
    criticalDur = time.Duration(ins.RebootDetected.CriticalLt)
    warnDur = time.Duration(ins.RebootDetected.WarnLt)

    if uptimeDur < criticalDur:
        event → Critical: "system uptime <uptimeHuman>, rebooted within critical threshold <threshold>"
    else if warnDur > 0 && uptimeDur < warnDur:
        event → Warning: "system uptime <uptimeHuman>, rebooted within warning threshold <threshold>"
    else:
        event → Ok: "system uptime <uptimeHuman>, everything is ok"

    q.PushFront(event)
```

### humanDuration

uptime 的人类可读格式需要覆盖从秒到天的范围，且尾部零值自动省略（`10m` 而非 `10m 0s`）：

```
humanDuration(d):
    days = int(d.Hours()) / 24
    hours = int(d.Hours()) % 24
    minutes = int(d.Minutes()) % 60
    seconds = int(d.Seconds()) % 60

    if days > 0:
        s = "<days>d"
        if hours > 0:   s += " <hours>h"
        if minutes > 0: s += " <minutes>m"
        return s
    if hours > 0:
        s = "<hours>h"
        if minutes > 0: s += " <minutes>m"
        if seconds > 0: s += " <seconds>s"
        return s
    if minutes > 0:
        s = "<minutes>m"
        if seconds > 0: s += " <seconds>s"
        return s
    return "<seconds>s"
```

示例：`10m`、`3m 20s`、`1h`、`2h 30m`、`15d 7h 30m`、`30d`。

短格式便于在 Description 和 FlashDuty 通知中一目了然。同时 `_attr_critical_lt` / `_attr_warn_lt` 也使用此函数格式化，保持同一事件内所有时间格式风格一致。

### 关键行为

1. **单次 Gather 产出恰好一个事件**（原则 7：自身故障可感知）
2. **`host.Uptime()` 失败产出 Critical 事件**，不静默跳过（原则 7）
3. **`host.BootTime()` 失败仅影响 `_attr_boot_time` 不显示**，不阻塞主逻辑（原则 13：局部失败不影响全局）
4. **无需并发、无需 goroutine**——同步单次调用（原则 8：采集开销可控）
5. **无需 `for_duration`**——重启是确定性事件，单次即可确认
6. **告警自愈**——uptime 自然增长超过阈值后自动恢复

## Description 示例

- 刚刚重启（Critical）：`system uptime 3m 20s, rebooted within critical threshold 10m`
- 最近重启（Warning）：`system uptime 45m 10s, rebooted within warning threshold 1h`
- 系统健康（Ok）：`system uptime 15d 7h 30m, everything is ok`
- 恰好整点（尾部零值省略）：`system uptime 2h, rebooted within warning threshold 4h`
- 获取失败（Critical）：`failed to get system uptime: <error>`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| critical_lt | `"10m"` | 10 分钟内 uptime 意味着刚刚重启；重启后 10 分钟自动恢复 |
| warn_lt | 不启用 | 默认只做 Critical 级别的重启检测；用户可按需开启 |
| interval | `"1m"` | 重启检测需及时；1 分钟检查一次，10 分钟内至少 10 次机会捕获 |
| for_duration | `0` | 重启是确定性事件，无需持续确认 |
| repeat_interval | `"1h"` | 10 分钟内自愈，实际最多通知 1 次 |
| repeat_number | `0` | 不限制，但因自愈特性，实际通知次数极少 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 检测整机重启 | **uptime** | 系统级 uptime 变化 |
| 检测进程崩溃重启 | procnum / systemd | 进程级监控 |
| 检测容器重启 | docker（计划中） | 容器 restart_count |
| 检测 CPU/内存异常导致 OOM 重启 | cpu / mem | 资源级监控 |

uptime 与 procnum/systemd 互补：进程崩溃后 systemd 自动拉起时 uptime 不变（无整机重启），但 procnum/systemd 能捕获；整机重启时所有进程都会重启，uptime 能捕获。

## 跨平台兼容性

| 平台 | 支持 | 数据来源 |
| --- | --- | --- |
| Linux | 完整支持 | `/proc/uptime`（单调时钟） |
| macOS | 完整支持 | `sysctl kern.boottime` |
| Windows | 完整支持 | `GetTickCount64` |

全部由 `gopsutil/v3/host` 封装，无需平台特定代码，无需 build tags。

### 容器环境

`host.Uptime()` 在容器内读取的是**宿主机**的 uptime（Linux `/proc/uptime` 在默认 namespace 下不隔离），而非容器自身的运行时间。若 catpaw 运行在容器内，检测的是宿主机重启，非容器重启。容器重启检测由 docker 插件覆盖。

## 文件结构

```
plugins/uptime/
    design.md             # 本文档
    uptime.go             # 主逻辑
    uptime_test.go        # 测试

conf.d/p.uptime/
    uptime.toml           # 默认配置
```

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 默认检测系统重启：uptime < 10 分钟时产出 Critical 告警
## uptime 自然增长超过阈值后自动恢复，无需人工介入

## 重启检测阈值
## critical_lt: uptime 低于此值时 Critical（默认 10 分钟）
## warn_lt: uptime 低于此值时 Warning（默认不启用）
[instances.reboot_detected]
critical_lt = "10m"
# warn_lt = "1h"
# title_rule = "[check]"

## 采集间隔（重启检测需及时，建议 1 分钟）
interval = "1m"

[instances.alerting]
for_duration = 0
repeat_interval = "1h"
repeat_number = 0
# disabled = false
# disable_recovery_notification = false
```
