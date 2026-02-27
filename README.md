# catpaw

catpaw 是一个轻量的事件监控工具：负责探测异常并产出标准事件。  
它通常与 Flashduty 配合使用（catpaw 产出事件，Flashduty 负责告警分发），也可以对接你自己的事件接收服务。

## 核心特点

- 轻量无重依赖，部署简单
- 插件化架构，按需启用
- 配置直观，适合“快速补监控”
- 适合监控系统自监控，避免循环依赖

## 插件列表

当前内置插件如下（与代码保持一致）：

- `disk`：磁盘空间、inode、可写性检查
- `exec`：执行脚本/命令并按约定输出产生事件
- `filecheck`：文件存在性、mtime、checksum、大小等检查
- `http`：HTTP 可用性、状态码、内容匹配检查
- `journaltail`：通过 `journalctl` 增量读取日志并匹配关键行
- `net`：TCP/UDP 连通性与响应能力检查
- `ping`：ICMP 可达性、丢包率、时延检查
- `procnum`：进程数量检查（支持多种查找方式）
- `scriptfilter`：执行脚本并按输出行过滤匹配告警

## 典型场景

- 不想引入大型监控系统，但需要可靠地覆盖关键风险点
- 对现有监控系统做“旁路自监控”，降低单点失效风险
- 对日志、命令输出、文本事件做快速匹配告警

## 安装

从 [GitHub Releases](https://github.com/cprobe/catpaw/releases) 下载对应平台的二进制。

## 快速开始

1. 准备配置目录（默认使用 `conf.d`）
2. 在 `conf.d/config.toml` 配置全局参数与 `flashduty.url`
3. 在 `conf.d/p.<plugin>/` 下启用你需要的插件配置（例如 `conf.d/p.http/`）
4. 启动 catpaw

可通过 `./catpaw --help` 查看可用参数。

## 对接 Flashduty（推荐）

先注册 Flashduty：

- [Flashduty 产品介绍](https://flashcat.cloud/product/flashduty/)
- [Flashduty 免费注册](https://console.flashcat.cloud/)

然后在 Flashduty 集成中心创建“标准告警事件”集成，获取 webhook，填入 `conf.d/config.toml` 的 `flashduty.url` 字段。

## 配置说明

- 每个插件都有独立配置目录：`conf.d/p.<plugin>/`
- 每个目录下可放置一个或多个 `.toml` 文件
- 示例：`conf.d/p.http/`、`conf.d/p.procnum/`、`conf.d/p.scriptfilter/`
- 插件示例配置文件内都带有详细注释，建议从示例改起

## 交流

可加微信 `picobyte` 进群交流，备注 `catpaw`。
