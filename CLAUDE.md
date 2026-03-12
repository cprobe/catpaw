# catpaw — Claude Code 项目指南

## 项目定位


catpaw 是一个**轻量级智能主机监控 Agent**（Go 语言，单二进制）：
- 25+ 插件化检查，产出标准化 Event → 推送告警平台
- 告警触发后自动调用 AI + 70+ 诊断工具进行根因分析
- `catpaw chat` 命令行交互式 AI 排障

## 关键目录

```
catpaw/
├── main.go              # CLI 入口（run/chat/inspect/diagnose/selftest/mcptest）
├── agent/               # Agent 生命周期、插件加载、Runner 调度
├── engine/              # 事件处理：去重、告警判定、恢复、触发诊断
├── plugins/             # 25+ 检查插件（每个子目录一个插件）
│   └── plugins.go       # 插件注册表 + 核心接口定义
├── diagnose/            # AI 诊断子系统
├── chat/                # 交互式 Chat REPL
├── mcp/                 # MCP 客户端（对接 Prometheus/Jaeger 等）
├── notify/              # 通知后端（Console/WebAPI/Flashduty/PagerDuty）
├── config/              # 配置结构定义与解析
├── types/               # 核心类型：Event、状态常量
├── conf.d/              # 默认配置目录
│   ├── config.toml      # 全局配置
│   └── p.<plugin>/      # 各插件配置
├── state.d/             # 运行时状态（诊断记录）
└── docs/                # 用户与开发文档
```

## 核心数据流

```
Plugins.Gather() → types.Event → engine.PushRawEvents()
  → handleAlertEvent() → notify.Forward()
  → mayTriggerDiagnose() → DiagnoseAggregator → DiagnoseEngine
  → AI 多轮对话 → 诊断报告 Event → notify.Forward()
```

## 关键接口（`plugins/plugins.go`）

```go
Gatherer    → Gather(*safe.Queue[*types.Event])    // 必须实现
Initer      → Init() error                          // 可选：校验配置
Dropper     → Drop()                                // 可选：清理资源
Diagnosable → RegisterDiagnoseTools(registry)       // 可选：注册诊断工具
IApplyPartials → ApplyPartials() error              // 可选：配置模板复用
```

插件注册：`plugins.Add(name, creator)` 在 `init()` 中调用。

## Event 字段约定（`types/event.go`）

| 字段 | 约定 |
|------|------|
| `Labels["check"]` | `plugin::dimension`（如 `disk::space_usage`） |
| `Labels["target"]` | 检查对象标识 |
| `Attrs["current_value"]` | 触发告警的主指标值 |
| `Attrs["threshold_desc"]` | 人类可读阈值描述，如 `"Warning ≥ 80.0%"` |
| `EventStatus` | `Critical` / `Warning` / `Info` / `Ok` |

## 构建与测试

```bash
./build.sh                          # 构建
go test ./...                        # 单测
./catpaw run --plugins cpu:mem       # 快速验证（仅 cpu+mem 插件）
./catpaw selftest                    # 诊断工具冒烟测试
./catpaw mcptest                     # MCP 连接测试
```

## 开发规范

1. **新建插件**：参考 `docs/plugin-development.md`，在 `plugins/<name>/` 下创建，`init()` 注册，`conf.d/p.<name>/` 提供示例配置
2. **新建诊断工具**：实现 `Diagnosable` 接口或使用 `plugins.DiagnoseRegistrars`
3. **新建通知后端**：实现 `notify.Notifier` 接口，在 `agent.go` 中注册
4. **修改 AI 提示词**：`diagnose/prompt.go`
5. **跨平台代码**：平台特有逻辑用 build tags（`//go:build linux`）隔离
6. **防 goroutine 泄漏**：可能 hang 的操作必须有 `inFlight` 防重入 + context 超时保护

## 设计原则

- **告警质量优先**：宁可漏报，不可误报；默认阈值保守
- **Fail-open**：采集失败本身产出告警事件，不能静默
- **优雅降级**：单个 target/instance/plugin 失败不影响其他
- **开箱即用**：默认配置下载即运行，无需调整

## 可用 Skills

- `catpaw-plugin-development`：开发、修改、补全或评审 catpaw 插件时使用

## 文档索引

| 文档 | 内容 |
|------|------|
| `docs/dev-guide.md` | 架构全貌与代码导航（新人必读） |
| `docs/plugin-development.md` | 插件开发指南 |
| `docs/event-model.md` | Event 结构、Labels 设计、AlertKey 规则 |
| `docs/cli.md` | 完整命令行参数 |
| `docs/deployment.md` | 部署指南 |
| `design.d/` | 设计原则文档 |

> **工作目录**：`/path/to/catpaw`
> 所有命令默认在此目录下执行，shell 操作前请先确认 pwd。
