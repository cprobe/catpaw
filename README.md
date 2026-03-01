English | [中文](README_zh.md)

# catpaw

catpaw is a lightweight event monitoring tool that detects anomalies and produces standardized events.  
It is typically used with [Flashduty](https://flashcat.cloud/product/flashduty/) (catpaw produces events, Flashduty handles alert routing and notification), but you can also integrate it with your own event receiver.

## Key Features

- Lightweight with zero heavy dependencies — easy to deploy
- Plugin-based architecture — enable only what you need
- Intuitive configuration — great for quickly adding monitoring coverage
- Ideal for self-monitoring of monitoring systems, avoiding circular dependencies

## Plugins

| Plugin | Description |
| --- | --- |
| `cert` | TLS certificate expiry check (remote TLS connections + local cert files; supports STARTTLS, per-target SNI, glob) |
| `conntrack` | Linux conntrack (nf_conntrack) table usage monitoring to prevent silent packet drops |
| `cpu` | CPU utilization and load average (normalized per-core) check |
| `disk` | Disk space, inode, and writability check |
| `dns` | DNS resolution check |
| `docker` | Docker container monitoring (running state, frequent restart detection, health check, CPU/memory usage) |
| `exec` | Run scripts/commands and produce events from their output (supports JSON and Nagios modes) |
| `filecheck` | File existence, mtime, and checksum check |
| `filefd` | System-level file descriptor usage monitoring to prevent fd exhaustion (Linux only) |
| `http` | HTTP availability, status code, response body, and certificate expiry check |
| `journaltail` | Incremental log reading via journalctl with keyword matching (Linux only) |
| `logfile` | Plain-text log file monitoring (offset tracking + log rotation detection + glob + multi-encoding) |
| `mem` | Memory and swap usage check |
| `mount` | Mount point baseline check (existence, filesystem type, mount option compliance; Linux only) |
| `neigh` | Linux ARP/neighbor table usage monitoring to prevent silent communication failures for new IPs (common in K8s) |
| `net` | TCP/UDP connectivity and response time check |
| `netif` | Network interface health check (link state, error/drop delta monitoring; Linux only) |
| `ntp` | NTP sync status, clock offset, and stratum check (Linux only) |
| `ping` | ICMP reachability, packet loss, and latency check |
| `procfd` | Per-process file descriptor usage monitoring to prevent nofile exhaustion (too many open files) |
| `procnum` | Process count check (multiple lookup methods; can also monitor total system process count) |
| `scriptfilter` | Run scripts and match output lines against filter rules to trigger alerts |
| `secmod` | SELinux / AppArmor security module baseline check (Linux only) |
| `sockstat` | TCP listen queue overflow detection (ListenOverflows delta monitoring; Linux only) |
| `sysctl` | Kernel parameter baseline check to detect silent resets after reboots/upgrades (Linux only) |
| `systemd` | systemd service status check (Linux only) |
| `tcpstate` | TCP connection state monitoring (CLOSE_WAIT/TIME_WAIT accumulation detection via Netlink; Linux only) |
| `uptime` | Unexpected reboot detection (alerts when uptime drops below threshold; self-healing event) |
| `zombie` | Zombie process detection (system-wide count of processes in Z state) |

## Use Cases

- You need reliable coverage of critical risk points without deploying a full monitoring stack
- Sidecar self-monitoring for existing monitoring systems to reduce single points of failure
- Quick pattern-matching alerts on logs, command output, or text-based events

## Quick Start

### Installation

Download the binary for your platform from [GitHub Releases](https://github.com/cprobe/catpaw/releases).

### Configuration

1. Edit `conf.d/config.toml` and set your FlashDuty `integration_key`. If you don't use Flashduty, you can point catpaw to your own event receiver.
2. Enable the desired plugin configs under `conf.d/p.<plugin>/`
3. Start catpaw

```bash
./catpaw
```

Test mode (events printed to terminal, not sent to FlashDuty):

```bash
./catpaw -test
```

For all CLI options, see [CLI Reference](docs/cli.md).

## Integrating with Flashduty

1. Sign up at [Flashduty](https://console.flashcat.cloud/)
2. Create a "Standard Alert Event" integration in the Integration Center to get a webhook URL
3. Set the URL in the `flashduty.url` field of `conf.d/config.toml`

Learn more: [Flashduty](https://flashcat.cloud/product/flashduty/)

## Configuration Reference

- Global config: `conf.d/config.toml`
- Plugin configs: `conf.d/p.<plugin>/*.toml` (multiple `.toml` files per directory are merged on load)
- Supports `SIGHUP` for hot-reloading plugin configs

```bash
kill -HUP $(pidof catpaw)
```

## Documentation

| Document | Description |
| --- | --- |
| [CLI Reference](docs/cli.md) | Complete command-line options |
| [Deployment Guide](docs/deployment.md) | Binary deployment, systemd service, Docker |
| [Event Data Model](docs/event-model.md) | Event structure, labels design, AlertKey rules, alert lifecycle |
| [Plugin Development Guide](docs/plugin-development.md) | How to create a new catpaw plugin |

## Community

WeChat: add `picobyte` and mention `catpaw` to join the group.
