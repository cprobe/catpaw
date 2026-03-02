# 插件规划

| 插件 | 说明 | 参考 |
| --- | --- | --- |
| psi | Pressure Stall Information（CPU/IO/Memory 压力） | 读 `/proc/pressure/*`，Linux 4.20+ |
| smart | 磁盘 S.M.A.R.T 健康状态，预测硬盘故障 | Nagios `check_smart` |
| raid | 硬件/软件 RAID 阵列状态（mdadm、MegaCLI） | Nagios `check_raid` |
| mailq | 邮件队列积压检测（Postfix/Sendmail） | Nagios `check_mailq` |
