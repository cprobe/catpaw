English | [中文](README_zh.md)

# 🐾 catpaw

catpaw is a lightweight monitoring agent with **AI-powered diagnostics**.
It detects anomalies through plugin-based checks, produces standardized events, and — when an alert fires — can automatically trigger AI root-cause analysis using 70+ built-in diagnostic tools.

Events can be forwarded to any alert platform (Flashduty, PagerDuty, or any HTTP endpoint), or simply printed to the console for quick validation.

## ✨ Key Features

- 🪶 **Lightweight, zero heavy dependencies** — single binary, easy to deploy
- 🔌 **Plugin-based monitoring** — 25+ check plugins, enable only what you need
- 🤖 **AI-powered diagnosis** — automatic root-cause analysis triggered by alerts
- 💬 **Interactive AI chat** — troubleshoot issues conversationally with AI + tools
- 🩺 **Proactive health inspection** — on-demand AI-driven health checks
- 🛠️ **70+ diagnostic tools** — system, network, storage, security, process, kernel
- 📡 **Flexible notification** — console, generic WebAPI, Flashduty, PagerDuty, or any combination
- 🔄 **Self-monitoring friendly** — ideal for monitoring your monitoring systems

## 🏗️ Architecture Overview

```text
┌─────────────────────────────────────────────────────────────────┐
│                        catpaw agent                             │
│                                                                 │
│  ┌─────────────┐   alert    ┌──────────────┐    AI + Tools     │
│  │  25+ Check  │ ────────── │  AI Diagnose │ ──────────────┐   │
│  │   Plugins   │  trigger   │    Engine    │               │   │
│  └──────┬──────┘            └──────────────┘               │   │
│         │                                                  ▼   │
│         │ events    ┌──────────────┐         ┌───────────────┐ │
│         └────────── │   Notifiers  │         │  70+ Diagnose │ │
│                     │  (multiple)  │         │     Tools     │ │
│                     └──────────────┘         └───────────────┘ │
│                                                                 │
│  ┌─────────────┐                                                │
│  │  AI Chat    │ ───── interactive troubleshoot                 │
│  │  (CLI)      │                                                │
│  └─────────────┘                                                │
└─────────────────────────────────────────────────────────────────┘
```

## 🔍 Check Plugins

| Plugin | Description |
| --- | --- |
| `cert` | TLS certificate expiry check (remote TLS + local files; STARTTLS, SNI, glob) |
| `conntrack` | Linux conntrack table usage — prevent silent packet drops |
| `cpu` | CPU utilization and per-core normalized load average |
| `disk` | Disk space, inode, and writability check |
| `dns` | DNS resolution check |
| `docker` | Docker container monitoring (state, restart, health, CPU/mem) |
| `exec` | Run scripts/commands to produce events (JSON and Nagios modes) |
| `filecheck` | File existence, mtime, and checksum check |
| `filefd` | System-level file descriptor usage (Linux) |
| `http` | HTTP availability, status code, response body, cert expiry |
| `journaltail` | Incremental journalctl log reading with keyword matching (Linux) |
| `logfile` | Log file monitoring (offset tracking, rotation, glob, multi-encoding) |
| `mem` | Memory and swap usage check |
| `mount` | Mount point baseline (fs type, options compliance; Linux) |
| `neigh` | ARP/neighbor table usage — prevent new-IP failures (K8s) |
| `net` | TCP/UDP connectivity and response time |
| `netif` | Network interface health (link state, error/drop delta; Linux) |
| `ntp` | NTP sync, clock offset, stratum (Linux) |
| `ping` | ICMP reachability, packet loss, latency |
| `procfd` | Per-process fd usage — prevent nofile exhaustion |
| `procnum` | Process count check (multiple lookup methods) |
| `redis` | Redis monitoring for standalone, master/replica, and Redis Cluster; includes Redis-specific AI diagnosis tools |
| `redis_sentinel` | Redis Sentinel monitoring for quorum, master reachability from Sentinel's view, and Sentinel-specific AI diagnosis tools |
| `scriptfilter` | Script output filter-rule matching |
| `secmod` | SELinux/AppArmor baseline (Linux) |
| `sockstat` | TCP listen queue overflow detection (Linux) |
| `sysctl` | Kernel parameter baseline — detect silent resets (Linux) |
| `systemd` | systemd service status (Linux) |
| `tcpstate` | TCP state monitoring (CLOSE_WAIT/TIME_WAIT; Netlink; Linux) |
| `uptime` | Unexpected reboot detection |
| `zombie` | Zombie process detection |

## 🧠 AI Diagnostic Tools (70+)

When AI diagnosis is triggered (by alert, inspection, or chat), the AI agent has access to a rich toolkit:

⚙️ **System & Process**: CPU top, memory breakdown, OOM history, cgroup limits, process threads (with wchan), open files, environment variables, PSI pressure

🌐 **Network**: ping, traceroute, DNS resolve, ARP neighbors, TCP connection states, socket details (RTT/cwnd), retransmission rate, connection latency summary, listen queue overflow, TCP tuning check, softnet stats, route table, IP addresses, interface stats, firewall rules

💾 **Storage**: disk I/O latency, block device topology, LVM status, mount info

🔐 **Kernel & Security**: dmesg, interrupts distribution, conntrack stats, NUMA stats, thermal zones, sysctl snapshot, SELinux/AppArmor status, coredump list

📜 **Logs**: log tail, log grep (with pattern matching), journald query

🐳 **Services**: systemd service status, failed services list, timer list, Docker ps/inspect

🔌 **Remote plugins** (Redis, Redis Sentinel, etc.) contribute their own specialized diagnostic tools for deep introspection.

For Redis-specific checks, cluster semantics, and diagnosis tools, see [plugins/redis/README.md](plugins/redis/README.md).
For Redis Sentinel-specific checks, diagnosis tools, and config semantics, see [plugins/redis_sentinel/README.md](plugins/redis_sentinel/README.md).

## 🖥️ CLI Commands

```bash
catpaw run [flags]                      # Start the monitoring agent
catpaw chat [-v]                        # Interactive AI chat for troubleshooting
catpaw inspect <plugin> [target]        # Proactive AI health inspection
catpaw diagnose list|show <id>          # View past diagnosis records
catpaw selftest [filter] [-q]           # Smoke-test all diagnostic tools
```

## 🚀 Quick Start

### 📦 Installation

Download the binary from [GitHub Releases](https://github.com/cprobe/catpaw/releases).

### Basic Monitoring

1. Enable plugin configs under `conf.d/p.<plugin>/`
2. Start:

```bash
./catpaw run
```

The default config enables `[notify.console]`, so events are printed to the terminal with colored output — no external service needed for a quick test.

### 📡 Event Notification

catpaw supports multiple notification channels. Configure one or more in `conf.d/config.toml`:

| Channel | Config Section | Description |
| --- | --- | --- |
| **Console** | `[notify.console]` | Print events to terminal (enabled by default) |
| **WebAPI** | `[notify.webapi]` | Push raw Event JSON to any HTTP endpoint |
| **Flashduty** | `[notify.flashduty]` | Forward to [Flashduty](https://flashcat.cloud/product/flashduty/) alert platform |
| **PagerDuty** | `[notify.pagerduty]` | Forward to [PagerDuty](https://www.pagerduty.com/) incident management |

Multiple channels can be active simultaneously. For example, you can print to console for debugging while also forwarding to your alert platform.

**Console** (default — for quick validation):

```toml
[notify.console]
enabled = true
```

**WebAPI** (push raw Event JSON to any HTTP endpoint):

```toml
[notify.webapi]
url = "https://your-service.example.com/api/v1/events"
# method = "POST"
# timeout = "10s"
[notify.webapi.headers]
Authorization = "Bearer ${WEBAPI_TOKEN}"
```

**Flashduty**:

```toml
[notify.flashduty]
integration_key = "your-integration-key"
```

**PagerDuty**:

```toml
[notify.pagerduty]
routing_key = "your-routing-key"
```

### 🤖 AI Diagnosis (optional)

Add to `conf.d/config.toml`:

```toml
[ai]
enabled = true
model_priority = ["default"]

[ai.models.default]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_API_KEY}"
model = "gpt-4o"
```

Now when alerts fire, AI automatically analyzes root cause using built-in diagnostic tools.

### 💬 Interactive Chat

```bash
./catpaw chat
```

Ask questions like "Why is CPU high?" or "Check disk I/O latency" — the AI uses diagnostic tools and shell commands (with confirmation) to investigate.

## ⚙️ Configuration

- Global config: `conf.d/config.toml`
- Local override: `conf.d/config.local.toml` (loaded last, git-ignored, ideal for developer-only changes)
- Plugin configs: `conf.d/p.<plugin>/*.toml` (multiple files merged on load)
- Top-level load order: `config.toml` -> other files in `conf.d/` -> `config.local.toml`
- Hot-reload plugin configs with `SIGHUP`:

```bash
kill -HUP $(pidof catpaw)
```

## 📚 Documentation

| Document | Description |
| --- | --- |
| [Developer Guide](docs/dev-guide.md) | Architecture overview and codebase walkthrough — **read this first** |
| [Deployment Guide](docs/deployment.md) | Binary, systemd, Docker deployment |
| [Event Data Model](docs/event-model.md) | Event structure, labels, AlertKey rules |
| [Plugin Development Guide](docs/plugin-development.md) | How to create a new catpaw plugin |

## 💬 Community

WeChat: add `picobyte` and mention `catpaw` to join the group.
