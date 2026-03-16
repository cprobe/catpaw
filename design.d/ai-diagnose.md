# AI 辅助诊断设计

## 背景与目标

catpaw 当前的工作流是：**探测问题 → 推送 FlashDuty → 用户手动排查**。

AI 辅助诊断的目标：**告警触发时自动诊断，将问题和根因分析一起推送给用户**，结构性缩短 MTTR。

```text
当前：catpaw → FlashDuty → 人登录机器排查 → 开始修复
目标：catpaw → 自动诊断 → FlashDuty 推送「问题 + 根因 + 建议」→ 人直接修复
```

诊断是按需的、事件触发的，不是周期性采集。catpaw 不替代 Exporter，两者互补。

## 架构总览

catpaw 充当 AI agent 的 tool executor：AI 决定执行哪些诊断命令，catpaw 执行后返回结果，AI 据此推理或输出最终报告。

```text
                          ┌─────────────┐
                          │  AI Model   │
                          │  (API)      │
                          └──────▲──────┘
                                 │  function calling
                          ┌──────┴──────┐
                          │  Diagnose   │
                          │  Engine     │  ← 中央编排
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
        └─────────────┘  └─────────────┘  └─────────────┘
```

### 包依赖关系

```text
agent  → diagnose     （Init 引擎、Shutdown）
engine → diagnose     （告警触发诊断 mayTriggerDiagnose）
engine → flashduty    （告警事件推送）
diagnose → flashduty  （诊断报告推送）
diagnose → aiclient   （AI API 调用）
```

`flashduty` 是独立的事件发送包，`engine` 和 `diagnose` 都直接 import 它，避免循环依赖。

## 三层架构

核心原则：**数据获取是公共能力，告警和诊断是两种消费方式**。

```text
┌──────────────────────────────────────────────┐
│              上层消费者（并列，互不依赖）         │
│                                              │
│   ┌─────────────┐      ┌──────────────────┐  │
│   │  Alerting   │      │  Diagnose Tools  │  │
│   │  阈值判定    │      │  AI 工具执行器    │  │
│   └──────┬──────┘      └────────┬─────────┘  │
│          │                      │            │
├──────────┴──────────────────────┴────────────┤
│                数据访问层 (Accessor)           │
│                                              │
│   封装连接、认证、协议交互、响应解析            │
│   返回结构化数据，不做任何判定                  │
├──────────────────────────────────────────────┤
│                传输/采集层 (Transport)         │
│                                              │
│   Redis RESP client / /proc reader /         │
│   shell executor / SQL driver                │
└──────────────────────────────────────────────┘
```

**Accessor 层设计原则**：

- 封装一类目标的数据获取（建连、认证、命令发送、响应解析）
- 返回结构化数据，无状态判定，不产出 Event
- 告警的 `Gather()` 和诊断的 `DiagnoseTool.Execute()` 复用同一个 Accessor

## 全局工具注册表

### 核心概念

- **ToolRegistry**：全局注册表，按 category 组织所有诊断工具
- **ToolCategory**：工具大类（如 redis、disk、cpu），含名称、来源插件、描述
- **DiagnoseTool**：具体工具定义（名称、参数、描述、Execute/RemoteExecute 函数）
- **ToolScope**：`Local`（本机，如 disk/cpu）或 `Remote`（需连接远端，如 redis/mysql）
- **DiagnoseSession**：管理一次诊断的生命周期，同一 target 的所有远端工具共享一个 Accessor（TCP 连接）

### 插件注册接口

插件可选实现 `Diagnosable` 接口以提供诊断工具。工具注册和插件是否被配置为监控实例**解耦**。

### 本机工具 vs 远端工具

| 维度             | 本机工具（disk, cpu, memory, os） | 远端工具（redis, mysql）             |
| ---------------- | --------------------------------- | ------------------------------------ |
| 注册时机         | 启动时静态注册，绑定 Execute      | 启动时注册定义 + RemoteExecute       |
| 凭据来源         | 不需要                            | DiagnoseRequest.InstanceRef          |
| 执行方式         | 直接调用 Execute                  | 通过 DiagnoseSession 共享 Accessor   |
| 是否依赖用户配置 | 否，编译即注册                    | 否（定义注册不依赖），诊断时需要凭据 |

### InstanceIndex（fallback）

Agent 维护全局索引 `plugin::target → Instance`，作为凭据获取的 fallback 路径。主路径是 `DiagnoseRequest.InstanceRef`（由 Gather 直接传递）。

## 渐进式工具发现

### 问题

全量注入工具定义到 AI prompt 会导致 token 消耗过大、准确率下降。

### 方案：三个元工具

catpaw 不把具体工具注册为 AI function，而是提供三个元工具让 AI 按需发现：

1. **`list_tool_categories()`**：返回所有工具大类及摘要
2. **`list_tools(category)`**：返回指定大类下的具体工具定义（支持子类层级）
3. **`call_tool(name, tool_args)`**：执行指定工具，`tool_args` 为 JSON 字符串，与 `call_tool` 自身参数隔离

### 混合模式

告警来源插件的工具**直接注入**为 AI 可调用的 function（减少交互轮次），其他插件通过元工具按需发现。直接注入的工具和 `call_tool` 互斥。

| 方案                                 | 工具定义 token 消耗 |
| ------------------------------------ | ------------------- |
| 全量注入 200 个工具                  | ~40,000 tokens      |
| 混合模式（直接注入 12 + 3 元工具）   | ~3,600 tokens       |

## AI API 配置

- 统一使用 OpenAI-compatible API 协议（`/v1/chat/completions` + function calling）
- 通过 `base_url` 可对接 OpenAI、Azure、DeepSeek、Ollama、vLLM 等
- `api_key` 支持 `${ENV_VAR}` 引用
- 关键限制参数：`max_tokens`、`max_rounds`、`request_timeout`、`tool_timeout`、`max_concurrent_diagnoses`、`daily_token_limit`
- 状态持久化到 `state.d/diagnose_state.json`（daily token 计数 + cooldown），重启后恢复

## 触发机制

### DiagnoseRequest

由 `Gather()` 在产出告警 Event 时一并生成，携带：

- 触发告警的 Event 列表（聚合后可能多个）
- 结构化的 CheckSnapshot（check 名、当前值、阈值、状态）
- InstanceRef（触发告警的 Instance 引用，供创建 Accessor）
- 诊断配置（timeout、cooldown）

### 短窗口聚合

同一 target 在短时间窗口内（默认 5s）的多个告警**合并为一次诊断请求**。好处：

- 省 AI API 调用
- AI 可做关联分析（同时看到多个异常，更容易定位共同根因）
- 减少目标连接压力

### 触发流程

```text
Gather() 产出告警 Event
  │
  ├─ Status == Ok / Info → 跳过
  ├─ Status < min_severity → 跳过
  ├─ 同一 target 在 cooldown 内已诊断过 → 跳过
  │
  └─ 提交到 DiagnoseAggregator →
      1. 告警 Event 先正常推送 FlashDuty（不阻塞）
      2. 聚合窗口内收集同一 target 的其他告警
      3. 窗口关闭后提交到 DiagnoseScheduler
      4. Scheduler 按优先级调度，异步启动诊断
      5. 诊断完成后追加推送 FlashDuty
```

cooldown 粒度为 **target**（非 target+check），因为聚合后一次诊断覆盖该 target 的所有异常 check。

告警推送和诊断完全解耦：**诊断失败不影响告警本身**。

## 安全模型

**AI 只能诊断，不能修复。**

| 机制 | 说明 |
| --- | --- |
| 插件级白名单 | AI 只能调用插件显式注册的只读工具，无法执行任意命令 |
| 敏感信息过滤 | 工具返回值中过滤密码等敏感字段（如 redis 的 requirepass） |
| 输出截断 | 所有工具返回值上限 32KB，防止大量数据发送给 AI API |
| 审计日志 | 每次 AI 交互的完整 tool_call 链记录到日志 |
| 参数校验 | `call_tool()` 执行前校验参数符合工具定义的 schema |

**各插件安全分级参考**：

| 插件    | 安全工具（只读）                                       | 危险操作（不注册）                         |
| ------- | ------------------------------------------------------ | ------------------------------------------ |
| redis   | INFO, SLOWLOG GET, CLIENT LIST, CONFIG GET             | DEL, FLUSHDB, CONFIG SET, SHUTDOWN         |
| mysql   | SHOW STATUS/PROCESSLIST, EXPLAIN, INFORMATION_SCHEMA   | DROP, ALTER, UPDATE, DELETE                |
| disk    | iostat, df, /proc/diskstats                            | mkfs, fdisk, dd                            |
| cpu     | /proc/stat, top -bn1                                   | kill, renice                               |
| memory  | /proc/meminfo, free                                    | sysctl 写入                                |
| process | ps, /proc/[pid]/status                                 | kill, signal                               |
| os      | dmesg, uptime, uname                                   | shutdown, reboot                           |

## 诊断引擎核心流程

### Agent Loop

1. 创建 DiagnoseSession（含共享 Accessor）
2. 构建 system prompt（含告警详情、可用工具、诊断提示）
3. 构建工具集（直接注入 + 三个元工具）
4. 循环：发送给 AI → 收到 tool_call → 执行工具 → 返回结果 → 下一轮
5. AI 不再调用工具时，输出最终诊断报告

**关键控制**：

- **轮次上限**：`max_rounds`，倒数第二轮插入强制收尾指令
- **上下文窗口管理**：维护 token 估算计数器（中文 1 token ≈ 2 字符），接近上限时强制收尾
- **per-tool 超时**：单个工具最多 `tool_timeout`，避免挂起耗尽总超时
- **并发调度**：semaphore 控制全局并发，优先级：Critical > Warning > 多样性 > 首次告警
- **重试策略**：429/500/503 指数退避重试，401/403 直接失败
- **Graceful Shutdown**：SIGTERM 时 cancel 所有 in-flight 诊断的 context

### System Prompt 要点

- 告知 AI 告警详情（支持单 check 和多 check 聚合模板）
- 列出直接可用的工具 + 元工具使用说明
- 提示根因可能跨域（如数据库慢可能是磁盘 I/O 瓶颈）
- 区分远端目标和本机目标（远端时本机工具反映的是 catpaw 所在主机状态）
- 要求输出格式：诊断摘要 → 根因分析 → 建议操作

## 诊断报告

### FlashDuty 集成

- 告警 Event 先正常推送（不等诊断）
- 诊断完成后构造**新 Event**（同 AlertKey、新 EventTime、新 Description）追加推送
- Description 拼接：原始告警 + 诊断报告浓缩结论
- FlashDuty description 上限 2048 字节，超长时按优先级截断（保留摘要和建议）

### 本地诊断记录

完整诊断过程（所有 tool_call、原始返回值、AI 推理链、最终报告）存储在 `state.d/diagnoses/` 下，每次诊断一个 JSON 文件。FlashDuty 只放浓缩结论，用户通过 `catpaw diagnose show <id>` 查看完整记录。

保留策略：按天数 + 最大条数自动清理。

## 跨插件诊断

告警来源和根因经常不在同一个领域（如 MySQL 慢查询的根因可能是磁盘 IOPS 打满）。

方案：

1. **本机基础设施工具始终可用**：disk、cpu、memory 等编译时即注册，不依赖用户配置
2. **渐进式发现**：AI 通过 `list_tool_categories()` 按需探索
3. **prompt 引导**：提示 AI 根因可能跨域

## 错误处理与降级

| 场景                          | 处理方式                                             |
| ----------------------------- | ---------------------------------------------------- |
| AI API 不可用（401/403）      | 不可重试，诊断跳过，告警正常推送                     |
| AI API 暂时错误（429/500/503）| 指数退避重试，仍失败则跳过                           |
| 每日 token 额度耗尽           | 诊断跳过，不影响告警                                 |
| 超过 max_rounds               | 倒数第二轮已强制收尾，仍未完成则返回提示             |
| 上下文窗口接近上限            | 强制收尾                                             |
| 并发诊断数达上限              | 按优先级排队或丢弃                                   |
| 工具执行失败                  | 错误信息返回给 AI，AI 自行决定下一步                 |
| AI 输出格式不符               | 原样附加，不做格式校验                               |
| 全局 ai.enabled = false       | 诊断功能完全不加载，零开销                           |

核心原则：**诊断是锦上添花，告警是基本功能。诊断的任何故障不能影响告警推送。**
