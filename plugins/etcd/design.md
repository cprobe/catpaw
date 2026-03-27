# etcd 插件设计

## 动机

catpaw 的告警与 AI 诊断链路只对引擎内产出的 Event 生效（`engine.mayTriggerDiagnose`）。
Prometheus 侧的 etcd 告警规则无法触发 catpaw 的诊断闭环。

本插件通过直接拉取 etcd 的 HTTP API 和 Prometheus 指标端点，
在 catpaw 引擎内产出面向故障语义的 Event，覆盖 5 个信号层次。

## 设计理念

参考 catpaw Redis 插件的设计哲学：

1. **语义监控 > 指标搬运** — 不做 exporter 替代品，把 etcd 语义翻译成故障判断
2. **两层架构** — 周期检查只做轻量强信号；重操作（metrics 深度分析、集群快照）留到 AI 诊断
3. **信号分层** — 按故障层次组织，默认只盯"硬故障"
4. **默认保守** — 与拓扑设计强相关的检查默认关闭
5. **delta 而非累计值** — 累计计数器用采集周期增量做阈值判断

## 部署架构

### 集中监控（推荐）

一个 catpaw 实例即可监控整个 etcd 集群，将所有成员地址填入 `targets`。

```
                     ┌─────────────────┐
                     │  catpaw (单点)   │
                     │  targets = [    │
                     │   node1:2379,   │
                     │   node2:2379,   │
                     │   node3:2379    │
                     │  ]              │
                     └───┬───┬───┬─────┘
                         │   │   │
              ┌──────────┘   │   └──────────┐
              ▼              ▼              ▼
        ┌──────────┐  ┌──────────┐  ┌──────────┐
        │  etcd-1  │  │  etcd-2  │  │  etcd-3  │
        └──────────┘  └──────────┘  └──────────┘
```

- catpaw 并发检查每个 target，事件通过 `target` label 区分来源节点
- AI 诊断的 `etcd_cluster_status` 工具遍历全部 targets 横向对比
- 单节点诊断工具自动路由到触发告警的那个 target（非固定 Targets[0]）
- 不需要在每个 etcd 成员上都安装 catpaw

### 分散监控

每个 etcd 成员上安装 catpaw，`targets` 只填 localhost。
适用于网络隔离或需要本机系统级诊断的场景，但失去了集群横向对比能力。

## 与 Prometheus 的差异

- 本插件不做通用 exporter，只关注面向故障的信号
- Prometheus 使用 `rate(metric[interval])` 对计数器求速率后再算分位数；本插件使用 **瞬时累积分布** 直接算分位数
- 与 Prometheus 告警可能重复，可在 FlashDuty 侧合并

## 信号分层

### Layer 1: 连通性 — "能不能连上"

| check | 默认 | 数据源 | 说明 |
|-------|------|--------|------|
| `etcd::health` | ON | GET /health | JSON health=="true" |

### Layer 2: 集群角色 — "集群状态对不对"

| check | 默认 | 数据源 | 说明 |
|-------|------|--------|------|
| `etcd::has_leader` | ON (Critical) | POST /v3/maintenance/status | leader != 0 |

集群无 leader = 无法写入 = 硬故障，默认开启。

### Layer 3: 容量边界 — "快撑满了吗"

| check | 默认 | 数据源 | 说明 |
|-------|------|--------|------|
| `etcd::db_size_pct` | ON (80/90%) | POST /v3/maintenance/status | dbSize / quota 百分比 |

quota 默认自动检测（从 etcd 的 `/metrics` 中读取 `etcd_server_quota_backend_bytes`）。
自动检测失败时回退到 etcd 默认值 2GB。也可在配置中手动指定。

### Layer 4: 告警 — "有没有硬故障告警"

| check | 默认 | 数据源 | 说明 |
|-------|------|--------|------|
| `etcd::alarm` | ON (Critical) | POST /v3/maintenance/alarm | NOSPACE / CORRUPT 告警检测 |

### Layer 5: 性能 — "磁盘 I/O 是否异常"

| check | 默认 | 数据源 | 说明 |
|-------|------|--------|------|
| `etcd::backend_commit_p*` | 需配置 | GET /metrics (histogram) | 后端 commit 直方图分位数 |
| `etcd::wal_fsync_p*` | 需配置 | GET /metrics (histogram) | WAL fsync 直方图分位数 |
| `etcd::slow_apply` | 需配置 | GET /metrics (counter) | slow apply delta (周期增量) |
| `etcd::leader_changes` | 需配置 | GET /metrics (counter) | leader election delta |
| `etcd::proposals_failed` | 需配置 | GET /metrics (counter) | proposal 失败 delta |

## 配置说明

### 基础配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `targets` | `[]string` | — | etcd endpoint URL（http:// 或 https://） |
| `interval` | `Duration` | `30s` | 采集周期 |
| `timeout` | `Duration` | `5s` | HTTP 请求超时 |
| `concurrency` | `int` | `5` | 并发检查数 |
| `tls_ca` | `string` | — | CA 证书路径 |
| `tls_cert` | `string` | — | 客户端证书路径（注意用 client.pem，不是 server.pem） |
| `tls_key` | `string` | — | 客户端私钥路径 |
| `insecure_skip_verify` | `bool` | `false` | 跳过服务端证书验证（不推荐） |
| `health_path` | `string` | `/health` | 健康检查端点 |
| `metrics_path` | `string` | `/metrics` | 指标端点 |
| `connectivity_severity` | `string` | `Critical` | 连通性失败告警级别 |

### has_leader

| 字段 | 默认 | 说明 |
|------|------|------|
| `disabled` | `false` | 禁用此检查 |
| `severity` | `Critical` | 无 leader 时的告警级别 |

### db_size_pct

| 字段 | 默认 | 说明 |
|------|------|------|
| `disabled` | `false` | 禁用此检查 |
| `quota_backend_bytes` | `0` (自动检测) | etcd 的 quota 值，0=从 metrics 自动获取 |
| `warn_ge` | `80` | Warning 百分比阈值 |
| `critical_ge` | `90` | Critical 百分比阈值 |

### alarm

| 字段 | 默认 | 说明 |
|------|------|------|
| `disabled` | `false` | 禁用此检查 |
| `severity` | `Critical` | 有 alarm 时的告警级别 |

### quantile_checks / wal_fsync_checks

| 字段 | 类型 | 说明 |
|------|------|------|
| `phi` | `float64` | 分位数（0.90/0.95/0.99） |
| `warn_ge` | `Duration` | Warning 阈值 |
| `critical_ge` | `Duration` | Critical 阈值 |
| `disabled` | `bool` | 跳过此项 |

`histogram_metric` 默认 `etcd_disk_backend_commit_duration_seconds`；
`wal_fsync_metric` 默认 `etcd_disk_wal_fsync_duration_seconds`。

### slow_apply / leader_changes / proposals_failed

| 字段 | 类型 | 说明 |
|------|------|------|
| `warn_ge` | `int` | 周期增量 >= 此值触发 Warning |
| `critical_ge` | `int` | 周期增量 >= 此值触发 Critical |

Delta 机制：首次采集建立基线（产出 Ok 事件），后续每周期计算 `当前值 - 上次值`。
计数器回退（如 etcd 重启）按 delta=0 处理，不产生误报。

### alerting

| 字段 | 类型 | 说明 |
|------|------|------|
| `for_duration` | `int` | 持续异常多少秒后才告警（防抖） |
| `repeat_interval` | `Duration` | 未恢复时重复通知间隔 |
| `repeat_number` | `int` | 最多重复通知次数 |

### diagnose

| 字段 | 类型 | 说明 |
|------|------|------|
| `enabled` | `bool` | 是否开启 AI 诊断 |
| `min_severity` | `string` | 触发诊断的最低告警级别 |
| `timeout` | `Duration` | AI 诊断超时（默认 120s） |
| `cooldown` | `Duration` | 同 target 诊断冷却期（默认 10m） |

## 最小落地方案

### 场景 1：单机 etcd（非 TLS）

```toml
[[instances]]
targets = ["http://127.0.0.1:2379"]
```

一行配置，4 项默认检查自动生效：health + has_leader + db_size_pct + alarm。

### 场景 2：3 节点集群（mTLS）

```toml
[[instances]]
targets = [
    "https://10.0.0.1:2379",
    "https://10.0.0.2:2379",
    "https://10.0.0.3:2379",
]
tls_ca   = "/opt/etcd/pki/ca.pem"
tls_cert = "/opt/etcd/pki/client.pem"
tls_key  = "/opt/etcd/pki/client-key.pem"

[[instances.quantile_checks]]
phi = 0.99
warn_ge = "100ms"
critical_ge = "250ms"
```

在默认 4 项检查基础上，加 backend commit P99 延迟检查。

### 场景 3：大 quota 集群 + 全量监控

```toml
[[instances]]
targets = ["https://10.0.0.1:2379"]
tls_ca   = "/opt/etcd/pki/ca.pem"
tls_cert = "/opt/etcd/pki/client.pem"
tls_key  = "/opt/etcd/pki/client-key.pem"

[instances.db_size_pct]
quota_backend_bytes = "8GB"

[[instances.quantile_checks]]
phi = 0.99
warn_ge = "100ms"
critical_ge = "250ms"

[[instances.wal_fsync_checks]]
phi = 0.99
warn_ge = "100ms"
critical_ge = "250ms"

[instances.slow_apply]
warn_ge = 1
critical_ge = 5

[instances.leader_changes]
warn_ge = 3
critical_ge = 5
```

## AI 诊断工具（8 个）

| 工具 | 方法 | 范围 | 诊断场景 |
|------|------|------|----------|
| `etcd_health` | GET /health | 告警节点 | 基础健康判断 |
| `etcd_version` | GET /version | 告警节点 | 版本信息/兼容性排查 |
| `etcd_endpoint_status` | POST /v3/maintenance/status | 告警节点 | leader/raft/dbSize |
| `etcd_cluster_status` | 遍历所有 targets 调用 status | **全集群** | 横向对比 dbSize/碎片率/leader/raftIndex |
| `etcd_commit_summary` | GET /metrics → 结构化解析 | 告警节点 | commit/wal 分位数、DB 大小、slow apply、自动提示 |
| `etcd_alarm_list` | POST /v3/maintenance/alarm | 告警节点 | NOSPACE/CORRUPT 告警（etcd alarm 是集群级别） |
| `etcd_member_list` | POST /v3/cluster/member/list | 集群 | 成员拓扑/离线检测 |
| `etcd_metrics_relevant` | GET /metrics（过滤） | 告警节点 | 原始指标深入分析 |

**目标精准路由**：`getEtcdAccessor()` 自动将 `baseURL` 切换到 `session.Record.Alert.Target`
（触发告警的节点），单节点工具查的是出问题的那个节点而非固定 Targets[0]。

### AI 系统级辅助工具

AI 在诊断时还会结合 catpaw 的通用 shell 工具，category description 中给出了提示：

- **进程检查**：`ps aux|grep etcd`、`ss -tlnp|grep 2379`、`netstat -tlnp|grep etcd`
- **启动参数**：`cat /proc/$(pgrep etcd)/cmdline|tr '\0' '\n'`
- **磁盘 I/O**：`iostat -xd 1 3`、`df -h <data-dir>`
- **系统资源**：`top -bn1|head -20`、`free -h`
- **日志**：`journalctl -u etcd` 或从进程 cmdline 找日志路径

### commit 慢排查路径与工具映射

```
1. 单节点 or 全集群？      → etcd_cluster_status（横向对比）
2. 磁盘 I/O？              → shell: iostat -xd 1 3
3. DB 膨胀 / 碎片化？      → etcd_commit_summary（自动计算碎片率 + 告警提示）
4. NOSPACE / CORRUPT？      → etcd_alarm_list
5. 系统资源？              → shell: top / free / vmstat
6. etcd 内部（慢 apply/wal）→ etcd_commit_summary + etcd_metrics_relevant
7. 进程参数 / 数据目录？    → shell: cat /proc/$(pgrep etcd)/cmdline
8. etcd 日志？             → shell: journalctl / log file
```
