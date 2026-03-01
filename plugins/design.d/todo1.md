# 插件规划

## sysctl — 内核参数基线检查

- **检查维度**：`param_mismatch`（实际值与期望值不匹配告警）
- **价值**：防止内核参数在重启、升级后丢失调优配置（如 `net.core.somaxconn` 被重置为 128 导致连接队列溢出）
- **配置示例**：用户定义期望基线，插件逐项比对
  ```toml
  [instances.param_mismatch]
  params = [
    { key = "net.core.somaxconn", expect = "65535", op = "ge" },
    { key = "vm.swappiness", expect = "10", op = "eq" },
    { key = "net.ipv4.ip_forward", expect = "1", op = "eq" },
  ]
  ```
- **实现**：读 `/proc/sys/` 对应路径（如 `net.core.somaxconn` → `/proc/sys/net/core/somaxconn`）
- **比较操作**：支持 `eq`（等于）、`ge`（大于等于）、`le`（小于等于）、`ne`（不等于）
- **参考**：Nagios `check_sysctl`

## selinux — 安全模块状态检查

- **检查维度**：`enforce_mode`（SELinux 实际模式是否与期望一致）
- **价值**：运维人员常临时 `setenforce 0` 排障后忘记恢复，导致安全策略长期失效
- **实现**：读 `/sys/fs/selinux/enforce`（0=permissive, 1=enforcing），或 `getenforce` 命令
- **扩展**：可选检查 AppArmor 状态（`/sys/module/apparmor/parameters/enabled`）
- **参考**：Nagios `check_selinux`

## mount — 挂载点一致性检查

- **检查维度**：`expected_mounts`（期望的挂载点是否存在）、`mount_options`（挂载选项是否符合预期）
- **价值**：NFS 掉线、磁盘未挂载、安全挂载选项（noexec/nosuid/nodev）被移除，都是严重的运行时隐患
- **与 disk 插件的区别**：disk 关注容量，mount 关注"是否挂载"和"挂载选项是否正确"
- **实现**：解析 `/proc/mounts`
- **配置示例**：
  ```toml
  [instances.expected_mounts]
  mounts = [
    { path = "/data", fstype = "ext4" },
    { path = "/backup", fstype = "nfs" },
  ]
  [instances.mount_options]
  checks = [
    { path = "/tmp", must_have = ["noexec", "nosuid"] },
  ]
  ```
- **参考**：Nagios `check_mount`

## dmesg — 内核日志监控

- **检查维度**：`kernel_error`（匹配指定模式的内核消息触发告警）
- **价值**：OOM Kill、硬件故障（MCE/ECC）、文件系统错误（ext4 error）、磁盘 I/O 错误等关键内核事件，应用层完全无感知
- **实现**：执行 `dmesg --time-format iso -l err,crit,alert,emerg` 并过滤新增消息（记录上次读取的时间戳）
- **默认匹配模式**：`Out of memory`、`I/O error`、`EXT4-fs error`、`XFS.*error`、`Hardware Error`、`mce:`
- **与 journaltail 的区别**：journaltail 针对 systemd 服务日志，dmesg 专门针对内核环缓冲区
- **参考**：Nagios `check_dmesg`

## coredump — 核心转储检测

- **检查维度**：`new_coredump`（指定目录下最近 N 分钟内是否出现新的 coredump 文件）
- **价值**：服务 crash 但被 systemd 自动拉起时，coredump 是唯一的痕迹；积累大量 coredump 还会耗尽磁盘空间
- **实现**：扫描 coredump 目录（默认 `/var/lib/systemd/coredump/`），按文件修改时间筛选
- **注意**：可选集成 `coredumpctl list --since` 获取更结构化的信息
- **参考**：无直接对标，属于 catpaw 特色功能

## 第四档：锦上添花

| 插件 | 说明 | 参考 |
| --- | --- | --- |
| smart | 磁盘 S.M.A.R.T 健康状态，预测硬盘故障 | Nagios `check_smart` |
| raid | 硬件/软件 RAID 阵列状态（mdadm、MegaCLI） | Nagios `check_raid` |
| backup | 备份文件新鲜度检查（最近 N 小时内是否有新备份） | filecheck 的 stale 模式可部分替代 |
| mailq | 邮件队列积压检测（Postfix/Sendmail） | Nagios `check_mailq` |
| apt | 可用安全更新数量 | Nagios `check_apt` |
| entropy | 系统熵池不足检测（影响 TLS/加密性能） | 读 `/proc/sys/kernel/random/entropy_avail` |
