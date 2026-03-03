English | [中文](README_zh.md)

# catpaw

catpaw is a lightweight monitoring agent with **AI-powered diagnostics**.
It detects anomalies through plugin-based checks, produces standardized events, and — when an alert fires — can automatically trigger AI root-cause analysis using 70+ built-in diagnostic tools.

It pairs naturally with [Flashduty](https://flashcat.cloud/product/flashduty/) for alert routing, but works with any event receiver.

## Key Features

- **Lightweight, zero heavy dependencies** — single binary, easy to deploy
- **Plugin-based monitoring** — 25+ check plugins, enable only what you need
- **AI-powered diagnosis** — automatic root-cause analysis triggered by alerts
- **Interactive AI chat** — troubleshoot issues conversationally with AI + tools
- **Proactive health inspection** — on-demand AI-driven health checks
- **70+ diagnostic tools** — system, network, storage, security, process, kernel
- **MCP integration** — connect external data sources (Prometheus, Jaeger, CMDB, etc.) via [Model Context Protocol](https://modelcontextprotocol.io/)
- **Self-monitoring friendly** — ideal for monitoring your monitoring systems

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        catpaw agent                             │
│                                                                 │
│  ┌─────────────┐   alert    ┌──────────────┐    AI + Tools     │
│  │  25+ Check  │ ────────── │  AI Diagnose │ ──────────────┐   │
│  │   Plugins   │  trigger   │    Engine    │               │   │
│  └──────┬──────┘            └──────────────┘               │   │
│         │                                                  ▼   │
│         │ events    ┌──────────────┐         ┌───────────────┐ │
│         └────────── │  FlashDuty / │         │  70+ Diagnose │ │
│                     │ Event Receiver│         │     Tools     │ │
│                     └──────────────┘         └───────┬───────┘ │
│                                                      │         │
│  ┌─────────────┐                            ┌────────┴───────┐ │
│  │  AI Chat    │ ───── interactive ──────── │  MCP External  │ │
│  │  (CLI)      │       troubleshoot         │  Data Sources  │ │
│  └─────────────┘                            └────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

## Check Plugins

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
| `scriptfilter` | Script output filter-rule matching |
| `secmod` | SELinux/AppArmor baseline (Linux) |
| `sockstat` | TCP listen queue overflow detection (Linux) |
| `sysctl` | Kernel parameter baseline — detect silent resets (Linux) |
| `systemd` | systemd service status (Linux) |
| `tcpstate` | TCP state monitoring (CLOSE_WAIT/TIME_WAIT; Netlink; Linux) |
| `uptime` | Unexpected reboot detection |
| `zombie` | Zombie process detection |

## AI Diagnostic Tools (70+)

When AI diagnosis is triggered (by alert, inspection, or chat), the AI agent has access to a rich toolkit:

**System & Process**: CPU top, memory breakdown, OOM history, cgroup limits, process threads (with wchan), open files, environment variables, PSI pressure

**Network**: ping, traceroute, DNS resolve, ARP neighbors, TCP connection states, socket details (RTT/cwnd), retransmission rate, connection latency summary, listen queue overflow, TCP tuning check, softnet stats, route table, IP addresses, interface stats, firewall rules

**Storage**: disk I/O latency, block device topology, LVM status, mount info

**Kernel & Security**: dmesg, interrupts distribution, conntrack stats, NUMA stats, thermal zones, sysctl snapshot, SELinux/AppArmor status, coredump list

**Logs**: log tail, log grep (with pattern matching), journald query

**Services**: systemd service status, failed services list, timer list, Docker ps/inspect

**Remote plugins** (Redis, etc.) contribute their own specialized diagnostic tools for deep introspection.

**MCP external tools**: Connect Prometheus, Jaeger, CMDB, or any MCP-compatible data source — the AI automatically discovers and uses their tools.

## CLI Commands

```
catpaw run [flags]                      Start the monitoring agent
catpaw chat [-v]                        Interactive AI chat for troubleshooting
catpaw inspect <plugin> [target]        Proactive AI health inspection
catpaw diagnose list|show <id>          View past diagnosis records
catpaw selftest [filter] [-q]           Smoke-test all diagnostic tools
catpaw mcptest                          Test MCP server connections
```

## Quick Start

### Installation

Download the binary from [GitHub Releases](https://github.com/cprobe/catpaw/releases).

### Basic Monitoring

1. Edit `conf.d/config.toml` — set your FlashDuty `integration_key` (or your own receiver)
2. Enable plugin configs under `conf.d/p.<plugin>/`
3. Start:

```bash
./catpaw run
```

Events are printed to the terminal by default (`[notify.console]` is enabled in the example config). When you're ready for production, disable console and enable Flashduty/PagerDuty/WebAPI.

### AI Diagnosis (optional)

Add to `conf.d/config.toml`:

```toml
[ai]
enabled = true
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_API_KEY}"
model = "gpt-4o"
```

Now when alerts fire, AI automatically analyzes root cause using built-in diagnostic tools.

### Interactive Chat

```bash
./catpaw chat
```

Ask questions like "Why is CPU high?" or "Check disk I/O latency" — the AI uses diagnostic tools and shell commands (with confirmation) to investigate.

### MCP External Data Sources (optional)

Connect Prometheus, Jaeger, or other MCP servers for AI to query historical metrics, traces, etc.:

```toml
[ai.mcp]
enabled = true

[[ai.mcp.servers]]
name = "prometheus"
command = "/usr/local/bin/mcp-prometheus"
args = ["serve"]
identity = "ident=\"my-host\""
[ai.mcp.servers.env]
PROMETHEUS_URL = "http://127.0.0.1:9090"
```

Verify connectivity:

```bash
./catpaw mcptest
```

## Integrating with Flashduty

1. Sign up at [Flashduty](https://console.flashcat.cloud/)
2. Create a "Standard Alert Event" integration to get a webhook URL
3. Set the URL in `conf.d/config.toml` under `flashduty.url`

Learn more: [Flashduty](https://flashcat.cloud/product/flashduty/)

## Configuration

- Global config: `conf.d/config.toml`
- Plugin configs: `conf.d/p.<plugin>/*.toml` (multiple files merged on load)
- Hot-reload plugin configs with `SIGHUP`:

```bash
kill -HUP $(pidof catpaw)
```

## Documentation

| Document | Description |
| --- | --- |
| [CLI Reference](docs/cli.md) | Complete command-line options |
| [Deployment Guide](docs/deployment.md) | Binary, systemd, Docker deployment |
| [Event Data Model](docs/event-model.md) | Event structure, labels, AlertKey rules |
| [Plugin Development Guide](docs/plugin-development.md) | How to create a new catpaw plugin |

## Community

WeChat: add `picobyte` and mention `catpaw` to join the group.
