# 设计与实现原则

- catpaw 配合 Flashduty、PagerDuty 等 On-call 产品使用
- catpaw 的实现可以参考 Sensu、Nagios 等同类产品，站在巨人的肩膀上，自然要超越他们
- catpaw 的职能不能和 Prometheus + Exporter 生态重叠（包括 node-exporter、redis-exporter、mysqld-exporter 等），不做指标采集，不暴露 `/metrics`
- catpaw 可以配置服务认证信息（Redis 密码、MySQL 账号等），但仅用于按需诊断，不用于持续采集。详见 [产品边界](product-boundary.md)
- catpaw 应该更加关注异常，而不是关注历史指标趋势

## 1. 设计优雅、灵活、易用

- 配置结构清晰，层次分明
- 支持多 instance、多检查维度的灵活组合
- 通过 partial 模式实现配置复用，减少重复
- 每个配置字段的语义明确，不存在二义性

## 2. 实现安全、可靠、鲁棒

- 所有外部输入（配置值、命令输出、网络响应）做边界校验
- goroutine 必须有 panic recovery
- 文件/网络操作必须有 timeout
- 不信任任何外部数据的格式和大小，做截断和限制

## 3. 匠人精神、精雕细琢

- 代码简洁但不简陋，每一行都有存在的理由
- 命名精准，注释只解释"为什么"而非"是什么"
- 错误信息要对用户有帮助，包含上下文（如哪个 target、哪个文件）
- 日志分级合理：正常路径 Debug，异常路径 Error，关键状态变化 Info

## 4. 跨平台兼容

- 尽量考虑 Linux、Windows、macOS 三平台兼容性
- 平台特有逻辑通过 build tags 隔离（`_windows.go`、`_notwindows.go`）
- 不可用的功能在不支持的平台上优雅跳过（返回明确错误而非 panic）
- 路径处理使用 `filepath` 而非硬编码分隔符

## 5. 开箱即用的默认配置

- 默认配置针对 Linux 生产环境优化，下载即可运行
- 所有可选配置都有合理默认值，不配置也能正常工作
- 默认阈值偏保守而非激进
- 配置文件自身即文档，顶部有"最小可用示例"注释块

## 6. 告警质量优先：宁可漏报，不可误报

- 默认阈值宁可保守，不可激进
- 对有波动的指标（CPU、内存），支持 `for_duration` 持续确认，而非单次触发
- 区分"真正异常"和"暂时获取不到数据"，后者不应直接产出告警事件
- 恢复事件只在之前确实发过告警时才发送，避免无中生有的 recovery 噪音

## 7. 自身故障可感知：Fail-open 而非 Fail-silent

- 插件 Gather 失败时应产出告警事件，让用户在 FlashDuty 端能感知采集异常
- Init 阶段的配置错误要清晰报错，说明如何修正
- panic recovery 后应产出事件，而非仅打日志
- 监控工具的沉默比被监控系统的故障更危险

## 8. 采集开销可控：监控不能成为负担

- 每个插件有 `timeout` 机制，避免卡死
- 并发控制有上限（`concurrency` 配置）
- 避免频繁文件 I/O 和大内存分配（如不一次性读整个文件到内存）
- 二进制体积和运行时内存占用保持在合理水平

## 9. 防止 Goroutine 泄漏：inFlight 防重入 + 超时保护

对于可能 hang 住的检查目标（如 NFS 挂载、不可达的远程主机），必须防止下一轮 Gather 对同一 target 重复创建 goroutine，导致 goroutine 无限积累。

**标准做法**（参考 disk 插件）：

- **inFlight 记录**：Gather 开始时将 target 标记为"执行中"（`sync.Map`），Gather 结束后移除
- **防重入**：下一轮 Gather 发现 target 仍在 inFlight 中，跳过该 target，不创建新 goroutine
- **超时检测**：如果 inFlight 持续时间超过阈值，产出 hung 告警事件
- **恢复通知**：target 从 hung 状态恢复后，产出 recovery 事件
- **Gather 整体超时**：`wg.Wait()` 配合 `time.After` 超时退出，不无限等待

**适用场景**：涉及文件系统操作（disk、filecheck）、网络连接（可能 DNS 阻塞）、外部命令执行（exec、scriptfilter）等所有可能阻塞的插件。对于已知不会 hang 的纯内存计算型插件（如 mem、cpu）可以不做。

## 10. 配置向后兼容：升级不破坏现有用户

- 字段重命名/移除要经过 deprecation 周期（至少一个版本的兼容）
- 新增字段必须有合理默认值，旧配置文件不加新字段也能正常工作
- 插件目录名一旦确定不轻易改
- 配置语义一旦发布即为承诺

## 11. 命名和约定一致：一次学会，处处适用

用户学会一个插件的配置后，配置其他插件应该零学习成本。

- `check` label 统一格式：`plugin::dimension`
- `target` label 含义一致：被检查的对象标识
- 阈值命名一致：
  - `warn_ge` / `critical_ge` — 大于等于触发
  - `warn_lt` / `critical_lt` — 小于触发
  - `warn_within` / `critical_within` — 时间窗口内触发
- Description 统一风格：纯文本，先说实际值再说阈值（如 "usage 94.2% >= critical threshold 90%"）
- 恢复事件统一描述："everything is ok"
- `attrs`（通过 `SetAttrs` 设置）携带动态度量数据，不参与 AlertKey 计算

## 12. 每个插件必须有测试覆盖

- `Init()` 的配置校验逻辑必须有测试（合法/非法输入边界）
- 核心判断逻辑（阈值比较、状态转换）必须有测试
- 不要求 100% 覆盖率，但决策路径必须有测试保护

## 13. 优雅降级：局部失败不影响全局

- 一个 target 失败不影响同 instance 内其他 targets
- 一个检查维度失败不影响同 instance 内其他维度
- 一个 instance 失败不影响同插件内其他 instances
- 一个插件失败不影响其他插件

## 14. AI 诊断工具设计：为 LLM 多轮对话优化

诊断由 LLM 驱动，每轮 tool call 意味着一次完整的 AI API 往返（1-3s 延迟 + token 消耗）。工具设计必须围绕**减少轮次**和**提高每轮信息密度**展开。

### 14.1 PreCollector 预注入：省掉信息收集首轮

为插件注册 `PreCollector`，在 AI 对话前自动采集基线数据，注入系统提示。LLM 从第一轮就能直接进入分析，而非花一轮去调"全景查看"工具。两类场景：

**插件预采集**（plugin PreCollector）：对触发告警的插件采集其核心数据。
- Remote 插件（Redis/MySQL 等）：在 session 建立后调用 accessor（如 `INFO all`、`SHOW GLOBAL STATUS`）
- Local 插件（cpu/mem/disk 等）：直接调用 OS API 采集 overview 数据

**系统基线**（system baseline）：对非 remote 诊断，引擎自动运行所有注册为 `BaselinePlugin` 的 PreCollector（cpu/mem/disk），排除触发插件避免重复。结果以 `<system_baseline>` 标签注入，让 LLM 在首轮就具备系统全貌，减少跨域探查的额外轮次。

- 预采集数据应选择信息密度最高的单一操作（覆盖面广、开销可控）
- 预采集失败不阻断诊断流程，LLM 仍可通过工具获取数据
- 插件预采集用 `<pre_collected>` 标签，系统基线用 `<system_baseline>` 标签
- 瓶颈在 LLM API 往返延迟（1-3s/轮），而非本地工具执行耗时（ms 级）——即使 local 工具执行很快，减少一轮 tool call 仍可省下一次 API 往返

### 14.2 合并"必定一起用"的工具

如果两个工具在所有诊断场景中都会被连续调用（无论什么告警类型，A 之后必调 B），应合并为一个工具。

- 合并前提：两者返回数据量之和仍可控（< 4KB），不会浪费 token
- 典型示例：Redis `MEMORY DOCTOR` + `MEMORY STATS` → `redis_memory_analysis`
- 反例：`INFO` 和 `SLOWLOG` 不应合并——它们适用于不同告警场景

### 14.3 消除冗余工具：减少 LLM 选择噪音

工具数量越多，LLM 在每轮需要决策的搜索空间越大，错误选择的概率越高。如果一个工具的输出可被另一个工具的参数变体完全覆盖，应删除前者。

- 典型示例：`redis_dbsize` 被 `redis_info section=keyspace` 覆盖
- 判断标准：如果移除该工具后，LLM 在任何场景下都能通过其他工具获得等价信息，则应删除

### 14.4 诊断路线图：引导 LLM 首轮并行调用

通过 `SetDiagnoseHints` 注册按告警类型分类的工具调用建议路径。LLM 在第一轮就能并行调用 2-3 个最相关的工具，而非逐个试探。

- 路线图应按告警类型组织（内存告警 → 工具 A + B，延迟告警 → 工具 C + D）
- 明确提示"首轮建议并行调用"，利用引擎的并行 tool call 能力
- 路线图是建议而非强制，LLM 仍可根据预采集数据的实际内容调整策略

### 14.5 工具 Description 精准：LLM 靠它做选择

LLM 根据 tool description 决定是否调用及如何传参。描述模糊 → 选错工具 → 浪费轮次。

- 带参数的工具：在 description 中列出所有合法参数值及其含义
- 有前置条件的工具：说明何时返回空/需要先做什么（如 `LATENCY LATEST` 需要开启 `latency-monitor-threshold`）
- 与预采集数据重叠的工具：明确提示"预采集数据已包含该信息，仅在需要刷新时使用"
