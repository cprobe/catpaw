# 示例选择

按任务类型挑最接近的现有实现，不要一次性读完整个 `plugins/` 目录。

## 最小骨架

- `plugins/zombie/zombie.go`
- `plugins/uptime/uptime.go`

适合：

- 单机指标
- 单维度或少量维度
- 简单阈值判断

## 多 target / 并发采集

- `plugins/ping/ping.go`
- `plugins/http/http.go`
- `plugins/net/net.go`

适合：

- 一个 instance 下检查多个 target
- 需要并发控制
- 需要对单个 target 产出多个维度 event

## `partials` 模板复用

- `plugins/ping/ping.go`
- `conf.d/p.ping/ping.toml`
- `conf.d/p.http/http.toml`
- `conf.d/p.net/net.toml`

适合：

- 多个 instance 共享大段配置
- 同类实例只覆盖少数参数

## 文件/进程/系统状态

- `plugins/filefd/filefd.go`
- `plugins/procfd/procfd.go`
- `plugins/filecheck/filecheck.go`

适合：

- 本地资源状态检查
- 阈值与属性输出

## 日志与文本匹配

- `plugins/logfile/logfile.go`
- `plugins/journaltail/journaltail.go`
- `plugins/systemd/systemd.go`

适合：

- 文本扫描
- 模式匹配
- 非结构化输入转 event

## 设计文档

如果要先做方案再编码，优先看：

- `plugins/ping/design.md`
- `plugins/http/design.md`
- 目标插件目录下的 `design.md`
