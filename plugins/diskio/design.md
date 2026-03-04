# diskio 插件设计

## 概述

监控块设备的 IO 延迟（await），自动探测设备类型（HDD/SSD/NVMe）并应用对应的默认阈值。仅支持 Linux，其他平台自动跳过。

**背景**：现有 `disk` 插件工作在挂载点维度（空间、inode、可写性），而磁盘 IO 性能工作在块设备维度。两者的过滤条件（`mount_points` vs `devices`）、数据来源（`disk.Usage()` vs `disk.IOCounters()` / `/proc/diskstats`）、状态管理（无状态 vs 需要前次采样做差值）完全独立，因此拆分为独立插件。

**为什么只做 await**：IOPS、吞吐量、%util 的合理阈值完全取决于硬件规格，无法提供通用默认值。await（平均 IO 延迟）是唯一接近"通用"的指标——延迟高就是慢，无论什么硬件都意味着性能问题。行业顶级项目（node_exporter、Telegraf、Datadog）均采集 disk IO 指标，但无一提供开箱即用的通用告警阈值，catpaw 通过设备类型自适应填补这一空白。

**参考**：Telegraf `inputs.diskio`、node_exporter `diskstats` collector、Datadog `io` check。三者均将 disk IO 与 disk space 分为独立模块。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| IO 延迟 | `diskio::io_latency` | 块设备平均 IO 延迟（await ms） |

- **target label** 为块设备名（如 `sda`、`nvme0n1`、`dm-0`）
- 每个设备产出一个独立事件，与 disk 插件每个挂载点一个事件的模式一致

## 为什么仅支持 Linux

IO 延迟计算依赖 `/proc/diskstats` 或 gopsutil `IOCounters()` 返回的 `ReadTime`/`WriteTime` 字段。macOS 的 `IOCounters` 不提供 IO 时间字段，Windows 的实现差异大且可靠性不足。Linux 服务器是 disk IO 监控的核心场景，优先覆盖。

非 Linux 平台：`Init()` 阶段静默禁用（记录一条 info 日志），不报错，不产出事件。

## 数据来源

| 数据 | 来源 | 说明 |
| --- | --- | --- |
| IO 计数器 | `gopsutil/v3/disk.IOCounters()` | 返回每个块设备的累计读写次数、字节数、耗时 |
| 设备类型 | `/sys/block/<name>/queue/rotational` | 0=SSD, 1=HDD; NVMe 通过设备名前缀判断 |

使用 gopsutil 而非直接读 `/proc/diskstats`，与现有 disk 插件保持依赖一致。gopsutil 在 Linux 上内部读取 `/proc/diskstats`，无额外开销。

## await 计算

标准 iostat 算法，两次采样间的差值：

```
delta_reads  = s2.ReadCount  - s1.ReadCount
delta_writes = s2.WriteCount - s1.WriteCount
delta_read_ms  = s2.ReadTime  - s1.ReadTime
delta_write_ms = s2.WriteTime - s1.WriteTime

total_ios = delta_reads + delta_writes
total_ms  = delta_read_ms + delta_write_ms

if total_ios == 0:
    await = 0   (设备空闲，状态 OK)
else:
    await = total_ms / total_ios
```

## 设备过滤

两层过滤，默认即合理，用户可选覆盖：

**第一层：跳过虚拟设备**
- 跳过 `loop*`、`ram*` 前缀的设备

**第二层：跳过分区，只保留整盘设备**
- 通过检查 `/sys/block/<name>` 是否存在来判断：存在 = 整盘设备，不存在 = 分区
- `dm-*`（LVM）、`md*`（软 RAID）保留，它们是 `/sys/block/` 下的一级设备

**用户可选过滤**
- `devices`：白名单，非空时只检测列出的设备
- `ignore_devices`：黑名单，从检测范围中排除

不过滤分区的话，一块物理盘会产生多条重复告警（`sda` + `sda1` + `sda2` ...），且分区级 IO 统计本身就是整盘统计的子集，告警意义不大。

## 设备类型探测与默认阈值

### 探测逻辑

1. 设备名以 `nvme` 开头 → NVMe
2. 读取 `/sys/block/<name>/queue/rotational`：值为 `1` → HDD，值为 `0` → SSD
3. 以上均失败 → Unknown

探测结果在 `Init()` 或首次 `Gather()` 时缓存到 `deviceTypes map`，进程生命周期内不再重复读取。

### 默认阈值

| 设备类型 | warn_ge (ms) | critical_ge (ms) | 依据 |
| --- | :---: | :---: | --- |
| HDD | 50 | 200 | 机械盘正常 5-15ms，50ms 已明显异常 |
| SSD | 20 | 100 | SATA SSD 正常 0.1-2ms，20ms 明显异常 |
| NVMe | 10 | 50 | NVMe 正常 0.02-0.1ms，10ms 明显异常 |
| Unknown | 100 | 500 | 保守兜底，避免误报 |

阈值整体偏保守——真正遇到磁盘问题时 await 通常飙到数百甚至上千 ms，这些阈值能可靠捕获。

### 用户覆盖

用户在配置中指定 `warn_ge` / `critical_ge` 后，对所有设备统一生效，覆盖自动探测的默认值。这是简洁性和精确性的取舍：需要逐设备类型微调阈值的用户通常已有专业监控平台。

## 第一次采样无数据

rate 类指标的固有特性。第一次 `Gather()` 时 `prevIOCounters` 为空，只做采样存储、不产出任何事件。第二次 `Gather()` 起才能计算差值并告警。这是正确行为，不是 bug。

## 边界情况处理

| 场景 | 处理方式 |
| --- | --- |
| counter 回绕（s2 < s1） | 跳过该设备本轮检查，下轮用 s2 作为新基线 |
| 设备热插拔（新设备出现） | 第一轮只做基线采样，下轮开始告警 |
| 设备消失（两次间移除） | 只处理两次都存在的设备，清理已消失设备的缓存 |
| 设备空闲（0 次 IO） | await = 0，产出 OK 事件 |
| 非 Linux 平台 | Init() 静默禁用，日志记录 info 级别 |
| 设备类型探测失败 | 使用 Unknown 的保守阈值 |

## 结构体设计

```go
type IOLatencyCheck struct {
    Enabled    bool    `toml:"enabled"`
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
}

type Instance struct {
    config.InternalConfig

    Devices       []string `toml:"devices"`
    IgnoreDevices []string `toml:"ignore_devices"`

    IOLatency IOLatencyCheck `toml:"io_latency"`

    // 运行时状态
    prevCounters map[string]disk.IOCountersStat
    prevTime     time.Time
    deviceTypes  map[string]string // 缓存设备类型探测结果
}

type DiskIOPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

不需要：
- `Concurrency` — 单次 `IOCounters()` 调用返回所有设备，无并发需求
- `GatherTimeout` — `IOCounters()` 读 `/proc/diskstats` 是纯内存操作，微秒级完成
- `Partials` — 配置极简，无模板复用场景

## 事件格式

```
labels:
    check  = "diskio::io_latency"
    target = "sda"

attrs:
    device_type    = "HDD"
    await_ms       = "67.3"
    util_percent   = "82.5"
    read_iops      = "120.5"
    write_iops     = "45.2"
    threshold_desc = "Warning >= 50.0ms, Critical >= 200.0ms (HDD auto)"

current_value: "67.3ms"
```

OK 事件（设备空闲或 await 在阈值内）也携带 attrs，便于巡检查看各设备 IO 状态。

## Description 示例

- 超阈值：`device sda (HDD) await 67.3ms >= warning threshold 50.0ms (util 82.5%, read 120.5 IOPS, write 45.2 IOPS)`
- 一切正常：`device sda (HDD) await 2.1ms, healthy (util 12.3%, read 30.0 IOPS, write 15.2 IOPS)`
- 设备空闲：`device sda (HDD) idle, no IO during sampling interval`

## 与现有能力的关系

| 模块 | 维度 | 持续告警 | 适用场景 |
| --- | --- | :---: | --- |
| `disk` 插件 | 挂载点：空间/inode/可写性 | 是 | 容量规划、磁盘健康 |
| **`diskio` 插件** | **块设备：IO 延迟** | **是** | **性能监控、IO 瓶颈发现** |
| `sysdiag/disklatency.go` | 块设备：IOPS/吞吐/await/%util | 否（按需诊断） | AI 诊断时的详细数据采集 |
| `sysdiag/iotop.go` | 进程：IO 字节排行 | 否（按需诊断） | 定位 IO 大户进程 |

diskio 插件与 sysdiag 诊断工具互补：diskio 持续监控并在异常时触发告警，AI 诊断引擎可进一步调用 sysdiag 工具定位根因（哪个进程在做大量 IO、是读还是写等）。

## 复用 detectDeviceType

`plugins/disk/diagnose.go` 中已有 `detectDeviceType(name string) string` 函数，但它是 disk 包内的私有函数。diskio 插件需要相同的逻辑，有两个选择：

1. 将 `detectDeviceType` 提取到公共包（如 `pkg/diskutil`）
2. 在 diskio 包内重新实现（逻辑简单，约 20 行）

鉴于该函数逻辑极简且与磁盘插件紧耦合，倾向于选择方案 2，避免为一个小函数创建公共包。

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| io_latency.enabled | `true` | 默认启用，核心监控指标 |
| io_latency.warn_ge | 自动（按设备类型） | 无需用户了解硬件细节 |
| io_latency.critical_ge | 自动（按设备类型） | 同上 |
| devices | 空（监控所有真实块设备） | 开箱即用 |
| ignore_devices | 空 | 默认过滤规则已处理虚拟设备和分区 |
| interval | `"30s"` | 与 disk 插件一致，30s 粒度足够反映 IO 趋势 |

## 文件结构

```
plugins/diskio/
    design.md              # 本文档
    diskio.go              # 主逻辑（设备枚举、过滤、delta 计算、阈值判断、事件生成）
    diskio_test.go         # 单元测试

conf.d/p.diskio/
    diskio.toml            # 默认配置
```

## 默认配置文件

```toml
[[instances]]
## ===== 磁盘 IO 延迟监控（仅支持 Linux，其他平台自动跳过）=====
## 自动过滤虚拟设备（loop/ram）和分区，只监控整盘块设备
## 自动探测设备类型（HDD/SSD/NVMe）并应用对应默认阈值

## 设备过滤（可选，默认监控所有真实块设备）
# devices = ["sda", "nvme0n1"]
# ignore_devices = ["sr0"]

## IO 延迟阈值
## 默认按设备类型自动选择：
##   HDD:  warn >= 50ms,  critical >= 200ms
##   SSD:  warn >= 20ms,  critical >= 100ms
##   NVMe: warn >= 10ms,  critical >= 50ms
##   未知: warn >= 100ms, critical >= 500ms
## 手工指定后覆盖自动阈值，对所有设备统一生效
[instances.io_latency]
enabled = true
# warn_ge = 50.0
# critical_ge = 200.0

## 采集间隔（首次采集仅做基线记录，第二次起才产出事件）
# interval = "30s"

## 追加自定义标签
# labels = { env = "production", team = "devops" }

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 3
# disabled = false
# disable_recovery_notification = false

## AI 智能诊断（生效前提：config.toml 中已配置 [ai]）
## IO 延迟告警时 AI 可调用 sysdiag 工具定位 IO 大户进程和读写模式
[instances.diagnose]
enabled = true
# min_severity = "Warning"
# timeout = "120s"
# cooldown = "10m"
```
