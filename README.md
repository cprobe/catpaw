# catpaw

catpaw 是一个轻量的事件监控工具：负责探测异常并产出标准事件。  
它通常与 Flashduty 配合使用（catpaw 产出事件，Flashduty 负责告警分发），也可以对接你自己的事件接收服务。

## 核心特点

- 轻量无重依赖，部署简单
- 插件化架构，按需启用
- 配置直观，适合"快速补监控"
- 适合监控系统自监控，避免循环依赖

## 插件列表

| 插件 | 说明 |
| --- | --- |
| `disk` | 磁盘空间、inode、可写性检查 |
| `exec` | 执行脚本/命令并按约定输出产生事件（支持 JSON 和 Nagios 模式） |
| `filecheck` | 文件存在性、mtime、checksum 检查 |
| `http` | HTTP 可用性、状态码、响应体、证书过期检查 |
| `journaltail` | 通过 journalctl 增量读取日志并匹配关键行（仅 Linux） |
| `net` | TCP/UDP 连通性与响应时间检查 |
| `ping` | ICMP 可达性、丢包率、时延检查 |
| `procnum` | 进程数量检查（支持多种查找方式） |
| `scriptfilter` | 执行脚本并按输出行过滤匹配告警 |

## 典型场景

- 不想引入大型监控系统，但需要可靠地覆盖关键风险点
- 对现有监控系统做"旁路自监控"，降低单点失效风险
- 对日志、命令输出、文本事件做快速匹配告警

## 快速开始

### 安装

从 [GitHub Releases](https://github.com/cprobe/catpaw/releases) 下载对应平台的二进制。

### 配置

1. 编辑 `conf.d/config.toml`，填入 FlashDuty 的 `integration_key`。当然也可以不用 Flashduty，那就需要自己写一个事件接收、告警分发的服务。
2. 在 `conf.d/p.<plugin>/` 下启用需要的插件配置
3. 启动 catpaw

```bash
./catpaw
```

测试模式（事件输出到终端，不发送到 FlashDuty）：

```bash
./catpaw -test
```

更多命令行参数见 [命令行参数](docs/cli.md)。

## 对接 Flashduty

1. 注册 [Flashduty](https://console.flashcat.cloud/)
2. 在集成中心创建"标准告警事件"集成，获取 webhook URL
3. 填入 `conf.d/config.toml` 的 `flashduty.url` 字段

产品介绍：[Flashduty](https://flashcat.cloud/product/flashduty/)

## 配置说明

- 全局配置：`conf.d/config.toml`
- 插件配置：`conf.d/p.<plugin>/*.toml`（每个目录可放多个 `.toml` 文件，内容合并加载）
- 支持 `SIGHUP` 热加载插件配置

```bash
kill -HUP $(pidof catpaw)
```

## 详细文档

| 文档 | 说明 |
| --- | --- |
| [命令行参数](docs/cli.md) | 完整的命令行参数说明 |
| [部署指南](docs/deployment.md) | 二进制部署、systemd 服务、Docker 部署 |
| [事件数据模型](docs/event-model.md) | Event 结构、Labels 设计、AlertKey 规则、告警生命周期 |
| [插件开发指南](docs/plugin-development.md) | 如何新增一个 catpaw 插件 |

## 交流

可加微信 `picobyte` 进群交流，备注 `catpaw`。
