# procnum 插件设计

## 概述

监控进程数量，当特定进程的数量低于或超过阈值时产出告警事件。支持多种进程查找方式，覆盖进程存活检查和进程泄漏检测两大场景。

**核心场景**：

1. **守护进程挂了**：关键进程（nginx、sshd、MySQL 等）意外退出
2. **进程泄漏**：Fork bomb 或代码 bug 导致进程数不断增长
3. **PID 文件过期**：进程已死但 PID 文件还在，需验证 PID 对应的进程是否存活

**与 systemd 插件的关系**：systemd 插件检查 unit 状态（active/inactive），procnum 直接计数进程。非 systemd 管理的进程（手动启动、容器内进程等）只能用 procnum 检查。

**参考**：Nagios `check_procs`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 进程数量 | `procnum::process_count` | 搜索条件摘要 | 匹配进程的数量是否在阈值范围内 |

## 进程查找模式

三种模式**互斥**：

### 1. 进程过滤模式（默认）

- `search_exec_name`：按可执行文件名**包含匹配**（取 basename，跨平台统一）
- `search_cmdline`：按完整命令行**包含匹配**
- `search_user`：按进程所属用户**精确匹配**
- 以上三项为 **AND** 关系，全部满足才算匹配
- 全不配 = 统计系统所有进程总数

### 2. PID 文件模式

- `search_pid_file`：从指定文件读取 PID，验证进程是否存活

### 3. Windows 服务模式

- `search_win_service`：按 Windows 服务名查询 PID（仅 Windows）

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    SearchExecName   string // 可执行文件名（包含匹配）
    SearchCmdline    string // 命令行（包含匹配）
    SearchUser       string // 用户名（精确匹配）
    SearchPidFile    string // PID 文件路径
    SearchWinService string // Windows 服务名
    ProcessCount     ProcessCountCheck // 阈值配置
}

type ProcessCountCheck struct {
    WarnLt     *int   // 数量 < 此值 → Warning
    CriticalLt *int   // 数量 < 此值 → Critical
    WarnGt     *int   // 数量 > 此值 → Warning
    CriticalGt *int   // 数量 > 此值 → Critical
    TitleRule  string
}
```

### 阈值方向说明

- **`_lt`（less than）**：常用于守护进程存活检查。`critical_lt = 1` 表示进程数 < 1（即 0 个）时 Critical
- **`_gt`（greater than）**：常用于进程泄漏检测。`warn_gt = 50` 表示进程数 > 50 时 Warning
- 两个方向可同时配置，判断优先级：critical_lt > warn_lt > critical_gt > warn_gt

## Init() 校验

1. 三种搜索模式互斥
2. `search_win_service` 仅 Windows 可用
3. 至少配置一个阈值
4. 所有阈值必须非负
5. `warn_lt >= critical_lt`（warn 范围比 critical 宽）
6. `warn_gt <= critical_gt`（warn 范围比 critical 宽）

## Gather() 逻辑

1. 根据搜索模式查找进程：
   - 进程过滤模式：遍历所有进程，按条件逐个匹配（`gopsutil`）
   - PID 文件模式：读取 PID 文件 → 验证进程存活
   - Windows 服务模式：查询服务 PID
2. 将进程计数与阈值比对，产出事件

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 全部搜索模式 |
| macOS | 部分支持 | 不支持 search_win_service |
| Windows | 完整支持 | 支持 search_win_service |
