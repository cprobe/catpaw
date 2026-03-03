# 监控插件规划

| 插件 | 说明 | 参考 |
| --- | --- | --- |
| psi | Pressure Stall Information（CPU/IO/Memory 压力） | 读 `/proc/pressure/*`，Linux 4.20+ |
| smart | 磁盘 S.M.A.R.T 健康状态，预测硬盘故障 | Nagios `check_smart` |
| raid | 硬件/软件 RAID 阵列状态（mdadm、MegaCLI） | Nagios `check_raid` |
| mailq | 邮件队列积压检测（Postfix/Sendmail） | Nagios `check_mailq` |

---

# 诊断工具增强计划

## 背景

catpaw 已从纯告警工具演进为 AI 排障平台（diagnose / inspect / chat），当前 31 个诊断工具均由告警插件提供，以"指标深度查询"为主。AI 排障需要更多横向诊断维度（日志、IO、连通性、内核事件、资源限制等）。

## 架构：新增 DiagnoseRegistrar 注册路径

现有告警插件通过 `Diagnosable` 接口注册诊断工具，不改动。新增独立注册入口，支持"不采集、只诊断"的纯诊断模块：

```go
// plugins/plugins.go 新增
type DiagnoseRegistrar func(registry *diagnose.ToolRegistry)
var DiagnoseRegistrars []DiagnoseRegistrar
func AddDiagnoseRegistrar(r DiagnoseRegistrar) {
    DiagnoseRegistrars = append(DiagnoseRegistrars, r)
}
```

调用方（agent.go / chat.go / inspect.go）在现有插件注册循环后，追加：

```go
for _, registrar := range plugins.DiagnoseRegistrars {
    registrar(registry)
}
```

纯诊断工具统一放在 `plugins/sysdiag/` 下，通过 `init()` 自注册。

## Token 控制策略

日志等高噪声工具的输出不能依赖外层暴力截断（32KB），须在工具内部控制：

1. **参数层面**：工具自身设置合理默认上限（如 `lines=50`, `max_matches=20`），AI 通过参数缩小范围
2. **结构化摘要**：优先输出统计摘要（日志聚类 Top N + 少量原始样本），而非原始日志流
3. **日志聚类算法**：Token 替换（IP/数字/UUID/路径 → 占位符）+ 分组计数，纯正则实现，零外部依赖

## 实施计划

### P1：补全现有插件的诊断工具 ✅ Done

| 插件 | 工具 | 文件 | 状态 |
| --- | --- | --- | --- |
| filefd | `filefd_usage`, `filefd_top_procs` | `plugins/filefd/diagnose.go` | ✅ |
| sysctl | `sysctl_snapshot`, `sysctl_get` | `plugins/sysctl/diagnose.go` | ✅ |
| sockstat | `sockstat_summary` | `plugins/sockstat/diagnose.go` | ✅ |
| ntp | `ntp_status` | `plugins/ntp/diagnose.go` | ✅ |
| systemd | `service_status`, `service_list_failed` | `plugins/systemd/diagnose.go` | ✅ |
| docker | `docker_ps`, `docker_inspect` | `plugins/docker/diagnose.go` | ✅ |

### P2：新增纯诊断工具（plugins/sysdiag/） ✅ Done

| 工具 | 文件 | 关键参数 | 状态 |
| --- | --- | --- | --- |
| `dmesg_recent` | `plugins/sysdiag/dmesg.go` | since(默认5m), level(默认warn), max_lines(默认50) | ✅ |
| `dns_resolve` | `plugins/sysdiag/dns.go` | domain(必选), server(可选) | ✅ |
| `ping_check` | `plugins/sysdiag/ping.go` | host(必选), count(默认3, max10) | ✅ |
| `traceroute` | `plugins/sysdiag/traceroute.go` | host(必选), max_hops(默认15, max30) | ✅ |
| `log_tail` | `plugins/sysdiag/log.go` | file(必选), lines(默认50), pattern(可选) | ✅ |
| `log_grep` | `plugins/sysdiag/log.go` | file(必选), pattern(必选), max_matches(默认20) | ✅ |

### P3：Tier 1 高频排障工具 ✅ Done

| 工具 | 文件 | 说明 | 状态 |
| --- | --- | --- | --- |
| `oom_history` | `plugins/sysdiag/oom.go` | 解析 dmesg OOM Kill 记录，支持 kernel 3.x-6.x 格式，回退兼容旧 util-linux | ✅ |
| `io_top` | `plugins/sysdiag/iotop.go` | 按进程统计 I/O 字节（/proc/*/io），支持按 read/write/total 排序 | ✅ |
| `netstat_summary` | `plugins/sockstat/netstat.go` | TCP/UDP 核心指标（/proc/net/snmp），含 retrans/errors/resets 等 | ✅ |
| `cgroup_usage` | `plugins/sysdiag/cgroup.go` | cgroup v1/v2 CPU/内存限制与用量，含 throttle 信息 | ✅ |
