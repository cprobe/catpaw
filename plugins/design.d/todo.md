# 插件规划

## mount — 挂载点基线检查（已实现）

- **检查维度**：挂载存在性、文件系统类型、挂载选项合规
- **价值**：NFS 掉线、磁盘未挂载、安全挂载选项（noexec/nosuid/nodev）被移除，都是严重的运行时隐患
- **实现**：解析 `/proc/mounts`，仅 Linux


## zombie — 僵尸进程检测（已实现，独立插件）

- **实现方式**：独立插件，遍历系统进程列表，统计状态为 Z 的进程数
- **价值**：僵尸进程积累暗示父进程 bug，不处理会耗尽 PID 资源
- **默认配置**：warn_gt=0, critical_gt=20，开箱即用

## kernel taint — 内核污染检测（已实现，sysctl 配置示例）

- **实现方式**：通过 sysctl 插件检查 `kernel.tainted == 0`
- **价值**：内核被污染可能导致不稳定，也影响内核 bug 的官方支持

## netif — 网络接口健康检查（已实现）

- **检查维度**：链路状态（link up/down）、错误增量（rx_errors + tx_errors）、丢包增量（rx_dropped + tx_dropped）
- **价值**：网线松动、网卡驱动故障、ring buffer 满导致的静默丢包，都是最早通过网卡计数器暴露的
- **实现**：读 `/sys/class/net/*/statistics/` 和 `operstate`，仅 Linux

## 锦上添花

| 插件 | 说明 | 参考 |
| --- | --- | --- |
| tcpstate | CLOSE_WAIT/TIME_WAIT 异常堆积（已实现，Netlink） | Netlink `INET_DIAG` + `/proc/net/sockstat` |
| psi | Pressure Stall Information（CPU/IO/Memory 压力） | 读 `/proc/pressure/*`，Linux 4.20+ |
| smart | 磁盘 S.M.A.R.T 健康状态，预测硬盘故障 | Nagios `check_smart` |
| raid | 硬件/软件 RAID 阵列状态（mdadm、MegaCLI） | Nagios `check_raid` |
| mailq | 邮件队列积压检测（Postfix/Sendmail） | Nagios `check_mailq` |
