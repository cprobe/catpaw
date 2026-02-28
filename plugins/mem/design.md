# mem 插件设计

## 概述

监控本机物理内存和 Swap 使用率，当使用率超过阈值时产出告警事件。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 内存使用率 | `mem::memory_usage` | 物理内存使用率，基于 `(Total - Available) / Total` |
| Swap 使用率 | `mem::swap_usage` | Swap 使用率，Swap 总量为 0 时自动跳过 |

- **target label** 统一为 `"memory"`（本机唯一资源，无需区分多 target）
- **默认 title_rule** 为 `"[check]"`（target 固定，标题中无需再显示）

## 数据来源

使用 `github.com/shirou/gopsutil/v3/mem`（已是项目依赖）：

- `mem.VirtualMemory()` → `Total`, `Available`, `Used`, `UsedPercent`, `Buffers`, `Cached`
- `mem.SwapMemory()` → `Total`, `Used`, `Free`, `UsedPercent`

两者都是纯内存读取（Linux 下读 `/proc/meminfo`），不会 hang，无需 inFlight 机制和并发控制。

### 跨平台兼容性

| 平台 | memory_usage | swap_usage | 数据来源 |
| --- | --- | --- | --- |
| Linux | 完整支持 | 完整支持 | `/proc/meminfo` |
| macOS | 完整支持 | 完整支持 | `sysctl` |
| Windows | 完整支持 | 完整支持 | WMI |

`Buffers` 和 `Cached` 在非 Linux 平台上为 0，`_attr_` 标签会显示 `0 B`，不影响使用。

## 结构体设计

```go
type MemoryUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type SwapUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig
    MemoryUsage MemoryUsageCheck `toml:"memory_usage"`
    SwapUsage   SwapUsageCheck   `toml:"swap_usage"`
}
```

不需要：`Concurrency`、`GatherTimeout`、`inFlight`、`Targets` — 纯内存操作，本机单一资源。

## _attr_ 标签

### memory_usage

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_total` | `15.6 GiB` | 物理内存总量 |
| `_attr_used` | `12.3 GiB` | 已使用内存 |
| `_attr_available` | `3.3 GiB` | 可用内存（含可回收缓存） |
| `_attr_used_percent` | `78.8%` | 使用率 |
| `_attr_buffers` | `256.0 MiB` | Buffers（Linux） |
| `_attr_cached` | `2.1 GiB` | Cached（Linux） |

### swap_usage

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_swap_total` | `4.0 GiB` | Swap 总量 |
| `_attr_swap_used` | `1.2 GiB` | Swap 已使用 |
| `_attr_swap_free` | `2.8 GiB` | Swap 可用 |
| `_attr_swap_used_percent` | `30.0%` | Swap 使用率 |

## Init() 校验

1. `warn_ge` < `critical_ge`（两者都配置时）
2. 至少一个维度有阈值（防止无效配置静默运行）

## Gather() 逻辑

1. 两个维度独立采集，互不影响（局部失败不影响全局）
2. gopsutil 调用失败时产出 Critical 事件（自身故障可感知）
3. Swap 总量为 0（未启用 Swap）时静默跳过（宁可漏报不误报）
4. Description 包含 total 和 available/free 上下文，运维人员收到告警无需再登录机器

## 默认配置关键决策

- `memory_usage`: warn_ge=85.0, critical_ge=90.0
- `swap_usage`: warn_ge=80.0, critical_ge=95.0（Swap 大量使用意味着严重性能退化，warn 阈值比内存更低）
- `for_duration = "60s"` — 内存有波动性，持续确认后再告警，避免瞬间峰值误报（与 disk 的 for_duration=0 不同）

## 文件结构

```
plugins/mem/mem.go         # 实现代码
plugins/mem/mem_test.go    # 测试
plugins/mem/design.md      # 本文档
conf.d/p.mem/mem.toml      # 默认配置
```
