# disk 插件设计报告

## 背景

catpaw 是一个轻量级事件监控系统，采用插件机制。disk 插件参考 Nagios `check_disk` 的思路，用 Go 原生实现，无外部依赖，支持跨平台。

已有依赖 `github.com/shirou/gopsutil/v3` 和 `github.com/gobwas/glob` 均已在 `go.mod` 中，无需新增依赖。

## 功能范围

### 1. 磁盘利用率检测

- 空间使用率超阈值告警
- inode 使用率超阈值告警（Windows 自动跳过）
- 支持 Warning / Critical 两档独立阈值

### 2. 读写健康检测

- 在挂载点下写入临时文件，读回内容校验，再删除
- 默认关闭，需要 catpaw 进程对目录有写权限
- 失败固定产生 Critical 事件
- 临时文件名固定为 `.catpaw_disk_check`，写入使用 `os.Create`（覆盖写），天然幂等
- 权限不足时 description 中明确标注，区分"磁盘故障"和"权限不足"，避免误报

## 配置设计

```toml
[[instances]]
# 留空 = 监控所有挂载点；非空 = 只监控匹配的挂载点，支持 glob 模式
mount_points = []

# 排除挂载点，支持 glob 模式
ignore_mount_points = ["/dev*", "/run*", "/sys*", "/proc*"]

# 排除文件系统类型
ignore_fs_types = ["tmpfs", "devtmpfs", "squashfs", "overlay", "nfs", "nfs4", "cifs"]

# 空间使用率阈值，0 = 禁用该档
warn_if_used_percent_ge     = 80.0
critical_if_used_percent_ge = 90.0

# inode 使用率阈值，0 = 禁用该档；Windows 下 InodesTotal=0 时自动跳过
warn_if_inodes_used_percent_ge     = 80.0
critical_if_inodes_used_percent_ge = 90.0

# 读写健康检测，默认关闭
check_writable     = false
writable_test_file = ".catpaw_disk_check"  # 相对于挂载点

# 并发控制：同时检查的挂载点数量上限
concurrency = 5

# 单次 Gather 超时时间，超时后未完成的挂载点下个周期报 "检查卡住"
gather_timeout = "10s"

check = "磁盘检测"

[instances.alerting]
enabled              = true
for_duration         = 0
repeat_interval      = "5m"
repeat_number        = 3
recovery_notification = true
```

## 告警事件设计

四类检查通过 `check_type` label 区分，alert_key 互相独立，可独立触发和恢复：

| check_type  | 触发条件               | 严重程度               |
|-------------|----------------------|----------------------|
| `usage`     | 空间使用率超阈值         | Warning / Critical   |
| `inode`     | inode 使用率超阈值      | Warning / Critical   |
| `writable`  | 读写测试失败            | Critical（固定）       |
| `hung`      | 检查超时卡住（NFS 等）   | Critical（固定）       |

每个事件的 labels：

```
check        = "磁盘检测"          # 来自配置
check_type   = "usage"            # usage / inode / writable / hung
mount_point  = "/data"            # 当前检查的挂载点
from_plugin  = "disk"             # 由引擎自动追加
```

description 包含：挂载点、设备名、文件系统类型、总量、已用量、使用率、可用量，
便于运维收到告警后无需 SSH 即可判断情况。

writable 检查失败时，description 需区分"权限不足（permission denied）"和"磁盘故障（I/O error 等）"。

## NFS / 网络磁盘挂起处理

### 问题

`syscall.Statfs`（`disk.Usage()` 内部调用）和 `disk.Partitions()` 都是不可被 context 取消的阻塞系统调用，
网络文件系统断连时会永久阻塞，若不加控制会导致 goroutine 泄漏。

### 关键约束：runner 的 queue 生命周期

```go
// agent/runner.go - gatherInstancePlugin
queue := safe.NewQueue[*types.Event]()
plugins.MayGather(ins, queue)       // 等 Gather() 返回
if queue.Len() > 0 {
    engine.PushRawEvents(...)       // 消费 queue
}
```

runner 在每次调度时创建全新的 queue，Gather() 返回后立即消费。
如果 Gather() 完全不等待（fire-and-forget），goroutine 还没 push 事件 Gather() 就返回了，
事件会 push 到一个已被消费过的 queue 中，**永远丢失**。

### 设计方案：inFlight 防重入 + 带超时等待

Instance 上维护：
- `inFlight sync.Map`：key 为挂载点路径，value 为检查启动的 Unix 时间戳
- `prevHung sync.Map`：key 为挂载点路径，记录上一轮处于 hung 状态的挂载点

```
Gather(q) 执行逻辑：

// 第 1 步：枚举 + 过滤挂载点
partitions = disk.Partitions()   // 注意：这一步本身也可能阻塞，被 gather_timeout 覆盖
过滤 ignore_fs_types、ignore_mount_points、mount_points

// 第 2 步：对每个挂载点启动检查
var wg sync.WaitGroup
se = semaphore.NewSemaphore(ins.Concurrency)

for each mountPoint:
    if mountPoint 在 inFlight 中:
        elapsed = now - 记录的启动时间
        if elapsed > gather_timeout:
            push "hung" Critical 事件到 q
        continue   // 跳过，不新建 goroutine
    
    // 上一轮是 hung，这一轮不在 inFlight 了 → 说明恢复了，发送 hung Ok 事件
    if mountPoint 在 prevHung 中:
        push "hung" Ok 事件到 q
        prevHung.Delete(mountPoint)

    wg.Add(1)
    se.Acquire()
    inFlight.Store(mountPoint, now)
    go func(mp string):
        defer se.Release()
        defer wg.Done()
        defer inFlight.Delete(mp)
        执行利用率检查 → push usage 事件到 q
        执行 inode 检查 → push inode 事件到 q
        执行 writable 检查 → push writable 事件到 q

// 第 3 步：带超时等待
done := make(chan struct{})
go func() { wg.Wait(); close(done) }()
select {
case <-done:       // 全部正常完成
case <-time.After(gather_timeout):
    // 超时：部分 goroutine 卡住了
    // 记录当前仍在 inFlight 中的挂载点到 prevHung，供下轮发 hung 恢复事件
    inFlight.Range(func(key, value) {
        prevHung.Store(key, true)
    })
}
```

### 效果

- **正常挂载点**：goroutine 秒级完成，事件正常入 queue，超时前 Gather 返回
- **某挂载点首次卡住**：本轮 Gather 超时返回，该 goroutine 留在 inFlight，该挂载点本轮事件丢失（可接受）
- **下一轮**：发现 inFlight 有该挂载点 → 直接产生 "hung" Critical 告警，不再启动新 goroutine
- **恢复**：旧 goroutine 解除阻塞 → inFlight 删除 → 下一轮检测到 prevHung 中有 → 发送 "hung" Ok 恢复事件，恢复正常检查

### 为什么不能像 net/ping 插件那样直接 wg.Wait()

net/ping 插件的每个 goroutine 有 timeout（tcp DialTimeout、ping deadline），保证必然返回。
disk 底层是 `syscall.Statfs`，不可设超时，不可被 context 取消，可能永久阻塞。

## 跨平台处理

| 差异点                    | Linux                          | Windows                          |
|--------------------------|-------------------------------|----------------------------------|
| 挂载点格式                | `/`、`/data`                   | `C:\`、`D:\`                     |
| `ignore_fs_types` 默认值  | tmpfs/devtmpfs/overlay 等      | cdfs/UDF 等                      |
| `ignore_mount_points` 默认值 | /dev\*、/run\*、/sys\*、/proc\* | 通常不需要排除                    |
| inode 检查               | 支持                           | InodesTotal=0 时自动跳过          |
| NFS 挂起问题              | 存在（nfs/nfs4/cifs）           | 存在（映射网络盘）                 |

inode 跳过逻辑：`usage.InodesTotal == 0` 时直接跳过，无需平台判断，天然兼容。

## 代码结构

```
plugins/disk/
    design.md      # 本设计文档
    disk.go        # 全部逻辑（利用率、inode、读写测试、inFlight、事件构建）

conf.d/p.disk/
    disk.toml      # 配置样例，含详细注释
```

与 procnum 不同，disk 插件无需拆分平台文件，平台差异（inode 跳过、FS 类型默认值）
均可在运行时通过简单判断处理，保持代码结构简单。
