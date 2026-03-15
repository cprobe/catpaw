# 不记命令也能排障：catpaw chat 实战手册

> **TL;DR**：`catpaw chat` 让你用自然语言排障——说"最近有 OOM 吗"，AI 帮你查 dmesg；说"谁在吃磁盘"，AI 帮你跑 du 和 df。本文整理 12 个高频排障场景，每个都对比"传统命令行"和"一句话搞定"，附带 AI 在幕后调用了什么工具。即使你不用 catpaw，也能当作一份排障命令速查表。

## 一个灵魂拷问

你能在 5 秒内回忆出以下命令吗？

```bash
# 查看 TCP 各状态连接数
ss -ant | awk '{print $1}' | sort | uniq -c | sort -rn

# 查看某进程的线程及其 wchan（卡在哪个内核函数）
ls /proc/<pid>/task | while read tid; do
  echo -n "$tid "; cat /proc/$tid/wchan 2>/dev/null; echo
done

# 查看 conntrack 表使用率
echo "$(cat /proc/sys/net/netfilter/nf_conntrack_count) / $(cat /proc/sys/net/netfilter/nf_conntrack_max)" | bc -l

# 查看 Listen 队列溢出次数
grep -E 'ListenOverflows|ListenDrops' /proc/net/netstat

# 按磁盘 I/O 排序进程
awk '{if(NR>1 && $1~/[0-9]+/) print $0}' /proc/<pid>/io
```

记不住？正常。

这些命令不是用来记的——它们散落在 `/proc` 的几十个文件和几百个命令行工具里，每个有自己的语法和参数，组合方式千变万化。**哪怕是工作十年的 SRE，排障时也经常要 Google 一下。**

但现在是 2025 年了。

**你有没有想过：为什么排障的时候，人类要去适应机器的语言，而不是反过来？**

```
You: 最近有 OOM 吗？
AI:  发现 2 次 OOM Kill 事件……
```

这就是 `catpaw chat` 在做的事——你说人话，它帮你跑命令。

## catpaw chat 是什么

catpaw 是一个带 AI 大脑的轻量监控 Agent。`catpaw chat` 是它的交互排障模式：

```bash
./catpaw chat
```

启动后进入对话界面，你可以用自然语言描述问题，AI 会调用内置的 **90+ 诊断工具** 来帮你排查，工具覆盖 CPU、内存、磁盘、网络、进程、内核、日志、容器等各个领域。

关键特性：

- **不用记命令**：说"帮我看看磁盘"，AI 知道该跑什么
- **不用解析输出**：AI 看原始数据，给你总结好的结论
- **多步推理**：AI 会根据前一步结果决定下一步查什么（"内存高 → 查 Top 进程 → 查具体进程的 fd 和连接"）
- **安全可控**：诊断工具是只读的；如果需要执行 shell 命令，AI 会先征得你的同意

下面我们通过 12 个场景看看它怎么用。

## 场景 1：CPU 飙高了，谁在吃？

### 传统方式

```bash
# 先看整体
top -bn1 | head -20

# 看负载是否超过核数
uptime
nproc

# 找到 CPU 最高的进程
ps aux --sort=-%cpu | head -10

# 如果是 Java，还得 jstack
jstack <pid> > /tmp/thread_dump.txt
```

4 条命令，还得人工比对"负载 vs 核数"来判断是不是真的高。

### catpaw chat

```
You: CPU 好像有点高，帮我看看

AI: [调用 cpu_usage]
    当前 CPU 总使用率 87.3%，其中：
    - user: 72.1%, system: 12.8%, iowait: 2.4%
    
    [调用 cpu_load_average]
    负载 1m/5m/15m = 12.3/10.8/8.2，当前 4 核，
    负载/核 比值 3.08，显著偏高。
    
    [调用 top_cpu_processes]
    Top 3 CPU 进程：
    1. java (PID 8847) — 68.2% CPU, 运行 3d12h
    2. python3 (PID 12301) — 11.5% CPU, 运行 2h
    3. nginx worker (PID 3021) — 3.1% CPU

    结论：CPU 压力主要来自 PID 8847 的 Java 进程，持续占用
    68% CPU。建议检查该进程是否存在死循环或 GC 问题。
```

**AI 做了什么**：调用了 `cpu_usage`、`cpu_load_average`、`top_cpu_processes` 三个工具，自动比对"负载/核数"的比值，帮你定位到具体进程。

## 场景 2：最近有没有 OOM？

### 传统方式

```bash
# 方法 1：从 dmesg 里找
dmesg | grep -i "out of memory"
dmesg | grep -i "oom"

# 方法 2：从 journal 里找
journalctl -k --since "24 hours ago" | grep -i oom

# 找到了还得手动解读：被 kill 的是谁、RSS 多大、当时内存什么情况
dmesg | grep -A 5 "Killed process"
```

光是"怎么 grep"就有两种路径，找到了还得人工解析 OOM 日志的格式。

### catpaw chat

```
You: 最近有 OOM 吗？

AI: [调用 oom_history]
    发现 2 次 OOM Kill 事件：

    ┌───────────────────┬─────────┬──────┬─────────┐
    │ 时间               │ 进程    │  PID │ RSS     │
    ├───────────────────┼─────────┼──────┼─────────┤
    │ 03-14 03:12:08    │ java    │ 8847 │ 7.8 GB  │
    │ 03-14 02:45:31    │ python3 │ 9123 │ 3.2 GB  │
    └───────────────────┴─────────┴──────┴─────────┘
    
    两次事件均发生在凌晨，间隔约 30 分钟。
    java 进程 RSS 达 7.8GB，可能存在内存泄漏。
    建议检查 JVM heap 配置和 cgroup 内存限制。

You: 看看当前的 cgroup 内存限制

AI: [调用 cgroup_usage]
    PID 12305 (java) 的 cgroup 内存限制：
    - memory.limit: 8.0 GB
    - memory.usage: 6.1 GB (76.3%)
    - memory.max_usage: 7.9 GB
    
    进程接近 cgroup 内存上限。上次 OOM 时 RSS 7.8GB 
    已非常接近 8GB 限制。
```

**AI 做了什么**：第一轮调用 `oom_history`（解析 dmesg 中的 OOM 事件），把原始日志整理成表格。你追问 cgroup，它又调用 `cgroup_usage` 查看内存限制——整个过程是多轮推理，不是一次性查完。

## 场景 3：磁盘空间告急

### 传统方式

```bash
# 看哪个分区满了
df -h

# 找大目录
du -sh /* 2>/dev/null | sort -rh | head -10
du -sh /var/* 2>/dev/null | sort -rh | head -10

# 看 inode（有时候文件太多 inode 先满）
df -i

# 查是不是有大文件被删了但进程还持有
lsof +L1 | head -20
```

每个 `du` 命令在大目录下要跑几十秒，而且你得手动一层层挖。

### catpaw chat

```
You: 磁盘快满了，帮我分析一下

AI: [调用 disk_usage]
    ┌────────┬───────┬──────┬──────┬────────┐
    │ 挂载点 │ 总量   │ 已用 │ 可用 │ 使用率 │
    ├────────┼───────┼──────┼──────┼────────┤
    │ /      │ 50 GB │ 47 GB│ 3 GB │ 94.0%  │
    │ /data  │ 200 GB│ 82 GB│118 GB│ 41.0%  │
    └────────┴───────┴──────┴──────┴────────┘
    
    根分区使用率 94%，接近满。

    [调用 disk_io_counters]
    根分区所在设备 vda 的 I/O 正常，非性能问题。

    建议我帮你查看根分区下哪些目录占用最多空间吗？

You: 好，查一下

AI: [调用 exec_shell: du -sh /* 2>/dev/null | sort -rh | head -10]
    ⚠️ 需要执行 shell 命令：du -sh /* | sort -rh | head -10
    是否允许？[Y/n]

You: y

AI: 根分区 Top 目录：
    - /var     32.1 GB
    - /usr     8.2 GB
    - /home    4.3 GB

    /var 最大。继续深入查看 /var 子目录：

    [调用 exec_shell: du -sh /var/* 2>/dev/null | sort -rh | head -10]
    - /var/log        28.7 GB
    - /var/lib        2.8 GB

    /var/log 占用 28.7 GB。再看具体文件：

    [调用 exec_shell: ls -lhS /var/log/*.log | head -5]
    - /var/log/app/access.log    25.3 GB

    根因：/var/log/app/access.log 占用 25.3 GB，疑似日志未
    配置轮转。建议配置 logrotate 并清理历史日志。
```

注意这里的安全机制：诊断工具是只读的直接执行；`du` 这种 shell 命令需要你确认才执行。AI 会自动逐层深入，最终帮你定位到具体文件。

## 场景 4：网络不通，不知道卡在哪

### 传统方式

```bash
# 先 ping 看通不通
ping -c 3 10.0.1.5

# 不通的话 traceroute 看卡在哪一跳
traceroute 10.0.1.5

# 域名解析有没有问题
dig api.example.com
nslookup api.example.com

# TCP 端口通不通
telnet 10.0.1.5 8080
# 或者
nc -zv 10.0.1.5 8080

# 防火墙规则
iptables -L -n
# 或者新版
nft list ruleset
```

先 ping、再 traceroute、再查 DNS、再试端口、再看防火墙——每一步都要根据上一步的结果决定下一步做什么。这个决策链条本身就需要经验。

### catpaw chat

```
You: 我的服务连不上 10.0.1.5:8080，帮我查查

AI: [调用 ping_check: host=10.0.1.5]
    PING 10.0.1.5: 5 packets, 0% loss, avg RTT 1.2ms
    → 网络层可达。

    [调用 tcp_ping: host=10.0.1.5, port=8080]
    TCP 连接 10.0.1.5:8080 失败：connection refused
    → 端口未监听或被防火墙阻断。

    [调用 traceroute: host=10.0.1.5]
    路由正常，共 3 跳，无丢包。

    结论：
    - 网络层：ICMP 可达，延迟正常
    - 传输层：TCP 8080 端口连接被拒绝（connection refused）
    - 路由：正常
    
    大概率是目标机器的 8080 端口未启动服务，或本地防火墙
    未放行。建议到目标机器检查服务状态和防火墙规则。
```

**AI 做了什么**：自动按"ICMP → TCP → 路由"的顺序逐层排查，每一步根据结果决定下一步——这就是 SRE 的排查逻辑，AI 替你跑了一遍。

## 场景 5：CLOSE_WAIT 堆积，哪个服务在泄漏？

### 传统方式

```bash
# 看 TCP 状态分布
ss -ant | awk '{print $1}' | sort | uniq -c | sort -rn

# 找 CLOSE_WAIT 对应的进程
ss -tnp state close-wait

# 按远端地址聚合
ss -tnp state close-wait | awk '{print $5}' | cut -d: -f1 | sort | uniq -c | sort -rn

# 进一步查是连谁的
ss -tnp state close-wait | grep <pid>
```

这套 `ss` + `awk` + `sort` 的组合拳，没几个人能一次性敲对。

### catpaw chat

```
You: CLOSE_WAIT 好像很多，帮我看看

AI: [调用 tcp_state_distribution]
    TCP 连接状态分布：
    - ESTABLISHED: 342
    - CLOSE_WAIT:  1,847  ← 异常偏高
    - TIME_WAIT:   128
    - LISTEN:      23

    CLOSE_WAIT 达 1847，明显异常（正常应接近 0）。

    [调用 top_connections_by_port]
    CLOSE_WAIT 按远端端口统计：
    1. :3306 (MySQL) — 1,203 条
    2. :6379 (Redis) — 589 条
    3. :80 (HTTP) — 55 条

    [调用 top_connections_by_local_port]
    CLOSE_WAIT 按本地端口统计：
    1. :8080 — 1,847 条 (来自 java, PID 8847)

    结论：PID 8847 的 Java 服务累积了 1847 个 CLOSE_WAIT 
    连接，主要连向 MySQL(:3306) 和 Redis(:6379)。
    
    这是典型的连接泄漏——应用收到远端的 FIN 后未调用 close()。
    建议检查数据库和 Redis 连接池的关闭逻辑。
```

**AI 做了什么**：调用 `tcp_state_distribution` 看全局分布，发现 CLOSE_WAIT 异常后，用 `top_connections_by_port` 和 `top_connections_by_local_port` 从两个维度定位——哪个进程、连的谁。三个工具串联，逻辑清晰。

## 场景 6：进程到底在干啥？

### 传统方式

```bash
# 基本信息
ps -p <pid> -o pid,ppid,user,%cpu,%mem,etime,args

# 线程
ls /proc/<pid>/task | wc -l
ps -T -p <pid>

# 打开的文件
ls -l /proc/<pid>/fd | wc -l
ls -l /proc/<pid>/fd | head -20

# 网络连接
ss -tnp | grep "pid=<pid>"

# 内存映射
cat /proc/<pid>/smaps_rollup

# 环境变量
cat /proc/<pid>/environ | tr '\0' '\n'
```

每个维度一条命令，全查一遍至少要敲六七条。

### catpaw chat

```
You: PID 8847 好像有问题，帮我全面看看

AI: [调用 process_detail: pid=8847]
    PID 8847 — java
    ├─ User: app
    ├─ Status: Running
    ├─ CPU: 68.2%
    ├─ RSS: 6.1 GB
    ├─ Threads: 247
    ├─ FDs: 4,892 / 65535 (7.5%)
    ├─ Uptime: 3d 12h
    └─ Cmdline: java -Xmx7g -jar /opt/app/service.jar

    [调用 open_files: pid=8847]
    FD 类型分布：
    - socket: 3,214 (65.7%)
    - regular file: 1,523 (31.1%)
    - pipe: 155 (3.2%)
    
    socket 数量异常多，与 CLOSE_WAIT 堆积一致。

    [调用 proc_threads: pid=8847]
    线程状态：
    - Running: 3
    - Sleeping: 238
    - Disk sleep: 6
    
    有 6 个线程处于 D 状态（不可中断睡眠），
    wchan 显示卡在 io_schedule，疑似磁盘 I/O 阻塞。

    综合判断：
    1. CPU 68% + 247 线程 → 高负载
    2. 3214 个 socket FD → 连接泄漏（CLOSE_WAIT）
    3. 6 个 D 状态线程 → I/O 阻塞
    建议优先修复连接泄漏，同时排查 I/O 阻塞原因。
```

一句"帮我全面看看"，AI 自己决定从哪几个维度切入。

## 场景 7：系统内核有没有异常？

### 传统方式

```bash
# 看 dmesg（内容巨多，怎么筛选？）
dmesg -T | tail -100
dmesg -T | grep -iE "error|warn|fail|oom|bug|panic"

# 看硬件错误
dmesg -T | grep -iE "hardware|mce|edac"

# 看 I/O 错误
dmesg -T | grep -iE "i/o error|sector"

# 看网络异常
dmesg -T | grep -iE "link|carrier|nf_conntrack"
```

最大的问题不是命令难记，而是 dmesg 的输出像个垃圾堆——几千行内核日志里，你需要凭经验判断哪些是信号、哪些是噪音。

### catpaw chat

```
You: 内核有没有什么异常？

AI: [调用 dmesg_recent]
    最近 24h 内核日志摘要：
    
    ⚠️ OOM (2 次)
    - 03:12 Out of memory: Killed process 8847 (java), RSS 7.8GB
    - 02:45 Out of memory: Killed process 9123 (python3), RSS 3.2GB
    
    ⚠️ I/O Error (1 次)
    - 14:32 blk_update_request: I/O error, dev sdb, sector 123456
    
    ℹ️ Network
    - nf_conntrack: table full, dropping packet (3 次)
    
    无 MCE/硬件错误，无 kernel panic/BUG。

    重点关注：
    1. 两次 OOM Kill 事件，建议检查内存配置
    2. sdb 磁盘有 I/O 错误，可能是磁盘故障的前兆
    3. conntrack 表满导致丢包，建议调大 nf_conntrack_max
```

**AI 做了什么**：`dmesg_recent` 工具自动过滤噪音，按类别（OOM / I/O / 网络 / 硬件）分类呈现，比你手动 grep 五六次效率高得多。

## 场景 8：证书什么时候过期？

### 传统方式

```bash
# 远程端点
echo | openssl s_client -servername api.example.com \
  -connect api.example.com:443 2>/dev/null | \
  openssl x509 -noout -dates -subject -issuer

# 本地证书文件
openssl x509 -in /etc/ssl/certs/server.crt -noout -dates -subject

# 想看完整证书链？更复杂
echo | openssl s_client -showcerts -servername api.example.com \
  -connect api.example.com:443 2>/dev/null
```

openssl 命令的参数组合大概是 Linux 世界里最难记的东西之一。`s_client` 还是 `x509`？`-noout` 还是 `-text`？管道怎么接？每次都得搜。

### catpaw chat

```
You: 帮我看看 api.example.com 的证书什么时候过期

AI: [调用 cert_inspect_remote: host=api.example.com, port=443]
    TLS 证书信息：
    ├─ Subject: api.example.com
    ├─ Issuer: Let's Encrypt Authority X3
    ├─ Not Before: 2025-01-15
    ├─ Not After:  2025-04-15 (还剩 31 天)
    ├─ SANs: api.example.com, *.example.com
    └─ Chain: 2 certificates, valid

    证书将在 31 天后过期，建议尽快续期。

You: 本地的 /etc/ssl/server.crt 呢？

AI: [调用 cert_inspect_file: path=/etc/ssl/server.crt]
    本地证书信息：
    ├─ Subject: *.internal.example.com
    ├─ Not After: 2025-09-01 (还剩 170 天)
    └─ Self-signed: Yes

    该证书还有 170 天有效期，暂时无需处理。
```

再也不用和 openssl 的参数搏斗了。

## 场景 9：TCP 重传严重吗？

### 传统方式

```bash
# 从 /proc/net/snmp 读取
cat /proc/net/snmp | grep Tcp

# 算重传率需要采样两次做差
watch -n 1 'cat /proc/net/snmp | grep Tcp'

# 或者用 netstat -s
netstat -s | grep -i retrans

# 更细致的 TcpExt
cat /proc/net/netstat | grep TcpExt
```

这些计数器都是累计值，看绝对数没意义，你需要采样、算增量、除以总发送量得到比率。手工算很麻烦。

### catpaw chat

```
You: TCP 重传率高不高？

AI: [调用 tcp_retrans_rate]
    TCP 重传统计（采样 3 秒）：
    ├─ 发送段数:   12,847/s
    ├─ 重传段数:   23/s
    ├─ 重传率:     0.18%
    ├─ 快速重传:   15/s
    └─ 超时重传:   8/s

    重传率 0.18%，处于正常范围（<1%）。
    
    [调用 conn_latency_summary]
    按远端 IP 聚合的 TCP RTT：
    - 10.0.1.5:3306  avg 1.2ms  (正常)
    - 10.0.2.8:6379  avg 0.8ms  (正常)
    - 172.16.0.3:80   avg 45.3ms (偏高)
    
    172.16.0.3 的 RTT 偏高，可能是跨机房延迟。
    如果觉得重传偏多，可以重点排查到该 IP 的链路。
```

**AI 做了什么**：`tcp_retrans_rate` 工具自动采样、算增量、除以总量，直接给你百分比。然后 `conn_latency_summary` 按远端 IP 聚合 RTT，帮你定位哪条链路有问题。

## 场景 10：容器为啥一直重启？

### 传统方式

```bash
# 看容器状态
docker ps -a | grep <name>

# 看退出原因
docker inspect <id> --format='{{.State.ExitCode}} {{.State.Error}} {{.State.OOMKilled}}'

# 看日志
docker logs --tail 100 <id>

# 看资源限制
docker inspect <id> --format='{{.HostConfig.Memory}} {{.HostConfig.CpuQuota}}'
```

`docker inspect` 的 Go template 语法堪称反人类。

### catpaw chat

```
You: my-api 容器一直在重启，什么情况？

AI: [调用 docker_ps]
    容器列表（异常项）：
    my-api  Status: Restarting  RestartCount: 47  
    Created: 2 days ago

    [调用 docker_inspect: container=my-api]
    容器详情：
    ├─ 退出码: 137 (被 kill)
    ├─ OOMKilled: true  ← 内存不足被杀
    ├─ 内存限制: 512 MB
    ├─ 健康检查: unhealthy (last 5 checks failed)
    └─ 最后退出时间: 2 分钟前

    根因：容器内存限制 512MB，进程因 OOM 被 kill 后自动重
    启，已累计重启 47 次。建议增大内存限制或排查应用内存泄漏。
```

## 场景 11：Listen 队列有没有溢出？

### 传统方式

```bash
# 看溢出计数（累计值，需要算增量）
grep 'ListenOverflows' /proc/net/netstat

# 看各 listen socket 的队列情况
ss -tlnp | awk 'NR>1 {print $2, $3, $4}'

# 看 somaxconn
sysctl net.core.somaxconn
```

你需要知道 `/proc/net/netstat` 里有 `ListenOverflows` 这个字段，还得知道它是 `TcpExt` 段下的——这属于"不知道自己不知道"的知识。

### catpaw chat

```
You: 有没有 TCP listen 队列溢出的情况？

AI: [调用 listen_overflow]
    Listen 队列状态：
    
    ┌──────────┬──────┬─────────┬───────────┬─────────┐
    │ 端口     │ 进程 │ Backlog │ Send-Q    │ 使用率  │
    ├──────────┼──────┼─────────┼───────────┼─────────┤
    │ :8080    │ java │ 128     │ 3         │ 2.3%    │
    │ :3000    │ node │ 511     │ 0         │ 0%      │
    │ :80      │ nginx│ 511     │ 0         │ 0%      │
    └──────────┴──────┴─────────┴───────────┴─────────┘
    
    ListenOverflows 增量: 0 (最近采样周期内无溢出)
    ListenDrops 增量: 0
    
    当前无溢出。但 :8080 的 backlog 只有 128，
    如果流量增长，建议调大 somaxconn 和应用的 backlog 参数。

    [调用 tcp_tuning_check]
    TCP 调优参数检查：
    - net.core.somaxconn = 128 (建议 ≥ 1024)
    - net.ipv4.tcp_max_syn_backlog = 256 (建议 ≥ 1024)
    
    ⚠️ somaxconn 偏低，限制了所有 listen socket 的队列上限。
```

AI 不仅查了当前状态，还主动检查了 TCP 内核参数并给出调优建议。

## 场景 12：日志里搜关键字

### 传统方式

```bash
# 简单搜索
grep "ERROR" /var/log/app/app.log | tail -20

# 带时间范围（假设日志格式是 2025-03-14）
grep "2025-03-14" /var/log/app/app.log | grep "ERROR"

# 看上下文
grep -n -B2 -A5 "NullPointerException" /var/log/app/app.log | tail -50

# 统计频率
grep -c "ERROR" /var/log/app/app.log

# 日志文件太大？先看最后的
tail -10000 /var/log/app/app.log | grep "ERROR"
```

grep 本身不难，但"搜什么关键字"和"怎么缩小范围"需要经验。

### catpaw chat

```
You: 看看应用日志里最近有没有报错

AI: [调用 log_tail: file=/var/log/app/app.log, lines=50]
    最后 50 行日志中发现 12 条 ERROR 级别日志。

    [调用 log_grep: file=/var/log/app/app.log, 
     pattern="ERROR|Exception", last_n=5000]
    最近 5000 行中的错误：
    
    频率 Top 3：
    1. "ConnectionPoolTimeoutException" — 87 次
    2. "SocketTimeoutException: Read timed out" — 23 次
    3. "NullPointerException at UserService.java:142" — 5 次
    
    最近一条：
    2025-03-14 14:32:18 ERROR ConnectionPoolTimeoutException: 
    Timeout waiting for connection from pool
    
    大量 ConnectionPoolTimeoutException 与之前发现的 
    CLOSE_WAIT 堆积一致——连接池被泄漏的连接耗尽，
    新请求拿不到连接。
```

注意 AI 会**关联上下文**——之前聊过 CLOSE_WAIT 堆积，现在在日志里看到连接池超时，AI 把两者串联了起来。

## 幕后：AI 的工具箱

上面 12 个场景展示的只是冰山一角。`catpaw chat` 的 AI 有 **90+ 诊断工具**可用，覆盖以下领域：

| 领域 | 工具数 | 典型工具 |
| --- | --- | --- |
| ⚙️ CPU | 3 | `cpu_usage`, `cpu_load_average`, `top_cpu_processes` |
| 🧠 内存 | 3 | `mem_usage`, `swap_usage`, `top_mem_processes` |
| 💾 磁盘 | 6 | `disk_usage`, `disk_io_counters`, `disk_io_latency`, `lvm_status` |
| 🌐 网络 | 15+ | `ping_check`, `traceroute`, `dns_resolve`, `tcp_ping`, `net_interface` |
| 🔌 TCP | 8 | `tcp_state_distribution`, `tcp_retrans_rate`, `listen_overflow`, `conn_latency_summary` |
| 📋 进程 | 7 | `process_detail`, `proc_threads`, `open_files`, `env_inspect` |
| 🔐 内核 | 10+ | `dmesg_recent`, `oom_history`, `sysctl_snapshot`, `conntrack_stat`, `psi_check` |
| 📜 日志 | 3 | `log_tail`, `log_grep`, `journal_query` |
| 🐳 容器 | 2 | `docker_ps`, `docker_inspect` |
| 🔒 安全 | 2 | `firewall_summary`, `selinux_status` |
| 🌡️ 硬件 | 3 | `thermal_zone`, `numa_stat`, `block_devices` |
| 📡 证书/HTTP | 4 | `cert_inspect_remote`, `cert_inspect_file`, `http_probe`, `dns_lookup` |

如果配置了 **MCP（Model Context Protocol）**，AI 还能查询 Prometheus 历史指标、Jaeger 链路追踪等外部数据源——这意味着它不仅能看到"现在"，还能看到"过去"。

而且，当工具不够用时，AI 还有 `exec_shell` 这个兜底手段——直接执行 shell 命令。当然，每次执行前都会征得你的同意。

## 它不能做什么

说了这么多好处，也得说说局限：

**1. 不能替代对系统的理解**

AI 给你的结论是建议，不是命令。"建议增大 somaxconn"——增大到多少合适？会不会影响内存？这些判断还得依赖你对业务和系统的理解。

**2. 需要配置 AI 模型**

`catpaw chat` 需要配置 LLM（支持 OpenAI 兼容 API），运行时有 token 消耗。不配置 AI，这个功能用不了。

**3. 复杂分析还是得靠人**

性能调优、架构问题、跨服务链路分析——AI 能给你原始数据和初步判断，但深度分析仍需人类专家。AI 是你的助手，不是你的替身。

**4. 不是实时监控**

`catpaw chat` 是交互式排障工具，不是持续监控。持续监控用 `catpaw run`（插件定期检查 + 告警），出了问题再用 `catpaw chat` 深入排查。

## 快速开始

### 1. 下载 catpaw

```bash
wget https://github.com/cprobe/catpaw/releases/latest/download/catpaw-linux-amd64.tar.gz
tar xzf catpaw-linux-amd64.tar.gz && cd catpaw
```

### 2. 配置 AI

在 `conf.d/config.toml` 中添加：

```toml
[ai]
enabled = true
model_priority = ["default"]

[ai.models.default]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_API_KEY}"
model = "gpt-4o"
```

支持所有 OpenAI 兼容 API（OpenAI、Claude、通义千问、DeepSeek 等），只需修改 `base_url` 和 `model`。

### 3. 开聊

```bash
export OPENAI_API_KEY="your-key"
./catpaw chat
```

试试这些问题：
- "帮我做个系统全面体检"
- "CPU 高不高？谁在吃？"
- "最近有没有 OOM 或者内核报错？"
- "看看网络有没有丢包或重传"
- "CLOSE_WAIT 多不多？"
- "磁盘 I/O 延迟怎么样？"

## 写在最后

命令行是强大的——这一点毋庸置疑。`ss`、`awk`、`/proc` 文件系统，这些是 Linux 排障的基石。

但"强大"和"好用"是两件事。

你不需要记住 `ss -ant | awk '{print $1}' | sort | uniq -c | sort -rn` 才能查 TCP 状态分布，就像你不需要记住经纬度才能导航——有工具帮你翻译。

`catpaw chat` 做的就是这件事：**把你的自然语言翻译成系统命令，把系统输出翻译成人类结论。**

如果你觉得这 12 个场景里有哪个曾经困扰过你——试试 `catpaw chat`，用你自己的话问一遍。

---

**GitHub**：[https://github.com/cprobe/catpaw](https://github.com/cprobe/catpaw)

**微信交流群**：加 `picobyte`，备注 `catpaw`
