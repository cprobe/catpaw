# procfd 插件设计

## 概述

监控指定进程的文件描述符（fd）使用率：当进程已打开的 fd 数接近其 `RLIMIT_NOFILE`（`ulimit -n`）上限时产出告警事件。

进程级 fd 耗尽是最常见的生产事故原因之一。默认 `nofile` 限制在很多系统上仅为 1024（CentOS 7 默认），Nginx、MySQL、Redis 等高连接数服务极易触及。表现为 `too many open files`、新连接被拒、日志写入失败——但这些错误信息分散在不同模块中，不容易第一时间联想到"是 fd 上限太低"。

**定位**：进程级资源上限监控。与 filefd 插件互补——filefd 监控**系统级** fd（`fs.file-max`，所有进程共享），procfd 监控**单个进程** 的 fd（`ulimit -n`，每个进程独立）。实践中进程级 fd 限制被触及的概率**远高于**系统级。

**参考**：Nagios `check_open_files`、Sensu `check-fd`、Prometheus `process-exporter`（导出 per-process `open_fds / max_fds`）、Zabbix `proc.open_files`。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 进程 fd 使用率 | `procfd::fd_usage` | 匹配到的所有进程中，fd 使用率最高的那个超阈值告警 |

- **target label** 为进程匹配条件的描述（与 procnum 一致，如 `"nginx"`、`"java && user:app"`）
- **默认 title_rule** 为 `"[check] [target]"`

## 进程匹配

复用 procnum 的 filter 模型，支持以下条件（AND 组合）：

| 条件 | 字段 | 匹配方式 |
| --- | --- | --- |
| 可执行文件名 | `search_exec_name` | 包含匹配（basename） |
| 命令行 | `search_cmdline` | 包含匹配（完整 cmdline） |
| 用户 | `search_user` | 精确匹配 |

**至少一个 filter 必须配置。** 与 procnum 不同，procfd 不支持"无 filter = 统计全部"——对所有进程都检查 fd 使用率既不现实（开销太大）也无意义（不知道该关注谁）。

也支持 `search_pid_file` 独立模式（从 pid 文件读取 PID）。

### 多进程匹配策略：取最差值

一个 filter 可能匹配到多个进程（如 `search_exec_name = "nginx"` 命中 master + 多个 worker）。

**每轮 Gather 只产出 1 个事件**，取所有匹配进程中 **fd 使用率最高** 的那个作为本轮事件状态。具体 PID 放在 `_attr_pid` 中辅助诊断，不参与 AlertKey 计算。

这个设计解决了一个关键问题：**进程重启后告警能自然恢复**。

| 如果 target 带 PID（错误做法） | 取最差值（正确做法） |
| --- | --- |
| `target = nginx[pid=1234]` | `target = nginx` |
| 进程重启 → PID 变为 5678 | 进程重启 → PID 变为 5678 |
| 旧事件 `nginx[pid=1234]` 永不恢复 | 新进程 fd 正常 → 使用率下降 → 自然恢复 |
| 新事件 `nginx[pid=5678]` 独立产生 | 同一个事件，状态从 Critical → Ok |

**语义**：本事件回答的问题是"我的 nginx 进程们的 fd 健康吗？"而非"pid 1234 健康吗？"

### 并发控制

匹配到的进程数可能较多（如 50 个 worker），读取 limits/fd 时需要并发控制。默认 `concurrency = 10`。

## 数据来源

### 上限：`/proc/{pid}/limits`

读取 `/proc/{pid}/limits`，解析 `Max open files` 行：

```
Limit                     Soft Limit           Hard Limit           Units
Max open files            1024                 1048576              files
```

取 **Soft Limit** 作为上限（进程实际受到的约束是 soft limit）。

- `unlimited` 视为无限制 → 跳过该进程（不计入最差值计算）
- 解析失败 → 该进程视为错误，若所有进程都失败则产出 Critical 事件

### 当前值：`/proc/{pid}/fd`

统计 `/proc/{pid}/fd/` 目录下的条目数。使用 `Readdirnames` 只取文件名，不 stat 每个 fd 链接。

**开销考量**（原则 8）：
- 普通进程：几十到几百个 fd，微秒级完成
- 高连接数进程（如 Nginx、Envoy）：可能 10 万+ fd，`Readdirnames` 仍然高效（仅 `getdents` syscall，不读取 fd 指向的实际文件）
- 极端场景（百万 fd）：单次 `Readdirnames(-1)` 可能分配较大的字符串切片。可考虑分批读取（`Readdirnames(1024)` 循环），但初版不做——百万 fd 本身就是需要告警的异常

### 进程退出的竞态

进程在 Gather 期间退出是常态（原则 13）。`/proc/{pid}/limits` 或 `/proc/{pid}/fd` 读取时返回 `ENOENT` / `ESRCH` 必须静默跳过该进程，不报错。

### 无新增依赖

进程匹配复用 gopsutil（已是 procnum 的依赖），fd 计数和 limits 解析使用标准库 `os.ReadFile`、`os.Open`、`Readdirnames`。

## 阈值设计

### 使用率百分比

`fd_usage = open_fds / nofile_soft_limit * 100`

| 阈值 | 含义 |
| --- | --- |
| `warn_ge = 80.0` | 使用率 ≥ 80% 时 Warning |
| `critical_ge = 90.0` | 使用率 ≥ 90% 时 Critical |

### 为什么默认 80/90

与 filefd 一致。fd 增长通常是渐进的（连接累积、泄漏），80% 留出处置窗口。90% 需立即处理——从 90% 到 100% 在突发连接场景下可能很快。

### 处置建议（供 Description 参考）

用户收到告警后典型处置：

1. 确认当前 fd 用量：`ls /proc/<pid>/fd | wc -l`
2. 确认当前限制：`cat /proc/<pid>/limits | grep "Max open files"`
3. 查看 fd 指向什么：`ls -la /proc/<pid>/fd | head -30`（socket? 文件? pipe?）
4. 临时提升：`prlimit --pid <pid> --nofile=65535:65535`
5. 持久化：
   - systemd 服务：`LimitNOFILE=65535` in unit file
   - 全局：`/etc/security/limits.conf` 或 `/etc/security/limits.d/`
6. 排查是否是 fd 泄漏（fd 数只增不减）

## 结构体设计

```go
type FdUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    SearchExecName string `toml:"search_exec_name"`
    SearchCmdline  string `toml:"search_cmdline"`
    SearchUser     string `toml:"search_user"`
    SearchPidFile  string `toml:"search_pid_file"`

    Concurrency int `toml:"concurrency"`

    FdUsage FdUsageCheck `toml:"fd_usage"`

    searchLabel string
}

type ProcfdPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

需要：
- `Concurrency` — 多进程匹配时的并发控制

不需要：
- `Timeout` — 读取 `/proc/{pid}/*` 是本地操作，不会 hang（procfs 不涉及磁盘 I/O 或网络）
- `inFlight` — 同理，procfs 读取不会 hang，无需防重入（原则 9：适用于可能 hang 的场景）

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_pid` | `1234` | fd 使用率最高的进程 PID |
| `_attr_open_fds` | `892` | 该进程当前已打开 fd 数 |
| `_attr_nofile_soft` | `1024` | 该进程 nofile soft limit |
| `_attr_nofile_hard` | `1048576` | 该进程 nofile hard limit（辅助诊断） |
| `_attr_usage_percent` | `87.1%` | 格式化的使用率 |
| `_attr_exec_name` | `nginx` | 进程可执行文件名（若可获取） |
| `_attr_matched_count` | `5` | 匹配到的进程总数（含 unlimited 的） |
| `_attr_checked_count` | `4` | 实际参与检查的进程数（排除 unlimited 的） |

Ok 事件也携带完整 `_attr_`，便于巡检时确认各进程 fd 水位。

## Init() 校验

```
Init():
    1. if runtime.GOOS != "linux":
           return error: "procfd plugin only supports linux (current: <os>)"

    2. if no filter configured (all search_* empty):
           return error: "at least one search condition must be configured"

    3. if search_pid_file set and any other search_* also set:
           return error: "search_pid_file is mutually exclusive with other filters"

    4. if concurrency == 0: concurrency = 10

    5. fd_usage:
       if warn_ge < 0 || warn_ge > 100 || critical_ge < 0 || critical_ge > 100:
           return error
       if warn_ge > 0 && critical_ge > 0 && warn_ge >= critical_ge:
           return error
       if warn_ge == 0 && critical_ge == 0:
           return error: "fd_usage thresholds must be configured"
```

与 procnum 不同，procfd **必须配置 fd_usage 阈值**——没有阈值的 procfd 毫无意义。

## Gather() 逻辑

```
Gather(q):
    // 1. 查找匹配的进程
    pids, err = findMatchingProcesses()
    if err:
        push Critical target=searchLabel "search error: ..."
        return

    if len(pids) == 0:
        // 匹配不到进程不是 procfd 的职责（那是 procnum 的事）
        // 静默跳过，不产出事件
        logger.Debug("no matching processes found")
        return

    // 2. 并发读取每个进程的 fd 使用率
    type fdResult struct {
        pid         int
        openFds     int
        softLimit   uint64
        hardLimit   uint64
        usagePercent float64
        execName    string
        err         error
    }

    results = concurrentCollect(pids, concurrency)

    // 3. 从结果中找最差值
    var worst *fdResult
    matchedCount = len(pids)
    checkedCount = 0
    errorCount = 0

    for each r in results:
        if r.err != nil:
            errorCount++
            continue
        if r.softLimit == 0:  // unlimited，跳过
            continue
        checkedCount++
        if worst == nil || r.usagePercent > worst.usagePercent:
            worst = r

    // 4. 所有进程都读取失败 → Critical
    if checkedCount == 0 && errorCount > 0:
        push Critical "failed to check fd usage: all %d matched processes returned errors"
        return

    // 5. 所有进程都是 unlimited → 不产出事件
    if checkedCount == 0:
        logger.Debug("all matched processes have unlimited nofile")
        return

    // 6. 基于最差值构建事件
    event = buildEvent(worst, matchedCount, checkedCount)

    status = EvaluateGeThreshold(worst.usagePercent, warn_ge, critical_ge)
    event.SetEventStatus(status)

    switch status:
        Critical: "fd usage 94.2% (965/1024) for pid 1234, above critical threshold 90%"
        Warning:  "fd usage 82.0% (840/1024) for pid 1234, above warning threshold 80%"
        Ok:       "fd usage 12.3% (126/1024), everything is ok"

    q.PushFront(event)
```

### readNofileLimit(pid)

```
readNofileLimit(pid) (soft uint64, hard uint64, err error):
    data, err = os.ReadFile(fmt.Sprintf("/proc/%d/limits", pid))
    if err: return ...

    for each line in data:
        if !strings.HasPrefix(line, "Max open files"):
            continue

        // "Max open files            1024                 1048576              files"
        // 固定格式：前 26 字符是标签名，后续是 soft / hard / units
        fields = strings.Fields(line[26:])
        if len(fields) < 2: return error

        if fields[0] == "unlimited":
            soft = 0  // 0 表示 unlimited
        else:
            soft = ParseUint(fields[0])

        if fields[1] == "unlimited":
            hard = 0
        else:
            hard = ParseUint(fields[1])

        return soft, hard, nil

    return 0, 0, fmt.Errorf("Max open files not found in /proc/%d/limits", pid)
```

### countOpenFds(pid)

```
countOpenFds(pid) (int, error):
    d, err = os.Open(fmt.Sprintf("/proc/%d/fd", pid))
    if err: return 0, err
    defer d.Close()

    names, err = d.Readdirnames(-1)
    if err: return 0, err

    return len(names), nil
```

### 关键行为

1. **每轮 Gather 只产出 0 或 1 个事件**——取所有匹配进程中 fd 使用率最高的，避免事件爆炸。
2. **target 不含 PID**——确保进程重启后告警能自然恢复。PID 放在 `_attr_pid` 中辅助诊断。
3. **匹配不到进程时静默跳过**——进程不存在不是 procfd 的职责，那是 procnum 的事。两个插件互补使用。
4. **所有进程 unlimited 时静默跳过**——无使用率概念，不浪费噪音。
5. **所有进程读取失败时产出 Critical**——如权限不足等（原则 7）。部分失败时忽略失败的，取成功的最差值。
6. **进程退出静默跳过**——`ENOENT` / `ESRCH` 不报错（原则 13）。
7. **并发控制**——多进程场景下不会一次性创建过多 goroutine（原则 8）。
8. **goroutine 有 panic recovery**（原则 2）。
9. **Description 中的 PID 仅作为信息**——告警时包含最差进程的 PID 方便排查，Ok 时省略 PID 减少噪音。

## Description 示例

- 使用率正常：`fd usage 12.3% (126/1024), everything is ok`
- 使用率偏高：`fd usage 82.0% (840/1024) for pid 1234, above warning threshold 80%`
- 即将耗尽：`fd usage 94.2% (965/1024) for pid 1234, above critical threshold 90%`
- 全部读取失败：`failed to check fd usage: all 5 matched processes returned errors`
- 搜索失败：`search error: <error>`

## 默认配置建议

| 决策 | 值 | 理由 |
| --- | --- | --- |
| warn_ge | `80.0` | 与 filefd 一致，渐进式增长留处置窗口 |
| critical_ge | `90.0` | 90% 以上需立即处理 |
| concurrency | `10` | 平衡检查速度和系统负载 |
| interval | `"30s"` | fd 泄漏是渐进的，30s 粒度足够 |
| for_duration | `0` | 超阈即需关注 |
| repeat_interval | `"5m"` | 持续高位时定期提醒 |
| repeat_number | `0` | 不限制 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 某个进程 fd 即将耗尽 | **procfd** | 按 nofile soft limit 百分比告警 |
| 系统级 fd 即将耗尽 | filefd | 所有进程共享的 `file-max` 使用率 |
| 进程是否存活 / 数量 | procnum | 进程存在性检查，与 procfd 互补 |
| 连接跟踪表耗尽 | conntrack | 另一个系统级资源 |

**推荐组合**：对关键服务同时配置 procnum（确保存活）和 procfd（确保不会因 fd 耗尽而拒绝服务）。

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/proc/{pid}/limits` 和 `/proc/{pid}/fd` |
| macOS | 不支持 | Init 返回错误。macOS 有 `lsof` 但无 `/proc` |
| Windows | 不支持 | Init 返回错误。Windows Handle 机制不同 |

### 容器环境

在容器内读取 `/proc/{pid}/limits` 和 `/proc/{pid}/fd` 获取的是**容器内进程**的值（PID namespace 隔离后看到容器内 PID）。若 catpaw 和目标进程在同一容器内运行，监控的是该容器内的进程。若 catpaw 运行在宿主机上，看到的是宿主机上所有进程。

## 文件结构

```
plugins/procfd/
    design.md        # 本文档
    procfd.go        # 主逻辑（仅 Linux）
    procfd_test.go   # 测试（Init 校验、limits 解析、fd 计数逻辑）

conf.d/p.procfd/
    procfd.toml      # 默认配置
```

通过 `runtime.GOOS` 在 Init 中限制为 Linux，无需 build tags。

## 默认配置文件示例

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 监控指定进程的文件描述符（fd）使用率
## 进程级 nofile 限制（默认 1024）被触及的频率远高于系统级 file-max
## 表现为 "too many open files"、新连接被拒、日志写不了
## 与 filefd（系统级）互补，本插件关注单个进程的 fd 健康

## 进程匹配条件（AND 组合，至少配一项）
search_exec_name = "nginx"
# search_cmdline = ""
# search_user = ""
# search_pid_file = ""

## 匹配到多个进程时的并发数，默认 10
# concurrency = 10

interval = "30s"

## fd 使用率阈值（已打开 fd 数 / nofile soft limit 百分比）
## 匹配到多个进程时，取 fd 使用率最高的进程进行评估
[instances.fd_usage]
warn_ge = 80.0
critical_ge = 90.0
# title_rule = "[check] [target]"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 0
# disabled = false
# disable_recovery_notification = false
```
