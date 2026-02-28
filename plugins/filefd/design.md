# filefd 插件设计

## 概述

监控 Linux 系统级文件描述符（file descriptor）使用率：当 `allocated / file-max` 超过阈值时产出告警事件。

系统级 fd 耗尽时所有进程都无法打开新文件、建立新连接、创建管道——但报错信息千差万别（`too many open files`、`Cannot allocate memory`、`Connection refused`、日志写入失败、数据库连接超时），是典型的"症状与根因严重脱节"的问题。

**定位**：系统级资源上限监控。与 filecheck 插件（检查特定文件的存在性/权限/修改时间）互补——filecheck 关注"某个具体文件怎么样"，filefd 关注"系统还能不能打开文件"。

**参考**：Prometheus `node_exporter` 的 filefd collector。node_exporter 采集 `node_filefd_allocated` 和 `node_filefd_maximum` 指标，catpaw 直接计算使用率并在逼近上限时主动告警。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 文件描述符使用率 | `filefd::filefd_usage` | allocated/max 百分比超阈值告警 |

- **target label** 为 `"system"`（文件描述符是系统级共享资源）
- **默认 title_rule** 为 `"[check]"`

## 数据来源

读取 `/proc/sys/fs/file-nr`，该文件包含三个 tab 分隔的值：

| 字段 | 含义 | 示例值 |
| --- | --- | --- |
| 第 1 列 | 当前已分配的文件描述符数（allocated） | `9344` |
| 第 2 列 | 已分配但未使用数（Linux 2.6+ 恒为 0，忽略） | `0` |
| 第 3 列 | 系统允许的最大文件描述符数（file-max） | `393164` |

### 关于 file-max

`file-max` 由内核启动时根据可用内存自动计算（约为内存 KB 数的 10%），也可通过 `sysctl fs.file-max` 手动调整。现代 16GB 内存服务器的 `file-max` 通常在 100 万以上。

### 系统级 vs 进程级

| 层级 | 限制参数 | 查看方式 | 本插件覆盖 |
| --- | --- | --- | --- |
| 系统级 | `fs.file-max` | `/proc/sys/fs/file-nr` | ✅ |
| 进程级 | `ulimit -n` / `RLIMIT_NOFILE` | `/proc/<pid>/limits` | ❌（不在本插件范围） |

进程级 fd 限制（`ulimit -n`，默认 1024 或 65535）在实践中更常被触发，但那属于进程级监控范畴。本插件聚焦系统级——当系统级 fd 耗尽时，**所有进程**都受影响，且无法通过提高单个进程的 `ulimit` 解决。

### 与 conntrack 的差异

| 对比项 | conntrack | filefd |
| --- | --- | --- |
| proc 文件 | 两个独立文件（count / max） | 一个文件三列（file-nr） |
| 路径回退 | 有（nf_conntrack → ip_conntrack） | 无（路径固定） |
| 模块未加载 | 需处理（静默跳过） | 不存在此场景（file-nr 始终存在） |
| 解析方式 | 各读一个文件 → ParseUint | 读一个文件 → Split → ParseUint |

### 无新增依赖

仅读取 proc 文件，使用标准库 `os.ReadFile` + `strings.Fields` + `strconv.ParseUint`，无需任何第三方依赖。

## 阈值设计

### 使用率百分比

`filefd_usage = allocated / max * 100`

| 阈值 | 含义 |
| --- | --- |
| `warn_ge = 80.0` | 使用率 ≥ 80% 时 Warning |
| `critical_ge = 90.0` | 使用率 ≥ 90% 时 Critical |

### 为什么默认阈值是 80/90 而非 conntrack 的 75/90

conntrack 使用 75% Warning 是因为连接跟踪表可以在秒级被突发流量填满（SYN flood 等），需要更大的预警缓冲。fd 增长通常是**渐进的**——fd 泄漏是进程逐步累积的过程，80% Warning 已提供充足的处置窗口。

### 处置建议（供 Description 参考）

用户收到告警后典型处置：
1. `cat /proc/sys/fs/file-nr` 确认当前用量
2. `lsof | awk '{print $1}' | sort | uniq -c | sort -rn | head -20`（找出 fd 占用最多的进程）
3. 临时扩容：`sysctl -w fs.file-max=2097152`
4. 定位 fd 泄漏进程并修复（`ls -la /proc/<pid>/fd | wc -l`）
5. 持久化：写入 `/etc/sysctl.d/99-file-max.conf`

## 结构体设计

```go
type FilefdUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    FilefdUsage FilefdUsageCheck `toml:"filefd_usage"`
}

type FilefdPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

不需要：
- `Timeout` — 读取 proc 文件是本地操作，微秒级完成
- `Concurrency` — 仅读一个文件，无并发需求
- `Targets` — 文件描述符是系统级唯一资源

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_allocated` | `9344` | 当前已分配 fd 数 |
| `_attr_max` | `393164` | 系统上限（file-max） |
| `_attr_usage_percent` | `2.4%` | 格式化的使用率 |

Ok 事件也携带完整 `_attr_` 标签，便于巡检时确认 fd 使用水位。

## Init() 校验

Init() 只校验配置**合法性**，不校验"是否启用"——阈值全为 0 时不报错，Gather 静默跳过即可。与 cpu、mem、conntrack 等插件保持一致。

```
Init():
    1. if runtime.GOOS != "linux":
        return error: "filefd plugin only supports linux (current: <os>)"

    2. if warn_ge < 0 || warn_ge > 100 || critical_ge < 0 || critical_ge > 100:
        return error: "filefd_usage thresholds must be between 0 and 100"

    3. if warn_ge > 0 && critical_ge > 0 && warn_ge >= critical_ge:
        return error: "filefd_usage.warn_ge must be less than critical_ge"
```

## Gather() 逻辑

```
Gather(q):
    // 阈值全为 0 时静默跳过
    if ins.FilefdUsage.WarnGe == 0 && ins.FilefdUsage.CriticalGe == 0:
        return

    allocated, max, err = readFileNr()

    if err != nil:
        event = buildEvent("filefd::filefd_usage", "system")
        event → Critical: "failed to read file-nr: <error>"
        q.PushFront(event)
        return

    if max == 0:
        event = buildEvent("filefd::filefd_usage", "system")
        event → Critical: "file-max is 0, cannot calculate usage"
        q.PushFront(event)
        return

    usagePercent = float64(allocated) / float64(max) * 100
    allocatedStr = strconv.FormatUint(allocated, 10)
    maxStr = strconv.FormatUint(max, 10)

    event = buildEvent("filefd::filefd_usage", "system")
    event._attr_allocated = allocatedStr
    event._attr_max = maxStr
    event._attr_usage_percent = fmt.Sprintf("%.1f%%", usagePercent)

    status = EvaluateGeThreshold(usagePercent, warn_ge, critical_ge)
    event.SetEventStatus(status)

    switch status:
        Critical: "filefd usage 92.3% (362945/393164), above critical threshold 90%"
        Warning:  "filefd usage 83.5% (327832/393164), above warning threshold 80%"
        Ok:       "filefd usage 2.4% (9344/393164), everything is ok"

    q.PushFront(event)
```

### readFileNr() 伪代码

```
readFileNr() (allocated uint64, max uint64, err error):
    data, err = os.ReadFile("/proc/sys/fs/file-nr")
    if err != nil:
        return 0, 0, fmt.Errorf("read /proc/sys/fs/file-nr: %v", err)

    // file-nr 格式: "9344\t0\t393164\n"（三列 tab 分隔）
    fields = strings.Fields(strings.TrimSpace(string(data)))
    if len(fields) < 3:
        return 0, 0, fmt.Errorf("unexpected file-nr format: %q", string(data))

    allocated, err = strconv.ParseUint(fields[0], 10, 64)
    if err != nil:
        return 0, 0, fmt.Errorf("parse allocated: %v", err)

    // fields[1] 在 Linux 2.6+ 恒为 0，跳过

    max, err = strconv.ParseUint(fields[2], 10, 64)
    if err != nil:
        return 0, 0, fmt.Errorf("parse max: %v", err)

    return allocated, max, nil
```

### 关键行为

1. **无"模块未加载"场景** — `/proc/sys/fs/file-nr` 在所有 Linux 系统上始终存在，比 conntrack 更简单
2. **文件读取/解析失败产出 Critical 事件**（原则 7：自身故障可感知）
3. **`file-max = 0` 产出 Critical 事件**（防止除零，且 max=0 本身是异常配置）
4. **格式校验** — 若 file-nr 内容不足 3 列，产出 Critical 事件并附带原始内容
5. **单次 Gather 产出 0 或 1 个事件**（阈值全为 0 时 0 个，其他情况 1 个）
6. **无需并发、无需 goroutine** — 同步读一个小文件（原则 8：采集开销可控）

## Description 示例

- 使用率正常（Ok）：`filefd usage 2.4% (9344/393164), everything is ok`
- 使用率偏高（Warning）：`filefd usage 83.5% (327832/393164), above warning threshold 80%`
- 即将耗尽（Critical）：`filefd usage 92.3% (362945/393164), above critical threshold 90%`
- 读取失败（Critical）：`failed to read file-nr: read /proc/sys/fs/file-nr: permission denied`
- 格式异常（Critical）：`failed to read file-nr: unexpected file-nr format: ""`
- 解析失败（Critical）：`failed to read file-nr: parse allocated: strconv.ParseUint: parsing "abc": invalid syntax`
- max 异常（Critical）：`file-max is 0, cannot calculate usage`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| warn_ge | `80.0` | fd 增长通常是渐进的（泄漏累积），80% 留出足够处置时间 |
| critical_ge | `90.0` | 90% 以上需要立即处理，防止全系统文件操作失败 |
| interval | `"30s"` | fd 泄漏是渐进的，30 秒粒度足够 |
| for_duration | `0` | 系统级 fd 使用率不应该高于阈值，单次超阈即需关注 |
| repeat_interval | `"5m"` | 持续高位时定期提醒 |
| repeat_number | `0` | 不限制，直到使用率下降 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 系统级 fd 即将耗尽 | **filefd** | 在所有进程受影响前预警 |
| 特定文件的存在性/权限/修改时间 | filecheck | 检查具体文件状态 |
| 内存不足 | mem | fd 泄漏常伴随内存增长，两者同时告警基本可确认是资源泄漏 |
| 连接跟踪表耗尽 | conntrack | 另一个"静默杀手"，与 filefd 互补覆盖系统级资源 |

## 跨平台兼容性

| 平台 | 支持 | 处理方式 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/proc/sys/fs/file-nr` |
| macOS | 不支持 | Init 返回错误，插件不加载 |
| Windows | 不支持 | Init 返回错误，插件不加载 |

macOS 通过 `sysctl kern.maxfiles` / `kern.num_files` 可获取类似信息，但 macOS 作为服务器极少见，fd 耗尽场景罕见。Windows 使用 Handle 机制，概念不同。

### 容器环境

在容器内读取 `/proc/sys/fs/file-nr` 获取的是**宿主机**的值（默认 namespace 下 proc 不隔离）。若 catpaw 运行在容器内，监控的是宿主机的文件描述符使用率。这通常是期望的行为——系统级 fd 是宿主机级别资源，在容器内监控正好覆盖"宿主机 fd 耗尽导致容器无法创建连接"的场景。

## 文件结构

```
plugins/filefd/
    design.md             # 本文档
    filefd.go             # 主逻辑（仅 Linux）
    filefd_test.go        # 测试

conf.d/p.filefd/
    filefd.toml           # 默认配置
```

不需要 build tags 文件——通过 `runtime.GOOS` 在 Init 中检查即可（与 conntrack 插件一致），保持文件结构简洁。

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 监控 Linux 系统级文件描述符（file descriptor）使用率
## fd 耗尽时所有进程都无法打开文件/建连/写日志
## 报错信息千差万别，是典型的"症状与根因脱节"问题

## 文件描述符使用率阈值（allocated / max 百分比）
[instances.filefd_usage]
warn_ge = 80.0
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
