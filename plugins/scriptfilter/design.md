# scriptfilter 插件设计

## 概述

执行外部命令，对其 stdout 逐行做关键字/正则过滤，命中时产出告警事件。相当于 `command | grep pattern` 的告警化封装。

**核心场景**：

1. **自定义数据源 + 关键字检测**：任何能输出文本的命令/脚本都可以作为数据源
2. **系统命令输出监控**：如 `dmesg`、`last`、`ss` 等系统命令的输出中检测异常
3. **弥补 journaltail 和 logfile 的空白**：不在 journal 中也不在文件中的数据，通过脚本获取后过滤

**与 exec 插件的关系**：exec 插件要求脚本自己输出结构化事件（JSON 或 Nagios 格式），scriptfilter 只需要脚本输出纯文本，由 catpaw 负责过滤和告警。scriptfilter 更适合"已有脚本但不想改输出格式"的场景。

**参考**：Nagios `check_log` + `check_procs`（管道组合）。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 文本匹配 | `scriptfilter::match` | 命令 basename | 命令输出中是否命中关键字 |

- **target** 自动取命令的 basename（如 `/opt/scripts/health.sh` → `health.sh`）
- 命中 0 行 → Ok 事件；命中 N 行 → severity 告警

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Command       string          // 必填：要执行的命令
    Timeout       config.Duration // 命令超时，默认 10s
    FilterInclude []string        // 必填：行级 include 规则（glob + /regex/）
    FilterExclude []string        // 可选：行级 exclude 规则
    MaxLines      int             // 告警描述中最多展示的匹配行数，默认 10
    Match         MatchCheck      // severity 和 title_rule
}
```

## Init() 校验

1. `command` 为空时静默跳过（不报错，Gather 也直接返回）
2. `filter_include` 不能为空（command 非空时）
3. 编译 include/exclude 为 filter（支持 glob 和 `/regex/` 混用）
4. `match.severity` 默认 Warning
5. `timeout` 默认 10s，不能为负

## Gather() 逻辑

1. 执行命令，带 timeout
2. 命令执行失败（找不到、超时、权限不足）→ emit Critical 错误事件
3. 非零退出码 **不视为错误**——继续处理 stdout（许多监控脚本用退出码传递状态信息）
4. 逐行对 stdout 做 include/exclude 过滤
5. 命中 0 行 → emit Ok
6. 命中 N 行 → emit severity（描述中展示最多 max_lines 行）

### 与 journaltail 的对比

| 特性 | scriptfilter | journaltail |
| --- | --- | --- |
| 数据来源 | 任意命令的 stdout | systemd journal |
| 增量/全量 | 每次全量执行命令 | cursor 增量读取 |
| 状态保持 | 无状态 | cursor 跨轮次保持 |
| 适用场景 | 一次性命令输出 | 持续产生的日志流 |

## 跨平台兼容性

| 平台 | 支持 |
| --- | --- |
| Linux | 完整支持 |
| macOS | 完整支持 |
| Windows | 完整支持（命令需用 Windows 语法） |
