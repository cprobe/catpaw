# 开发必读

本文档帮助新开发者（以及 AI 助手）快速掌握 catpaw 的全貌，避免从零探索带来的时间和 token 浪费。

## 项目定位

catpaw 是一个**轻量级智能主机监控 Agent**，定位与 Nagios/Sensu 类似但更现代：

- **专注异常检测**，不是指标采集器（不与 Prometheus + Node-Exporter 重叠）
- **带 AI 诊断能力**，告警触发后自动分析根因
- **交互式 AI Chat**，登录机器后用自然语言排查问题，不用记命令
- **单二进制部署**，无重型依赖
- **开箱即用**，默认配置针对 Linux 生产环境优化

两个核心能力：

1. **事件产出**：插件检查 → 标准化 Event → 通知平台，这是传统监控 Agent 的职能
2. **AI 排障**：包括自动诊断（告警触发）和交互 Chat（人工登录机器后用自然语言排查）。运维人员不用记忆复杂命令，`catpaw chat` 即可调用 70+ 诊断工具和 shell 命令来定位问题

## 目录结构

```text
catpaw/
├── main.go              # CLI 入口：run/chat/inspect/diagnose/selftest
├── agent/               # Agent 生命周期管理、插件加载、Runner 调度
├── plugins/             # 30+ 检查插件，每个子目录一个插件
├── chat/                # 交互式 AI Chat REPL
├── conf.d/              # 默认配置目录
│   ├── config.toml      # 全局配置（AI、通知、日志等）
│   └── p.<plugin>/      # 各插件配置（支持多文件合并）
├── state.d/             # 运行时状态（诊断记录、状态持久化）
├── design.d/            # 设计原则文档
├── docs/                # 用户文档
└── build.sh             # 构建脚本

以下包在 digcore 模块中（github.com/cprobe/digcore）：
├── engine/              # 事件处理引擎：去重、告警判定、恢复、触发诊断
├── diagnose/            # AI 诊断子系统：引擎、聚合器、注册表、提示词、记录
├── notify/              # 通知后端：Console、WebAPI、Flashduty、PagerDuty
├── config/              # 配置结构定义与解析
├── types/               # 核心类型：Event、状态常量
├── logger/              # 日志封装
└── pkg/                 # 通用工具包（safe queue、并发工具等）
```

## 核心数据流

```text
  ┌──────────┐  events   ┌──────────┐  alert    ┌──────────────┐
  │ Plugins  │ ────────→ │  Engine  │ ────────→ │   Diagnose   │
  │ (Gather) │           │(PushRaw) │  trigger  │   Engine     │
  └──────────┘           └────┬─────┘           └──────┬───────┘
                              │                        │
                              │ forward                │ report
                              ▼                        ▼
                        ┌──────────┐             ┌──────────┐
                        │ Notifiers│ ←───────────│  新事件   │
                        └──────────┘  forward     └──────────┘
```

**完整路径**：

1. `agent.PluginRunner` 按 interval 定时调用 `Instance.Gather(queue)`
2. 插件将检查结果封装为 `types.Event`，推入 queue
3. `engine.PushRawEvents()` 消费 queue：
   - `clean()`：补充时间戳、合并 Labels（plugin → instance → global）、计算 AlertKey（Labels 排序拼接 → MD5）
   - Ok 事件 → `handleRecoveryEvent()`：清缓存，按需发恢复通知
   - 告警事件 → `handleAlertEvent()`：ForDuration / RepeatInterval / RepeatNumber 控制，满足条件则 `notify.Forward()`
4. 告警实际发送后 → `mayTriggerDiagnose()`：提交到 `DiagnoseAggregator`
5. 聚合器按 `plugin::target` 在时间窗口（默认 5s）内聚合同一目标的多个告警
6. 窗口到期 → `DiagnoseEngine.Submit()` → 信号量控制并发 → `RunDiagnose()`
7. AI 多轮对话：调用诊断工具 → 生成报告 → `forwardReport()` 为每个 AlertKey 创建新事件推到 notify

## 关键抽象

### Event（`types/event.go`）

```go
type Event struct {
    EventTime         int64
    EventStatus       string              // Critical/Warning/Info/Ok
    AlertKey          string              // Labels 的 MD5，唯一标识一条告警
    Labels            map[string]string   // 身份标签，参与 AlertKey
    Attrs             map[string]string   // 展示属性，不参与 AlertKey
    Description       string
    DescriptionFormat string              // text/markdown
    // 内部字段
    FirstFireTime     int64
    NotifyCount       int64
    LastSent          int64
}
```

**约定**：

- `Labels["check"]` 格式：`plugin::dimension`（如 `disk::space_usage`）
- `Labels["target"]`：检查对象标识
- `Attrs["current_value"]`：触发告警的主指标值
- `Attrs["threshold_desc"]`：人类可读的阈值描述，如 `"Warning ≥ 80.0%, Critical ≥ 95.0%"`

### Plugin 接口（`plugins/plugins.go`）

```go
Plugin    → GetLabels(), GetInterval()
Instance  → GetLabels(), GetInterval(), GetAlerting(), GetDiagnoseConfig()
Gatherer  → Gather(*safe.Queue[*types.Event])         // 必须实现
Initer    → Init() error                               // 可选：校验配置
Dropper   → Drop()                                     // 可选：清理资源
InstancesGetter → GetInstances() []Instance            // 多实例插件必须实现
Diagnosable     → RegisterDiagnoseTools(registry)       // 可选：注册诊断工具
IApplyPartials  → ApplyPartials() error                // 可选：配置模板复用
```

注册方式：`plugins.Add(name, creator)` 在 `init()` 中调用，由 `agent/agent.go` 的 blank import 触发。

### DiagnoseTool（`diagnose/types.go`）

```go
type DiagnoseTool struct {
    Name        string
    Description string
    Parameters  []ToolParam
    Scope       ToolScope              // Local / Remote
    Execute     func(ctx, args) (string, error)          // 本地工具
    RemoteExecute func(ctx, session, args) (string, error) // 远程工具
}
```

工具按 **ToolCategory** 分组，注册到 `ToolRegistry`。AI 通过 `call_tool(name, args)` 调用，`list_tools(category)` 查询参数。

### DiagnoseEngine（`diagnose/engine.go`）

引擎负责：

- 接收 `DiagnoseRequest` → 信号量控制并发（默认 3）
- 构建系统提示词（包含完整工具目录 + 告警上下文）
- 多轮 AI 对话，解析 tool_calls 并执行
- 状态管理：冷却期、每日 Token 额度、记录持久化
- 结果转发：为每个唯一 AlertKey 创建新 Event

## diagnose/ 子系统文件职责

| 文件 | 职责 |
| ------ | ------ |
| `engine.go` | 引擎主循环：Submit → RunDiagnose → AI 对话 → 报告转发 |
| `aggregator.go` | 按 `plugin::target` 聚合告警事件，窗口到期后提交请求 |
| `registry.go` | 工具注册表：按类别管理工具，`ListToolCatalogSmart()` 生成混合目录 |
| `prompt.go` | 系统提示词模板：Go template，区分 alert/inspect 模式 |
| `toolconv.go` | 内部工具 → AI function-calling 格式转换，定义 meta-tools |
| `executor.go` | 工具执行路由：解析 AI 参数 → 分发到对应 handler |
| `types.go` | 核心类型定义：DiagnoseTool、DiagnoseRequest、DiagnoseRecord 等 |
| `state.go` | 持久化状态：每日 Token 用量、冷却期（`state.d/diagnose_state.json`） |
| `record.go` | 诊断记录：创建 ID、序列化/反序列化、保存到 `state.d/diagnoses/` |
| `cleanup.go` | 定期清理：按 retention 和 max_count 淘汰旧记录 |
| `global.go` | 全局单例：`Init()`、`GlobalAggregator()`、`GlobalEngine()`、`Shutdown()` |
| `cli.go` | CLI 子命令：`diagnose list`、`diagnose show` |
| `report.go` | 报告格式化：Markdown 输出、UTF-8 安全截断 |
| `selftest.go` | 工具冒烟测试：`selftest` 命令的实现 |
| `streaming.go` | AI 流式响应处理 |

## notify/ 子系统

| 文件 | 职责 |
| ------ | ------ |
| `notify.go` | `Notifier` 接口 + 注册/分发逻辑，所有后端同时接收 |
| `console.go` | 彩色终端输出，默认启用，方便快速验证 |
| `webapi.go` | 通用 HTTP 推送，把 Event JSON 原样发送到用户 endpoint |
| `flashduty.go` | Flashduty 告警平台适配 |
| `pagerduty.go` | PagerDuty Events API v2 适配 |

HTTP 类 Notifier 支持重试退避、超时、自定义 Headers。

## config/ 结构速览

```text
config.toml
├── [global]           # interval、labels
├── [log]              # level、filename、max_size
├── [ai]               # enabled、max_rounds、aggregate_window、language ...
│   ├── [ai.models.xxx]   # base_url、api_key、model、context_window、input_price ...
├── [notify.console]   # enabled
├── [notify.webapi]    # url、method、timeout、headers
├── [notify.flashduty] # integration_key
└── [notify.pagerduty] # routing_key
```

**内联配置**（在插件 toml 中）：

```toml
[[instances]]
interval = "30s"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 3

[instances.diagnose]
enabled = true
min_severity = "Warning"
timeout = "120s"
cooldown = "30m"
```

## CLI 命令总览

| 命令 | 说明 |
| ------ | ------ |
| `catpaw run` | 启动 Agent（`--interval`、`--plugins` 过滤） |
| `catpaw chat` | 交互式 AI 对话（`-v` 详细、`--model` 指定模型） |
| `catpaw inspect <plugin> [target]` | 主动 AI 健康检查 |
| `catpaw diagnose list` | 列出诊断记录 |
| `catpaw diagnose show <id>` | 查看诊断详情 |
| `catpaw selftest [filter]` | 诊断工具冒烟测试（`-q` 安静模式） |

全局 flag：`--configs`（配置目录）、`--loglevel`、`--version`

## 开发快速链接

| 场景 | 入口 |
| ------ | ------ |
| 新增检查插件 | [插件开发指南](plugin-development.md) |
| 新增诊断工具 | 实现 `Diagnosable` 接口 或 使用 `plugins.DiagnoseRegistrars` |
| 新增 Notifier | 实现 `notify.Notifier` 接口，在 `agent.go` 中注册 |
| 修改 AI 提示词 | `diagnose/prompt.go` 中的 `promptRaw` 模板 |
| 修改 AI 工具执行 | `diagnose/executor.go` |
| 修改事件处理逻辑 | `engine/engine.go` |
| 修改配置结构 | `config/config.go` + `config/inline.go` |

## 设计原则摘要

完整版见 [`design.d/principles.md`](../design.d/principles.md)，要点：

1. **告警质量优先**：宁可漏报，不可误报；默认阈值偏保守
2. **Fail-open**：采集失败本身应产出告警事件，不能静默
3. **优雅降级**：单个 target/instance/plugin 失败不影响其他
4. **开箱即用**：默认配置下载即可运行，无需调整
5. **跨平台**：Linux/Windows/macOS，平台特有逻辑用 build tags 隔离
6. **命名一致**：`check` 格式 `plugin::dimension`，`threshold_desc` 统一阈值描述
7. **防 goroutine 泄漏**：可能 hang 的操作必须有 inFlight 防重入 + 超时保护

## 构建与测试

```bash
# 构建
./build.sh

# 运行测试
go test ./...

# 快速验证（仅运行 cpu 和 mem 插件，事件输出到 console）
./catpaw run --plugins cpu:mem

# 测试诊断工具
./catpaw selftest
```
