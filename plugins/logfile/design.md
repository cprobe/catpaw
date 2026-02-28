# logfile 插件设计

## 概述

监控本机纯文本日志文件，跟踪文件偏移量（类似 `tail -f`），只处理每次采集周期内的新增内容。当新增行匹配 `filter_include` 规则时产出告警事件。

支持 Fingerprint 防 inode 复用、偏移量跨重启持久化、匹配行上下文展示、多编码透明转换。

**定位**：补充 journaltail（依赖 journalctl，仅 Linux）和 scriptfilter（需自行写脚本）的不足，覆盖直接写文件的应用日志场景——Nginx access.log、Java 应用日志、自研服务日志等。

**参考**：Nagios `check_log`、Sensu `check-log`、Filebeat harvester。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 日志行匹配 | `logfile::match` | 新增日志行匹配 filter_include 规则时告警 |

- **target label** 为日志文件路径（如 `/var/log/myapp/error.log`），每个文件独立告警/恢复
- **默认 title_rule** 为 `"[check] [target]"`（多 target，标题中需区分）

## 数据来源

纯标准库文件 I/O：`os.Open` → `file.Seek` → `io.ReadAll` + `io.LimitReader`。

glob 展开复用已有依赖 `filepath.Glob`（标准库），无需引入新依赖。

编码转换使用 `golang.org/x/text/transform`（Go 官方扩展库）。

### 偏移量追踪

每个 target 文件维护一个 `fileState`：

```go
type fileState struct {
    Offset      int64  `json:"offset"`      // 下次读取的起始字节偏移
    Inode       uint64 `json:"inode"`        // 文件 inode，用于检测日志轮转（rename 类型）
    Fingerprint string `json:"fingerprint"`  // hex(文件前 N 字节)，用于检测 inode 复用
}
```

Gather 流程：
1. `os.Stat` 获取当前文件大小和 inode
2. 读取文件 fingerprint（前 256 字节的 hex 编码）
3. 比对 inode、fingerprint 和 size 综合判定日志轮转（详见下文）
4. `file.Seek(offset, io.SeekStart)` 跳到上次读取位置
5. 读取原始字节（`io.ReadAll` + `io.LimitReader`），以最后一个 `\n` 为截止，确保只处理完整行
6. 基于原始字节数更新 offset（不受编码转换影响）
7. 如有非 UTF-8 编码，用 `transform.String` 解码后再 `strings.Split` 分行
8. 逐行过 filter 规则

### Fingerprint 机制

**问题**：纯 inode 检测在 inode 复用场景下会失效。当旧日志文件被删除、新日志文件恰好获得相同 inode 号时，仅靠 inode 比对会误认为"同一个文件"，导致从旧 offset 继续读取，跳过新文件的起始内容或读到乱码。

**方案**：读取文件前 256 字节作为内容指纹（fingerprint），hex 编码后存储。每次 Gather 重新读取并比对：

```go
const fingerprintSize = 256
```

| 场景 | inode | fingerprint | 判定 | 处理 |
| --- | --- | --- | --- | --- |
| 正常增长 | 相同 | 相同 | 同一文件 | 从 offset 继续读 |
| rename 轮转 | 不同 | — | 新文件 | offset=0, 更新 inode+fingerprint |
| copytruncate | 相同 | 可能变 | size < offset | offset=0, 更新 fingerprint |
| **inode 复用** | 相同 | **不同** | 新文件（伪装） | offset=0, 更新 fingerprint |
| 文件未变 | 相同 | 相同 | size == offset | 跳过（无新内容） |

fingerprint 比对使用**方向性前缀匹配**：对于 append-only 的日志文件，fingerprint 只应增长或不变。如果当前 fingerprint 比存储的更短（文件变小或被替换），直接判定不匹配。只在当前 fingerprint 更长或等长时进行前缀比较。

fingerprint 比对开销极小（一次 256 字节 pread + hex 编码），对 I/O 无感知影响。

### 日志轮转检测

覆盖三种主流场景：

| 场景 | 现象 | 检测条件 | 处理 |
| --- | --- | --- | --- |
| **rename 轮转**（logrotate 默认） | 旧文件被重命名，新文件被创建 | `currentInode != storedInode` | offset 重置为 0，读取新文件全部内容 |
| **copytruncate 轮转** | 文件被截断 | `currentSize < storedOffset` | offset 重置为 0，从头读取 |
| **inode 复用** | 旧文件删除，新文件复用同一 inode | `inode 相同但 fingerprint 不同` | offset 重置为 0，从头读取 |
| **正常增长** | 文件增大 | inode + fingerprint 均匹配，size >= offset | 从 storedOffset 继续读 |

### 偏移量持久化

**问题**：catpaw 重启后，内存中的 fileState 丢失，所有文件从 `initial_position` 重新开始。如果 `initial_position = "end"`，重启期间的新增日志会被跳过；如果 `initial_position = "beginning"`，会重复处理历史日志。

**方案**：将 fileState 持久化到 JSON 文件（类似 Filebeat 的 registry）。

配置项 `state_file`：
- 默认值：`<StateDir>/p.logfile/.logfile_state_<hash>.json`（hash 基于 targets 列表，确保多实例互不冲突）
- `StateDir` 是 catpaw 全局的运行时状态目录，默认为 `conf.d` 同级的 `state.d/`（如 `conf.d` → `state.d`，`/etc/catpaw/conf.d` → `/etc/catpaw/state.d`）
- State 文件与配置文件分离，配置目录可保持只读（适配容器化/配置管理部署）
- 用户可自定义路径

#### 文件格式

```json
{
  "/var/log/myapp/error.log": {
    "offset": 12345,
    "inode": 67890,
    "fingerprint": "48656c6c6f..."
  }
}
```

#### 读写策略

| 时机 | 操作 |
| --- | --- |
| Init() | 加载 state file（不存在则跳过，解析失败则 warn + 从零开始） |
| 每次 Gather 结束 | 如果 state 有变化，原子写入（write temp → rename） |
| Drop() | 最后一次保存 state |

原子写入确保即使 catpaw 在写入过程中崩溃，state file 也不会损坏。

### 首次运行行为

`initial_position` 控制首次遇到文件时的偏移量起点：

| 值 | 行为 | 适用场景 |
| --- | --- | --- |
| `"end"`（默认） | 跳到文件末尾，只监控新增内容 | 生产环境（避免历史日志洪泛告警） |
| `"beginning"` | 从文件开头读取 | 测试/调试（想扫描已有内容） |

注：如果 state_file 中已有该文件的记录，则忽略 initial_position，从持久化的 offset 继续。

### 跨平台兼容性

| 平台 | 文件读取 | inode 轮转检测 | copytruncate 检测 | fingerprint |
| --- | --- | --- | --- | --- |
| Linux | 完整支持 | 完整支持（`syscall.Stat_t.Ino`） | 完整支持 | 完整支持 |
| macOS | 完整支持 | 完整支持（`syscall.Stat_t.Ino`） | 完整支持 | 完整支持 |
| Windows | 完整支持 | 不可用（返回 0，退化为 fingerprint + size 检测） | 完整支持 | 完整支持 |

Windows 下无 inode 概念，rename 轮转的检测由 fingerprint 兜底：即使 inode 恒为 0，只要文件内容变了（fingerprint 不同），就能正确检测到轮转。

平台差异通过 build tags 隔离：
- `logfile_inode_unix.go`（`//go:build !windows`）：从 `os.FileInfo.Sys()` 提取 inode
- `logfile_inode_windows.go`（`//go:build windows`）：始终返回 0

## 编码支持

### 设计原则

- 默认 UTF-8，零配置即可用
- 用户显式指定编码（而非自动检测），因为 chardet 在短文本上不可靠，且日志编码通常固定
- 对非 UTF-8 内容做防御性处理：`strings.ToValidUTF8(line, "\uFFFD")`

### 配置

`encoding` 字段，大小写不敏感：

| 值 | 说明 |
| --- | --- |
| `""` 或 `"utf-8"` | 默认，不做转换 |
| `"gbk"` / `"gb2312"` | 简体中文 GBK |
| `"gb18030"` | 简体中文 GB18030 |
| `"big5"` | 繁体中文 Big5 |
| `"shift_jis"` | 日文 Shift_JIS |
| `"euc-jp"` | 日文 EUC-JP |
| `"euc-kr"` | 韩文 EUC-KR |
| `"latin1"` / `"iso-8859-1"` | 西欧 Latin-1 |
| `"windows-1252"` | Windows 西欧 |

### 实现

**关键设计决策**：offset 始终基于原始文件字节追踪，不受编码转换影响。

读取流程（Raw Bytes → Decode → Lines）：

```go
// 1. 读取原始字节
rawBuf, _ := io.ReadAll(io.LimitReader(f, bytesToRead))

// 2. 以最后一个 \n 为截止，只处理完整行
lastNL := bytes.LastIndexByte(rawBuf, '\n')
completeRaw := rawBuf[:lastNL+1]
state.Offset += int64(len(completeRaw))  // 基于原始字节

// 3. 编码转换（如需要）
if ins.enc != nil {
    text, _, _ = transform.String(ins.enc.NewDecoder(), string(completeRaw))
} else {
    text = string(completeRaw)
}

// 4. 分行 + 匹配
lines := strings.Split(text, "\n")
```

**为什么不用 `bufio.Scanner` + `transform.NewReader`**：
- `bufio.Scanner` 遇到超过 buffer 的行时直接返回 `ErrTooLong`，后续所有行丢失
- `transform.NewReader` 返回解码后的 UTF-8 字节，导致 offset 计算偏移（GBK 汉字 2 字节 → UTF-8 3 字节）
- 直接读原始字节再解码，offset 天然正确，且能正确处理超长行（截断而非丢弃）

**`\n` 作为行边界在所有支持编码中的安全性**：GBK、Shift_JIS、EUC-JP 等多字节编码的后续字节范围不包含 0x0A，因此 `bytes.LastIndexByte(rawBuf, '\n')` 可安全定位行边界。

所有后续的行匹配、上下文处理均基于 UTF-8 字符串，编码转换对业务逻辑透明。

## Targets 与 Glob 展开

`targets` 支持两种写法：

| 写法 | 示例 | 说明 |
| --- | --- | --- |
| **精确路径** | `/var/log/myapp/error.log` | 固定文件，路径不含 glob 元字符 |
| **glob 模式** | `/var/log/myapp/*.log` | 按 `filepath.Glob` 展开，每次 Gather 动态解析 |

判断方式：`filter.HasMeta(target)` 检测是否含 `*`、`?`、`[` — 已有基础设施。

### resolveTargets()

每次 Gather 开始时调用，将 targets 展开为具体文件列表：

```go
func (ins *Instance) resolveTargets() []string {
    var files []string
    seen := make(map[string]bool)
    for _, target := range ins.Targets {
        if !filter.HasMeta(target) {
            if !seen[target] {
                // 精确路径也做存在性检查，不存在的文件由 Gather 的 "disappeared" 逻辑处理
                if info, err := os.Stat(target); err == nil && !info.IsDir() {
                    files = append(files, target)
                }
                seen[target] = true
            }
            continue
        }
        matches, _ := filepath.Glob(target)
        for _, m := range matches {
            info, err := os.Stat(m)
            if err != nil || info.IsDir() {
                continue
            }
            if !seen[m] {
                files = append(files, m)
                seen[m] = true
            }
        }
    }
    return files
}
```

### 文件消失/不可访问的差异化处理

| target 来源 | 异常情况 | 行为 | 理由 |
| --- | --- | --- | --- |
| **精确路径** | 文件不存在（之前见过） | Critical："file disappeared" | 用户显式指定的文件不应消失 |
| **精确路径** | 文件不存在（从未见过） | 静默跳过（Debug 日志） | 应用可能尚未启动，避免误报 |
| **精确路径** | stat 返回非 NotExist 错误（权限、I/O 等） | Critical："file inaccessible: ..." | 权限变更、NFS 断连等异常必须可见 |
| **glob 解析** | 文件消失 | 静默清理 fileState | 文件自然轮转（被压缩/删除），属于正常生命周期 |

### max_targets 保护

防止过于宽泛的 glob 匹配出大量文件：

- 默认 `max_targets = 100`
- 展开后超过上限时：截断到 `max_targets`，产出一个 Warning 事件提示用户收窄 glob

### fileState 清理

glob 解析的文件集合每次 Gather 可能变化。对于不再出现在解析结果中的文件：

- 如果该文件的 fileState 存在 → 删除 state（释放内存）
- 不产出任何事件（glob 文件消失是正常行为）

精确路径的文件不做自动清理。文件消失时保留 fileState 并持续产出 Critical（每次 Gather），直到文件重新出现。保留 state 的好处：文件重新出现时，rotation 检测会发现 fingerprint/inode 变化，正确重置 offset=0 从头读取，不会因 `initial_position = "end"` 跳过新内容。

## 上下文行 (Context Lines)

### 设计动机

只看匹配行往往不够定位问题。例如 `OutOfMemoryError` 之前的几行可能包含触发 OOM 的操作信息，之后的几行可能包含 stack trace。

### 配置

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `context_before` | `0` | 匹配行之前展示的上下文行数 |
| `context_after` | `0` | 匹配行之后展示的上下文行数 |

### 实现策略

1. 将本次 Gather 读取的所有新行存入 `[]string` 切片
2. 逐行过滤，记录匹配行的索引 `matchedIndices []int`
3. 对前 `max_lines` 个匹配，计算上下文区间 `[i-context_before, i+context_after]`
4. 合并重叠/相邻区间，避免重复行
5. 格式化输出：匹配行用 `> ` 前缀，上下文行用 `  ` 前缀，不相邻的区间用 `...` 分隔

### Description 示例

`context_before=2, context_after=1`：

```
matched 2 lines (context: -2/+1):
  2026-02-28 14:31:59 INFO Processing request
  2026-02-28 14:32:00 INFO Allocating buffer size=512MB
> 2026-02-28 14:32:01 ERROR [main] OutOfMemoryError: Java heap space
  2026-02-28 14:32:01 WARN Attempting cleanup
  ...
  2026-02-28 14:35:12 INFO Request from 10.0.0.1
> 2026-02-28 14:35:12 ERROR [auth] Authentication failed: token expired
  2026-02-28 14:35:12 WARN Returning 401
```

无上下文时（`context_before=0, context_after=0`）保持现有行为：

```
matched 3 lines:
2026-02-28 14:32:01 ERROR [main] OutOfMemoryError: Java heap space
2026-02-28 14:32:02 ERROR [gc] GC overhead limit exceeded
2026-02-28 14:35:12 ERROR [auth] Authentication failed: token expired
```

## 结构体设计

```go
type MatchCheck struct {
    Severity  string `toml:"severity"`
    TitleRule string `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    Targets         []string        `toml:"targets"`
    InitialPosition string          `toml:"initial_position"`
    FilterInclude   []string        `toml:"filter_include"`
    FilterExclude   []string        `toml:"filter_exclude"`
    MaxLines        int             `toml:"max_lines"`
    MaxReadBytes    config.Size     `toml:"max_read_bytes"`
    MaxLineLength   int             `toml:"max_line_length"`
    MaxTargets      int             `toml:"max_targets"`
    ContextBefore   int             `toml:"context_before"`
    ContextAfter    int             `toml:"context_after"`
    Encoding        string          `toml:"encoding"`
    StateFile       string          `toml:"state_file"`
    GatherTimeout   config.Duration `toml:"gather_timeout"`
    Match           MatchCheck      `toml:"match"`

    mu              sync.Mutex        // 保护 Gather/Drop 并发安全
    includeFilter   filter.Filter
    excludeFilter   filter.Filter
    fileStates      map[string]*fileState
    explicitTargets map[string]bool
    enc             encoding.Encoding // nil for UTF-8
    stateDirty      bool              // state 是否有变更，优化写盘频率
}
```

需要：`GatherTimeout`（文件读取可能阻塞在 NFS）。`GatherTimeout` 在每个文件处理**之前**检查，超时后跳过剩余文件。注意它不会中断正在进行的单个文件 I/O — 如果某个文件在 NFS 上挂起，需等待 OS 层面的 I/O 超时返回。

不需要：`Concurrency`、`inFlight` — 文件顺序处理，单个文件的读取非常快。

需要：`mu sync.Mutex` — 保护 `Gather()` 和 `Drop()` 对 `fileStates` / `stateDirty` 的并发访问。框架在停止插件时可能从不同 goroutine 调用 `Drop()`。

## _attr_ 标签

### match 事件（有匹配行时）

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_matched_count` | `5` | 本次采集匹配的行数 |
| `_attr_bytes_read` | `23.4 KiB` | 本次采集读取的字节数 |

### match 事件（无匹配行时 / OK）

基础 labels（check、target）即可，不需要额外 attr。

## Init() 校验

1. `targets` 不能为空
2. `filter_include` 不能为空（没有匹配规则的 logfile 监控无意义）
3. `filter_include` / `filter_exclude` 编译为 `filter.Filter`，失败则报错
4. `match.severity` 校验合法性（空字符串默认为 `"Warning"`）
5. `initial_position` 校验：只允许 `"end"` 或 `"beginning"`（空字符串默认为 `"end"`）
6. `max_read_bytes` 默认 1MB，`max_lines` 默认 10（负数也归为默认），`max_line_length` 默认 8192
7. `max_targets` 默认 100
8. `context_before` / `context_after` 默认 0，不允许负数，上限 10
9. `encoding` 校验：查表确认支持，空字符串默认 UTF-8，初始化 decoder
10. `state_file` 默认 `<StateDir>/p.logfile/.logfile_state_<hash>.json`（hash = FNV32(targets)，保证多实例隔离）
11. `gather_timeout` 默认 10s
12. 初始化 `fileStates` map：优先从 state_file 加载，加载失败则 warn + 从零开始
13. 构建 `explicitTargets`：遍历 targets，将不含 glob 元字符的路径存入 `explicitTargets` 集合

## Gather() 逻辑

```
Gather(q):
    设置 gather_timeout 定时器
    
    // 第 1 步：展开 targets 为具体文件列表
    resolvedFiles = resolveTargets()
    if 展开后数量 > max_targets:
        截断到 max_targets，产出 Warning 事件提示 glob 过宽
    
    // 第 2 步：清理 glob 来源的 stale fileState
    for path in fileStates:
        if path 不在 resolvedFiles 中 AND path 不在 explicitTargets 中:
            删除 fileStates[path], stateDirty = true
    
    // 第 3 步：处理精确路径中不可访问的文件
    for path in explicitTargets:
        if path 不在 resolvedFiles 中:
            err = os.Stat(path)
            if 文件不存在 (ENOENT):
                if path 在 fileStates 中（之前见过）:
                    产出 Critical 事件："file disappeared"
                    保留 fileState（不删除，持续告警直到文件重新出现）
                else:
                    跳过（Debug 日志），文件可能尚未创建
            else if 其他错误 (EPERM, EIO 等):
                产出 Critical 事件："file inaccessible: <error>"
    
    // 第 4 步：逐文件处理新增内容
    for each file in resolvedFiles:
        如果超时，跳出循环，产出 error 事件
        
        state = fileStates[file]（不存在则新建）
        info = os.Stat(file)
        currentInode = getInode(info)
        currentFingerprint = readFingerprint(file)
        
        // 轮转检测（综合 inode + fingerprint + size）
        if state 是新建的:
            if state_file 中无此文件记录:
                按 initial_position 设置 offset
            state.Inode = currentInode
            state.Fingerprint = currentFingerprint
        else:
            if currentInode != state.Inode:
                // rename 轮转
                offset = 0
                更新 inode + fingerprint
            else if currentFingerprint != state.Fingerprint:
                // inode 复用：内容变了但 inode 相同
                offset = 0
                更新 fingerprint
            else if currentSize < state.Offset:
                // copytruncate 轮转
                offset = 0
            // else: 正常增长，保持 offset
        
        if currentSize == state.Offset:
            产出 Ok 事件（无新内容）
            continue
        
        打开文件，Seek 到 offset
        io.ReadAll(io.LimitReader(f, bytesToRead)) 读取原始字节
        以最后一个 \n 为截止，确保只处理完整行
        基于原始字节数更新 state.Offset（不受编码影响）
        
        if encoding 配置了非 UTF-8:
            transform.String 解码为 UTF-8
        
        strings.Split 分行，每行 TrimRight("\r") + ToValidUTF8
        逐行过 filter_include / filter_exclude，收集 matchedIndices（基于完整行匹配）
        匹配完成后，对超长行做 truncateUTF8（截断仅影响展示，不影响匹配判定）
        
        if 有匹配行:
            if context_before > 0 || context_after > 0:
                构建上下文区间，合并重叠，格式化输出
            else:
                直接输出前 max_lines 行匹配内容
            产出 alert 事件（severity = match.severity）
        else:
            产出 Ok 事件
    
    // 第 5 步：持久化 state
    if stateDirty:
        原子写入 state_file
        stateDirty = false
```

### 关键行为

1. **精确路径 + 文件不存在 + 从未见过** → 静默跳过（Debug 日志）。避免 catpaw 启动时应用尚未启动导致的误报。
2. **精确路径 + 文件不存在 + 之前见过** → 持续产出 Critical 事件（每次 Gather），保留 fileState。当文件重新出现时，rotation 检测自动重置 offset=0 从头读取，并产出 Ok/Warning 事件以解除告警。
3. **glob 路径 + 文件消失** → 静默清理 fileState。glob 匹配的文件自然轮转（压缩/删除）是正常行为。
4. **每个 target 独立产出事件**，互不影响（原则 13：局部失败不影响全局）。
5. **文件读取失败**（权限、I/O 错误）→ Critical 事件，description 包含具体错误和文件路径。
6. **`max_read_bytes` 限制**：每次 Gather 每个文件最多读 1MB 新内容。如果两次 Gather 之间积累了大量日志，只处理前 1MB，剩余部分下次继续。这防止 I/O 风暴，同时确保不丢数据（只是延迟处理）。
7. **行边界保证**：只处理完整行（以 `\n` 结尾）。读取原始字节后以最后一个 `\n` 为截止，截止后的不完整字节留到下次 Gather 处理。如果整个 `max_read_bytes` 块无 `\n`（极端超长行），强制推进 offset 避免卡死。
8. **超长行韧性**：不使用 `bufio.Scanner`（遇超长行会 `ErrTooLong` 停止所有后续扫描），而是直接读原始字节后分行，超长行被截断（`truncateUTF8` 在 UTF-8 字符边界安全截断）而非丢弃后续行。
9. **glob 新发现文件** → `initial_position = "end"` 跳到末尾，不扫描历史内容，避免告警洪泛。
10. **非 UTF-8 内容防御**：即使用户配置了 encoding，仍对解码后的字符串做 `strings.ToValidUTF8` 保护，防止损坏的日志导致下游异常。
11. **CRLF 兼容**：`strings.TrimRight(line, "\r")` 透明处理 Windows 风格 `\r\n` 行尾。
12. **多实例隔离**：state_file 路径包含 targets 的 FNV hash，多个 `[[instances]]` 不会互相覆盖状态。

### Description 示例

- 匹配告警（无上下文）：
  ```
  matched 3 lines:
  2026-02-28 14:32:01 ERROR [main] OutOfMemoryError: Java heap space
  2026-02-28 14:32:01 ERROR [main] at com.example.App.process(App.java:42)
  2026-02-28 14:32:02 ERROR [gc] GC overhead limit exceeded
  ```
- 匹配告警（有上下文 context_before=1, context_after=1）：
  ```
  matched 2 lines (context: -1/+1):
    2026-02-28 14:32:00 INFO Allocating buffer
  > 2026-02-28 14:32:01 ERROR [main] OutOfMemoryError: Java heap space
    2026-02-28 14:32:01 WARN Attempting cleanup
    ...
    2026-02-28 14:35:12 INFO Request from 10.0.0.1
  > 2026-02-28 14:35:12 ERROR [auth] Authentication failed
    2026-02-28 14:35:12 WARN Returning 401
  ```
- 匹配数超过 max_lines：
  ```
  matched 47 lines:
  2026-02-28 14:32:01 ERROR [main] Connection refused
  ... (前 10 行)
  ... and 37 more lines
  ```
- 精确路径文件消失：`file /var/log/myapp/error.log disappeared (was previously monitored)`
- 精确路径权限异常：`file /var/log/myapp/error.log inaccessible: permission denied`
- glob 过宽：`targets resolved to 247 files, exceeding max_targets(100), only monitoring the first 100`
- 读取失败：`failed to read /var/log/myapp/error.log: permission denied`
- 恢复：`everything is ok`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| initial_position | `"end"` | 避免首次启动处理大量历史日志导致告警洪泛 |
| max_read_bytes | `1MB` | 30s 间隔内，1MB 足以覆盖绝大多数应用日志增量；防止 I/O 风暴 |
| max_line_length | `8192` | 超长行（如 JSON 日志）截断后仍可辨识，避免内存暴涨 |
| max_lines | `10` | Description 展示前 10 行匹配内容，足以定位问题又不会过长 |
| max_targets | `100` | 防止过于宽泛的 glob 匹配出大量文件；100 个文件 × 1MB = 最坏情况 100MB I/O |
| context_before | `0` | 默认无上下文，按需开启 |
| context_after | `0` | 默认无上下文，按需开启 |
| encoding | `""` (UTF-8) | 绝大多数现代日志为 UTF-8 |
| state_file | 自动（StateDir 下） | 开箱即用，状态与配置分离 |
| gather_timeout | `10s` | 本地文件毫秒级完成；10s 给 NFS 留余量 |
| severity | `"Warning"` | 日志错误不一定是紧急事故，默认 Warning 偏保守 |
| for_duration | `0` | 日志是事件型检查，出现即告警，无需持续确认 |
| filter_include | 无默认值，必填 | 不同应用的日志格式差异巨大，无法提供通用默认值 |

## 与 journaltail / scriptfilter 的关系

| 特性 | logfile | journaltail | scriptfilter |
| --- | --- | --- | --- |
| 数据源 | 纯文本文件 | systemd journal | 任意命令 stdout |
| 平台 | Linux/macOS/Windows | 仅 Linux | 全平台 |
| 增量机制 | 字节偏移量 + fingerprint | journal cursor | 无（每次全量） |
| 轮转处理 | inode + fingerprint + size | journal 内置 | N/A |
| 编码支持 | 多编码（配置） | UTF-8 | 取决于脚本 |
| 依赖 | golang.org/x/text | journalctl | 用户脚本 |
| 最佳场景 | 应用直写的日志文件 | systemd 管理的服务日志 | 需要自定义采集逻辑 |

三者互补，不重叠：
- systemd 服务 → journaltail
- 自定义脚本输出 → scriptfilter
- 应用直写的文本日志 → logfile

## 文件结构

```
plugins/logfile/
    design.md                 # 本文档
    logfile.go                # 主逻辑
    logfile_inode_unix.go     # Unix inode 提取（Linux/macOS）
    logfile_inode_windows.go  # Windows 占位（返回 0）
    logfile_test.go           # 测试

conf.d/p.logfile/
    logfile.toml              # 默认配置
```

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 1) targets 填你的日志文件路径（支持精确路径 + glob 模式）
## 2) filter_include 填你关心的关键字（glob + /regex/ 混用）
## 3) 启动后只处理新增日志，不会扫描历史内容
## 例子：
## targets = ["/var/log/myapp/error.log"]
## targets = ["/var/log/myapp/*.log"]
## targets = ["/var/log/*/error.log"]
## filter_include = ["*ERROR*", "/(?i)panic|fatal/"]

## 要监控的日志文件路径（必填，可多个）
targets = ["/var/log/myapp/error.log"]

## 首次遇到文件时的起始位置
## "end"（默认）：从文件末尾开始，只监控新增内容
## "beginning"：从文件开头读取（测试/调试用，慎用于大文件）
# initial_position = "end"

## 行过滤规则（核心配置，必填）
filter_include = ["*ERROR*", "*FATAL*", "/(?i)exception|panic/"]

## 排除规则（可选），优先级高于 include
# filter_exclude = ["*expected*", "*DeprecationWarning*"]

## 告警描述中最多展示多少条匹配行（默认 10）
# max_lines = 10

## 每次采集每个文件最多读取的字节数（默认 1MB）
# max_read_bytes = "1MB"

## 单行最大长度（默认 8192 字节），超长行会被截断
# max_line_length = 8192

## glob 展开后允许的最大文件数量（默认 100）
# max_targets = 100

## 匹配行的上下文行数（默认 0，不展示上下文）
## 设置后，告警描述中会在匹配行前后展示指定行数的上下文
## 匹配行以 "> " 前缀标记，上下文行以 "  " 前缀标记
# context_before = 0
# context_after = 0

## 日志文件编码（默认 UTF-8）
## 支持：gbk, gb18030, big5, shift_jis, euc-jp, euc-kr, latin1, windows-1252
## 仅在日志文件不是 UTF-8 编码时需要配置
# encoding = ""

## 偏移量持久化文件路径（默认自动生成于 state.d/p.logfile/ 下）
## state 文件与配置文件分离存放，配置目录可保持只读
## 持久化后 catpaw 重启不会丢失读取进度
# state_file = ""

## 文件操作超时（默认 10s，主要防范 NFS 挂载的日志文件）
# gather_timeout = "10s"

## 采集间隔
interval = "30s"

## 匹配到内容后的事件级别
[instances.match]
severity = "Warning"
# title_rule = "[check] [target]"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 3
```
