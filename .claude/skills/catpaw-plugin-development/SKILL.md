---
name: catpaw-plugin-development
description: 为 Catpaw 项目开发、修改、补全或评审插件。用户只要提到 Catpaw 插件、监控检查、plugins 目录下的新插件、补充 agent 注册、生成 conf.d 示例、实现 Gather/Init/partials、对照现有插件仿写或重构插件时，都应使用这个 skill。
---

# Catpaw Plugin Development

按 Catpaw 现有插件体系工作，不要发明新的注册方式、配置格式或事件模型。

## 先读哪些文件

先读这些文件，再开始实现：

1. [`docs/plugin-development.md`](../../../docs/plugin-development.md)
2. [`plugins/plugins.go`](../../../plugins/plugins.go)
3. [`agent/agent.go`](../../../agent/agent.go)
4. 按场景读取一个最接近的现有插件，选择规则见 [`references/examples.md`](references/examples.md)

如果任务只涉及修改已有插件，优先读目标插件目录下的 `.go`、`design.md` 和对应 `conf.d/p.<name>/<name>.toml`。

## 默认交付物

除非用户明确只要方案或解释，否则直接落地代码，通常至少包含：

- `plugins/<name>/<name>.go`
- `agent/agent.go` 中的匿名导入
- `conf.d/p.<name>/<name>.toml`

复杂插件可以补：
 - `plugins/<name>/design.md`
- `plugins/<name>/<name>_test.go`

## 开发流程

### 1. 明确插件形态

先确定：

- 插件名是什么，`pluginName` 必须与目录名和配置目录名一致
- 有几个检查维度，每个维度的 `check` label 是什么
- `target` 应该是什么稳定标识
- 是否需要并发
- 是否需要 `partials`
- 是否需要 `Init()` 做参数校验和默认值填充

一个维度对应一个独立 event。不要把多个检查结果塞进同一个 event。

### 2. 选参考实现

不要从零设计。总是找最相近的现有插件比照实现：

- 简单单维度阈值类：`zombie`、`uptime`
- 多 target 并发类：`ping`、`http`、`net`
- 文件/进程状态类：`filefd`、`procfd`、`filecheck`
- 日志/文本匹配类：`logfile`、`journaltail`、`systemd`
- 需要 `partials`：`ping`、`http`、`net`

### 3. 实现插件结构

遵循现有骨架：

```go
const pluginName = "myplugin"

type Instance struct {
    config.InternalConfig
    // instance fields
}

type MyPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}

func init() {
    plugins.Add(pluginName, func() plugins.Plugin {
        return &MyPlugin{}
    })
}

func (p *MyPlugin) GetInstances() []plugins.Instance { ... }
func (ins *Instance) Init() error { ... }
func (ins *Instance) Gather(q *safe.Queue[*types.Event]) { ... }
```

只有在确实需要模板复用时才实现 `ApplyPartials() error` 并在 plugin 顶层增加 `Partials []Partial`。

### 4. 遵守事件约定

所有插件都要遵守这些约定：

- `check` label 必填，格式为 `<plugin>::<dimension>`
- `target` label 必填，值要稳定、可读
- 动态附加字段放进 `event.SetAttrs(map[string]string{...})`，不参与 AlertKey 计算
- `Description` 只写纯文本，不写 Markdown
- 告警标题由 FlashDuty 输出层根据事件自动生成（有 target 时用 `[TPL]${check} ${from_hostip} ${target}`，否则用 `[TPL]${check} ${from_hostip}`）
- 正常态要显式产出 `types.EventStatusOk` 或默认 OK event，以支持恢复

如果某个检查维度有独立阈值、标题规则或属性，就给它独立 event 和独立 builder，避免在 `Gather()` 里堆大量分支。

### 5. 做好 `Init()`

`Init()` 用来做这几类事：

- 校验阈值关系，例如 `warn < critical`
- 处理默认值
- 预计算运行时字段
- 编译正则、解析地址、检查互斥配置

`Init()` 返回错误时，实例不会启动。校验应尽量具体，错误信息直接指出字段和值。

### 6. 做好 `Gather()`

`Gather()` 只负责采集和组装 event：

- 空配置尽早返回
- 每个 target/维度独立判断
- 出错时返回能定位问题的 critical/warning event
- 成功时也尽量补充关键 attr labels，便于告警渲染和排查

如果使用 goroutine，并发单元内部要自己做 panic 保护，参考 `ping`、`http`。

### 7. 接入项目

实现完插件后同步完成：

1. 在 [`agent/agent.go`](../../../agent/agent.go) 的 import 块里添加匿名导入
2. 新增默认配置文件到 `conf.d/p.<name>/<name>.toml`
3. 确认配置字段的 `toml` tag 与示例配置一致

不要只写插件代码而漏掉导入或默认配置。

## `partials` 何时使用

只有多个 instance 共享大量相同配置时才使用 `partials`。常见信号：

- HTTP 请求参数很多
- 网络探测参数很多
- 同类实例只改 target 或少量阈值

实现时遵守当前项目习惯：

- plugin 顶层有 `Partials []Partial`
- instance 上有 `Partial string \`toml:"partial"\``
- `ApplyPartials()` 只在 instance 字段为空值时回填 partial 值

不要让 partial 覆盖 instance 显式配置。

## 检查清单

提交前至少自检这些点：

- 插件名、目录名、配置目录名是否一致
- `GetInstances()` 是否正确返回 `[]plugins.Instance`
- `Init()` 和 `Gather()` 的接收者是否是 `*Instance`
- `check`/`target` 是否完整
- `agent/agent.go` 是否已注册
- `conf.d/p.<name>/<name>.toml` 是否可作为最小可用示例
- 新逻辑是否有明显可测分支，若有应补测试

## 输出要求

如果用户要求“写一个插件”，最终输出应优先是已修改的仓库文件，而不是只给伪代码。

如果用户要求“设计一个插件”，输出至少要包含：

- 建议的 `Instance`/`Plugin` 结构
- 维度划分和 `check` label 设计
- 配置样例
- 推荐参考插件
