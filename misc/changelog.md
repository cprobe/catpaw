# Changelog

## v0.9.0

- Refactor: 所有插件 Description 改为纯文本，不再使用 Markdown 格式
- Refactor: 结构化数据统一放入 Labels，动态字段使用 `_attr_` 前缀
- Refactor: AlertKey 计算跳过 `_attr_` 前缀的 labels，保证告警去重稳定性
- Refactor: sfilter 插件重命名为 scriptfilter
- Fix: engine 中 FirstFireTime 在重复告警时丢失的 bug
- Fix: goreleaser ldflags 路径与 go.mod 不一致导致版本号注入失败
- Fix: 全局 label 处理中 `$hostname` 的 continue 导致同一 label 中 `$ip` 和环境变量无法展开
- Fix: 配置文件中 disabled/disable_recovery_notification 注释位置不在 [instances.alerting] 段内
- Fix: http.toml 中 follow_redirects 默认值注释与实际行为不一致
- Improve: hostname 解析增加 5s 缓存，避免高频事件时重复系统调用
- Improve: 替换已废弃的 ioutil 为 os.ReadFile
- Improve: ping 插件并发控制统一使用 semaphore，与其他插件一致
- Improve: main.go 中 flag 变量名统一为小写（Go 惯例）
- Improve: Docker 基础镜像从 ubuntu:23.04（EOL）升级到 ubuntu:24.04（LTS）
- Improve: Docker 镜像内置默认 conf.d 配置目录
- Improve: goreleaser 打包路径改为 `conf.d/**` 确保递归包含子目录
- Improve: 补充 README 文档（命令行参数、部署指南、事件模型、插件开发等）

## v0.8.0

- New: refactor http/net/ping plugin configurations

## v0.7.0

- New: add procnum plugin

## v0.6.0

- New: add filechange plugin

## v0.4.0

- New: add sfilter plugin
- Fix: remove configuration keywords of plugin journaltail

## v0.3.0

- New: add mtime plugin

## v0.1.2

- New: add journaltail plugin
- New: add script greplog.sh
