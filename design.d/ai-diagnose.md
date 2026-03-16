# AI 辅助诊断设计

## 背景与动机

catpaw 当前的工作流是：**探测问题 → 推送 FlashDuty → 用户手动排查**。用户收到告警后，需要登录机器、执行各种命令才能定位根因，这个过程既慢又依赖经验。

AI 辅助诊断的目标：**告警触发时自动诊断，将问题和根因分析一起推送给用户**，结构性缩短 MTTR。

```
当前：catpaw → FlashDuty → 人登录机器排查 → 开始修复
目标：catpaw → 自动诊断 → FlashDuty 推送「问题 + 根因 + 建议」→ 人直接修复
```

### 产品边界

诊断子系统会连接 Redis、MySQL 等服务执行诊断命令，但这不是指标采集——诊断是按需的、事件触发的，而非周期性的持续拉取。catpaw 不替代 Exporter，两者互补。详见 [产品边界](product-boundary.md)。

## 架构总览

```
                          ┌─────────────┐
                          │  AI Model   │
                          │  (API)      │
                          └──────▲──────┘
                                 │  function calling
                          ┌──────┴──────┐
                          │  Diagnose   │
                          │  Engine     │  ← 中央编排，不属于任何插件
                          │             │
                          │  元工具：     │
                          │  - list_tool_categories()
                          │  - list_tools(category)
                          │  - call_tool(name, args)
                          └──────┬──────┘
                                 │
               ┌─────────────────┼─────────────────┐
               │                 │                 │
        ┌──────▼──────┐  ┌──────▼──────┐  ┌───────▼─────┐
        │   redis     │  │   mysql     │  │   disk/cpu  │
        │   tools     │  │   tools     │  │   tools     │
        └──────┬──────┘  └──────┬──────┘  └───────┬─────┘
               │                 │                 │
        ┌──────▼──────┐  ┌──────▼──────┐  ┌───────▼─────┐
        │   redis     │  │   mysql     │  │   disk/cpu  │
        │  accessor   │  │  accessor   │  │  accessor   │
        └─────────────┘  └─────────────┘  └─────────────┘
```

catpaw 充当 AI agent 的 tool executor：AI 决定执行哪些诊断命令，catpaw 执行后返回结果，AI 据此推理下一步或输出最终报告。

### 包依赖关系

```
agent  → diagnose     （Init 引擎、Shutdown）
engine → diagnose     （告警触发诊断 mayTriggerDiagnose）
engine → flashduty    （告警事件推送）
diagnose → flashduty  （诊断报告推送）
diagnose → aiclient   （AI API 调用）
```

`flashduty` 是独立的事件发送包，`engine` 和 `diagnose` 都直接 import 它，避免循环依赖和回调模式。

## 三层架构

诊断功能的引入要求对插件代码做分层重构。核心原则：**数据获取是公共能力，告警和诊断是两种消费方式**。

```
┌──────────────────────────────────────────────┐
│              上层消费者（并列，互不依赖）         │
│                                              │
│   ┌─────────────┐      ┌──────────────────┐  │
│   │  Alerting   │      │  Diagnose Tools  │  │
│   │  阈值判定    │      │  AI 工具执行器    │  │
│   │  产出 Event  │      │  产出文本/结构体  │  │
│   └──────┬──────┘      └────────┬─────────┘  │
│          │                      │            │
├──────────┴──────────────────────┴────────────┤
│                数据访问层 (Accessor)           │
│                                              │
│   封装连接、认证、协议交互、响应解析            │
│   返回结构化数据，不做任何判定                  │
│                                              │
│   RedisAccessor:                             │
│     .Info(section) → map[string]string       │
│     .SlowlogGet(n) → []SlowlogEntry          │
│     .ClientList()  → []ClientInfo            │
│     .ConfigGet(p)  → map[string]string       │
│                                              │
│   DiskAccessor:                              │
│     .IOStat()   → []DiskIOStat               │
│     .Usage()    → []MountPoint               │
│                                              │
│   CPUAccessor:                               │
│     .Usage()    → CPUUsage                   │
│     .TopN(n)    → []ProcessCPU               │
│                                              │
├──────────────────────────────────────────────┤
│                传输/采集层 (Transport)         │
│                                              │
│   Redis RESP client / /proc reader /         │
│   shell executor / SQL driver                │
└──────────────────────────────────────────────┘
```

### Accessor 层设计原则

- 每个 Accessor 封装一类目标的数据获取能力（建连、认证、命令发送、响应解析）
- 返回**结构化数据**（map、struct），不返回原始字节流
- **无状态判定**：不做阈值比较，不产出 Event
- **可复用**：告警的 `Gather()` 和诊断的 `DiagnoseTool.Execute()` 调用同一个 Accessor
- Accessor 的生命周期由调用者管理（创建、使用、关闭）

### 告警层与诊断层的关系

两者是平级消费者，共享 Accessor 层，互不依赖：

```go
// 告警层：Accessor → 取值 → 阈值判定 → Event
func (ins *Instance) gatherTarget(target string, q *safe.Queue[*types.Event]) {
    acc, err := NewRedisAccessor(target, ins.Password, ins.tlsConfig)
    if err != nil { ... }
    defer acc.Close()

    info, _ := acc.Info("memory")
    usedMem, _, _ := infoGetInt64(info, "used_memory")
    event := types.EvaluateGeThreshold(usedMem, ins.UsedMemory.Warning, ...)
    q.PushBack(event)
}

// 诊断层：Accessor → 取值 → 格式化文本返回给 AI
func redisInfoTool(acc *RedisAccessor, section string) (string, error) {
    info, err := acc.Info(section)
    if err != nil {
        return "", err
    }
    return formatInfoMap(info), nil
}
```

## 全局工具注册表

### 数据结构

```go
type ToolRegistry struct {
    mu         sync.RWMutex
    categories map[string]*ToolCategory
}

type ToolCategory struct {
    Name        string        // "redis", "disk", "cpu"
    Plugin      string        // 来源插件名
    Description string        // 给 AI 看的一句话说明
    Scope       ToolScope     // local / remote
    Tools       []DiagnoseTool
}

type DiagnoseTool struct {
    Name           string      // "redis_info", "disk_iostat"
    Description    string      // 给 AI 看的工具说明
    Parameters     []ToolParam // 参数定义（名称、类型、说明、是否必填）
    Scope          ToolScope   // 继承自 category，冗余存储方便查找

    // 本机工具：注册时绑定，直接调用
    Execute        func(ctx context.Context, args map[string]string) (string, error)

    // 远端工具：注册时绑定，通过 DiagnoseSession 的共享 Accessor 执行
    // session 持有该 target 的 Accessor，工具复用同一连接
    RemoteExecute  func(ctx context.Context, session *DiagnoseSession, args map[string]string) (string, error)
}

// DiagnoseSession 管理一次诊断的生命周期
// 同一 target 的所有工具调用共享一个 Accessor（一个 TCP 连接），
// 避免每次工具调用都重新建连/认证
type DiagnoseSession struct {
    Request   *DiagnoseRequest
    Accessor  any            // 远端工具的共享 Accessor，由插件创建
    Record    *DiagnoseRecord // 本地诊断记录，实时写入每轮结果
    StartTime time.Time
    mu        sync.Mutex     // 保护 Accessor 的并发访问（同一轮的多个 tool_call 可能并行）
}

func (s *DiagnoseSession) Close() {
    if closer, ok := s.Accessor.(io.Closer); ok {
        closer.Close()
    }
}

type ToolParam struct {
    Name        string
    Type        string // "string", "int"
    Description string
    Required    bool
}

type ToolScope int
const (
    ToolScopeLocal  ToolScope = iota // 在本机执行（disk, cpu, mem）
    ToolScopeRemote                   // 需要连接远端（redis, mysql）
)
```

### 插件注册接口

```go
// 插件可选实现此接口，以提供诊断工具
type Diagnosable interface {
    RegisterDiagnoseTools(registry *ToolRegistry)
}
```

### 注册时机与两条路径

工具注册和插件是否被配置为监控实例**解耦**。但本机工具和远端工具的注册方式不同：

**本机工具（disk, cpu, memory, os, process）**

- 启动时静态注册，Execute 函数直接绑定（读 `/proc`、执行命令等不需要额外凭据）
- 即使用户没有配置这些插件的监控实例，诊断工具仍然注册到全局工具表
- 诊断时直接调用 Execute 函数

**远端工具（redis, mysql, ...）**

- 启动时静态注册工具**定义**（名称、参数、描述）+ `RemoteExecute` 函数，但不绑定本机 `Execute`
- 诊断时通过 DiagnoseRequest 携带的 InstanceRef 直接获取连接凭据（主路径）
- 用凭据创建共享 Accessor（DiagnoseSession），整个诊断会话复用同一连接
- InstanceIndex 作为 fallback，用于特殊场景（跨 Instance 诊断）

```
本机工具注册：
  启动 → RegisterDiagnoseTools() → 注册 {定义 + Execute} → 完成

远端工具注册：
  启动 → RegisterDiagnoseTools() → 注册 {定义, RemoteExecute} → 完成
  诊断触发 → DiagnoseRequest.InstanceRef → 创建共享 Accessor（DiagnoseSession）
           → 所有远端工具通过 Session 复用同一 Accessor → 执行
```

实现方式：`agent.Start()` 阶段遍历所有已编译的插件（而非仅已配置的），调用 `RegisterDiagnoseTools()`。

### InstanceIndex：target 反查 Instance（fallback 路径）

> **注意**：首版中远端工具凭据的主路径是 `DiagnoseRequest.InstanceRef`（由 Gather 直接传递）。InstanceIndex 作为 fallback，用于未来可能的跨 Instance 诊断场景。

Agent 维护一个全局索引，在加载插件实例时构建：

```go
type InstanceIndex struct {
    mu    sync.RWMutex
    index map[string]any // key: "redis::10.0.0.1:6379", value: *redis.Instance
}

func (idx *InstanceIndex) Register(plugin, target string, instance any) {
    idx.mu.Lock()
    defer idx.mu.Unlock()
    idx.index[plugin+"::"+target] = instance
}

func (idx *InstanceIndex) Lookup(plugin, target string) (any, bool) {
    idx.mu.RLock()
    defer idx.mu.RUnlock()
    ins, ok := idx.index[plugin+"::"+target]
    return ins, ok
}
```

诊断触发时的完整链路（主路径使用 DiagnoseRequest.InstanceRef）：

```
DiagnoseRequest (plugin="redis", target="10.0.0.1:6379", InstanceRef=*redis.Instance)
  │
  ├─ 本机工具：直接执行，不需要额外信息
  │
  └─ 远端工具（主路径）：
      1. req.InstanceRef → *redis.Instance（含 password、TLS 配置等）
      2. 创建共享 RedisAccessor → 挂载到 DiagnoseSession
      3. 所有远端工具复用该 Accessor
  
  └─ 远端工具（fallback，用于跨 Instance 场景）：
      1. InstanceIndex.Lookup("redis", "10.0.0.1:6379")
      2. 拿到 *redis.Instance → 创建 Accessor
```

索引构建时机：每个 Instance 在 `Init()` 完成后，将自身所有 target 注册到 InstanceIndex。target 使用 `normalizeTarget()` 归一化后的值作为 key，确保与 Event 中的 target 一致。

**边界情况**：同一 target 出现在多个 Instance 中时，后注册的覆盖先注册的，日志记录 Warning。实际场景中这种配置极少出现。

## 渐进式工具发现

### 问题

随着插件增多，全局工具可能达到数百个。全量注入 AI prompt 会导致：
- token 消耗过大（200 工具 ≈ 40,000 tokens 工具定义）
- AI 在过多选择中准确率下降
- 可能超出上下文窗口

### 方案：三个元工具

catpaw 不把具体工具注册为 AI 的 function，而是提供三个元工具让 AI 按需发现：

**元工具 1：`list_tool_categories()`**

返回所有工具大类及摘要。

```
redis     (12 tools) - Redis 实例诊断：INFO、SLOWLOG、CLIENT 等
mysql     (15 tools) - MySQL 诊断：SHOW、EXPLAIN、慢查询等
disk      (5 tools)  - 磁盘诊断：iostat、df、读写延迟等
cpu       (3 tools)  - CPU 诊断：使用率、top 进程等
memory    (4 tools)  - 内存诊断：free、OOM 记录等
process   (5 tools)  - 进程诊断：列表、资源占用等
network   (4 tools)  - 网络诊断：连接状态、延迟等
os        (3 tools)  - 系统诊断：dmesg、uptime、sysctl 等
```

**元工具 2：`list_tools(category)`**

返回指定大类下的具体工具定义（名称、参数、说明）。

如果单个大类内工具过多（如未来 MySQL 有 50 个），支持子类层级：

```
mysql/
  ├── query      (8 tools) - 查询分析：EXPLAIN、慢查询、执行计划
  ├── connection (5 tools) - 连接诊断：PROCESSLIST、连接池状态
  ├── replication(6 tools) - 复制诊断：SHOW SLAVE STATUS、延迟
  └── storage    (7 tools) - 存储诊断：表大小、碎片、INNODB 状态
```

`list_tools("mysql")` 返回子类摘要，`list_tools("mysql/query")` 返回具体工具。

**元工具 3：`call_tool(name, tool_args)`**

执行指定工具并返回结果。参数使用显式嵌套，避免命名空间冲突：

- `name`（string，必填）：工具名称，如 `"disk_iostat"`
- `tool_args`（string，可选）：JSON 格式的工具参数，如 `"{\"device\":\"sda\"}"`

`tool_args` 作为 JSON 字符串传递，诊断引擎内部解析后转发给实际工具。这样 `call_tool` 自身的参数（`name`、`tool_args`）和被调用工具的参数完全隔离，不会出现同名冲突。

### 混合模式：直接注入 + 按需发现

为减少交互轮次，告警来源插件的工具**直接注入**为 AI 可直接调用的 function，其他插件的工具通过元工具按需发现：

```go
func (e *DiagnoseEngine) buildToolSet(req *DiagnoseRequest) []Tool {
    var tools []Tool

    // 直接可用：告警来源插件的工具，注册为独立 function
    // AI 可直接调用，如 redis_info(section="memory")
    for _, t := range e.registry.ByPlugin(req.Plugin) {
        tools = append(tools, t.ToFunctionTool())
    }

    // 按需发现：其他插件的工具通过元工具获取
    // AI 先调用 list_tool_categories()，再 list_tools(category)，最后 call_tool(name, tool_args)
    tools = append(tools, listCategoriesTool, listToolsTool, callToolTool)

    return tools
}
```

**双路径规则**：直接注入的工具和 `call_tool` 互斥——直接注入的工具只能通过其函数名直接调用，不能再通过 `call_tool` 包装调用。`call_tool` 仅用于调用通过 `list_tools` 发现的非直接注入工具。System prompt 中明确告知 AI 这一规则：

```
你可以直接调用以下 {{.Plugin}} 工具（无需通过 call_tool）：
{{.DirectTools}}

如需使用其他领域的工具，请先调用 list_tool_categories() 发现可用大类，
再通过 list_tools(category) 查看具体工具，最后通过 call_tool(name, args) 调用。
```

### token 成本对比

| 方案 | 工具定义 token 消耗 |
|------|-------------------|
| 全量注入 200 个工具 | ~40,000 tokens |
| 混合模式（直接注入 12 + 3 元工具） | ~3,000 + ~600 ≈ **3,600 tokens** |

随着工具总量增长，混合模式的成本几乎不变。

## AI API 配置

全局配置，所有插件共享：

```toml
[ai]
# 是否启用 AI 辅助诊断
enabled = false

# 统一使用 OpenAI-compatible API 协议
# 可用于 OpenAI、Azure OpenAI、DeepSeek、本地 Ollama/vLLM 等
# 首版只支持 OpenAI-compatible 协议，不做多协议适配
base_url = "https://api.openai.com/v1"
api_key = "${AI_API_KEY}"
model = "gpt-4o"

# 请求限制
max_tokens = 4000          # 单次诊断的最大输出 token
max_rounds = 8             # 单次诊断的最大 tool_call 轮次
request_timeout = "60s"    # 单次 AI API 请求超时

# 重试策略
max_retries = 2            # AI API 调用失败时最大重试次数（仅对 429/500/503 等可重试错误）
retry_backoff = "2s"       # 重试间隔基数，实际间隔 = retry_backoff * 2^(重试次数-1)

# 并发与成本控制
max_concurrent_diagnoses = 3   # 全局最大并发诊断数，防止告警风暴时打爆 AI API
queue_full_policy = "drop"     # 诊断队列满时的行为：drop（丢弃低优先级）/ wait（排队等待）
daily_token_limit = 500000     # 每日 token 消耗上限，0 表示不限

# per-tool 超时
tool_timeout = "10s"           # 单个工具执行的最大耗时，避免挂起耗尽总超时

# 聚合窗口
aggregate_window = "5s"        # 同一 target 的告警聚合窗口
```

### 状态持久化

`daily_token_limit` 和 `cooldown` 状态需要在重启后恢复，否则：
- 重启后 daily token 计数器归零，可能突破预算限制
- 重启后 cooldown 丢失，可能对刚诊断过的 target 重复诊断

持久化到 `state.d/diagnose_state.json`：

```json
{
  "daily_token_usage": {
    "date": "2026-03-02",
    "input_tokens": 182000,
    "output_tokens": 45000
  },
  "cooldowns": {
    "redis::10.0.0.1:6379": "2026-03-02T14:40:52+08:00",
    "mysql::10.0.0.2:3306": "2026-03-02T14:35:10+08:00"
  }
}
```

写入时机：每次诊断完成后异步更新（同样采用原子写入）。启动时加载，如 date 不匹配当天则重置 token 计数。

### 可观测性

诊断子系统暴露结构化 metrics 和日志，便于运维监控诊断功能本身的健康状况：

**关键 metrics**（通过 catpaw 现有 metrics 机制暴露）：

| Metric | 类型 | 说明 |
|--------|------|------|
| `diagnose_total` | Counter | 诊断请求总数（按 status 标签区分 success/failed/timeout/cancelled） |
| `diagnose_duration_seconds` | Histogram | 诊断耗时分布 |
| `diagnose_ai_rounds` | Histogram | 每次诊断的 AI 交互轮次 |
| `diagnose_tool_calls_total` | Counter | 工具调用总数（按 tool_name 标签） |
| `diagnose_tool_errors_total` | Counter | 工具调用失败数 |
| `diagnose_tokens_used` | Counter | token 消耗（按 input/output 标签） |
| `diagnose_daily_token_remaining` | Gauge | 当日剩余 token 配额 |
| `diagnose_concurrent_active` | Gauge | 当前正在运行的诊断数 |
| `diagnose_queue_depth` | Gauge | 等待调度的诊断队列深度 |
| `diagnose_dropped_total` | Counter | 因队列满/cooldown/daily limit 被丢弃的诊断请求 |

**结构化日志**（每次诊断产出一条 summary log）：

```json
{
  "level": "info",
  "msg": "diagnose completed",
  "diagnose_id": "d8f3a2b1",
  "plugin": "redis",
  "target": "10.0.0.1:6379",
  "checks_count": 2,
  "status": "success",
  "rounds": 3,
  "input_tokens": 2840,
  "output_tokens": 680,
  "duration_ms": 12300,
  "tools_called": ["redis_info", "redis_slowlog"]
}
```

### 环境变量引用

`api_key` 支持 `${ENV_VAR}` 语法，避免在配置文件中硬编码密钥。

### 私有部署

通过 `base_url` 指向本地模型（Ollama、vLLM 等），数据不出内网。使用私有部署时，基础设施信息（进程列表、连接状态等）不会发送到外部：

```toml
base_url = "http://localhost:11434/v1"
model = "llama3.1:70b"
api_key = "unused"
```

### 协议适配说明

首版仅支持 OpenAI-compatible API 协议（`/v1/chat/completions` + function calling）。该协议已被广泛兼容：

- OpenAI（原生）
- Azure OpenAI（通过 base_url 切换）
- DeepSeek（OpenAI 兼容）
- Ollama（内置 OpenAI 兼容层）
- vLLM（内置 OpenAI 兼容层）

如未来需要支持非 OpenAI-compatible 的 API（如 Anthropic Messages API），在 AI client 层增加协议适配器即可，不影响诊断引擎和工具注册表。

## 触发机制

### DiagnoseRequest

诊断请求由 `Gather()` 在产出告警 Event 时一并生成，携带诊断引擎所需的完整上下文：

```go
type DiagnoseRequest struct {
    // 触发告警的 Event 列表（短窗口聚合后可能包含多个）
    Events            []*types.Event

    // 从 Gather 上下文中提取的结构化信息，避免诊断引擎反向解析 Description
    Plugin            string            // "redis"
    Target            string            // "10.0.0.1:6379"
    Checks            []CheckSnapshot   // 每个触发的 check 的当前值和阈值

    // 触发告警的 Instance 引用（直接传递，不走 InstanceIndex 反查）
    InstanceRef       any               // *redis.Instance, *mysql.Instance, ...

    // 诊断会话（由 RunDiagnose 入口创建，挂载到 Request 上供整个诊断过程使用）
    Session           *DiagnoseSession

    // 诊断配置（从 Instance 的 [diagnose] 段读取）
    Timeout           time.Duration
    Cooldown          time.Duration
}

type CheckSnapshot struct {
    Check         string  // "redis::used_memory"
    Status        string  // "Warning" / "Critical"
    CurrentValue  string  // "1.8GB"
    ThresholdDesc string  // "Warning ≥ 1GB, Critical ≥ 2GB"
    Description   string  // "used_memory 1.8GB >= warning threshold 1GB"
}
```

DiagnoseRequest 直接携带 `InstanceRef`，诊断引擎据此创建 Accessor，无需通过 InstanceIndex 反查。InstanceIndex 降级为可选的备用查找路径。

### 实例级配置

```toml
[instances.diagnose]
# 是否启用该实例的 AI 诊断（需要全局 [ai].enabled = true）
enabled = true

# 最低触发严重级别，低于此级别不触发诊断
# Ok 和 Info 永远不触发
min_severity = "Warning"

# 单次诊断的总超时（包含所有 AI 交互轮次）
timeout = "60s"

# 同一 target 的诊断冷却期
# 冷却期内不重复诊断同一个 target（无论哪个 check 触发）
cooldown = "10m"
```

### 短窗口聚合

同一 target 在短时间窗口内（默认 5s）产生的多个告警，**合并为一次诊断请求**：

```
T=0s  redis::used_memory  Warning     ─┐
T=1s  redis::blocked_clients  Warning  ─┼─ 聚合为一个 DiagnoseRequest
T=3s  redis::persistence  Critical     ─┘   Events=[3个], Checks=[3个]
T=5s  聚合窗口关闭，提交诊断请求
```

实现方式：

```go
type DiagnoseAggregator struct {
    mu       sync.Mutex
    pending  map[string]*DiagnoseRequest // key: plugin::target
    timers   map[string]*time.Timer       // 聚合窗口定时器
    window   time.Duration                // 默认 5s
}

// Gather 产出告警时调用
func (a *DiagnoseAggregator) Submit(event *types.Event, snapshot CheckSnapshot, insRef any, cfg DiagnoseConfig) {
    key := event.Plugin + "::" + event.Target
    a.mu.Lock()
    defer a.mu.Unlock()

    if req, exists := a.pending[key]; exists {
        // 已有聚合窗口，追加 event
        req.Events = append(req.Events, event)
        req.Checks = append(req.Checks, snapshot)
        return
    }

    // 新建聚合窗口
    req := &DiagnoseRequest{
        Events:      []*types.Event{event},
        Plugin:      event.Plugin,
        Target:      event.Target,
        Checks:      []CheckSnapshot{snapshot},
        InstanceRef: insRef,
        Timeout:     cfg.Timeout,
        Cooldown:    cfg.Cooldown,
    }
    a.pending[key] = req

    // 窗口到期后提交
    a.timers[key] = time.AfterFunc(a.window, func() {
        a.mu.Lock()
        req := a.pending[key]
        delete(a.pending, key)
        delete(a.timers, key)
        a.mu.Unlock()
        a.scheduler.Submit(req) // 提交给 DiagnoseScheduler
    })
}
```

聚合的好处：
- **省 AI API 调用**：3 个告警 1 次诊断 vs 3 次诊断
- **AI 可做关联分析**：同时看到内存高 + 连接阻塞 + 持久化失败，更容易定位共同根因
- **减少目标连接压力**：共享一个 DiagnoseSession / Accessor

### 触发流程

```
Gather() 产出告警 Event
  │
  ├─ Event.Status == Ok / Info → 跳过
  ├─ Event.Status < min_severity → 跳过
  ├─ 同一 target 在 cooldown 内已诊断过 → 跳过
  │
  └─ 提交到 DiagnoseAggregator →
      1. 告警 Event 先正常推送 FlashDuty（不阻塞）
      2. 聚合窗口（5s）内收集同一 target 的其他告警
      3. 窗口关闭后，将聚合的 DiagnoseRequest 提交到 DiagnoseScheduler
      4. Scheduler 按优先级调度，异步启动诊断 goroutine
      5. 诊断完成后，将报告追加推送给 FlashDuty

告警推送和诊断完全解耦：诊断失败不影响告警本身
```

注意：cooldown 从原来的 `target+check` 粒度改为 **`target` 粒度**，因为聚合后一次诊断覆盖该 target 的所有异常 check。

### 为什么异步

- AI API 调用可能耗时数秒到数十秒
- 告警的时效性不能被诊断拖慢
- 诊断失败（API 不可用、超时、token 用尽）不应导致告警丢失

## 安全模型

### 设计原则

**AI 只能诊断，不能修复。**

catpaw 不给 AI 通用 shell 执行能力，而是让每个插件定义受限的、只读的诊断操作。

### 安全机制

**1. 插件级白名单**

AI 只能调用插件显式注册的工具，无法执行任意命令：

```go
// redis 插件只注册只读操作
func (ins *Instance) RegisterDiagnoseTools(registry *ToolRegistry) {
    registry.Register("redis", DiagnoseTool{
        Name: "redis_info",
        // Execute 内部只执行 INFO 命令
    })
    registry.Register("redis", DiagnoseTool{
        Name: "redis_slowlog",
        // Execute 内部只执行 SLOWLOG GET
    })
    // 不注册 DEL、FLUSHDB、CONFIG SET 等写操作
}
```

**2. 敏感信息过滤**

工具返回值中过滤敏感字段：

```go
var redisConfigDenyList = map[string]bool{
    "requirepass": true,
    "masterauth":  true,
    "tls-key-file": true,
}

func (a *RedisAccessor) ConfigGet(pattern string) (map[string]string, error) {
    result, err := a.configGetRaw(pattern)
    if err != nil {
        return nil, err
    }
    for key := range redisConfigDenyList {
        if _, exists := result[key]; exists {
            result[key] = "***REDACTED***"
        }
    }
    return result, nil
}
```

**3. 输出截断**

所有工具返回值有大小上限，防止大量数据发送给 AI API：

```go
const maxToolOutputSize = 32 * 1024 // 32KB

func truncateOutput(s string) string {
    if len(s) <= maxToolOutputSize {
        return s
    }
    return s[:maxToolOutputSize] + "\n... [truncated, total " + strconv.Itoa(len(s)) + " bytes]"
}
```

**4. 审计日志**

每次 AI 交互的完整 tool_call 链记录到日志：

```
[INFO] diagnose: target=10.0.0.1:6379 check=redis::used_memory
[INFO] diagnose: round=1 tool_call=redis_info args={"section":"memory"}
[INFO] diagnose: round=2 tool_call=disk_iostat args={}
[INFO] diagnose: round=3 tool_call=list_tools args={"category":"memory"}
[INFO] diagnose: completed rounds=3 input_tokens=2840 output_tokens=680
```

**5. tool_call 参数校验**

`call_tool()` 执行前校验参数符合工具定义的 schema，拒绝未声明的参数。

### 各插件工具安全分级参考

| 插件 | 安全工具（只读） | 危险操作（不注册） |
|------|-----------------|-------------------|
| redis | INFO, SLOWLOG GET, CLIENT LIST, CONFIG GET, DBSIZE | DEL, FLUSHDB, CONFIG SET, SHUTDOWN |
| mysql | SHOW STATUS/PROCESSLIST, EXPLAIN, INFORMATION_SCHEMA 查询 | DROP, ALTER, UPDATE, DELETE |
| disk | iostat, df, /proc/diskstats | mkfs, fdisk, dd |
| cpu | /proc/stat, top -bn1 | kill, renice, taskset |
| memory | /proc/meminfo, free | sysctl 写入 |
| process | ps, /proc/[pid]/status | kill, signal |
| os | dmesg, uptime, uname | shutdown, reboot |

## 诊断报告格式

### 结构化输出

AI 的最终输出为 Markdown 格式，包含固定段落。受 FlashDuty description 2048 字节限制，报告需精炼：

```markdown
## 诊断摘要
一句话描述问题根因。

## 根因分析
- 要点 1（含关键数值）
- 要点 2
- 要点 3

## 建议操作
1. 【紧急】...
2. 【短期】...
3. 【中期】...
```

不设"原始数据"段落——FlashDuty description 放不下。关键数值内嵌到根因分析要点中。完整的诊断过程和原始数据存入本地诊断记录（见「本地诊断记录」章节）。

### 本地诊断记录

每次诊断的完整过程（所有 tool_call、原始返回值、AI 推理链、最终报告）结构化存储在本地，解决 FlashDuty 2048 字节放不下详情的问题。FlashDuty 只放摘要和结论，用户需要深入排查时通过诊断 ID 查看完整记录。

**存储位置**：`state.d/diagnoses/`，与 catpaw 现有目录规范一致（`conf.d` 放配置、`state.d` 放运行时状态数据）。

**文件格式**：每次诊断一个 JSON 文件，文件名 `{日期}-{时间}-{短ID}.json`：

```
state.d/diagnoses/
  ├── 20260302-143052-d8f3a2b1.json
  ├── 20260302-143108-a1e7c3f9.json
  └── ...
```

**记录结构**：

```json
{
  "id": "d8f3a2b1",
  "status": "success",
  "error": "",
  "created_at": "2026-03-02T14:30:52+08:00",
  "duration_ms": 12300,

  "alert": {
    "plugin": "redis",
    "target": "10.0.0.1:6379",
    "checks": [
      {
        "check": "redis::used_memory",
        "status": "Warning",
        "current_value": "1.8GB",
        "warning_threshold": "1GB",
        "critical_threshold": "2GB",
        "description": "used_memory 1.8GB >= warning threshold 1GB"
      },
      {
        "check": "redis::blocked_clients",
        "status": "Warning",
        "current_value": "15",
        "warning_threshold": "10",
        "description": "blocked_clients 15 >= warning threshold 10"
      }
    ]
  },

  "ai": {
    "model": "gpt-4o",
    "total_rounds": 3,
    "input_tokens": 2840,
    "output_tokens": 680
  },

  "rounds": [
    {
      "round": 1,
      "tool_calls": [
        {
          "name": "redis_info",
          "args": {"section": "memory"},
          "result": "used_memory:1932735283\nused_memory_human:1.80G\n...",
          "duration_ms": 45
        }
      ],
      "ai_reasoning": "内存使用 1.8GB，碎片率 3.21 偏高..."
    },
    {
      "round": 2,
      "tool_calls": [
        {
          "name": "redis_info",
          "args": {"section": "keyspace"},
          "result": "db0:keys=12847,expires=4021,avg_ttl=0\n...",
          "duration_ms": 32
        },
        {
          "name": "redis_slowlog",
          "args": {"n": "10"},
          "result": "1) KEYS * (8.2ms)\n2) KEYS * (7.1ms)\n...",
          "duration_ms": 28
        }
      ],
      "ai_reasoning": "8853 个 key 没有 TTL，慢查询中大量 KEYS *..."
    }
  ],

  "report": "## 诊断摘要\nRedis 内存超阈值...\n## 根因分析\n...\n## 建议操作\n..."
}
```

**status 字段取值**：`success`（正常完成）、`failed`（AI/工具异常）、`cancelled`（graceful shutdown 中断）、`timeout`（超时）。

注意：`rounds[].tool_calls[].result` 存储工具的**完整原始输出**，不做截断——本地存储没有大小限制。

**原子写入**：诊断记录采用 temp file + rename 方式写入，防止进程中途退出产生损坏的 JSON 文件：

```go
func (r *DiagnoseRecord) Save() error {
    data, err := json.MarshalIndent(r, "", "  ")
    if err != nil {
        return err
    }
    tmpPath := r.FilePath() + ".tmp"
    if err := os.WriteFile(tmpPath, data, 0644); err != nil {
        return err
    }
    return os.Rename(tmpPath, r.FilePath()) // 原子操作
}
```

**查询 CLI**：

```bash
# 列出最近的诊断记录
catpaw diagnose list
catpaw diagnose list --target=10.0.0.1:6379
catpaw diagnose list --check=redis::used_memory
catpaw diagnose list --since=1h

# 查看某条诊断的完整详情
catpaw diagnose show d8f3a2b1

# 查看某条诊断的某一轮工具输出
catpaw diagnose show d8f3a2b1 --round=2
```

**保留策略**：

```toml
[ai]
diagnose_retention = "7d"      # 保留最近 7 天
diagnose_max_count = 1000      # 最多保留 1000 条
```

catpaw 启动时和每次写入新记录后检查，超期或超量的旧记录自动删除。

**与 FlashDuty 的联动**：FlashDuty description 中包含诊断 ID，用户可据此查看完整记录：

```
详情: catpaw diagnose show d8f3a2b1
```

### 与 FlashDuty 的集成

FlashDuty 只有 `description` 字段，没有独立的诊断报告字段。诊断报告拼接到 `description` 中：

```
description = 原始告警描述 + "\n\n---\n\n" + 诊断报告
```

推送流程：

1. 告警 Event 产出后，立即推送 FlashDuty（description 中只有告警信息）
2. 诊断完成后，构造一条**新 Event** 推送给 FlashDuty，要求：
   - **AlertKey 相同**（关联到同一告警）
   - **EventTime 不同**（使用诊断完成时的时间戳，而非原始告警时间）
   - **Description 不同**（拼接了诊断报告）
   - FlashDuty 会根据 AlertKey 关联，但 EventTime 和 Description 必须都与上一条不同，否则会被去重丢弃
3. 如果诊断失败，不推送第二条，原始告警不受影响

**description 长度限制**：FlashDuty 的 description 字段最大 **2048 字节**。原始告警描述通常占 50-100 字节，留给诊断报告的空间约 **1900 字节**（约 600-900 个中文字符）。诊断报告拼接后如果超长，按以下优先级截断：

1. 保留「诊断摘要」和「建议操作」（对用户价值最高）
2. 截断「原始数据」（可通过工具重新获取）
3. 最后截断「根因分析」（极端情况）

截断后在末尾标注 `[已截断] 详情: catpaw diagnose show <id>`，完整报告存储在本地诊断记录中。

拼接后的 description 示例：

```
used_memory 1.8GB >= warning threshold 1GB

---
[AI 诊断] Redis 内存超阈值，碎片率 3.2 偏高，大量无 TTL key 占用 1.2GB。
建议：1) 排查 KEYS * 调用源 2) 清理无 TTL key 3) 考虑 allkeys-lru
⚠️ AI 辅助诊断，仅供参考 | 详情: catpaw diagnose show d8f3a2b1
```

FlashDuty description 只放**浓缩结论**（摘要 + 建议 + 诊断 ID），确保在 2048 字节内。用户如需查看完整的根因分析、每轮工具调用和原始数据，通过诊断 ID 在本机查看。

## 诊断引擎核心流程

### 并发调度

诊断引擎维护一个带优先级的调度器，控制全局并发：

```go
type DiagnoseScheduler struct {
    semaphore chan struct{}            // 容量 = max_concurrent_diagnoses
    queue     PriorityQueue           // 按优先级排序的等待队列
}

// 优先级规则（数值越小优先级越高）：
// 1. Critical = 0, Warning = 1
// 2. 同级别中，不同 check 类型的告警优先（多样性优先，避免同类告警垄断诊断资源）
// 3. 首次告警优先于持续告警（NotifyCount == 0 优先）
```

当 `queue_full_policy = "drop"` 且队列已满时，丢弃最低优先级的诊断请求，日志记录被丢弃的告警信息。

### Agent Loop

```go
// RunDiagnose 是诊断 goroutine 的入口，包含 panic recovery
func (e *DiagnoseEngine) RunDiagnose(req *DiagnoseRequest) {
    // 1. 创建 DiagnoseSession 并挂载到 Request
    req.Session = &DiagnoseSession{
        Request:   req,
        Record:    NewDiagnoseRecord(req),
        StartTime: time.Now(),
    }

    // panic recovery：符合 catpaw principles.md 要求
    defer func() {
        if r := recover(); r != nil {
            logger.Logger.Errorw("diagnose panic recovered",
                "target", req.Target, "panic", r, "stack", string(debug.Stack()))
            req.Session.Record.Status = "failed"
            req.Session.Record.Error = fmt.Sprintf("panic: %v", r)
            req.Session.Record.Save()
        }
    }()
    defer req.Session.Close()

    ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
    defer cancel()

    e.registerInFlight(req, cancel)
    defer e.unregisterInFlight(req)

    report, err := e.diagnose(ctx, req)
    if err != nil {
        req.Session.Record.Status = "failed"
        req.Session.Record.Error = err.Error()
    } else {
        req.Session.Record.Status = "success"
        req.Session.Record.Report = report
    }
    req.Session.Record.DurationMs = time.Since(req.Session.StartTime).Milliseconds()
    req.Session.Record.Save()

    // 2. 累加 daily token 计数（用 AI 返回的实际 usage）
    e.addDailyTokenUsage(req.Session.Record.AI.InputTokens, req.Session.Record.AI.OutputTokens)

    // 3. 更新 cooldown 时间戳（无论成功失败，避免故障时反复触发）
    e.updateCooldown(req.Plugin, req.Target, req.Cooldown)

    // 4. 持久化状态（daily token + cooldown）
    e.persistState()

    if err == nil {
        e.pushReportToFlashDuty(req, report)
    }
}

func (e *DiagnoseEngine) diagnose(ctx context.Context, req *DiagnoseRequest) (string, error) {
    // 1. 创建远端 Accessor（共享连接，整个诊断会话复用）
    if err := e.initSessionAccessor(ctx, req); err != nil {
        return "", fmt.Errorf("failed to create accessor: %w", err)
    }

    // 2. 构建初始 prompt（支持多 check 聚合）
    messages := []Message{
        {Role: "system", Content: e.buildSystemPrompt(req)},
    }

    // 3. 构建工具集（直接注入 + 元工具）
    tools := e.buildToolSet(req)

    // 4. 跟踪 token 消耗（中文环境保守估算：1 token ≈ 2 字符）
    estimatedTokens := estimateTokensChinese(messages[0].Content)
    contextWindowLimit := e.contextWindowSize * 80 / 100

    // 5. Agent loop
    for round := 0; round < e.maxRounds; round++ {
        if round == e.maxRounds-1 {
            messages = append(messages, Message{
                Role:    "user",
                Content: "你已使用了所有可用的工具调用轮次。请基于目前收集到的信息，立即输出最终诊断报告。不要再调用任何工具。",
            })
        }

        if estimatedTokens > contextWindowLimit {
            messages = append(messages, Message{
                Role:    "user",
                Content: "上下文空间即将耗尽。请基于目前收集到的信息，立即输出最终诊断报告。",
            })
        }

        resp, err := e.chatWithRetry(ctx, messages, tools)
        if err != nil {
            return "", fmt.Errorf("AI API error at round %d: %w", round, err)
        }

        // 用 API 返回的实际 token 数校正估算
        if resp.Usage.TotalTokens > 0 {
            estimatedTokens = resp.Usage.TotalTokens
        }

        if len(resp.ToolCalls) == 0 {
            return resp.Content, nil
        }

        messages = append(messages, resp.ToAssistantMessage())
        estimatedTokens += estimateTokensChinese(resp.Content)

        // 记录本轮到诊断记录
        roundRecord := &RoundRecord{Round: round + 1}

        for _, tc := range resp.ToolCalls {
            // per-tool timeout：单个工具最多 10s，避免单个挂起耗尽总超时
            toolCtx, toolCancel := context.WithTimeout(ctx, e.toolTimeout)
            result, err := e.executeTool(toolCtx, req, tc)
            toolCancel()

            if err != nil {
                result = "error: " + err.Error()
            }
            truncated := truncateOutput(result)

            messages = append(messages, Message{
                Role: "tool", ToolCallID: tc.ID, Content: truncated,
            })
            estimatedTokens += estimateTokensChinese(truncated)

            // 本地诊断记录存完整原始输出（不截断）
            roundRecord.ToolCalls = append(roundRecord.ToolCalls, ToolCallRecord{
                Name: tc.Name, Args: tc.Args, Result: result, DurationMs: ...,
            })
        }
        roundRecord.AIReasoning = resp.Content
        req.Session.Record.Rounds = append(req.Session.Record.Rounds, roundRecord)
    }

    return "[诊断未完成] 已达到最大轮次限制，AI 未能在限定轮次内输出最终报告。", nil
}
```

### Graceful Shutdown

catpaw 收到 SIGTERM 时：

1. 调用所有 in-flight 诊断的 `cancel()` 取消 context
2. 已完成的诊断正常写入本地记录
3. 未完成的诊断标记 `status: "cancelled"`，写入记录后退出
4. 不等待 AI API 响应——context 取消后 HTTP 请求自动终止

// chatWithRetry 对可重试错误（429/500/503）做指数退避重试
func (e *DiagnoseEngine) chatWithRetry(ctx context.Context, messages []Message, tools []Tool) (*ChatResponse, error) {
    var lastErr error
    for attempt := 0; attempt <= e.maxRetries; attempt++ {
        if attempt > 0 {
            backoff := e.retryBackoff * time.Duration(1<<(attempt-1))
            select {
            case <-ctx.Done():
                return nil, ctx.Err()
            case <-time.After(backoff):
            }
        }
        resp, err := e.aiClient.Chat(ctx, messages, tools)
        if err == nil {
            return resp, nil
        }
        if !isRetryableError(err) {
            return nil, err
        }
        lastErr = err
    }
    return nil, fmt.Errorf("AI API failed after %d retries: %w", e.maxRetries, lastErr)
}
```

### 上下文窗口管理

AI 模型有上下文窗口限制（如 GPT-4o 为 128K tokens）。Agent loop 每轮追加 tool_call 和 tool result，可能导致累计 token 超出窗口。

管理策略：

1. **token 估算**：维护 `estimatedTokens` 计数器，每次追加 message 时累加（中文环境保守估算 1 token ≈ 2 字符），并在 AI API 返回实际 `usage` 时用真实值校正
2. **预留空间**：设置上限为窗口大小的 80%，预留 20% 给 AI 的最终输出
3. **强制收尾**：当估算 token 接近上限时，插入 user message 要求 AI 立即输出报告
4. **工具输出截断**：`truncateOutput()` 确保单个工具返回值不超过 32KB，从源头控制增长速度

### System Prompt 模板

```
你是一位资深运维和 DBA 专家。catpaw 监控系统检测到以下告警：

插件: {{.Plugin}}
目标: {{.Target}}

{{if eq (len .Checks) 1}}
### 告警详情
检查项: {{(index .Checks 0).Check}}
严重级别: {{(index .Checks 0).Status}}
当前值: {{(index .Checks 0).CurrentValue}}
阈值: {{(index .Checks 0).ThresholdDesc}}
描述: {{(index .Checks 0).Description}}
{{else}}
### 告警详情（同一目标有 {{len .Checks}} 个异常检查项，可能存在关联）
{{range $i, $c := .Checks}}
[{{add $i 1}}] {{$c.Check}} - {{$c.Status}}
    当前值: {{$c.CurrentValue}}
    阈值: {{$c.ThresholdDesc}}
    描述: {{$c.Description}}
{{end}}
请特别关注这些异常之间是否存在共同根因。
{{end}}

你的任务是诊断这个问题的根因，并给出建议操作。

## 可用工具

你可以直接调用以下 {{.Plugin}} 工具（无需通过 call_tool）：
{{.DirectTools}}

如需使用其他领域的工具（磁盘、CPU、内存、网络等），请：
1. 调用 list_tool_categories() 查看可用工具大类
2. 调用 list_tools(category) 查看某个大类下的具体工具
3. 调用 call_tool(name, tool_args) 执行具体工具
   tool_args 为 JSON 字符串格式，如 call_tool(name="disk_iostat", tool_args='{"device":"sda"}')

注意：上述 {{.Plugin}} 工具请直接调用，不要通过 call_tool 包装。

## 诊断提示

- 根因可能不在 {{.Plugin}} 自身，例如数据库慢可能是磁盘 I/O 瓶颈，
  服务延迟可能是 CPU 或内存压力，请根据需要探索其他领域的工具
{{if .IsRemoteTarget}}- ⚠️ 目标 {{.Target}} 是远端主机，本机基础设施工具（disk、cpu、memory 等）
  反映的是 catpaw 所在主机 {{.LocalHost}} 的状态，不是目标主机的状态
  这些工具的结果仅在 catpaw 与目标部署在同一台机器时有参考价值
{{else}}- catpaw 与目标 {{.Target}} 在同一台机器上，本机基础设施工具可直接用于辅助诊断
{{end}}
## 输出要求

- 请只使用工具获取信息，不要假设或编造数据
- 语言精炼，关键数值内嵌到分析要点中
- 最终输出请按以下格式：
  1. 诊断摘要（一句话）
  2. 根因分析（要点列表，每条含关键数值）
  3. 建议操作（按紧急/短期/中期分类）
- 不要输出原始数据的完整内容，只引用关键数值
```

### 工具执行逻辑

```go
func (e *DiagnoseEngine) executeTool(ctx context.Context, req *DiagnoseRequest, tc ToolCall) (string, error) {
    switch tc.Name {

    // 元工具：工具发现
    case "list_tool_categories":
        return e.registry.ListCategories(), nil
    case "list_tools":
        category := tc.Args["category"]
        return e.registry.ListTools(category), nil

    // 元工具：间接调用（用于非直接注入的工具）
    case "call_tool":
        toolName := tc.Args["name"]
        tool, ok := e.registry.Get(toolName)
        if !ok {
            return "", fmt.Errorf("unknown tool: %s", toolName)
        }
        // 解析嵌套的 tool_args（JSON 字符串 → map）
        toolArgs := parseToolArgs(tc.Args["tool_args"])
        return e.executeToolImpl(ctx, req, tool, toolArgs)

    // 直接注入的工具（告警来源插件），不经过 call_tool
    default:
        tool, ok := e.registry.Get(tc.Name)
        if !ok {
            return "", fmt.Errorf("unknown tool: %s", tc.Name)
        }
        return e.executeToolImpl(ctx, req, tool, tc.Args)
    }
}

func (e *DiagnoseEngine) executeToolImpl(ctx context.Context, req *DiagnoseRequest, tool DiagnoseTool, args map[string]string) (string, error) {
    // 本机工具：Execute 在注册时已绑定，直接调用
    if tool.Scope == ToolScopeLocal {
        return tool.Execute(ctx, args)
    }

    // 远端工具：通过 DiagnoseSession 的共享 Accessor 执行
    // Session 持有 InstanceRef → Accessor，同一诊断内所有远端工具复用同一连接
    session := req.Session
    session.mu.Lock()
    defer session.mu.Unlock()
    return tool.RemoteExecute(ctx, session, args)
}

func parseToolArgs(raw string) map[string]string {
    if raw == "" {
        return nil
    }
    var m map[string]string
    if err := json.Unmarshal([]byte(raw), &m); err != nil {
        return map[string]string{"_raw": raw}
    }
    return m
}
```

## 配置示例

### 全局配置（conf.d/config.toml）

```toml
[ai]
enabled = true
base_url = "https://api.openai.com/v1"
api_key = "${CATPAW_AI_API_KEY}"
model = "gpt-4o"
max_tokens = 4000
max_rounds = 8
request_timeout = "60s"
tool_timeout = "10s"
aggregate_window = "5s"
daily_token_limit = 500000
max_concurrent_diagnoses = 3
```

### 插件实例配置（conf.d/p.redis/redis.toml）

```toml
[[instances]]
targets = ["10.0.0.1:6379", "10.0.0.2:6379"]
password = "${REDIS_PASSWORD}"

  [instances.used_memory]
  warning = "1GB"
  critical = "2GB"

  [instances.diagnose]
  enabled = true
  min_severity = "Warning"
  timeout = "60s"
  cooldown = "10m"
```

## 跨插件诊断

### 问题

告警来源和根因经常不在同一个领域：

| 告警插件 | 表象 | 真正根因 | 需要的诊断能力 |
|----------|------|----------|---------------|
| MySQL | 慢查询突增 | 磁盘 IOPS 打满 | disk 工具 |
| Redis | 响应延迟高 | CPU 被其他进程吃光 | cpu 工具 |
| Redis | 持久化失败 | 磁盘空间不足 | disk 工具 |
| MySQL | 连接数暴涨 | 应用异常重启 | process 工具 |
| 任何服务 | 进程消失 | OOM Killer | memory + os 工具 |

### 方案

1. **本机基础设施工具始终可用**：disk、cpu、memory、process、os 插件的诊断工具在编译时即注册，不依赖用户是否配置了这些插件的监控实例
2. **渐进式发现**：AI 通过 `list_tool_categories()` 看到所有已注册的工具大类，按需展开
3. **prompt 引导**：system prompt 中提示 AI 根因可能跨域，鼓励其探索其他大类

### 远端工具的上下文传递

远端插件（redis、mysql）的诊断工具需要连接信息。通过 `DiagnoseRequest.InstanceRef` 直接获取（由 Gather 在生成诊断请求时传递），无需反查 InstanceIndex：

```go
// 诊断引擎初始化 Session 时，用 InstanceRef 创建共享 Accessor
func (e *DiagnoseEngine) initSessionAccessor(ctx context.Context, req *DiagnoseRequest) error {
    if req.InstanceRef == nil {
        return nil // 纯本机工具诊断，无需远端 Accessor
    }

    // 由插件提供的工厂方法创建 Accessor
    // 例如 redis 插件注册时提供 NewAccessor(ins *Instance) → *RedisAccessor
    accessor, err := e.registry.CreateAccessor(req.Plugin, ctx, req.InstanceRef)
    if err != nil {
        return fmt.Errorf("create accessor for %s::%s: %w", req.Plugin, req.Target, err)
    }
    req.Session.Accessor = accessor
    return nil
}
```

InstanceIndex 作为 fallback 路径，用于未来可能的场景（如 AI 主动发起对其他 target 的跨 Instance 诊断）。

本机工具（disk, cpu 等）不需要连接信息，Execute 在注册时已绑定，直接读取本机数据。

## 错误处理与降级

| 场景 | 处理方式 |
|------|---------|
| AI API 不可用（401/403） | 不可重试，诊断跳过，告警正常推送，日志记录错误 |
| AI API 暂时错误（429/500/503） | 指数退避重试（最多 max_retries 次），仍失败则跳过 |
| AI API 超时 | 重试一次，仍超时则跳过 |
| 每日 token 额度耗尽 | 诊断跳过，日志提示额度已用完，不影响告警 |
| 单次诊断超过 max_rounds | 倒数第二轮已插入强制收尾指令，AI 通常会在最后一轮输出报告；若仍未完成则返回 `[诊断未完成]` 提示 |
| 上下文窗口接近上限 | 插入强制收尾指令，要求 AI 基于已有信息输出报告 |
| 并发诊断数达到上限 | 按优先级排队或丢弃（取决于 queue_full_policy） |
| target 反查 Instance 失败 | 远端工具不可用，仅本机工具可用，日志记录 Warning |
| 工具执行失败 | 将错误信息返回给 AI，AI 可自行决定跳过或换其他工具 |
| AI 输出格式不符 | 原样附加到报告，不做格式校验 |
| 诊断报告超过 description 长度限制 | 按优先级截断，完整报告写入本地日志 |
| 全局 [ai].enabled = false | 诊断功能完全不加载，零开销 |

核心原则：**诊断是锦上添花，告警是基本功能。诊断的任何故障不能影响告警推送。**

## 注意事项

### 连接竞争

诊断工具的 Accessor 和 Gather 的 Accessor 可能同时连接同一个 target。对于 Redis 等协议，多个 TCP 连接不是问题。但如果 target 有连接数限制（如 MySQL 的 `max_connections`），需注意：

- 诊断 Accessor 使用与 Gather 相同的认证凭据
- 诊断属于只读操作，不会产生锁竞争
- 如果 target 连接数已紧张，诊断连接失败应视为正常降级（工具返回 error，AI 据此推理）

首版不做连接池复用（Gather 和 Diagnose 各自建连），后续可优化为共享连接池。

### 诊断记录 HTTP 查询（预留）

首版通过 `catpaw diagnose list / show` CLI 查询本地诊断记录。后续可预留 HTTP 端点：

```
GET /api/v1/diagnoses              # 列表
GET /api/v1/diagnoses/{id}         # 详情
```

首版不实现，但记录结构（JSON）设计时已考虑 HTTP API 的序列化需求，后续接入零改造。

### token 估算修正

中文环境下 token 估算不能用简单的「1 token ≈ 4 字符」规则。主流模型的 BPE 分词器对中文约 1 token ≈ 1.5~2 个汉字。设计中使用 `estimateTokensChinese()` 函数做保守估算（1 token ≈ 2 字符），并在 AI API 返回实际 `usage` 时用真实值校正。

## 实施计划

### Phase 1：Accessor 层重构

**目标**：为现有插件抽取数据访问层，告警逻辑不变。

1. 定义 Accessor 接口规范
2. 从 redis 插件开始，将 `gatherTarget()` 中的建连/认证/INFO 查询逻辑抽取为 `RedisAccessor`
3. `Gather()` 改为调用 Accessor，行为不变
4. 逐步完成 disk、cpu、memory、process 等插件的 Accessor 抽取
5. 确保所有现有测试通过

### Phase 2：全局工具注册表 + DiagnoseRequest + 聚合器

**目标**：建立工具注册、诊断请求、会话管理和短窗口聚合基础设施。

1. 实现 `ToolRegistry`、`DiagnoseTool`、`ToolCategory` 类型
2. 实现 `DiagnoseRequest`、`CheckSnapshot`、`DiagnoseSession` 类型
3. 实现 `DiagnoseAggregator`：短窗口聚合同一 target 的多个告警
4. 实现 `InstanceIndex`（作为 fallback）：target → Instance 反查索引
5. 定义 `Diagnosable` 接口
6. 在 `agent.Start()` 中遍历所有已编译插件，调用 `RegisterDiagnoseTools()`
7. 在 Gather 产出告警时，同步构建 `CheckSnapshot` 并提交到 `DiagnoseAggregator`
8. 为 redis 插件实现 `Diagnosable`，注册 `redis_info`、`redis_slowlog`、`redis_client_list`、`redis_config_get`、`redis_dbsize` 工具（远端工具，通过 DiagnoseSession 共享连接）
9. 为 disk、cpu、memory 插件注册基础诊断工具（本机工具，定义 + Execute）

### Phase 3：AI Client + 诊断引擎

**目标**：实现与 AI API 的交互和 agent loop。

1. 实现 OpenAI-compatible AI client（`/v1/chat/completions` + function calling）
2. 实现 `chatWithRetry()`：可重试错误（429/500/503）指数退避，不可重试错误（400/401）直接失败
3. 实现 `DiagnoseEngine`：
   - prompt 构建（含 IsRemoteTarget 判断、LocalHost 注入、多 check 聚合模板）
   - 工具集构建（直接注入 + 三个元工具，call_tool 使用 tool_args 嵌套参数）
   - agent loop（含上下文窗口 token 估算（中文修正）、AI usage 校正、倒数第二轮强制收尾）
   - per-tool timeout（默认 10s）
   - 输出截断（工具输出 32KB 上限）
   - panic recovery + graceful shutdown（context cancel）
4. 实现 `DiagnoseScheduler`：并发控制（semaphore）、优先级队列（Critical > Warning、多样性优先、首次告警优先）
5. 实现触发机制：severity 过滤、cooldown（target 粒度）、异步执行
6. 实现安全机制：参数校验、敏感信息过滤、审计日志
7. 全局配置解析（`[ai]` 段）和实例级配置解析（`[instances.diagnose]` 段）
8. 实现状态持久化：`state.d/diagnose_state.json`（daily token 计数 + cooldown 状态）
9. 实现可观测性 metrics（diagnose_total、diagnose_duration、diagnose_tokens_used 等）
10. 实现本地诊断记录：原子写入、status/error、保留策略

### Phase 4：FlashDuty 集成 + 端到端测试

**目标**：打通完整链路。

1. 实现诊断报告拼接到 description 的逻辑：
   - 同 AlertKey、新 EventTime、新 Description
   - description 超长时按优先级截断，完整报告写入本地日志
   - 报告末尾附加元数据（模型、耗时、轮次、token 用量）
2. 实现诊断报告的异步推送（不阻塞首次告警）
3. 端到端测试：Redis 内存告警 → AI 诊断 → 报告推送
4. 模拟跨插件诊断：MySQL 慢查询 → AI 调用 disk_iostat → 定位磁盘瓶颈
5. 测试降级场景：AI API 不可用 / 超时 / token 耗尽时告警不受影响

### Phase 5：扩展与优化

**目标**：覆盖更多插件，优化成本和质量。

1. 为 mysql、http、net 等插件实现 Diagnosable
2. 基于用户反馈调整 system prompt 和工具描述
3. 监控 token 消耗，优化工具输出格式减少浪费
4. 考虑诊断结果缓存：相同问题的诊断报告复用
5. 评估是否需要支持非 OpenAI-compatible 的 AI 协议（如 Anthropic Messages API）

## 代码文件规划

### 目录总览（首版实际实现）

```
catpaw/
├── diagnose/                        # 诊断引擎核心（新顶层包）
│   ├── types.go                     # 核心类型 + DiagnoseSession（含 Accessor 生命周期管理）
│   ├── config.go                    # SeverityRank 辅助函数
│   ├── registry.go                  # 全局工具注册表 + AccessorFactory
│   ├── index.go                     # InstanceIndex（fallback）
│   ├── aggregator.go                # 短窗口聚合器 + shouldTrigger + ExtractCheckSnapshot
│   ├── global.go                    # 全局单例管理（Init/Shutdown/定时清理）
│   ├── engine.go                    # 诊断引擎：Submit → RunDiagnose → diagnose(agent loop) → forwardReport
│   ├── prompt.go                    # System Prompt 模板（text/template）
│   ├── executor.go                  # 工具执行（buildToolSet + executeTool + 元工具 + 截断）
│   ├── report.go                    # 报告格式化（2048 字节截断 + UTF-8 安全）
│   ├── record.go                    # 本地诊断记录（原子写入 temp+rename）
│   ├── state.go                     # 状态持久化（daily token + cooldown → diagnose_state.json）
│   ├── cleanup.go                   # 诊断记录自动清理（保留天数 + 最大数量）
│   ├── cli.go                       # CLI: catpaw diagnose list/show（直接在 diagnose 包内）
│   └── aiclient/                    # AI API 客户端（子包）
│       ├── client.go                # OpenAI-compatible HTTP 客户端
│       ├── types.go                 # Message, ChatResponse, ToolCall, Tool, Usage
│       ├── retry.go                 # ChatWithRetry + IsRetryableError
│       └── token.go                 # EstimateTokensChinese
│
├── flashduty/                       # FlashDuty 事件发送（独立包，engine 和 diagnose 共享）
│   └── flashduty.go                 # Forward() + doForward() + PrintStdout()
│
├── plugins/
│   ├── plugins.go                   # Diagnosable 接口 + MayRegisterDiagnoseTools（已有文件扩展）
│   │
│   ├── redis/
│   │   ├── redis.go                 # 现有（Gather 改为调用 Accessor）
│   │   ├── accessor.go              # RedisAccessor（建连、INFO、SLOWLOG、CLIENT LIST、CONFIG GET 等）
│   │   ├── diagnose.go              # RegisterDiagnoseTools（5 个远端工具 + AccessorFactory）
│   │   └── diagnose_test.go
│   │
│   ├── disk/
│   │   ├── disk.go                  # 现有（未重构，diagnose 工具直接用 gopsutil）
│   │   ├── diagnose.go              # RegisterDiagnoseTools（3 个本机工具: usage/partitions/io）
│   │   └── diagnose_test.go
│   │
│   ├── cpu/
│   │   ├── cpu.go                   # 现有
│   │   ├── diagnose.go              # RegisterDiagnoseTools（3 个本机工具: usage/load/top_processes）
│   │   └── diagnose_test.go
│   │
│   └── mem/
│       ├── mem.go                   # 现有
│       ├── diagnose.go              # RegisterDiagnoseTools（3 个本机工具: mem/swap/top_processes）
│       └── diagnose_test.go
│
├── config/
│   ├── config.go                    # 扩展：AIConfig 定义 + [ai] 段解析
│   └── inline.go                    # 扩展：DiagnoseConfig 嵌入 InternalConfig
│
├── engine/
│   └── engine.go                    # 扩展：mayTriggerDiagnose + 调用 flashduty.Forward
│
├── agent/
│   └── agent.go                     # 扩展：initDiagnoseEngine + diagnose.Shutdown
│
└── main.go                          # 扩展：handleSubcommand（diagnose list/show）
```

### 文件职责详解

#### `diagnose/types.go` — 核心类型定义

所有诊断子系统共享的类型定义，其他文件 import 此文件的类型。

```go
// 核心类型：
// - DiagnoseRequest       诊断请求（Gather 产出，聚合后传入引擎）
// - CheckSnapshot         告警快照（check 名、当前值、阈值）
// - DiagnoseTool          诊断工具定义（名称、参数、Execute/RemoteExecute）
// - ToolCategory          工具大类
// - ToolParam             工具参数定义
// - ToolScope             工具作用域（Local / Remote）
// - ToolCall              AI 返回的工具调用请求
// - RoundRecord           单轮记录（tool_calls + ai_reasoning）
// - ToolCallRecord        单次工具调用记录
```

#### `diagnose/config.go` — 配置解析

解析全局 `[ai]` 段和实例级 `[instances.diagnose]` 段。

```go
// AIConfig          全局 AI 配置（base_url, api_key, model, limits...）
// DiagnoseConfig    实例级诊断配置（enabled, min_severity, timeout, cooldown）
// ParseAIConfig()   从 config.toml 解析 [ai] 段
// ParseDiagnoseConfig()  从插件 TOML 解析 [instances.diagnose] 段
```

#### `diagnose/registry.go` — 全局工具注册表

管理所有插件注册的诊断工具，提供按分类查询能力。

```go
// ToolRegistry
//   .Register(category, tool)       注册工具
//   .Get(name) → (DiagnoseTool, bool)
//   .ByPlugin(plugin) → []DiagnoseTool
//   .ListCategories() → string       格式化输出（给 AI 看）
//   .ListTools(category) → string    格式化输出（给 AI 看）
//   .CreateAccessor(plugin, ctx, insRef) → (any, error)  插件提供的 Accessor 工厂
//   .RegisterAccessorFactory(plugin, factory)
```

#### `diagnose/index.go` — InstanceIndex

fallback 路径的 target → Instance 反查索引。

```go
// InstanceIndex
//   .Register(plugin, target, instance)
//   .Lookup(plugin, target) → (any, bool)
```

#### `diagnose/aggregator.go` — 短窗口聚合器

5s 窗口内聚合同一 target 的多个告警为一次 DiagnoseRequest。

```go
// DiagnoseAggregator
//   .Submit(event, snapshot, insRef, cfg)  Gather 调用入口
//   内部：pending map + timer → 窗口到期后提交给 Scheduler
```

#### `diagnose/scheduler.go` — 优先级调度器

全局并发控制 + 优先级队列。

```go
// DiagnoseScheduler
//   .Submit(req)          接收聚合后的 DiagnoseRequest
//   .Run()                主循环：从队列取任务 → 获取 semaphore → 启动 goroutine
//   .Shutdown()           graceful shutdown：cancel 所有 in-flight
//   PriorityQueue         Critical > Warning > 多样性 > 首次告警
```

#### `diagnose/session.go` — 诊断会话管理

管理一次诊断的生命周期（Accessor 共享连接、Record 引用）。

```go
// DiagnoseSession
//   .Accessor    共享的远端 Accessor
//   .Record      本地诊断记录引用
//   .Close()     关闭 Accessor 连接
```

#### `diagnose/engine.go` — 诊断引擎主逻辑

核心编排：RunDiagnose（入口）→ diagnose（agent loop）。

```go
// DiagnoseEngine
//   .RunDiagnose(req)     goroutine 入口（panic recovery, session 创建, cooldown 更新）
//   .diagnose(ctx, req)   agent loop（prompt → AI → tool_call → 循环）
//   .initSessionAccessor()  创建共享 Accessor
//   .registerInFlight() / .unregisterInFlight()
//   .Shutdown()           graceful shutdown
```

#### `diagnose/prompt.go` — System Prompt 模板

构建发送给 AI 的 system prompt。

```go
// buildSystemPrompt(req) → string
//   - 单 check / 多 check 聚合模板
//   - 直接注入工具列表
//   - 元工具使用说明
//   - IsRemoteTarget 条件判断
//   - 输出格式要求
```

#### `diagnose/executor.go` — 工具执行 + 元工具

executeTool 路由 + 三个元工具实现 + parseToolArgs。

```go
// executeTool(ctx, req, tc)        工具路由（元工具 / 直接工具）
// executeToolImpl(ctx, req, tool, args)  实际执行（本机 / 远端）
// parseToolArgs(raw) → map         解析 call_tool 的嵌套 tool_args
// buildToolSet(req) → []Tool       构建 AI 可用工具集
```

#### `diagnose/report.go` — 报告格式化 + FlashDuty 拼接

诊断报告的后处理和推送。

```go
// pushReportToFlashDuty(req, report)
//   - 拼接 description（原始告警 + 诊断报告）
//   - 按优先级截断到 2048 字节
//   - 构造新 Event（同 AlertKey、新 EventTime）
//   - 推送 FlashDuty
// formatReportForFlashDuty(report, maxBytes) → string
// truncateByPriority(report, limit) → string
```

#### `diagnose/record.go` — 本地诊断记录

诊断记录的创建、保存、查询、清理。

```go
// DiagnoseRecord       JSON 记录结构（id, status, alert, ai, rounds, report）
// NewDiagnoseRecord(req) → *DiagnoseRecord
// .Save()              原子写入（temp + rename）
// .FilePath() → string
// ListRecords(filter) → []DiagnoseRecord    CLI 查询
// LoadRecord(id) → *DiagnoseRecord          CLI 详情
// CleanupRecords(retention, maxCount)        保留策略
```

#### `diagnose/state.go` — 状态持久化

daily token 计数、cooldown 时间戳的持久化与恢复。

```go
// DiagnoseState         持久化结构（daily_token_usage + cooldowns）
// .Load(path)           启动时从 state.d/diagnose_state.json 加载
// .Save(path)           原子写入
// addDailyTokenUsage(input, output)
// updateCooldown(plugin, target, duration)
// isCooldownActive(plugin, target) → bool
// resetIfNewDay()       日期变更时重置 token 计数
```

#### `diagnose/metrics.go` — 可观测性

Prometheus-style metrics 注册和更新。

```go
// 注册 metrics：
//   diagnose_total, diagnose_duration_seconds, diagnose_ai_rounds,
//   diagnose_tool_calls_total, diagnose_tool_errors_total,
//   diagnose_tokens_used, diagnose_daily_token_remaining,
//   diagnose_concurrent_active, diagnose_queue_depth, diagnose_dropped_total
```

#### `diagnose/security.go` — 安全机制

工具参数校验、敏感信息过滤、输出截断。

```go
// validateToolArgs(tool, args) → error     参数 schema 校验
// truncateOutput(s) → string               32KB 上限截断
// filterSensitiveFields(result) → string   由各插件自定义 denyList
```

#### `diagnose/aiclient/client.go` — AI HTTP 客户端

OpenAI-compatible API 的 HTTP 调用。

```go
// AIClient
//   .Chat(ctx, messages, tools) → (*ChatResponse, error)
//   内部：构造 /v1/chat/completions 请求，解析 function calling 响应
```

#### `diagnose/aiclient/types.go` — AI 协议类型

与 OpenAI API 交互的数据结构。

```go
// Message          {Role, Content, ToolCallID}
// ChatRequest      {Model, Messages, Tools, MaxTokens}
// ChatResponse     {Content, ToolCalls, Usage}
// Tool             {Type, Function{Name, Description, Parameters}}
// Usage            {PromptTokens, CompletionTokens, TotalTokens}
```

#### `diagnose/aiclient/retry.go` — 重试逻辑

```go
// ChatWithRetry(ctx, messages, tools) → (*ChatResponse, error)
//   指数退避重试（429/500/503），不可重试错误（400/401）直接失败
// isRetryableError(err) → bool
```

#### `diagnose/aiclient/token.go` — token 估算

```go
// EstimateTokensChinese(text) → int    保守估算（1 token ≈ 2 字符）
```

#### `diagnose/cli/cmd.go` — CLI 子命令

```go
// RunDiagnoseList(args)    catpaw diagnose list [--target=...] [--since=...]
// RunDiagnoseShow(args)    catpaw diagnose show <id> [--round=N]
```

#### `plugins/diagnosable.go` — Diagnosable 接口

```go
// Diagnosable interface {
//     RegisterDiagnoseTools(registry *diagnose.ToolRegistry)
// }
```

放在 plugins 包级别，所有插件共享此接口定义。

#### `plugins/redis/accessor.go` — RedisAccessor（新文件）

从现有 `redis.go` 的 `gatherTarget()` 中抽取建连 / 认证 / 命令执行逻辑。

```go
// RedisAccessor
//   NewRedisAccessor(target, password, tlsConfig) → (*RedisAccessor, error)
//   .Info(section) → (map[string]string, error)
//   .SlowlogGet(n) → ([]SlowlogEntry, error)
//   .ClientList() → ([]ClientInfo, error)
//   .ConfigGet(pattern) → (map[string]string, error)   含敏感字段过滤
//   .DBSize() → (int64, error)
//   .Ping() → error
//   .Close()
```

#### `plugins/redis/diagnose_tools.go` — Redis 诊断工具注册（新文件）

```go
// RegisterDiagnoseTools(registry)
//   注册 redis_info, redis_slowlog, redis_client_list,
//   redis_config_get, redis_dbsize 五个远端工具
// NewRedisAccessorFactory() → AccessorFactory
//   供 ToolRegistry.CreateAccessor 调用
```

#### `plugins/disk/accessor.go` — DiskAccessor（新文件）

```go
// DiskAccessor
//   .IOStat() → ([]DiskIOStat, error)       读 /proc/diskstats
//   .Usage() → ([]MountPoint, error)         df
//   .ReadLatency(device) → (float64, error)  读延迟
```

#### `plugins/disk/diagnose_tools.go` — Disk 诊断工具注册（新文件）

```go
// RegisterDiagnoseTools(registry)
//   注册 disk_iostat, disk_usage, disk_read_latency 等本机工具
```

#### `plugins/cpu/accessor.go` + `diagnose_tools.go`（新文件）

```go
// CPUAccessor: .Usage(), .TopN(n)
// 注册 cpu_usage, cpu_top 等本机工具
```

#### `plugins/mem/accessor.go` + `diagnose_tools.go`（新文件）

```go
// MemAccessor: .Info(), .OOMRecent()
// 注册 mem_info, mem_oom 等本机工具
```

### 首版文件数量汇总

| 目录 | 新增文件数 | 修改文件数 |
|------|-----------|-----------|
| `diagnose/` | 14（含测试） | 0 |
| `diagnose/aiclient/` | 5（含测试） | 0 |
| `flashduty/` | 1 | 0 |
| `plugins/redis/` | 3（accessor + diagnose + test） | 1（redis.go 重构） |
| `plugins/disk/` | 2（diagnose + test） | 0 |
| `plugins/cpu/` | 2（diagnose + test） | 0 |
| `plugins/mem/` | 2（diagnose + test） | 0 |
| `config/` | 0 | 2（config.go + inline.go） |
| `plugins/` | 0 | 1（plugins.go 扩展） |
| `engine/` | 0 | 1（engine.go 扩展） |
| `agent/` | 0 | 1（agent.go 扩展） |
| `main.go` | 0 | 1（diagnose 子命令） |
| **合计** | **29 新增** | **7 修改** |

### 设计 vs 实际差异说明

| 设计中的计划 | 实际处理 |
|-------------|---------|
| `diagnose/scheduler.go` 优先级调度器 | 首版用 semaphore 并发控制（engine.go），优先级队列推迟 |
| `diagnose/metrics.go` 可观测性 metrics | 推迟到 Phase 5，结构化日志已覆盖核心指标 |
| `diagnose/security.go` 安全机制 | 敏感过滤在 `redis/accessor.go`，截断在 `executor.go`，分散实现 |
| `diagnose/session.go` 独立文件 | 合并到 `types.go`，Session 类型与其他核心类型放在一起 |
| `diagnose/cli/cmd.go` 子包 | 简化为 `diagnose/cli.go`，无需独立子包 |
| `flashduty/` 独立包 | 原设计中报告推送在 engine 包，后重构为独立包以避免循环依赖 |
| `plugins/*/accessor.go`（disk/cpu/mem） | 本机插件不需要 Accessor 抽象层，直接在 `diagnose.go` 中用 gopsutil |
| `plugins/procnum/` `plugins/net/` 诊断工具 | 推迟到 Phase 5 |
