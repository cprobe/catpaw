# docker 插件设计

## 概述

监控 Docker 容器的运行状态和资源使用，覆盖从"容器是否在跑"到"容器是否健康、是否在 crashloop（滑动窗口频繁重启检测）、资源是否快爆"的完整链路。

**定位**：补充 procnum/systemd（只看进程/服务）和 uptime（只看整机重启）的不足，覆盖容器化部署场景。在 Kubernetes 之外，大量单机 Docker Compose 部署缺乏容器级别的健康监控——这正是 docker 插件的核心价值。

**参考**：Nagios `check_docker`（timdaman/check_docker）、Sensu `check-docker-container`、Prometheus cAdvisor、Datadog Docker Integration。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 容器运行状态 | `docker::container_running` | 期望的容器未运行或不存在 |
| 频繁重启检测 | `docker::restart_detected` | 滑动窗口内重启次数超阈值，检测 crashloop |
| 健康检查状态 | `docker::health_status` | Docker HEALTHCHECK 状态为 unhealthy |
| CPU 使用率 | `docker::cpu_usage` | 容器 CPU 使用率超阈值 |
| 内存使用率 | `docker::memory_usage` | 容器内存使用率超阈值 |

- **target label** 为容器名称（去除 Docker API 返回的前导 `/`）
- **默认 title_rule** 为 `"[check] [target]"`

### 维度启用规则

| 维度 | 启用条件 | 说明 |
| --- | --- | --- |
| `container_running` | 始终启用 | 对显式名称(非 glob)的容器，未找到或未运行即告警 |
| `restart_detected` | `warn_ge > 0 \|\| critical_ge > 0` | 按阈值开关，对所有状态的容器检查 |
| `health_status` | 始终启用 | 仅对定义了 HEALTHCHECK 的容器生效，无 HEALTHCHECK 则跳过 |
| `cpu_usage` | `warn_ge > 0 \|\| critical_ge > 0` | 按阈值开关 |
| `memory_usage` | `warn_ge > 0 \|\| critical_ge > 0` | 按阈值开关 |

`container_running` 和 `health_status` 是核心健康信号，始终开启；`restart_detected` 通过滑动窗口检测短时间内频繁重启（真正的 crashloop 信号）；资源类维度按阈值开关，避免不必要的 stats API 调用。

## 数据来源

### Docker Engine API

通过 Unix socket（Linux/macOS）或 TCP（Windows/远程）发送 HTTP 请求，无需引入 Docker SDK，纯标准库实现。

所需 API 端点：

| 端点 | 用途 | 调用频率 |
| --- | --- | --- |
| `GET /containers/json?all=true` | 列出所有容器（含已停止） | 每次 Gather 1 次 |
| `GET /containers/{id}/json` | 容器详情（RestartCount、Health、OOMKilled、ExitCode） | 每个匹配容器 1 次（始终调用） |
| `GET /containers/{id}/stats?stream=false` | 一次性资源统计（CPU、内存） | 每个匹配容器 1 次（当 cpu_usage 或 memory_usage 需要时） |

API 调用策略：
1. **List 调用**：每次 Gather 仅 1 次，获取所有容器基本状态
2. **Inspect 调用**：每个匹配容器始终调用——本地 Unix socket 通信极快（< 10ms），inspect 提供 RestartCount、Health、OOMKilled、ExitCode 等关键信息，跳过 inspect 节省的时间可忽略不计，但丢失的诊断信息不可接受
3. **Stats 调用**：**仅在 cpu_usage 或 memory_usage 启用时**发起——stats 是相对昂贵的调用（~50-100ms），且仅对 running 容器有意义，按需调用收益显著

### 连接方式

根据 `socket` 配置值自动选择连接方式：

| socket 值 | 连接方式 | 适用场景 |
| --- | --- | --- |
| `/var/run/docker.sock`（默认） | Unix socket | Linux / macOS |
| `http://localhost:2375` | 标准 HTTP | Windows TCP 模式 / 远程 Docker |
| `https://...` | HTTPS | TLS 加密远程连接 |

**Unix socket 模式**（Linux / macOS）：

```go
transport := &http.Transport{
    DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
        return net.DialTimeout("unix", socketPath, timeout)
    },
}
client := &http.Client{Transport: transport, Timeout: timeout}
// baseURL = "http://localhost"（占位符，实际通过 Unix socket 通信）
resp, err := client.Get("http://localhost/v1.43/containers/json?all=true")
```

**TCP 模式**（Windows / 远程）：

```go
client := &http.Client{Timeout: timeout}
// baseURL = socket 配置值本身（如 "http://localhost:2375"）
resp, err := client.Get("http://localhost:2375/v1.43/containers/json?all=true")
```

判断逻辑：`socket` 以 `http://` 或 `https://` 开头时使用 TCP 模式，否则使用 Unix socket 模式。

### API 版本协商

Docker Engine API 有严格的版本控制：**请求的 API 版本高于 daemon 支持的版本时会被拒绝**（返回 `client version X is too new. Maximum supported API version is Y`）。

**协商策略**：

1. `api_version` 为空（默认）→ 自动协商：
   - 首次 Gather 时调用 `GET /version`（无需版本前缀，所有 Docker 版本均支持）
   - 从响应中提取 `ApiVersion` 字段（daemon 支持的最高版本）
   - 缓存结果，后续 Gather 不再重复协商
2. `api_version` 非空 → 使用用户指定的版本（跳过协商）

```go
// 首次 Gather 时自动协商（非 Init，因为 Docker 可能在 Init 后才启动）
if ins.apiVersion == "" {
    resp, err = client.Get(baseURL + "/version")
    if err == nil:
        ins.apiVersion = resp.ApiVersion   // 如 "1.43"
    else:
        ins.apiVersion = "1.25"            // 协商失败，降级到最低支持版本
}
```

**为什么不在 Init 时协商**：与 socket 不验证存在性的逻辑一致——Docker 可能在 catpaw 启动后才启动。延迟到首次 Gather 时协商，确保 Docker 已就绪。

**为什么不硬编码 v1.43**：硬编码意味着 Docker 23.0 以下（API < v1.43）的用户必须手动配置 `api_version`，否则 daemon 直接拒绝请求。自动协商让老版本 Docker 零配置可用。

### 平台默认 socket

```go
if ins.Socket == "" {
    if runtime.GOOS == "windows" {
        ins.Socket = "http://localhost:2375"
    } else {
        ins.Socket = "/var/run/docker.sock"
    }
}
```

Windows 默认使用 TCP 模式（需用户在 Docker Desktop 中开启 "Expose daemon on tcp://localhost:2375"），Linux / macOS 默认使用 Unix socket。

### 无新增依赖

纯标准库：`net/http`、`net`、`encoding/json`、`runtime`。

## 目标匹配

### targets 配置

`targets` 为容器名称列表，支持 glob 模式：

```toml
targets = ["nginx", "redis", "app-*"]
```

使用 `filter.HasMeta()` 区分显式名称和 glob 模式：

| 类型 | 示例 | container_running 行为 | 匹配范围 |
| --- | --- | --- | --- |
| 显式名称 | `"nginx"` | 未找到 → Critical（"容器必须存在"语义） | 所有容器（含已停止） |
| Glob 模式 | `"app-*"` | 匹配 0 个容器 → 不产出事件（"发现式"语义） | **仅活跃容器** |

关键区别：glob 模式**只发现活跃容器**（`state` 为 `running`、`paused`、`restarting`）——已停止（`exited`、`dead`）、未启动（`created`）、删除中（`removing`）的容器不会被 glob 匹配到，避免大量无用告警。显式名称则在所有容器中查找（含已停止），以准确报告"停止"还是"不存在"。

这与 cert 插件的 file_targets glob 处理逻辑一致。

### 容器名称匹配

Docker API 返回的容器名称带前导 `/`（如 `/nginx`），匹配时自动去除。Docker Compose 容器名称格式为 `project-service-1`，用户可用 `"project-*"` 匹配同一项目的所有容器。

### 安全限制

`max_containers`（默认 100）：限制单次 Gather 处理的最大容器数。防止 `targets = ["*"]` 在容器数量极多的主机上产生过多 API 调用。超出时产出 Warning 事件并处理前 N 个。

## 阈值设计

### container_running

无阈值——二元判断：容器在运行则 Ok，否则告警。

| 容器状态 | 事件 |
| --- | --- |
| 未找到（仅显式名称） | Critical: `container "nginx" not found` |
| exited / dead | Critical: `container "nginx" is not running (state: exited, exit code: 137)` |
| created | Critical: `container "nginx" is not running (state: created)` |
| removing | Critical: `container "nginx" is not running (state: removing)` |
| paused | Warning: `container "nginx" is paused` |
| restarting | Warning: `container "nginx" is restarting` |
| running | Ok: `container "nginx" is running` |

### restart_detected

**滑动窗口**内重启次数超阈值则告警。核心思想：**单次重启可能是预期内的（部署、配置变更），短时间多次重启才是异常信号（crashloop）**。

| 配置项 | 默认值 | 含义 |
| --- | --- | --- |
| `window` | `"10m"` | 检测窗口时长 |
| `warn_ge` | `3` | 窗口内重启 >= 3 次 → Warning（可能有问题） |
| `critical_ge` | `5` | 窗口内重启 >= 5 次 → Critical（确认 crashloop） |

**工作原理**：

1. 每次 Gather 从 Inspect API 获取容器的 `RestartCount`
2. 与**上次记录的 RestartCount** 比较，差值为本次采集周期内的重启次数
3. 将重启事件（次数 + 时间戳）追加到滑动窗口
4. 清除窗口外的过期事件
5. 汇总窗口内的总重启次数，与阈值比较

```go
type restartRecord struct {
    count     int       // 本次采集周期内的重启增量
    timestamp time.Time // 记录时间
}

// 每个容器维护独立的状态（Instance 级别，跨 Gather 持久）
type containerRestartState struct {
    lastRestartCount int               // 上次 Gather 时的 RestartCount
    records          []restartRecord   // 滑动窗口内的重启记录
}
```

**关键设计决策**：

- **对所有状态的容器检查**——exited 容器的 RestartCount 同样有诊断价值（crashloop 后彻底挂了）
- **Docker RestartCount 归零处理**：容器重新创建时 RestartCount 归零。若当前 RestartCount < lastRestartCount，说明容器被重新创建，将本次视为一次全新开始（重置 lastRestartCount = 当前值，不计入窗口）
- **首次见到的容器**：lastRestartCount 不存在时，记录当前值作为基线，不产生重启增量（避免首次 Gather 就触发告警）
- **自愈特性**：容器稳定运行后，窗口内无新增重启事件，过期记录被清除，告警自动恢复为 Ok
- **零额外 API 成本**：`RestartCount` 来自 Inspect API 响应，已对每个匹配容器调用

**为什么优于 restart_count + container_uptime 双维度**：

| 方案 | 场景：10 分钟重启 3 次 | 场景：正常部署重启 1 次 | 场景：容器重新创建 |
| --- | --- | --- | --- |
| restart_count（累积） | 需等到 RestartCount 累积到阈值 | 可能误报（RestartCount 不清零） | RestartCount 归零后盲区 |
| container_uptime（实时） | 能检测，但无法区分首次部署 | 必定误报（uptime < 阈值） | 能检测 |
| **restart_detected（窗口）** | 精确检测：窗口内 3 次 → 告警 | 不误报：窗口内仅 1 次 < 阈值 | 重新开始计数，无盲区 |

单维度统一了 crashloop 检测逻辑，**消除了 container_uptime 在正常部署时的误报问题**，同时保留了对频繁重启的敏感度。

### health_status

无阈值——基于 Docker HEALTHCHECK 状态：

| Health 状态 | 事件 |
| --- | --- |
| healthy | Ok |
| starting | Ok（容器刚启动，health check 尚未完成） |
| unhealthy | Critical |
| 无 HEALTHCHECK | 跳过，不产出事件 |

### cpu_usage

标准 `warn_ge` / `critical_ge` 阈值，作用于 **CPU 使用率百分比**：

| 阈值 | 默认值 | 含义 |
| --- | --- | --- |
| `warn_ge` | `80` | CPU% >= 80 → Warning |
| `critical_ge` | `95` | CPU% >= 95 → Critical |

**CPU% 计算方式**：

```
cpuDelta = cpu_stats.cpu_usage.total_usage - precpu_stats.cpu_usage.total_usage
systemDelta = cpu_stats.system_cpu_usage - precpu_stats.system_cpu_usage

// online_cpus（API v1.27+ 可用；v1.25-v1.26 需 fallback）
onlineCPUs = cpu_stats.online_cpus
if onlineCPUs == 0:
    onlineCPUs = len(cpu_stats.cpu_usage.percpu_usage)

// 除零保护
if systemDelta == 0 || onlineCPUs == 0:
    跳过本次 cpu_usage 检查，等待下次采样

// 宿主机维度的 CPU 使用（如 docker stats 所示，150% = 1.5 核）
cpuCores = (cpuDelta / systemDelta) * onlineCPUs

// 归一化到 0-100%：相对于分配的 CPU 限制（有限制时）或宿主机总 CPU（无限制时）
if 有 CPU 限制:
    allocatedCPUs = NanoCPUs / 1e9  (或 CpuQuota / CpuPeriod)
    cpuPercent = cpuCores / allocatedCPUs * 100
else:
    cpuPercent = cpuCores / onlineCPUs * 100
```

归一化后 0-100% 的范围使阈值设置直观且跨容器通用。

### memory_usage

标准 `warn_ge` / `critical_ge` 阈值，作用于 **内存使用率百分比**：

| 阈值 | 默认值 | 含义 |
| --- | --- | --- |
| `warn_ge` | `80` | 内存% >= 80 → Warning |
| `critical_ge` | `95` | 内存% >= 95 → Critical |

**内存% 计算方式**：

```
// 文件缓存的获取方式因 Docker 版本和 cgroup 版本而异：
fileCache = getFileCache(memory_stats)

func getFileCache(memStats):
    // 优先级 1：cgroup v2 — inactive_file
    if memStats.stats["inactive_file"] > 0:
        return memStats.stats["inactive_file"]
    // 优先级 2：cgroup v1（Docker >= 19.04）— total_inactive_file
    if memStats.stats["total_inactive_file"] > 0:
        return memStats.stats["total_inactive_file"]
    // 优先级 3：cgroup v1（Docker < 19.04）— cache
    if memStats.stats["cache"] > 0:
        return memStats.stats["cache"]
    // Windows 或无缓存数据
    return 0

actualUsage = memory_stats.usage - fileCache
memoryPercent = actualUsage / memory_stats.limit * 100
```

**limit 健全性检查**：Docker 在未设内存限制时，不同版本行为不同——多数返回宿主机总内存，部分返回 `math.MaxInt64`（约 9.2 EB），后者会导致 memoryPercent 趋近于 0%，告警永远不会触发。`memory_stats.limit == 0` 或 `> 1 PiB` 时视为无限制，跳过 memory_usage 检查，不产出事件。

## 结构体设计

```go
type ContainerRunningCheck struct {
    TitleRule string `toml:"title_rule"`
}

type RestartDetectedCheck struct {
    Window     config.Duration `toml:"window"`
    WarnGe     int             `toml:"warn_ge"`
    CriticalGe int             `toml:"critical_ge"`
    TitleRule   string          `toml:"title_rule"`
}

type HealthStatusCheck struct {
    TitleRule string `toml:"title_rule"`
}

type CpuUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule   string  `toml:"title_rule"`
}

type MemoryUsageCheck struct {
    WarnGe     float64 `toml:"warn_ge"`
    CriticalGe float64 `toml:"critical_ge"`
    TitleRule   string  `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    Socket        string          `toml:"socket"`
    APIVersion    string          `toml:"api_version"`
    Timeout       config.Duration `toml:"timeout"`
    Targets       []string        `toml:"targets"`
    Concurrency   int             `toml:"concurrency"`
    MaxContainers int             `toml:"max_containers"`

    ContainerRunning ContainerRunningCheck `toml:"container_running"`
    RestartDetected  RestartDetectedCheck  `toml:"restart_detected"`
    HealthStatus     HealthStatusCheck     `toml:"health_status"`
    CpuUsage         CpuUsageCheck         `toml:"cpu_usage"`
    MemoryUsage      MemoryUsageCheck      `toml:"memory_usage"`

    // runtime
    httpClient     *http.Client
    baseURL        string
    apiVersion     string                          // 协商后的实际 API 版本（如 "1.43"）
    explicitNames  map[string]struct{}
    restartStates  map[string]*containerRestartState // key: 容器名称
}

type DockerPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

## _attr_ 标签

### 各维度通用

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_container_id` | `a1b2c3d4e5f6` | 容器短 ID（12 字符） |
| `_attr_container_image` | `nginx:1.25` | 镜像名:标签 |

### container_running 维度

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_state` | `exited` | 容器状态 |
| `_attr_status` | `Exited (137) 5 minutes ago` | Docker 人类可读状态 |

### restart_detected 维度

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_restarts_in_window` | `3` | 窗口内重启次数 |
| `_attr_window` | `10m` | 检测窗口时长（humanDuration 格式） |
| `_attr_restart_count` | `15` | Docker 管理的累计 RestartCount |
| `_attr_oom_killed` | `true` | 容器最近一次退出是否被 OOM Kill（来自 Docker Inspect API 的最新状态，可能早于滑动窗口内的重启事件） |
| `_attr_exit_code` | `137` | 容器最近一次退出码（来自 Docker Inspect API 的最新状态，可能早于滑动窗口内的重启事件） |
| `_attr_started_at` | `2026-02-28 10:30:00 CST` | 容器启动时间（本地时区） |
| `_attr_finished_at` | `2026-02-28 10:29:55 CST` | 上次停止时间（本地时区） |

### health_status 维度

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_health_status` | `unhealthy` | 健康检查状态 |
| `_attr_health_failing_streak` | `5` | 连续失败次数 |
| `_attr_health_last_output` | `connection refused` | 最近一次检查输出（截断至 200 字符） |

### cpu_usage 维度

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_cpu_percent` | `85.3%` | CPU 使用率 |
| `_attr_cpu_limit` | `2.0 cores` | 分配的 CPU 核数（或 `unlimited`） |
| `_attr_online_cpus` | `4` | 宿主机在线 CPU 核数 |
| `_attr_cpu_throttled_periods` | `1205` | CFS 限流周期数（仅 Linux cgroup 环境有值） |
| `_attr_cpu_throttled_time` | `3.2s` | CFS 限流总时间（仅 Linux cgroup 环境有值） |

CPU throttling 数据来自 stats API 响应中的 `cpu_stats.throttling_data`，**零额外 API 调用成本**。容器平均 CPU 使用率不高但 throttling 严重时，说明 burst 期间被限流导致延迟飙升——这是 cAdvisor 的核心洞察。Windows 原生容器无 CFS throttling，对应 `_attr_` 不设置。

### memory_usage 维度

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_memory_percent` | `78.5%` | 内存使用率 |
| `_attr_memory_used` | `1.2 GiB` | 实际使用量（扣除缓存） |
| `_attr_memory_limit` | `2.0 GiB` | 内存限制（或宿主机总内存） |

OK 事件也携带完整 `_attr_` 标签。

### "not found" 容器

当显式名称容器未找到时，无法获取容器数据，事件不携带 `_attr_container_id` 和 `_attr_container_image` 标签——只有 check 和 target 标签。

## Init() 校验

1. `socket` 按平台自动设置默认值：Windows → `http://localhost:2375`，其他 → `/var/run/docker.sock`（不在 Init 时验证 socket 是否存在——Docker 可能稍后启动，运行时连接失败会产出 Critical 事件）
2. `api_version` 默认为空（首次 Gather 时自动协商）；非空时直接赋值给 `ins.apiVersion`
3. `timeout` 默认 `10s`
4. `targets` 不能为空
5. `concurrency` 默认 `5`，最小 `1`
6. `max_containers` 默认 `100`，最小 `1`
7. `restart_detected`：
   - `window` 默认 `10m`，最小 `1m`
   - `warn_ge > 0 && critical_ge > 0 && warn_ge >= critical_ge` → 报错
   - 初始化 `restartStates = make(map[string]*containerRestartState)`
8. `cpu_usage` / `memory_usage`：`warn_ge > 0 && critical_ge > 0 && warn_ge >= critical_ge` → 报错
10. 分类 targets：用 `filter.HasMeta()` 将显式名称存入 `explicitNames` map
11. `socket` 去除尾部 `/`（防止 URL 拼接出双斜杠）
12. 根据 `socket` 是否以 `http://` / `https://` 开头，选择构建方式：
    - 是：标准 HTTP transport，`baseURL = socket`
    - 否：Unix socket transport，`baseURL = "http://localhost"`

## Gather() 逻辑

```
Gather(q):
    // 0. 首次 Gather 时自动协商 API 版本（如未手动配置）
    if ins.apiVersion == "":
        negotiateAPIVersion()

    // 1. 列出所有容器（all=true，用于检测显式名称的停止容器）
    containers, err = listContainers()
    if err:
        event → Critical "docker::container_running" target="docker-engine":
            "failed to list containers: <error>"
        q.PushFront(event)
        return

    // 1.5 Docker Engine 可达 → 产出 Ok 事件（确保之前的 Critical 能恢复）
    q.PushFront(Ok "docker::container_running" target="docker-engine":
        "Docker Engine is reachable, <len(containers)> containers found")

    // 2. 按 targets 匹配容器
    //    显式名称：在所有容器中匹配（含已停止）
    //    Glob 模式：仅在活跃容器中匹配（running / paused / restarting）
    matched = matchTargets(containers, ins.Targets, ins.explicitNames)

    // 3. max_containers 安全限制
    if len(matched) > ins.MaxContainers:
        q.PushFront(Warning "docker::container_running" target="docker-engine":
            "matched <N> containers, exceeding max_containers <limit>, only checking first <limit>")
        matched = matched[:ins.MaxContainers]

    // 4. 显式名称未匹配 → container_running Critical（无 _attr_ 标签）
    for name in ins.explicitNames:
        if name not in matched:
            q.PushFront(Critical "docker::container_running" target=name:
                "container not found")

    // 5. 判断是否需要 stats 调用（inspect 始终调用）
    needStats = cpu_usage 或 memory_usage 启用

    // 6. 预取 restartStates 指针（避免 goroutine 并发读写 map）
    //    Go map 非并发安全，在主 goroutine 中预创建条目，goroutine 内只操作各自的指针
    restartStatePointers = {}
    for each container in matched:
        name = trimPrefix(c.Names[0], "/")
        if name not in ins.restartStates:
            ins.restartStates[name] = &containerRestartState{}
        restartStatePointers[name] = ins.restartStates[name]

    // 7. 并发检查每个匹配的容器
    wg, se = new WaitGroup, new Semaphore(ins.Concurrency)
    for each container in matched:
        wg.Add(1)
        state = restartStatePointers[trimPrefix(c.Names[0], "/")]
        go func(c, state):
            se.Acquire()
            defer { recover → panic event; se.Release(); wg.Done() }

            name = trimPrefix(c.Names[0], "/")

            // container_running 检查（list 数据即可判断状态）
            checkContainerRunning(q, c, name)

            // inspect（始终调用，获取 RestartCount / Health / OOMKilled 等诊断信息）
            detail, err = inspectContainer(c.Id)
            if err:
                q.PushFront(Critical "docker::container_running" target=name:
                    "failed to inspect container: <error>")
                return

            // restart_detected 检查（传入预取的 state 指针，不再操作 map）
            //   running 且频繁重启 = 活跃 crashloop
            //   exited 且频繁重启 = crashloop 后彻底挂了
            //   restarting 且频繁重启 = 正在 crashloop 中
            checkRestartDetected(q, detail, name, state)

            // 以下维度仅对 running 容器有意义
            if c.State != "running":
                return

            // health_status 检查（仅对有 HEALTHCHECK 的 running 容器）
            checkHealthStatus(q, detail, name)

            // stats（仅在 cpu_usage 或 memory_usage 启用时调用）
            if needStats:
                stats, err = getContainerStats(c.Id)
                if err:
                    // 为每个启用的资源维度分别产出 Critical 事件
                    if cpu_usage 启用:
                        q.PushFront(Critical "docker::cpu_usage" target=name:
                            "failed to get container stats: <error>")
                    if memory_usage 启用:
                        q.PushFront(Critical "docker::memory_usage" target=name:
                            "failed to get container stats: <error>")
                else:
                    checkCpuUsage(q, stats, detail, name)
                    checkMemoryUsage(q, stats, name)
        (container, state)
    wg.Wait()

    // 8. 清理已不存在的容器的 restartStates 条目（防止长期运行场景下内存缓慢增长）
    currentNames = { trimPrefix(c.Names[0], "/") for c in containers }  // 全量容器列表
    for name in ins.restartStates:
        if name not in currentNames:
            delete(ins.restartStates, name)
```

### 关键行为

1. **List API 失败 → Critical 事件**，target 为 `"docker-engine"`，表示 Docker 引擎不可达；**List 成功 → Ok 事件**，确保之前的 Critical 能正常恢复（原则 7：自身故障可感知）
2. **每个匹配容器独立产出事件**，某个容器的 API 调用失败不影响其他容器（原则 13：局部失败不影响全局）
3. **Inspect 始终调用**——本地 socket < 10ms，提供的诊断信息（RestartCount、OOMKilled、Health、ExitCode）不可或缺
4. **restart_detected 对所有状态的容器都检查**——exited/restarting 容器的频繁重启同样需要告警
5. **restart_detected 滑动窗口自愈**——容器稳定运行后窗口内无重启事件，过期记录被清除，告警自动恢复
6. **health_status / cpu_usage / memory_usage 仅对 running 容器检查**——exited 容器的 CPU/内存统计和健康检查无意义
7. **Stats 按需调用**——只启用资源维度时才发起 stats 调用（stats 是相对昂贵的 API）
8. **Stats 失败 → 为每个启用的资源维度产出 Critical 事件**——避免某个维度的告警无法恢复（如之前 memory_usage 处于 Critical，stats 失败后仅产出 cpu_usage 的错误事件而遗漏 memory_usage）
9. **并发控制**——semaphore 限制并行 API 调用数，避免冲击 Docker daemon
10. **restartStates 并发安全**——在主 goroutine 中预取 `*containerRestartState` 指针，goroutine 内仅操作各自指针，不读写 map（Go map 非并发安全）
11. **restartStates 过期清理**——每轮 Gather 结束后，对比全量容器列表，删除已不存在容器的条目，避免长期运行场景下内存缓慢增长
12. **Panic recovery**——goroutine 内 panic 产出 Critical 事件，与其他插件一致

## Description 示例

### container_running
- `container "nginx" is running`
- `container "nginx" not found`
- `container "redis" is not running (state: exited, exit code: 137)`
- `container "app" is not running (state: created)`
- `container "app" is not running (state: removing)`
- `container "app" is paused`

### restart_detected
- `container "app" restarted 5 times in last 10m, above critical threshold 5`
- `container "app" restarted 3 times in last 10m, above warning threshold 3 (OOM killed, exit code: 137)`
- `container "app" restarted 0 times in last 10m`

### health_status
- `container "app" health check: healthy`
- `container "app" health check: unhealthy (failing streak: 5)`

### cpu_usage
- `container "app" CPU usage 85.3% of 2.0 cores limit, above warning threshold 80%`
- `container "app" CPU usage 45.2% of 4 cores (unlimited)`

### memory_usage
- `container "redis" memory usage 92.1% (1.8 GiB / 2.0 GiB), above warning threshold 80%`
- `container "redis" memory usage 45.0% (921 MiB / 2.0 GiB)`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| socket | 按平台自动：Linux/macOS → `/var/run/docker.sock`，Windows → `http://localhost:2375` | 平台自适应，零配置即可运行 |
| api_version | 空（自动协商） | 通过 `GET /version` 获取 daemon 支持的最高版本；最低支持 v1.25（Docker 1.13+） |
| timeout | `10s` | 本地 Unix socket 通信，10 秒足够 |
| concurrency | `5` | 平衡并行度与 Docker daemon 压力 |
| max_containers | `100` | 防止 `targets = ["*"]` 在大规模主机上失控 |
| restart_detected window | `"10m"` | 10 分钟窗口覆盖典型 crashloop 周期 |
| restart_detected warn_ge | `3` | 10 分钟内重启 3 次足以表明异常 |
| restart_detected critical_ge | `5` | 10 分钟内重启 5 次确认 crashloop |
| memory_usage warn_ge | `80` | 80% 内存是常见预警线 |
| memory_usage critical_ge | `95` | 95% 接近 OOM，需立即关注 |
| cpu_usage warn_ge | `80` | 80% CPU 是常见预警线；memory_usage 启用时 Stats API 已在调用，cpu_usage 分析零额外成本；同时采集 CFS throttling 数据，帮助诊断延迟飙升 |
| cpu_usage critical_ge | `95` | 95% 接近 CPU 饱和，需立即关注 |
| interval | `30s` | 容器状态变化比系统级指标更频繁 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 容器是否在跑 / crashloop / 健康检查 / 资源 | **docker** | 容器级监控（5 个维度全覆盖） |
| 容器内进程是否存在 | procnum | 宿主机进程级（可选） |
| 容器化服务 systemd 状态 | systemd | 仅 systemd 管理的容器 |
| 整机重启 | uptime | 宿主机级别 |
| 宿主机 CPU / 内存 | cpu / mem | 宿主机级资源 |
| 容器磁盘使用 | disk | 挂载卷的宿主机路径 |

docker 与宿主机插件互补：docker 关注容器内部视角，cpu/mem/disk 关注宿主机全局视角。

## Docker 版本兼容性

### 版本覆盖范围

| Docker Engine | API 版本 | 发布时间 | 支持状态 |
| --- | --- | --- | --- |
| 1.13 | v1.25 | 2017-01 | 最低支持版本 |
| 17.03 | v1.27 | 2017-03 | `online_cpus` 字段可用 |
| 17.06 | v1.30 | 2017-06 | |
| 18.09 | v1.39 | 2018-11 | |
| 19.03 | v1.40 | 2019-07 | 内存 cache 字段语义变更 |
| 20.10 | v1.41 | 2020-12 | `CgroupVersion` 可查询 |
| 23.0 | v1.42 | 2023-02 | |
| 24.0 | v1.43 | 2024-03 | |
| 25.0 | v1.44 | 2024-01 | |
| 26.0+ | v1.45+ | 2024-04+ | 最新版本 |

**最低支持版本为 API v1.25（Docker 1.13）**——所有 List / Inspect / Stats 端点和核心字段（RestartCount、Health、StartedAt、OOMKilled）在此版本均已可用。

### 各维度的版本依赖

| 维度 | 最低 API 版本 | 版本敏感字段 | 兼容处理 |
| --- | --- | --- | --- |
| container_running | v1.25 | 无 | 所有字段均稳定 |
| restart_detected | v1.25 | 无 | `RestartCount` 自 Docker 1.10+ 可用 |
| health_status | v1.25 | `Health` 字段 | Docker 1.12+ 支持 HEALTHCHECK；无 HEALTHCHECK 时 `Health` 为 nil，已处理 |
| cpu_usage | v1.25 | `online_cpus`（v1.27+） | v1.25-v1.26 fallback 到 `len(percpu_usage)` |
| memory_usage | v1.25 | `cache` 字段语义 | 按优先级 fallback（见下文） |

### 关键字段的版本差异处理

#### 1. CPU `online_cpus`（v1.27+ 新增）

API v1.27（Docker 17.03.1）新增 `cpu_stats.online_cpus` 字段。更老版本此字段为 `nil` 或 `0`。

```go
onlineCPUs := stats.CPUStats.OnlineCPUs
if onlineCPUs == 0 {
    // v1.25-v1.26 fallback：使用 percpu_usage 数组长度
    onlineCPUs = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
}
```

这与 Docker 官方文档的建议一致：*"If this field is nil then for compatibility with older daemons the length of the corresponding cpu_usage.percpu_usage array should be used."*

#### 2. 内存 cache 字段（随 Docker 版本和 cgroup 版本变化）

Docker 不同版本、不同 cgroup 版本下，"文件缓存"字段的名称和位置不同：

| Docker 版本 | cgroup 版本 | cache 字段 |
| --- | --- | --- |
| < 19.03 | v1 | `memory_stats.stats["cache"]` |
| >= 19.03 | v1 | `memory_stats.stats["total_inactive_file"]` |
| >= 20.10 | v2 | `memory_stats.stats["inactive_file"]` |

代码按优先级依次尝试：`inactive_file` → `total_inactive_file` → `cache` → `0`。无论 Docker/cgroup 版本如何组合，都能正确扣除文件缓存。

#### 3. CPU throttling `throttling_data`

`cpu_stats.throttling_data`（含 `periods`、`throttled_periods`、`throttled_time`）自 API v1.21（Docker 1.9）起可用，覆盖所有支持版本。不存在版本兼容问题。

#### 4. Health 字段

`State.Health` 自 Docker 1.12（API v1.24）起可用。由于最低支持版本为 v1.25，不存在兼容问题。但 `Health` 仅在容器定义了 `HEALTHCHECK` 时存在，无 HEALTHCHECK 时为 `nil`——已在 `health_status` 维度中处理（跳过，不产出事件）。

#### 5. `NanoCPUs` vs `CpuQuota/CpuPeriod`

CPU 限制的表达方式有两种：
- `NanoCPUs`：v1.25+ 可用，单位为十亿分之一 CPU（如 `2e9` = 2 核）
- `CpuQuota` / `CpuPeriod`：v1.19+ 可用（更老，更通用）

两者可能同时存在，也可能只有一种。获取 CPU 限制的逻辑：

```go
func getAllocatedCPUs(hostConfig):
    if hostConfig.NanoCPUs > 0:
        return float64(hostConfig.NanoCPUs) / 1e9
    if hostConfig.CpuQuota > 0 && hostConfig.CpuPeriod > 0:
        return float64(hostConfig.CpuQuota) / float64(hostConfig.CpuPeriod)
    return 0  // 无限制
```

### API 版本协商失败处理

| 场景 | 行为 |
| --- | --- |
| `GET /version` 调用成功 | 使用 daemon 返回的 `ApiVersion` |
| `GET /version` 调用失败（如 Docker 未启动） | 降级到 `v1.25`；首次 Gather 时 List 调用会失败并产出 Critical 事件 |
| 用户显式配置 `api_version` | 直接使用，跳过协商；版本过高时 daemon 会拒绝，Gather 产出 Critical 事件 |

## 跨平台兼容性

### 连接层

| 平台 | 默认 socket | 连接方式 | 说明 |
| --- | --- | --- | --- |
| Linux | `/var/run/docker.sock` | Unix socket | Docker 默认配置，零配置可用 |
| macOS | `/var/run/docker.sock` | Unix socket | Docker Desktop 提供 Unix socket |
| Windows | `http://localhost:2375` | TCP HTTP | 需在 Docker Desktop 开启 TCP（Settings → General → "Expose daemon on tcp://localhost:2375"） |

自动检测逻辑：`socket` 为空时根据 `runtime.GOOS` 设置默认值。用户始终可通过显式配置 `socket` 覆盖默认行为。

Windows Named pipe (`//./pipe/docker_engine`) 方案：Go 标准库的 `net.Dial("unix", ...)` 不支持 Windows Named pipe，需引入 `github.com/Microsoft/go-winio`。初始版本使用 TCP 模式（无外部依赖），后续按需引入 npipe 支持。

### 数据层

Docker Engine API 在各平台返回的数据结构一致，但底层实现有差异：

| 维度 | Linux | macOS (Docker Desktop) | Windows |
| --- | --- | --- | --- |
| container_running | 完整支持 | 完整支持 | 完整支持 |
| restart_detected | 完整支持 | 完整支持 | 完整支持 |
| health_status | 完整支持 | 完整支持 | 完整支持 |
| cpu_usage | 完整支持 | 完整支持（Linux VM） | 完整支持（Windows Job Objects） |
| memory_usage | 完整支持 | 完整支持（Linux VM） | 完整支持（Windows Job Objects） |
| CPU throttling `_attr_` | 有值（CFS cgroup） | 有值（Linux VM 内 cgroup） | 无值（无 CFS），不设置 `_attr_` |
| memory `cache` 字段 | `cache`（cgroup v1）/ `inactive_file`（v2） | 同 Linux | `cache` 可能为 0，直接使用 `usage` |

**关键处理**：
1. **CPU throttling**：`throttling_data.throttled_periods == 0 && throttling_data.throttled_time == 0` 时不设置对应 `_attr_`（Windows 原生容器或未发生限流）
2. **Memory cache**：按优先级 fallback（`inactive_file` → `total_inactive_file` → `cache` → `0`），详见"Docker 版本兼容性"章节
3. **CPU system_cpu_usage**：Windows 容器可能不返回 `system_cpu_usage`，此时降级跳过 CPU 计算，不产出 cpu_usage 事件

### 编译层

无 build tags，无平台特定代码文件。所有平台差异通过运行时 `runtime.GOOS` 判断（仅 socket 默认值）和数据层零值检测（throttling / cache）处理。

## 文件结构

```
plugins/docker/
    design.md             # 本文档
    docker.go             # 主逻辑
    docker_test.go        # 测试
    api.go                # Docker API 客户端封装

conf.d/p.docker/
    docker.toml           # 默认配置
```

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 监控所有运行中的容器，检测：
## - 显式指定的容器是否在运行
## - 容器是否在 crashloop（滑动窗口内频繁重启）
## - Docker HEALTHCHECK 是否通过
## - 内存使用率是否过高

## Docker Engine 连接
## Linux/macOS 默认 /var/run/docker.sock（Unix socket）
## Windows 默认 http://localhost:2375（TCP 模式）
# socket = "/var/run/docker.sock"
## API 版本（留空自动协商，支持 Docker 1.13+）
# api_version = ""
# timeout = "10s"

## 监控目标（容器名称，支持 glob 模式）
## 显式名称：容器必须存在且运行，否则 Critical
## Glob 模式：发现匹配的容器并监控，无匹配不报错
targets = ["*"]

## 并发控制
# concurrency = 5
# max_containers = 100

## 容器运行状态检查（始终启用）
[instances.container_running]
# title_rule = "[check] [target]"

## 频繁重启检测（滑动窗口内重启次数超阈值 → crashloop）
## 单次重启不告警，短时间多次重启才是异常信号
[instances.restart_detected]
window = "10m"
warn_ge = 3
critical_ge = 5
# title_rule = "[check] [target]"

## Docker HEALTHCHECK 状态（始终启用，仅对有 HEALTHCHECK 的容器生效）
[instances.health_status]
# title_rule = "[check] [target]"

## 内存使用率
[instances.memory_usage]
warn_ge = 80.0
critical_ge = 95.0
# title_rule = "[check] [target]"

## CPU 使用率（默认不启用，按需开启）
# [instances.cpu_usage]
# warn_ge = 80.0
# critical_ge = 95.0
# title_rule = "[check] [target]"

## 采集间隔
interval = "30s"

[instances.alerting]
# for_duration = 0
# repeat_interval = "5m"
# repeat_number = 0
# disabled = false
# disable_recovery_notification = false
```
