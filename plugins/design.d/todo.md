# 插件规划

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

## coredump — 核心转储检测

- **检查维度**：`new_coredump`（指定目录下最近 N 分钟内是否出现新的 coredump 文件）
- **价值**：服务 crash 但被 systemd 自动拉起时，coredump 是唯一的痕迹；积累大量 coredump 还会耗尽磁盘空间
- **实现**：扫描 coredump 目录（默认 `/var/lib/systemd/coredump/`），按文件修改时间筛选
- **注意**：可选集成 `coredumpctl list --since` 获取更结构化的信息
- **参考**：无直接对标，属于 catpaw 特色功能

## 锦上添花

| 插件 | 说明 | 参考 |
| --- | --- | --- |
| smart | 磁盘 S.M.A.R.T 健康状态，预测硬盘故障 | Nagios `check_smart` |
| raid | 硬件/软件 RAID 阵列状态（mdadm、MegaCLI） | Nagios `check_raid` |
| backup | 备份文件新鲜度检查（最近 N 小时内是否有新备份） | filecheck 的 stale 模式可部分替代 |
| mailq | 邮件队列积压检测（Postfix/Sendmail） | Nagios `check_mailq` |
| apt | 可用安全更新数量 | Nagios `check_apt` |
