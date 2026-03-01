# journaltail 插件设计

## 概述

持续跟踪 systemd journal 日志，按关键字/正则匹配异常行，命中时产出告警事件。类似 `journalctl -f` + `grep`，但以 cursor 机制保证不漏不重。

**核心场景**：

1. **内核级异常**：OOM Kill、kernel panic、soft lockup、I/O error 等只出现在 journal 中的关键信号
2. **服务异常日志**：systemd 管理的服务（nginx、sshd、docker 等）输出的错误日志
3. **硬件故障早期信号**：MCE（machine check exception）、磁盘坏扇区（medium error）等

**与 logfile 插件的关系**：logfile 监控普通文本日志文件（如 `/var/log/app.log`），journaltail 监控 systemd journal（二进制格式，通过 `journalctl` 命令访问）。两者互补，不重叠。

**参考**：Nagios `check_journal`、`journalctl --since`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 日志匹配 | `journaltail::match` | units 或 filter 摘要 | 新增日志中是否命中关键字 |

- **target** 自动生成：配了 units 取 unit 名，否则取 filter_include 摘要
- 命中 0 行 → Ok 事件；命中 N 行 → severity 告警（附带匹配内容）

## 数据来源

### journalctl 命令

```bash
journalctl --no-pager --no-tail --show-cursor \
    --after-cursor <cursor> \
    [--unit <unit>] \
    [--priority <priority>]
```

- **cursor 机制**：首次用 `--since <启动时间>`，后续用 `--after-cursor <上次游标>` 实现增量读取
- **预过滤**：`--unit` 和 `--priority` 在 journalctl 层面过滤，减少需要传输和匹配的数据量
- **后过滤**：`filter_include` / `filter_exclude` 在 catpaw 中逐行匹配（支持 glob + `/regex/`）

### 为什么调命令而非直接读 journal 文件

- systemd journal 是二进制格式（`.journal` 文件），直接解析复杂度高且需要依赖 `libsystemd`
- `journalctl` 是标准接口，所有 systemd 发行版都有
- cursor 机制天然支持增量读取

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Units         []string        // journalctl --unit 预过滤
    Priority      string          // journalctl --priority 预过滤（如 "emerg..err"）
    FilterInclude []string        // 必填：行级 include 规则（glob + /regex/）
    FilterExclude []string        // 可选：行级 exclude 规则
    MaxLines      int             // 告警描述中最多展示的匹配行数，默认 10
    Timeout       config.Duration // journalctl 执行超时，默认 30s
    Match         MatchCheck      // severity 和 title_rule
}
```

## Init() 校验

1. 仅 Linux 支持
2. `filter_include` 不能为空
3. 编译 include/exclude 为 filter（支持 glob 和 `/regex/` 混用）
4. 检测 `journalctl` 是否存在
5. `match.severity` 默认 Warning
6. 记录启动时间作为首次 `--since`

## Gather() 逻辑

```
1. 构建 journalctl 命令（cursor 或 since + units + priority）
2. 执行命令，带 timeout
3. 解析输出：提取日志行 + 提取末尾 cursor
4. 对每行执行 include/exclude 过滤
5. 更新 cursor（仅在命令成功时更新）
6. 命中 0 行 → emit Ok
7. 命中 N 行 → emit severity（描述中展示最多 max_lines 行）
```

### 关键行为

1. **Cursor 保证不漏不重**——journalctl 的 cursor 是精确的日志位置标记，跨重启也可靠
2. **命令失败不更新 cursor**——防止跳过未读日志
3. **退出码 1 + 空 stderr 视为正常**——journalctl 在无匹配条目时返回 1
4. **stdout 上限 1MB**——防止日志爆发时 OOM

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 依赖 journalctl |
| macOS | 不支持 | Init 返回错误 |
| Windows | 不支持 | Init 返回错误 |
