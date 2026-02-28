# 插件规划

## 第一档：基础必备

### mem — 内存/Swap 监控

- **检查维度**：`memory_usage`（内存使用率）、`swap_usage`（Swap 使用率）
- **实现**：`gopsutil/v3/mem` 的 `VirtualMemory()` 和 `SwapMemory()`（已是项目依赖）
- **参考**：Nagios `check_memory`、Sensu `check-memory-percent`

### cpu — CPU / 负载监控

- **检查维度**：`load_average`（1/5/15 分钟负载阈值）、`cpu_usage`（CPU 使用率阈值，需采样两个时间点）
- **实现**：`gopsutil/v3/load` 获取 Load Average，`gopsutil/v3/cpu` 获取 CPU 利用率
- **参考**：Nagios `check_load`、Sensu `check-cpu`

### logfile — 纯文本日志文件监控

- **核心能力**：跟踪文件偏移量（类似 tail -f）只处理新增内容、支持日志轮转（log rotation）检测、复用现有 filter 包做 filter_include / filter_exclude
- **定位**：补充 journaltail（依赖 journalctl）和 scriptfilter（需自行写脚本）的不足，覆盖直接写文件的应用日志场景
- **参考**：Nagios `check_log`、Sensu `check-log`

## 第二档：常见场景

### cert — TLS 证书检查

- **检查维度**：`remote_expiry`（远程 TLS 连接获取证书过期时间）、`file_expiry`（本地证书文件过期时间）
- **远程模式**：`crypto/tls.Dial` 连接任意 host:port 获取证书链，覆盖 MySQL TLS、SMTP STARTTLS、gRPC 等非 HTTP 协议
- **本地模式**：读取 PEM/DER 文件解析证书，检测 Nginx/Let's Encrypt 等证书文件的过期时间
- **与 http 插件的区别**：http 的 cert_expiry 只能检查 HTTPS 端点，cert 插件覆盖所有 TLS 协议和磁盘上的证书文件
- **参考**：Nagios `check_ssl_cert`、Sensu `check-tls-cert`

### reboot — 异常重启检测

- **检查维度**：uptime 小于阈值时产生一次性告警
- **实现**：`gopsutil/v3/host` 的 `Uptime()` 或读 `/proc/uptime`
- **参考**：Nagios `check_uptime`

### docker — Docker 容器健康检查

- **检查维度**：`container_running`（指定容器是否在运行）、`restart_count`（重启次数是否超阈值，检测 crashloop）
- **实现**：Docker Engine API（HTTP over Unix socket `/var/run/docker.sock`），无需引入 Docker SDK
- **参考**：Sensu `check-docker-container`

### port — 监听端口检查

- **检查维度**：`expected_ports`（这些端口必须在监听）、`unexpected_ports`（可选，意外端口告警）
- **与 net 插件的区别**：net 是从外部连接测试，port 是检查本机 ss/netstat 状态
- **实现**：`gopsutil/v3/net` 的 `Connections()` 或读 `/proc/net/tcp`
- **参考**：Nagios `check_ports`

### ntp — 时钟同步检查

- **检查维度**：`clock_offset`（时钟偏移超阈值告警）、`sync_status`（同步服务是否正常运行）
- **价值**：时钟不准会导致分布式系统异常、证书校验失败、日志时间错乱，且问题极难排查
- **实现**：解析 `chronyc tracking` 或 `ntpq -p` 输出，读取 offset 和 reach 字段
- **跨平台**：Linux 下 chrony/ntpd，macOS 下 `sntp`，Windows 下 `w32tm /query /status`
- **参考**：Nagios `check_ntp_time`、Sensu `check-ntp`

### conntrack — 连接跟踪表监控

- **检查维度**：`conntrack_usage`（`nf_conntrack_count / nf_conntrack_max` 使用率）
- **价值**：连接跟踪表满时新连接被内核静默丢弃，表现为随机连接失败，是最难排查的网络问题之一
- **实现**：读 `/proc/sys/net/netfilter/nf_conntrack_count` 和 `nf_conntrack_max`
- **注意**：未加载 nf_conntrack 模块的机器应优雅跳过（返回 Ok 而非报错）
- **参考**：Prometheus `node_exporter` 的 conntrack collector

## 第三档：系统配置基线检查

### ulimit — 进程资源限制检查

- **检查维度**：`fd_usage`（系统级文件描述符使用率 `fs.file-nr`）、`process_fd`（指定进程的 fd 使用率 = 实际打开 / nofile 上限）、`process_nproc`（指定进程的线程数使用率）
- **价值**：fd 耗尽是最常见的生产事故原因之一，表现为 "too many open files" 导致服务拒绝新连接。通常到出错才发现 ulimit 配置太低
- **实现**：系统级读 `/proc/sys/fs/file-nr`；进程级读 `/proc/{pid}/limits` 获取上限，`/proc/{pid}/fd` 计数获取实际使用
- **配置**：支持按进程名/PID/用户指定检查目标，类似 procnum 的匹配方式
- **参考**：Nagios `check_open_files`、Sensu `check-fd`

### sysctl — 内核参数基线检查

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

### selinux — 安全模块状态检查

- **检查维度**：`enforce_mode`（SELinux 实际模式是否与期望一致）
- **价值**：运维人员常临时 `setenforce 0` 排障后忘记恢复，导致安全策略长期失效
- **实现**：读 `/sys/fs/selinux/enforce`（0=permissive, 1=enforcing），或 `getenforce` 命令
- **扩展**：可选检查 AppArmor 状态（`/sys/module/apparmor/parameters/enabled`）
- **参考**：Nagios `check_selinux`

### mount — 挂载点一致性检查

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

### dmesg — 内核日志监控

- **检查维度**：`kernel_error`（匹配指定模式的内核消息触发告警）
- **价值**：OOM Kill、硬件故障（MCE/ECC）、文件系统错误（ext4 error）、磁盘 I/O 错误等关键内核事件，应用层完全无感知
- **实现**：执行 `dmesg --time-format iso -l err,crit,alert,emerg` 并过滤新增消息（记录上次读取的时间戳）
- **默认匹配模式**：`Out of memory`、`I/O error`、`EXT4-fs error`、`XFS.*error`、`Hardware Error`、`mce:`
- **与 journaltail 的区别**：journaltail 针对 systemd 服务日志，dmesg 专门针对内核环缓冲区
- **参考**：Nagios `check_dmesg`

### coredump — 核心转储检测

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
