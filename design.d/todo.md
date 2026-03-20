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

### P4：Tier 2 中频排障工具 ✅ Done

| 工具 | 文件 | 说明 | 状态 |
| --- | --- | --- | --- |
| `journal_query` | `plugins/systemd/journal.go` | 查询 systemd journal，按 unit/时间/优先级过滤，支持 --output short-iso | ✅ |
| `mount_info` | `plugins/sysdiag/mount.go` | 显示挂载点信息，高亮只读文件系统，过滤伪 FS，支持 octal 转义 | ✅ |
| `env_inspect` | `plugins/sysdiag/env.go` | 查看进程环境变量（/proc/pid/environ），自动掩码敏感值 | ✅ |
| `open_files` | `plugins/sysdiag/openfiles.go` | 列出进程打开的文件描述符，分类统计 file/socket/pipe/anon | ✅ |
| `ss_detail` | `plugins/sysdiag/ss.go` | 详细 TCP socket 信息（Send-Q/Recv-Q/RTT/cwnd/重传），内存安全 | ✅ |

### P5：Tier 3 专项排障工具 ✅ Done

| 工具 | 文件 | 说明 | 状态 |
| --- | --- | --- | --- |
| `psi_check` | `plugins/sysdiag/psi.go` | PSI 压力指标（CPU/内存/IO），avg10/60/300 百分比，Linux 4.20+ | ✅ |
| `interrupts` | `plugins/sysdiag/interrupts.go` | 中断分布 Top N，检测中断不均衡（热点 CPU），按总数排序 | ✅ |
| `conntrack_stat` | `plugins/sysdiag/conntrack_stat.go` | 连接追踪表用量+内核统计（drop/insert_failed/early_drop），hex 解析 | ✅ |
| `coredump_list` | `plugins/sysdiag/coredump.go` | 列出 coredump 记录（coredumpctl 优先，回退扫描文件目录） | ✅ |
| `numa_stat` | `plugins/sysdiag/numa.go` | NUMA 节点内存分布+跨节点访问统计，检测不对称内存和高 miss 率 | ✅ |
| `thermal_zone` | `plugins/sysdiag/thermal.go` | 热区温度读取（毫摄氏度转换），含 trip point，高温告警 | ✅ |
| `lvm_status` | `plugins/sysdiag/lvm.go` | LVM 卷组+逻辑卷状态（vgs/lvs），解析 attr 标志位检测异常 | ✅ |

### P6：Tier A 网络/存储基础信息工具 ✅ Done

| 工具 | 文件 | 说明 | 状态 |
| --- | --- | --- | --- |
| `net_interface` | `plugins/sysdiag/netif.go` | 网卡级统计（/proc/net/dev）：RX/TX 字节/包数/丢包/错误，标注问题接口 | ✅ |
| `ip_addr` | `plugins/sysdiag/ipaddr.go` | 网络接口列表：IP/子网/MAC/MTU/状态，JSON+文本双模式解析，标注 DOWN 接口 | ✅ |
| `route_table` | `plugins/sysdiag/route.go` | IPv4/IPv6 路由表：目标/网关/设备/metric/src，JSON+文本双模式，标注缺失默认路由 | ✅ |
| `block_devices` | `plugins/sysdiag/blockdev.go` | 块设备拓扑树（lsblk）：磁盘→分区→LVM→挂载点，JSON+文本双模式 | ✅ |

### P7：Tier B 安全/进程/定时任务工具 ✅ Done

| 工具 | 文件 | 说明 | 状态 |
| --- | --- | --- | --- |
| `arp_neigh` | `plugins/sysdiag/arp.go` | ARP 邻居表摘要：条目数/gc_thresh3 对比/incomplete 检测/按接口汇总 | ✅ |
| `firewall_summary` | `plugins/sysdiag/firewall.go` | 防火墙规则摘要：nftables 优先回退 iptables，chain/rule/DROP/REJECT 统计 | ✅ |
| `selinux_status` | `plugins/sysdiag/selinux.go` | SELinux/AppArmor 状态+最近拒绝，自动检测 MAC 类型，ausearch/journalctl 获取 denied | ✅ |
| `proc_threads` | `plugins/sysdiag/threads.go` | 进程线程列表（/proc/pid/task/），TID/名称/状态/CPU 时间，D 状态高亮 | ✅ |
| `systemd_timers` | `plugins/systemd/timers.go` | systemd timer 列表+下次触发时间，过期 timer 高亮，支持 --all | ✅ |

### P8：Tier C Timeout 排障专项工具 ✅ Done

| 工具 | 文件 | 说明 | 状态 |
| --- | --- | --- | --- |
| `listen_overflow` | `plugins/sysdiag/listen.go` | LISTEN socket 队列使用率（Recv-Q/Send-Q），ListenOverflows/ListenDrops 内核计数器 | ✅ |
| `tcp_retrans_rate` | `plugins/sysdiag/retrans.go` | TCP 重传/错误速率（两次采样 delta）：RetransSegs/s、InErrs/s、TCPTimeouts/s，含重传比 | ✅ |
| `disk_io_latency` | `plugins/sysdiag/disklatency.go` | 磁盘 IO 延迟（两次采样 /proc/diskstats）：IOPS、MB/s、await ms、%util，高延迟/饱和标注 | ✅ |
| `tcp_tuning_check` | `plugins/sysdiag/tcptune.go` | TCP 内核参数批量检查：SYN retry/keepalive/backlog/tw/内存/拥塞，推荐值范围对比 | ✅ |
| `conn_latency_summary` | `plugins/sysdiag/connlatency.go` | 按远端 IP:port 聚合 TCP RTT 分布：count/avg/p99/max，定位慢下游服务 | ✅ |
| `proc_threads` 增强 | `plugins/sysdiag/threads.go` | 新增 wchan 字段：显示线程在内核中的等待函数，D 状态线程排障关键信息 | ✅ |
| `softnet_stat` | `plugins/sysdiag/softnet.go` | 每 CPU softnet 统计：processed/dropped/time_squeeze，网卡层丢包检测 | ✅ |

### P9：MCP 外部数据源集成（已移除）

MCP 集成功能已从 catpaw 中移除。外部数据源的诊断扩展将通过其他方式实现。
