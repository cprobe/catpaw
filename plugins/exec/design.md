# exec 插件设计

## 概述

执行外部命令/脚本，将输出解析为 catpaw 事件。支持两种模式：JSON（脚本输出 JSON 事件数组）和 Nagios（兼容 Nagios 插件的退出码 + 状态行格式）。

**核心场景**：

1. **自定义检查**：catpaw 内置插件不覆盖的场景，用户写脚本实现任意检查逻辑
2. **Nagios 迁移**：直接复用现有的 Nagios 插件（check_load、check_disk 等），无需重写
3. **多脚本批量执行**：commands 支持 glob 模式，自动发现并执行目录下所有脚本

**参考**：Telegraf `inputs.exec`、Sensu check system。

## 检查维度

取决于脚本输出：

- **JSON 模式**：脚本自己定义 check/target/severity/description，插件透传
- **Nagios 模式**：check label 固定为 `exec::nagios`，target 为命令字符串；退出码映射为 0=Ok, 1=Warning, 2=Critical
- **执行失败**：check label 为 `exec::error`，target 为命令字符串

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Commands    []string          // 要执行的命令列表（支持 glob）
    Timeout     config.Duration   // 单条命令超时，默认 10s
    Concurrency int               // 最大并发执行数，默认 5
    Mode        string            // "json"（默认）或 "nagios"
    EnvVars     map[string]string // 传递给命令的环境变量
}
```

## Init() 校验

1. `commands` 为空时静默跳过（不报错）
2. `mode` 必须是 `json` 或 `nagios`
3. `timeout` 默认 10s，`concurrency` 默认 5

## Gather() 逻辑

1. 展开 glob：`commands` 中的路径可能包含 `*`，展开为实际脚本路径
2. 并发执行每条命令，带 timeout 控制
3. **JSON 模式**：要求退出码 0 + stdout 为合法 JSON 事件数组 `[{...}, ...]`
4. **Nagios 模式**：解析 stdout 第一行为 `STATUS TEXT | perfdata`，退出码转 severity

### 安全防护

- stdout 上限 1MB，stderr 上限 256KB（`limitedWriter` 防 OOM）
- 超时后自动 kill 进程（`cmdx.RunTimeout`）
- panic recovery 保护每个 goroutine

## 跨平台兼容性

| 平台 | 支持 |
| --- | --- |
| Linux | 完整支持 |
| macOS | 完整支持 |
| Windows | 完整支持（命令路径需用正斜杠或双反斜杠） |
