# zombie 插件设计

## 概述

监控系统中僵尸进程（状态为 Z）的数量。僵尸进程是已退出但父进程尚未调用 `wait()` 回收的进程，大量积累会耗尽 PID 资源。

**核心场景**：

1. **父进程 bug**：父进程未正确处理 `SIGCHLD` 或未调用 `wait()`，导致子进程变僵尸
2. **PID 耗尽预警**：默认 PID 上限 32768，僵尸进程不释放 PID，积累后新进程无法创建
3. **容器环境**：容器内 PID 1 进程（如 bash 脚本）常常不处理子进程回收

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 僵尸进程数 | `zombie::count` | system | 系统中状态为 Z 的进程总数 |

- **target 固定为 `"system"`**——系统级全局检查
- 使用 `_gt`（大于）阈值模式：`warn_gt = 0` 表示"只要有僵尸就告警"

## 数据来源

通过 `gopsutil/v3/process` 遍历 `/proc/*/stat`，统计状态为 `"Z"` 的进程数量。

底层实际读取的是 `/proc/<pid>/status` 或 `/proc/<pid>/stat` 中的进程状态字段。

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    WarnGt     *int   // 数量 > 此值 → Warning
    CriticalGt *int   // 数量 > 此值 → Critical
    TitleRule  string
}
```

## Init() 校验

1. 至少配置一个阈值（`warn_gt` 或 `critical_gt`）
2. 阈值必须非负
3. `warn_gt <= critical_gt`

## Gather() 逻辑

1. 遍历所有进程，统计状态为 `"Z"` 的进程数量
2. 比对阈值，产出事件

### 为什么独立成插件（而非放入 procnum）

- **职责单一**：procnum 按条件搜索特定进程并计数，zombie 统计系统级的僵尸进程总数，两者关注点不同
- **性能考虑**：zombie 只需读取进程状态（O(N) 轻量），procnum 可能需要读取 cmdline、username 等更多信息
- **配置简洁**：zombie 只需两个阈值，嵌入 procnum 会增加配置复杂度

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 通过 /proc 获取进程状态 |
| macOS | 部分支持 | gopsutil 支持但僵尸进程场景在 macOS 上较少 |
| Windows | 不适用 | Windows 无僵尸进程概念 |
