# mount 插件设计

## 概述

检查 Linux 挂载点是否符合用户定义的期望基线：期望的路径是否已挂载、文件系统类型是否正确、挂载选项是否到位。当实际挂载状态偏离期望时产出告警事件。

**核心场景**：

1. **NFS/远程挂载掉线**：`/backup` 的 NFS 掉了，应用静默写入本地空目录，备份数据全丢
2. **安全选项被移除**：运维临时 `mount -o remount,exec /tmp` 排障后忘记恢复，违反 CIS 合规要求
3. **磁盘未挂载**：服务器重启后 `/etc/fstab` 配置丢失，数据盘没有挂上

**与 disk 插件的关系**：disk 关注"挂载点的容量和健康"，mount 关注"挂载点是否存在、配置是否正确"。disk 遍历已挂载的分区做检查，如果某个挂载点消失了，disk 不会告警——它只是静默跳过。mount 插件弥补了这个盲区。

**定位**：挂载基线一致性检查。与 sysctl（内核参数基线）、secmod（安全模块基线）互补，共同覆盖"系统配置基线"。

**参考**：Nagios `check_mount`、Sensu `check-mounts`、CIS Benchmark 挂载选项检查项（1.1.x 系列）。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 挂载基线合规 | `mount::compliance` | 挂载点的存在性、文件系统类型、挂载选项是否符合期望 |

- **每个挂载条目独立产出事件**——一个挂载点检查失败不影响其他挂载点（原则 13）
- **target label** 为被检查的挂载路径（如 `"/data"`、`"/tmp"`）
- **默认 title_rule** 为 `"[check] [target]"`

### 为什么用单一 check label 而非拆分 existence 和 options

考虑过两种方案：

| 方案 | 优点 | 缺点 |
| --- | --- | --- |
| 拆成 `mount::existence` + `mount::options` | 可为同一路径配不同 severity | 两个事件共享同一个 target，增加配置复杂度 |
| **合并为 `mount::compliance`** | 一个条目一个事件，配置简洁；AlertKey 天然唯一 | 同一路径不能对 existence 和 options 设不同 severity |

选择**合并方案**：挂载点不存在时，options 检查也没有意义——二者本质上是同一个检查的不同层面。如果确实需要不同 severity，用户可以配两个条目（一个只检 existence，一个只检 options），但这是极端少见的场景。

## 数据来源

### `/proc/mounts`

每行格式：`device mountpoint fstype options dump pass`

```
/dev/sda1 / ext4 rw,relatime,errors=remount-ro 0 0
tmpfs /tmp tmpfs rw,nosuid,nodev,noexec,relatime 0 0
192.168.1.100:/share /backup nfs rw,relatime,vers=4.2,addr=192.168.1.100 0 0
```

**特殊字符转义**：`/proc/mounts` 对挂载路径中的特殊字符使用八进制转义（空格 → `\040`，制表符 → `\011`，换行 → `\012`，反斜杠 → `\134`）。解析时需反转义。

**为什么读 `/proc/mounts` 而非 `mount` 命令**：
- `/proc/mounts` 读取是纯内存操作（内核 VFS 数据结构），不会 hang
- 不依赖 `util-linux` 包
- 与项目中其他插件（sysctl、conntrack、filefd）的风格一致

**关键特性：读 `/proc/mounts` 不会被 NFS hang 影响**。`/proc/mounts` 的数据来自内核的挂载表，不涉及对实际挂载点的 I/O 操作。即使某个 NFS 挂载完全无响应，`/proc/mounts` 仍能正常读取。

### 解析策略

每次 Gather 读取一次 `/proc/mounts`，解析为 `map[string]mountEntry`（key 为挂载路径），然后逐个挂载条目查表检查。

## 挂载条目配置

每个条目描述对一个挂载路径的期望：

```toml
[[instances.mounts]]
path = "/data"            # 必填：期望的挂载路径
fstype = "ext4"           # 可选：期望的文件系统类型
options = ["rw"]          # 可选：必须存在的挂载选项
severity = "Critical"     # 可选：告警级别，默认 Warning
# title_rule = "[check] [target]"
```

检查逻辑：
1. **path 必检**：该路径是否出现在 `/proc/mounts` 中？不在 → 告警
2. **fstype 选检**：如果配置了 fstype，实际文件系统类型是否匹配？不匹配 → 告警
3. **options 选检**：如果配置了 options，每个选项是否都存在于实际挂载选项中？缺失 → 告警
4. **全部通过** → Ok

## 结构体设计

```go
type MountSpec struct {
    Path      string   `toml:"path"`
    FSType    string   `toml:"fstype"`
    Options   []string `toml:"options"`
    Severity  string   `toml:"severity"`
    TitleRule string   `toml:"title_rule"`
}

type FstabCheck struct {
    Enabled        bool     `toml:"enabled"`
    Severity       string   `toml:"severity"`
    ExcludeFSTypes []string `toml:"exclude_fstype"`
    ExcludePaths   []string `toml:"exclude_paths"`
}

type Instance struct {
    config.InternalConfig

    Mounts []MountSpec `toml:"mounts"`
    Fstab  FstabCheck  `toml:"fstab"`
}

type MountPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

内部数据结构：

```go
type mountEntry struct {
    device     string
    fsType     string
    options    map[string]bool // O(1) 查找
    rawOptions string          // 原始选项字符串，用于 description
}
```

不需要：
- `Timeout` — 读 `/proc/mounts` 是纯内存操作，不会 hang
- `inFlight` — 同理
- `Concurrency` — 挂载条目数量有限，串行遍历即可

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_actual` | `ext4, rw,relatime` | 实际状态（fstype + options） |
| `_attr_expect` | `ext4, noexec,nosuid,nodev` | 期望状态 |

当挂载点不存在时：
- `_attr_actual` = `not mounted`
- `_attr_expect` = 配置的期望

Ok 事件也携带完整 `_attr_`，便于巡检确认挂载状态。

## Init() 校验

```
Init():
    1. if runtime.GOOS != "linux":
           return error: "mount plugin only supports linux"

    2. if len(mounts) == 0 && !fstab.enabled:
           return error: "at least one check must be configured: mounts list or fstab.enabled"

    3. for each mount:
       a. path = strings.TrimSpace(path)
          if path is empty:
              return error: "mount path must not be empty"

       b. if !strings.HasPrefix(path, "/"):
              return error: "mount path must be absolute (start with /)"

       b2. if len(path) > 1:
               path = strings.TrimRight(path, "/")

       c. if fstype is set:
              fstype = strings.TrimSpace(strings.ToLower(fstype))

       d. for each option:
              option = strings.TrimSpace(option)
              if option is empty:
                  return error: "mount option must not be empty"

       e. normalize severity:
          - if severity is "": set severity = "Warning"
          - if not valid severity:
                return error

    4. check path uniqueness:
       - if duplicate paths detected:
             return error: "duplicate mount path: %s"

    5. if fstab.enabled:
       a. normalize fstab.severity: default "Warning"
       b. if fstab.exclude_fstype is empty: default ["tmpfs", "devtmpfs", "squashfs", "overlay"]
```

### path 校验的考量

- 必须是绝对路径（以 `/` 开头）——挂载路径永远是绝对路径
- **尾部斜杠归一化**——`/data/` 自动归一化为 `/data`，根路径 `/` 保持不变。防止用户手误导致匹配失败
- 不做路径穿越检查——这里不是把 path 拼接到其他路径，直接作为 map key 查找
- 检查重复——同一个 path 出现两次会产生 AlertKey 冲突（归一化后检查）

## Gather() 逻辑

```
Gather(q):
    mountMap, err = parseProcMounts()
    if err:
        push Critical target="mounts"
            "failed to parse /proc/mounts: <err>"
        return

    for each spec in mounts:
        checkMount(q, spec, mountMap)


checkMount(q, spec, mountMap):
    tr = spec.titleRule or "[check] [target]"
    entry, exists = mountMap[spec.path]

    if !exists:
        push spec.severity target=spec.path
            _attr_actual = "not mounted"
            _attr_expect = formatExpect(spec)
            "/data is not mounted"
        return

    // 检查 fstype
    if spec.fstype != "" && entry.fsType != spec.fstype:
        push spec.severity target=spec.path
            _attr_actual = entry.fsType + ", " + entry.rawOptions
            _attr_expect = formatExpect(spec)
            "/data is mounted as xfs, expected ext4"
        return

    // 检查 options
    var missing []string
    for _, opt := range spec.options:
        if !entry.options[opt]:
            missing = append(missing, opt)

    if len(missing) > 0:
        push spec.severity target=spec.path
            _attr_actual = entry.fsType + ", " + entry.rawOptions
            _attr_expect = formatExpect(spec)
            "/tmp is missing mount options: noexec, nosuid (actual: rw,relatime,nodev)"
        return

    // 全部通过
    push Ok target=spec.path
        _attr_actual = entry.fsType + ", " + entry.rawOptions
        _attr_expect = formatExpect(spec)
        "/data is mounted as ext4 with expected configuration"


parseProcMounts() (map[string]mountEntry, error):
    data, err = os.ReadFile("/proc/mounts")
    if err:
        return nil, err

    result = map[string]mountEntry{}
    for each line:
        fields = strings.Fields(line)
        if len(fields) < 4:
            continue
        mountPoint = unescapeOctal(fields[1])
        optList = strings.Split(fields[3], ",")
        optSet = map[string]bool{}
        for _, o := range optList:
            optSet[o] = true
        result[mountPoint] = mountEntry{
            device:     fields[0],
            fsType:     fields[2],
            options:    optSet,
            rawOptions: fields[3],
        }
    return result, nil
```

### 关键行为

1. **`/proc/mounts` 只读一次**——所有挂载条目共享同一次解析结果，高效。
2. **每个条目独立产出事件**——一个挂载点检查失败不影响其他挂载点。
3. **解析失败影响全局**——`/proc/mounts` 无法读取是系统级问题（极罕见），此时产出一个全局 Critical 事件。
4. **fstype 优先于 options 检查**——如果 fstype 就不对，再检查 options 没有意义（可能是挂错了分区），直接告警。
5. **挂载路径的八进制转义需反转**——用户配 `path = "/my data"`，`/proc/mounts` 里是 `/my\040data`，解析时做反转义才能匹配。

## Description 示例

存在性：
- 未挂载：`/data is not mounted`
- 未挂载（带 fstype 期望）：`/data is not mounted (expected ext4)`

文件系统类型：
- 不匹配：`/data is mounted as xfs, expected ext4`

挂载选项：
- 缺失：`/tmp is missing mount options: noexec, nosuid (actual: rw,relatime,nodev)`

全部通过：
- 无 options 配置：`/data is mounted as ext4`
- 有 options 配置：`/tmp is mounted as tmpfs with expected options (noexec, nosuid, nodev)`

解析失败：
- `failed to parse /proc/mounts: permission denied`

## 默认配置建议

| 决策 | 值 | 理由 |
| --- | --- | --- |
| severity | `"Warning"` | 挂载选项偏离通常是安全隐患而非紧急故障 |
| interval | `"60s"` | 挂载状态极少变化 |
| for_duration | `0` | 挂载变化是确定性的，不需要持续确认 |
| repeat_interval | `"30m"` | 长期问题，适度提醒 |
| repeat_number | `3` | 防止噪音 |

## NFS hang 场景的说明

当 NFS 挂载无响应时：

| 检查方式 | 行为 |
| --- | --- |
| 读 `/proc/mounts` | **正常**——内核 VFS 数据结构，不涉及实际 I/O |
| `stat` 挂载路径 | **hang**——触发对 NFS 服务器的实际请求 |
| disk 插件（`disk.Usage`） | **hang**——底层调 `statfs` |

mount 插件只读 `/proc/mounts`，不 `stat` 任何挂载路径，因此不会被 NFS hang 影响。但这也意味着：NFS 挂载如果仍在挂载表中但服务端已不可达，mount 插件不会发现（它只看"是否挂载"，不看"是否可用"）。NFS 可用性检测应由 disk 插件的 writable 检查或 ping/net 插件覆盖。

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 挂载点消失/选项漂移 | **mount** | 基线检查 |
| 磁盘空间/inode 告警 | disk | 容量监控 |
| 磁盘是否可写 | disk (writable) | 健康检测 |
| 内核参数被重置 | sysctl | 另一类配置漂移 |
| 安全模块状态偏离 | secmod | 安全基线 |

mount + sysctl + secmod 三者组合覆盖"系统配置基线"的主要场景。

## fstab 自动导入

除了手动列举 `[[instances.mounts]]` 条目外，还支持自动从 `/etc/fstab` 导入挂载检查：

```toml
[instances.fstab]
enabled = true
severity = "Critical"
# exclude_fstype = ["tmpfs", "devtmpfs", "squashfs", "overlay"]
# exclude_paths = ["/tmp"]
```

### 行为

1. 读取 `/etc/fstab`，解析每个条目
2. **硬编码跳过**（始终生效，不受配置影响）：`swap` fstype、`none` 挂载点、`noauto` 选项
3. **用户排除**：`exclude_fstype`（默认 `["tmpfs", "devtmpfs", "squashfs", "overlay"]`）和 `exclude_paths` 额外过滤
4. **手动配置优先**：如果某个路径已在 `mounts` 中手动配置，fstab 不再重复检查
5. 对剩余条目检查**存在性 + fstype 匹配**（不检查 options——fstab 中的 options 如 `defaults` 与运行时 options 格式不同，比对无意义。需要 options 检查请用手动条目）

### 典型使用方式

- **仅 fstab**：不配任何 `mounts`，只启用 `fstab.enabled = true`，自动检查所有 fstab 条目
- **fstab + 手动**：fstab 兜底检查全量，手动条目额外检查 options（如 CIS 合规）
- **仅手动**：不启用 fstab，完全手动控制检查范围

### 与 Sensu check-mounts 的对比

Sensu `check-mounts` 的核心功能就是"检查 fstab 中的条目是否都已挂载"。catpaw 的 fstab 模式提供了等价能力，同时可以与手动条目组合使用，支持更精细的 options 检查。

## CIS Benchmark 常见检查项

mount 插件可直接覆盖以下 CIS Benchmark 条目：

| CIS ID | 检查内容 | mount 配置 |
| --- | --- | --- |
| 1.1.2-1.1.5 | `/tmp` 独立分区，noexec/nosuid/nodev | `path="/tmp", options=["noexec","nosuid","nodev"]` |
| 1.1.8-1.1.9 | `/var/tmp` 独立分区，noexec/nosuid/nodev | `path="/var/tmp", options=["noexec","nosuid","nodev"]` |
| 1.1.14 | `/home` 独立分区，nosuid | `path="/home", options=["nosuid"]` |
| 1.1.15-1.1.17 | `/dev/shm` noexec/nosuid/nodev | `path="/dev/shm", options=["noexec","nosuid","nodev"]` |

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/proc/mounts` |
| macOS | 不支持 | Init 返回错误。macOS 挂载信息在 `/etc/mtab` 或 `mount` 命令，格式不同 |
| Windows | 不支持 | Init 返回错误。Windows 无挂载点概念（使用盘符） |

## 文件结构

```
plugins/mount/
    design.md        # 本文档
    mount.go         # 主逻辑（仅 Linux）
    mount_test.go    # 测试

conf.d/p.mount/
    mount.toml       # 默认配置
```

通过 `runtime.GOOS` 在 Init 中限制为 Linux，无需 build tags。

## 默认配置文件示例

```toml
[[instances]]
## ===== 挂载点基线检查 =====
## 检查期望的挂载点是否存在、文件系统类型是否正确、挂载选项是否到位
## 典型场景：NFS 掉线检测、CIS 合规（noexec/nosuid/nodev）、重启后数据盘未挂载
## severity 支持 Info / Warning / Critical，默认 Warning

interval = "60s"

## 挂载条目列表
## path：必填，期望的挂载路径（绝对路径）
## fstype：可选，期望的文件系统类型（ext4/xfs/nfs/tmpfs/...）
## options：可选，必须存在的挂载选项列表
## severity：可选，默认 Warning
## title_rule：可选，默认 "[check] [target]"

## 数据盘 — 检查是否挂载且是 ext4
# [[instances.mounts]]
# path = "/data"
# fstype = "ext4"
# severity = "Critical"

## NFS 备份盘 — 检查是否挂载
# [[instances.mounts]]
# path = "/backup"
# fstype = "nfs"
# severity = "Critical"

## CIS: /tmp 安全选项
# [[instances.mounts]]
# path = "/tmp"
# options = ["noexec", "nosuid", "nodev"]
# severity = "Warning"

## CIS: /dev/shm 安全选项
# [[instances.mounts]]
# path = "/dev/shm"
# options = ["noexec", "nosuid", "nodev"]
# severity = "Warning"

## 自动检查 /etc/fstab 中的挂载条目
## swap 和 mountpoint=none 始终跳过（硬编码），noauto 条目也会跳过
## manual mounts 优先级高于 fstab（同一路径不会重复检查）
[instances.fstab]
# enabled = true
# severity = "Critical"
## 排除的文件系统类型，默认 ["tmpfs", "devtmpfs", "squashfs", "overlay"]
## swap 无需在此配置，始终自动跳过
# exclude_fstype = ["tmpfs", "devtmpfs", "squashfs", "overlay"]
# exclude_paths = []

[instances.alerting]
for_duration = 0
repeat_interval = "30m"
repeat_number = 3
# disabled = false
# disable_recovery_notification = false
```
