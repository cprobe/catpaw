# 插件规划

## mount — 挂载点基线检查（已实现）

- **检查维度**：挂载存在性、文件系统类型、挂载选项合规
- **价值**：NFS 掉线、磁盘未挂载、安全挂载选项（noexec/nosuid/nodev）被移除，都是严重的运行时隐患
- **实现**：解析 `/proc/mounts`，仅 Linux


## 锦上添花

| 插件 | 说明 | 参考 |
| --- | --- | --- |
| smart | 磁盘 S.M.A.R.T 健康状态，预测硬盘故障 | Nagios `check_smart` |
| raid | 硬件/软件 RAID 阵列状态（mdadm、MegaCLI） | Nagios `check_raid` |
| mailq | 邮件队列积压检测（Postfix/Sendmail） | Nagios `check_mailq` |
