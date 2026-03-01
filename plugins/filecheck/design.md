# filecheck 插件设计

## 概述

监控文件的存在性、修改时间和内容完整性（SHA-256 校验和）。当文件消失、被意外修改或长时间未更新时产出告警事件。

**核心场景**：

1. **关键文件被篡改**：配置文件、证书文件被意外或恶意修改（checksum 维度）
2. **日志/数据文件停更**：应用静默挂死，日志文件的 mtime 停止更新（mtime stale 模式）
3. **文件意外消失**：备份文件被误删、挂载点丢失导致文件不可见（existence 维度）
4. **关键文件变更审计**：coredump 文件出现（mtime changed 模式）

**参考**：Nagios `check_file_age`、OSSEC file integrity monitoring。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 存在性 | `filecheck::existence` | targets 拼接 | 文件/目录是否存在且可访问 |
| 修改时间 | `filecheck::mtime` | targets 拼接 | 文件 mtime 是否在/超出时间窗口 |
| 校验和 | `filecheck::checksum` | targets 拼接 | 文件 SHA-256 是否发生变化 |

- **targets 支持 glob**：如 `/var/log/*.log`、`/etc/nginx/conf.d/*`
- **目录自动递归**：target 是目录时自动遍历其下所有文件（上限 10000 个）
- **每个维度独立事件**——existence 告警不影响 mtime 检查

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Targets   []string       // 必填：文件/目录/glob 路径列表
    Mtime     MtimeCheck     // mtime 检查（mode: "changed" 或 "stale"）
    Checksum  ChecksumCheck  // SHA-256 校验和变化检测
    Existence ExistenceCheck // 文件存在性检查
}
```

### Mtime 模式说明

- **`changed`**（默认）：检测在 `time_span` 时间内**被修改过**的文件 → 常用于监控 coredump、异常日志出现
- **`stale`**：检测超过 `time_span` 时间**未被修改**的文件 → 常用于监控日志停更、数据文件生成中断

### Checksum 行为

- 首次执行记录 baseline（不告警）
- 后续执行对比 SHA-256，变化则告警
- 大文件保护：`max_file_size` 默认 50MB，超过的文件跳过

## 跨平台兼容性

| 平台 | 支持 |
| --- | --- |
| Linux | 完整支持 |
| macOS | 完整支持 |
| Windows | 完整支持（路径分隔符自动适配） |
