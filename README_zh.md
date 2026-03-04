[English](README.md) | 中文

# 🐾 catpaw

catpaw 是一个轻量的监控 Agent，具备 **AI 智能诊断**能力。
它通过插件化检查探测异常、产出标准事件，并在告警触发时自动调用 AI 进行根因分析——内置 70+ 诊断工具，覆盖系统、网络、存储、安全等各个维度。

事件可推送到任意告警平台（Flashduty、PagerDuty 或任何 HTTP 端点），也可直接输出到控制台快速验证。

## ✨ 核心特点

- 🪶 **轻量无重依赖** — 单二进制，部署简单
- 🔌 **插件化监控** — 25+ 检查插件，按需启用
- 🤖 **AI 自动诊断** — 告警触发后自动分析根因
- 💬 **AI 交互排障** — 命令行对话式排障，AI + 工具联动
- 🩺 **主动健康巡检** — 按需对目标执行 AI 驱动的深度检查
- 🛠️ **70+ 诊断工具** — 系统、网络、存储、安全、进程、内核全覆盖
- 🔗 **MCP 集成** — 通过 [Model Context Protocol](https://modelcontextprotocol.io/) 接入 Prometheus、Jaeger、CMDB 等外部数据源
- 📡 **灵活通知** — 控制台、通用 WebAPI、Flashduty、PagerDuty，可同时开启多个
- 🔄 **适合自监控** — 监控系统的监控系统，避免循环依赖

## 🏗️ 架构概览

```text
┌─────────────────────────────────────────────────────────────────┐
│                        catpaw agent                             │
│                                                                 │
│  ┌─────────────┐   告警    ┌──────────────┐    AI + 工具       │
│  │  25+ 检查   │ ────────── │  AI 诊断    │ ──────────────┐   │
│  │    插件     │   触发     │    引擎     │               │   │
│  └──────┬──────┘            └──────────────┘               │   │
│         │                                                  ▼   │
│         │ 事件      ┌──────────────┐         ┌───────────────┐ │
│         └────────── │   通知渠道   │         │  70+ 诊断    │ │
│                     │  （多选）    │         │     工具     │ │
│                     └──────────────┘         └───────┬───────┘ │
│                                                      │         │
│  ┌─────────────┐                            ┌────────┴───────┐ │
│  │  AI Chat    │ ───── 交互式排障 ───────── │  MCP 外部     │ │
│  │  (命令行)   │                            │  数据源       │ │
│  └─────────────┘                            └────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

## 🔍 检查插件

| 插件 | 说明 |
| --- | --- |
| `cert` | TLS 证书有效期检查（远程 TLS + 本地文件，支持 STARTTLS、SNI、glob） |
| `conntrack` | 连接跟踪表使用率监控，预防表满导致静默丢包（Linux） |
| `cpu` | CPU 使用率、归一化每核 Load Average 检查 |
| `disk` | 磁盘空间、inode、可写性检查 |
| `dns` | DNS 解析检查 |
| `docker` | Docker 容器监控（运行状态、频繁重启、健康检查、CPU/内存） |
| `exec` | 执行脚本/命令产生事件（支持 JSON 和 Nagios 模式） |
| `filecheck` | 文件存在性、mtime、checksum 检查 |
| `filefd` | 系统级文件描述符使用率监控（Linux） |
| `http` | HTTP 可用性、状态码、响应体、证书过期检查 |
| `journaltail` | journalctl 增量日志读取 + 关键词匹配（Linux） |
| `logfile` | 日志文件监控（偏移量追踪 + 轮转检测 + glob + 多编码） |
| `mem` | 内存、Swap 使用率检查 |
| `mount` | 挂载点基线检查（文件系统类型、挂载选项合规，Linux） |
| `neigh` | ARP/邻居表使用率监控，预防新 IP 通信失败（K8s 重灾区） |
| `net` | TCP/UDP 连通性与响应时间检查 |
| `netif` | 网卡健康检查（链路状态、错误/丢包增量，Linux） |
| `ntp` | NTP 同步状态、时钟偏移、时间源层级检查（Linux） |
| `ping` | ICMP 可达性、丢包率、时延检查 |
| `procfd` | 进程级 fd 使用率监控，预防 nofile 耗尽 |
| `procnum` | 进程数量检查（多种查找方式） |
| `scriptfilter` | 脚本输出行过滤匹配告警 |
| `secmod` | SELinux/AppArmor 基线检查（Linux） |
| `sockstat` | TCP listen 队列溢出检测（Linux） |
| `sysctl` | 内核参数基线检查，防止重启后静默重置（Linux） |
| `systemd` | systemd 服务状态检查（Linux） |
| `tcpstate` | TCP 连接状态监控（CLOSE_WAIT/TIME_WAIT，Netlink 采集，Linux） |
| `uptime` | 系统异常重启检测 |
| `zombie` | 僵尸进程检测 |

## 🧠 AI 诊断工具（70+）

AI 诊断被触发时（告警、巡检或 Chat），AI Agent 可调用以下工具深入排查：

⚙️ **系统与进程**：CPU Top、内存分布、OOM 历史、cgroup 限制/用量、进程线程（含 wchan）、打开文件列表、环境变量、PSI 压力指标

🌐 **网络**：ping、traceroute、DNS 解析、ARP 邻居表、TCP 连接状态、Socket 详情（RTT/cwnd）、重传率、连接延迟分布、Listen 队列溢出、TCP 内核调优检查、softnet 统计、路由表、IP 地址、网卡流量、防火墙规则

💾 **存储**：磁盘 I/O 延迟、块设备拓扑树、LVM 状态、挂载信息

🔐 **内核与安全**：dmesg 内核日志、中断分布、conntrack 统计、NUMA 内存分布、热区温度、sysctl 快照、SELinux/AppArmor 状态、coredump 列表

📜 **日志**：日志尾部读取、日志 grep（模式匹配）、journald 查询

🐳 **服务**：systemd 服务状态、失败服务列表、定时器列表、Docker ps/inspect

🔌 **远程插件**（如 Redis）会注册专用诊断工具，用于对目标实例进行深入检查。

🔗 **MCP 外部工具**：接入 Prometheus、Jaeger、CMDB 或任何 MCP 兼容数据源后，AI 自动发现并使用其提供的工具。

## 🖥️ 命令行

```bash
catpaw run [flags]                      # 启动监控 Agent
catpaw chat [-v]                        # AI 交互式排障
catpaw inspect <plugin> [target]        # AI 主动健康巡检
catpaw diagnose list|show <id>          # 查看历史诊断记录
catpaw selftest [filter] [-q]           # 诊断工具自检
catpaw mcptest                          # MCP 连接测试
```

## 🚀 快速开始

### 📦 安装

从 [GitHub Releases](https://github.com/cprobe/catpaw/releases) 下载对应平台的二进制。

### 基础监控

1. 在 `conf.d/p.<plugin>/` 下启用需要的插件配置
2. 启动：

```bash
./catpaw run
```

默认配置已开启 `[notify.console]`，事件会以带颜色的格式输出到终端——无需任何外部服务即可快速验证。

### 📡 事件通知

catpaw 支持多种通知渠道，在 `conf.d/config.toml` 中配置，可同时启用多个：

| 渠道 | 配置段 | 说明 |
| --- | --- | --- |
| **控制台** | `[notify.console]` | 输出到终端（默认开启） |
| **通用 WebAPI** | `[notify.webapi]` | 将原始 Event JSON 推送到任意 HTTP 端点 |
| **Flashduty** | `[notify.flashduty]` | 对接 [Flashduty](https://flashcat.cloud/product/flashduty/) 告警平台 |
| **PagerDuty** | `[notify.pagerduty]` | 对接 [PagerDuty](https://www.pagerduty.com/) 事件管理平台 |

**控制台**（默认开启，快速验证）：

```toml
[notify.console]
enabled = true
```

**通用 WebAPI**（推送原始 Event JSON 到任意 HTTP 端点）：

```toml
[notify.webapi]
url = "https://your-service.example.com/api/v1/events"
# method = "POST"
# timeout = "10s"
[notify.webapi.headers]
Authorization = "Bearer ${WEBAPI_TOKEN}"
```

**Flashduty**：

```toml
[notify.flashduty]
integration_key = "your-integration-key"
```

**PagerDuty**：

```toml
[notify.pagerduty]
routing_key = "your-routing-key"
```

### 🤖 AI 智能诊断（可选）

在 `conf.d/config.toml` 中添加：

```toml
[ai]
enabled = true
model_priority = ["default"]

[ai.models.default]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_API_KEY}"
model = "gpt-4o"
```

配置后，告警触发时 AI 会自动调用内置诊断工具分析根因。

### 💬 交互式 Chat

```bash
./catpaw chat
```

直接提问，如"CPU 为什么高？"、"检查磁盘 I/O"等，AI 会使用诊断工具和 Shell 命令（需用户确认）进行排查。

### 🔗 MCP 外部数据源（可选）

接入 Prometheus、Jaeger 等 MCP Server，让 AI 能查询历史指标、链路追踪等：

```toml
[ai.mcp]
enabled = true

[[ai.mcp.servers]]
name = "prometheus"
command = "/usr/local/bin/mcp-prometheus"
args = ["serve"]
identity = 'instance="${IP}:9100"'
[ai.mcp.servers.env]
PROMETHEUS_URL = "http://127.0.0.1:9090"

[[ai.mcp.servers]]
name = "nightingale"
command = "npx"
args = ["-y", "@n9e/n9e-mcp-server", "stdio"]
identity = 'ident="${HOSTNAME}"'
tools_allow = []
[ai.mcp.servers.env]
N9E_TOKEN = "480c04ed-ebe7-4266-xxxx-f8daf7819a6d"
N9E_BASE_URL = "http://127.0.0.1:17000"
```

验证连通性：

```bash
./catpaw mcptest
```

## ⚙️ 配置说明

- 全局配置：`conf.d/config.toml`
- 插件配置：`conf.d/p.<plugin>/*.toml`（每个目录可放多个 `.toml` 文件，合并加载）
- 支持 `SIGHUP` 热加载插件配置：

```bash
kill -HUP $(pidof catpaw)
```

## 📚 详细文档

| 文档 | 说明 |
| --- | --- |
| [开发必读](docs/dev-guide.md) | 架构全貌与代码导航 — **新人请先读这篇** |
| [命令行参数](docs/cli.md) | 完整的命令行参数说明 |
| [部署指南](docs/deployment.md) | 二进制部署、systemd 服务、Docker 部署 |
| [事件数据模型](docs/event-model.md) | Event 结构、Labels 设计、AlertKey 规则、告警生命周期 |
| [插件开发指南](docs/plugin-development.md) | 如何新增一个 catpaw 插件 |

## 💬 交流

可加微信 `picobyte` 进群交流，备注 `catpaw`。
