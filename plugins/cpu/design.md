# cpu 插件设计

## 概述

监控本机 CPU 使用率和系统负载（Load Average），当指标超过阈值时产出告警事件。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| CPU 使用率 | `cpu::cpu_usage` | 整体 CPU 使用率百分比（两次采集间的平均值） |
| 负载均值 | `cpu::load_average` | 1/5/15 分钟负载均值，按 CPU 核心数归一化后比较阈值 |

- **target label** 统一为 `"cpu"`（本机单一资源）
- **默认 title_rule** 为 `"[check]"`（target 固定，标题中无需显示）

## 数据来源

使用已有依赖 `github.com/shirou/gopsutil/v3`：

### CPU 使用率

`cpu.Percent(0, false)` — 与上次调用做差值计算，不阻塞。

- 返回 `[]float64`，`percpu=false` 时返回单个元素（总体使用率）
- 采集值是两次调用之间的平均 CPU 使用率（采集间隔即平滑窗口）
- 默认 30s 间隔 = 30 秒平均值，比 `top`（~3s）更平滑，短暂尖峰不会触发

### Load Average

`load.Avg()` → `AvgStat{Load1, Load5, Load15}`

### CPU 核心数

`cpu.Counts(true)` → 逻辑核心数（含超线程），用于 Load Average 归一化。

所有调用都是纯内存读取，不会 hang，无需 inFlight 机制和并发控制。

### 跨平台兼容性

| 平台 | cpu_usage | load_average | 数据来源 |
| --- | --- | --- | --- |
| Linux | 完整支持 | 完整支持 | `/proc/stat` + `/proc/loadavg` |
| macOS | 完整支持 | 完整支持 | `sysctl` |
| Windows | 完整支持 | 支持（模拟） | WMI；Load Average 为模拟值，前几秒可能为 0 |

## Load Average 归一化

Load Average 的绝对值与 CPU 核心数相关。4 核机器 load=4 是满载，64 核机器 load=4 很空闲。

归一化为每核负载：`per_core_load = load / logical_cpu_count`

这样 `warn_ge = 3.0` 在任何机器上都表示"每个 CPU 核心排队 3 个任务"，含义一致，实现"一份配置管所有机器"。

## 结构体设计

```go
type CpuUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type LoadAverageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    Period     string  `toml:"period"`     // "1m", "5m"(默认), "15m"
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig
    CpuUsage    CpuUsageCheck    `toml:"cpu_usage"`
    LoadAverage LoadAverageCheck `toml:"load_average"`
    cpuCores    int              // Init() 时缓存
}
```

不需要：`Concurrency`、`GatherTimeout`、`inFlight`、`Targets`。

## _attr_ 标签

### cpu_usage

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_cpu_usage` | `72.3%` | CPU 总体使用率 |
| `_attr_cpu_cores` | `8` | 逻辑核心数 |

### load_average

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_load1` | `3.21` | 1 分钟负载 |
| `_attr_load5` | `2.85` | 5 分钟负载 |
| `_attr_load15` | `2.10` | 15 分钟负载 |
| `_attr_per_core_load` | `0.36` | 归一化后的每核负载（被判断的值） |
| `_attr_cpu_cores` | `8` | 逻辑核心数 |
| `_attr_period` | `5m` | 使用的 Load Average 周期 |

## Init() 校验

1. `warn_ge` < `critical_ge`（两者都配置时），两个维度分别校验
2. 至少一个维度有阈值（防止无效配置静默运行）
3. `period` 必须是 `"1m"`、`"5m"`、`"15m"` 之一（为空时默认 `"5m"`）
4. 获取并缓存 `cpu.Counts(true)` → `cpuCores`；失败则返回错误
5. `cpuCores` 为 0 的防御处理（兜底设为 1）

## Gather() 逻辑

两个维度独立采集，互不影响（局部失败不影响全局）。

### Description 示例

- cpu_usage 告警：`CPU usage 92.3% (30s avg) >= critical threshold 95.0%, cores: 8`
- load_average 告警：`load average (5m) per-core 3.56 >= warning threshold 3.00, raw load: 28.45, cores: 8`
- 恢复：`everything is ok`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| cpu_usage warn/critical | 80/90 | CPU 80%+ 需要关注，90%+ 可能影响业务响应 |
| load_average warn/critical | 3.0/5.0 (per core) | 每核排队 3 个任务开始有性能退化，5 个已严重 |
| load_average period | 5m | 兼顾灵敏度和稳定性，1m 太抖，15m 太滞后 |
| for_duration | 120s | CPU 是最容易波动的指标，持续 2 分钟确认后再告警 |

## 文件结构

```
plugins/cpu/cpu.go         # 实现代码
plugins/cpu/cpu_test.go    # 测试
plugins/cpu/design.md      # 本文档
conf.d/p.cpu/cpu.toml      # 默认配置
```
