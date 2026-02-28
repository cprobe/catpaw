# cert 插件设计

## 概述

检查 TLS 证书有效性，覆盖两种场景：

1. **远程模式**（`remote_targets`）：建立 TLS 连接获取对端证书，支持直接 TLS 和 SMTP STARTTLS
2. **文件模式**（`file_targets`）：读取本地 PEM/DER 证书文件，支持 glob 模式批量匹配

检测证书即将过期、已过期、或尚未生效（NotBefore 在未来），产出分级告警事件。

**定位**：补充 `http` 插件的 `cert_expiry` 仅覆盖 HTTPS 端点的不足，扩展到所有 TLS 协议（MySQL TLS、gRPC、Redis over TLS、SMTP STARTTLS 等）和磁盘上的证书文件（Nginx、Let's Encrypt、自签证书等）。

**参考**：Nagios `check_ssl_cert`、Sensu `check-tls-cert`。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 远程证书过期 | `cert::remote_expiry` | 远程 TLS 连接获取对端证书，检查有效期 |
| 文件证书过期 | `cert::file_expiry` | 本地 PEM/DER 证书文件，检查有效期 |

有效期检查同时覆盖：
- **即将过期**（NotAfter 在阈值窗口内）→ Warning / Critical
- **已过期**（NotAfter 已过）→ 无条件 Critical
- **尚未生效**（NotBefore 在未来）→ 无条件 Critical

- **target label** 为检查对象标识：远程模式为 `host:port`（含 per-target SNI 时仍为 `host:port`），文件模式为文件路径
- **默认 title_rule** 为 `"[check] [target]"`

### 未来扩展维度（本版本不实现）

| 维度 | check label | 说明 |
| --- | --- | --- |
| 主机名匹配 | `cert::hostname_match` | 检查证书 SAN/CN 是否匹配 target hostname |

hostname_match 可捕获"负载均衡器配错导致返回其他域名证书"的问题。作为可选维度，default off，预留在后续版本实现。

## 数据来源

### 远程模式

纯标准库：`crypto/tls` + `net`。

连接流程：
1. `net.DialTimeout("tcp", host:port, timeout)` 建立 TCP 连接
2. 如配置 STARTTLS，先完成应用层协议协商（详见下文 STARTTLS 章节）
3. `tls.Client(conn, tlsConfig).Handshake()` 完成 TLS 握手
4. 从 `conn.ConnectionState().PeerCertificates` 获取证书链

TLS 连接始终使用 `InsecureSkipVerify = true`，原因：
- 本插件的核心职责是检查**到期时间**，不是验证证书链
- 自签证书、过期证书、中间证书缺失等场景仍需获取证书信息
- Go 的 `tls.Dial` 在 verify 失败时直接返回错误，无法读取证书
- 如需证书链验证，使用 `http` 插件的连通性检查（TLS 握手失败即告警）

### 文件模式

纯标准库：`crypto/x509` + `encoding/pem` + `path/filepath`。

`file_targets` 支持精确路径和 glob 模式：
- 精确路径：`"/etc/ssl/certs/app.pem"`
- glob 模式：`"/etc/letsencrypt/live/*/fullchain.pem"`、`"/etc/nginx/ssl/*.crt"`

glob 解析在每次 `Gather` 时动态执行（非 Init 阶段），原因：
- 证书文件可能在运行时新增/删除（如 Let's Encrypt 自动续期新增域名）
- 与 logfile 插件的 glob 行为保持一致

解析流程（per file）：
1. `os.ReadFile` 读取文件（限制 1MB 防止误指大文件）
2. 尝试 PEM 解码：遍历所有 `CERTIFICATE` block，逐一 `x509.ParseCertificate`
3. 若 PEM 无有效证书，尝试 DER 解码：`x509.ParseCertificate(raw)`
4. 两种都失败则报错

PEM 文件可能包含证书链（leaf + intermediates），所有证书均被解析，取最早过期的那张报告。

### 无新增依赖

所有功能基于 `crypto/tls`、`crypto/x509`、`encoding/pem`、`net` 标准库，零外部依赖。

## 远程模式详解

### 连接类型

| `starttls` 配置 | 行为 | 适用场景 |
| --- | --- | --- |
| `""`（默认） | 直接 TLS 握手 | MySQL TLS、gRPC、Redis over TLS、LDAPS、任意 TLS 端口 |
| `"smtp"` | SMTP 协议协商后升级 TLS | 邮件服务器（25/587 端口） |

`starttls` 字段是可扩展的枚举，未来可新增协议（见下文"扩展性"章节）。Init 校验时通过 handler 注册表白名单验证，而非硬编码 `== "smtp"`。

### 直接 TLS

```go
conn, err := net.DialTimeout("tcp", target, timeout)
tlsConn := tls.Client(conn, tlsConfig)
err = tlsConn.HandshakeContext(ctx)
certs := tlsConn.ConnectionState().PeerCertificates
```

### SMTP STARTTLS

SMTP 要求先完成文本协议协商，再升级 TLS：

```
客户端                     服务器
   -------- TCP connect -------->
   <------- 220 banner ---------
   -------- EHLO catpaw ------->
   <------- 250 OK -------------
   -------- STARTTLS ---------->
   <------- 220 Ready ----------
   ======== TLS Handshake ======
```

实现要点：
- 读取响应使用 `bufio.Reader`，按 `\r\n` 分行
- SMTP 多行响应以 `250-` 开头，最后一行为 `250 `（空格）
- 超时控制：每次读写前 `conn.SetDeadline`
- 协商失败立即返回错误，不尝试 TLS

### STARTTLS 扩展性设计

STARTTLS handler 实现为内部注册表，便于后续新增协议而无需修改核心逻辑：

```go
type starttlsHandler func(conn net.Conn, timeout time.Duration) error

var starttlsHandlers = map[string]starttlsHandler{
    "smtp": smtpStartTLS,
    // 未来扩展：
    // "imap": imapStartTLS,
    // "pop3": pop3StartTLS,
    // "ftp":  ftpStartTLS,
}
```

Init 校验使用注册表：
```go
if ins.StartTLS != "" {
    if _, ok := starttlsHandlers[ins.StartTLS]; !ok {
        return fmt.Errorf("unsupported starttls protocol: %q (supported: %v)", ...)
    }
}
```

Nagios `check_ssl_cert` 支持 12+ 种 STARTTLS 协议（SMTP、IMAP、POP3、FTP、XMPP、LDAP、PostgreSQL 等）。第一版只做 SMTP（最常用），后续按需扩展，架构已预留。

### 端口默认值

如果 target 不含端口（如 `example.com`），自动追加 `:443`。用户也可显式指定任意端口。

### SNI 控制

TLS 握手中的 SNI（Server Name Indication）决定服务器返回哪张证书。三层优先级：

| 优先级 | 来源 | 示例 |
| --- | --- | --- |
| 1（最高） | per-target `@sni` 后缀 | `"10.0.0.1:443@api.example.com"` |
| 2 | instance 级 `server_name` 配置 | `server_name = "example.com"` |
| 3（默认） | 从 target 的 hostname 自动提取 | `"example.com:443"` → SNI=`example.com` |

**per-target SNI 语法**：`host:port@sni`

```toml
remote_targets = [
    "10.0.0.1:443@api.example.com",        # 通过 IP 连接，SNI=api.example.com
    "10.0.0.2:443@dashboard.example.com",   # 同一 LB，不同域名
    "example.com:443",                      # 自动提取 SNI=example.com
]
```

target label 中不包含 `@sni` 部分（事件的 target 始终为 `host:port`），SNI 信息记录在 `_attr_cert_sni` 标签中。

**为什么需要 per-target SNI**：instance 级 `server_name` 是全局单一值，当一个 instance 中多个 IP 需要不同 SNI 时，只能拆成多个 instance——违背配置简洁原则。per-target SNI 在不增加 instance 的前提下解决此问题。

## 文件模式详解

### 支持的格式

| 格式 | 检测方式 | 文件扩展名示例 |
| --- | --- | --- |
| PEM | `pem.Decode` 成功 | `.pem`、`.crt`、`.cert` |
| DER | PEM 解析无果后 `x509.ParseCertificate` | `.der`、`.cer` |

自动检测，无需用户指定格式。

### Glob 模式支持

`file_targets` 同时支持精确路径和 glob 模式（复用 `filter.HasMeta` + `filepath.Glob`）：

```toml
file_targets = [
    "/etc/ssl/certs/app.pem",                     # 精确路径
    "/etc/letsencrypt/live/*/fullchain.pem",       # glob: 所有 Let's Encrypt 域名
    "/etc/nginx/ssl/*.crt",                        # glob: Nginx 所有证书
]
```

行为：
- 精确路径不存在 → Critical 事件（用户显式指定的文件不应消失）
- glob 无匹配 → 静默跳过（模式尚未有匹配文件属正常）
- glob 匹配的文件在两次 Gather 间消失 → 静默跳过（证书自然轮转）

### 多证书文件（链 / bundle）

一个 PEM 文件可能包含多张证书（证书链）。所有 `CERTIFICATE` block 均被解析，取**最早过期**的证书报告，确保不遗漏链上任何即将过期的证书。

**Leaf vs Intermediate 区分**：如果最早过期的不是链中第一张（通常是 leaf），Description 中会注明 `"intermediate cert <subject> expires in ..."`，提示用户这不是他直接管理的证书，可能需要联系 CA 或更新 chain bundle。

`_attr_cert_chain_count` 标签记录链中证书总数，让用户一眼区分单证书和 bundle。

### 文件大小限制

读取限制 1MB（`maxCertFileSize = 1 << 20`）。正常证书文件远小于此限制，超过说明误指了非证书文件。

## 阈值设计

### "within" 反向阈值

与 `http` 插件的 `cert_expiry` 一致，使用 `warn_within` / `critical_within` 表示"距到期还有多久时触发"：

| 阈值 | 含义 |
| --- | --- |
| `warn_within = "720h"` | 证书在 30 天内过期时 Warning |
| `critical_within = "168h"` | 证书在 7 天内过期时 Critical |

约束：`critical_within < warn_within`（critical 是更紧急的内圈，warn 是外圈）。

### 已过期特殊处理

如果证书已过期（`time.Until(expiry) < 0`），**无条件 Critical**，不受阈值配置影响。描述明确说明已过期多久。

### 尚未生效特殊处理

如果证书尚未生效（`time.Now().Before(cert.NotBefore)`），**无条件 Critical**。这可能是：
- 提前部署了未来才生效的证书
- 服务器时钟偏移（时间比实际慢）
- 证书签发错误

与已过期检查对称——描述中明确说明何时生效：`"cert not yet valid (starts at <NotBefore>)"`。

### 默认阈值

如果对应 targets 非空但 `warn_within` 和 `critical_within` 都未配置（均为 0），自动填充默认值：

| 阈值 | 默认值 | 理由 |
| --- | --- | --- |
| `warn_within` | `720h`（30 天） | Let's Encrypt 标准续期窗口，给运维充足响应时间 |
| `critical_within` | `168h`（7 天） | 一周内过期属紧急，需立即行动 |

这确保"开箱即用"——只填 targets 就能获得合理的告警行为。

## 结构体设计

```go
type ExpiryCheck struct {
    WarnWithin     config.Duration `toml:"warn_within"`
    CriticalWithin config.Duration `toml:"critical_within"`
    TitleRule      string          `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    RemoteTargets []string        `toml:"remote_targets"`
    FileTargets   []string        `toml:"file_targets"`
    Timeout        config.Duration `toml:"timeout"`
    Concurrency    int             `toml:"concurrency"`
    MaxFileTargets int             `toml:"max_file_targets"`
    StartTLS       string          `toml:"starttls"`
    ServerName     string          `toml:"server_name"`

    RemoteExpiry ExpiryCheck `toml:"remote_expiry"`
    FileExpiry   ExpiryCheck `toml:"file_expiry"`

    tlsConfig         *tls.Config
    targetSNI         map[string]string // per-target SNI: host:port → sni
    explicitFilePaths []string          // 非 glob 的精确文件路径
    fileGlobPatterns  []string          // glob 模式
}
```

不需要：
- `GatherTimeout` / `inFlight` — 每个连接有 `Timeout` 硬上限，不会无限阻塞（与 NFS 不同）
- `mu sync.Mutex` — Gather 内部并发由 WaitGroup 管理，实例无共享可变状态
- `encoding` — 证书是二进制/Base64 格式，不涉及文本编码

## _attr_ 标签

### remote_expiry / file_expiry 事件

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_cert_subject` | `CN=*.example.com` | 证书 Subject |
| `_attr_cert_issuer` | `CN=Let's Encrypt Authority X3` | 证书颁发者 |
| `_attr_cert_serial` | `03:A1:45:9B:...` | 证书序列号（唯一标识） |
| `_attr_cert_sha256` | `2F:3C:8A:...` | 证书 SHA-256 指纹 |
| `_attr_cert_not_before` | `2026-01-01 00:00:00` | 证书生效时间 |
| `_attr_cert_expires_at` | `2026-06-15 23:59:59` | 过期时间 |
| `_attr_time_until_expiry` | `106d 12h` / `-2d 6h` | 距过期剩余时间（已过期为负值绝对值加 `-` 前缀） |
| `_attr_cert_dns_names` | `*.example.com, example.com` | 证书 SAN DNS 名称 |
| `_attr_cert_chain_count` | `3` | 链中证书总数（仅 bundle 文件和远程链有意义） |
| `_attr_cert_sni` | `api.example.com` | 实际使用的 SNI（仅远程模式） |
| `_attr_warn_within` | `30 days` | 配置的 warn 阈值 |
| `_attr_critical_within` | `7 days` | 配置的 critical 阈值 |

**`_attr_cert_serial` + `_attr_cert_sha256`** 的价值：
- 确认证书是否被正确替换（对比 serial）
- 与 CDN、WAF 等其他系统核对证书一致性（对比 SHA-256 fingerprint）
- 跟踪证书轮换历史

### OK 事件

基础 labels（check、target）即可，不需要额外 attr。OK 事件仍在 Description 中包含过期时间，便于一眼确认。

## Init() 校验

1. `remote_targets` 和 `file_targets` 不能同时为空
2. 远程目标解析：
   - 提取 per-target SNI（`@` 分隔），存入内部 `targetSNI map[string]string`
   - 剩余部分用 `net.SplitHostPort` 解析，无端口时自动补 `:443`
   - 标准化后的 `host:port` 存回 `remote_targets`
3. `starttls` 校验：通过 `starttlsHandlers` 注册表白名单验证（空字符串 = 直接 TLS）
4. `remote_expiry` 阈值关系校验：如果两者都 > 0，`critical_within` 必须 < `warn_within`
5. `file_expiry` 阈值关系校验：同上
6. 默认阈值填充：如果对应 targets 非空且两个阈值都为 0，填充 720h / 168h
7. `timeout` 默认 `10s`
8. `concurrency` 默认 `10`
9. `max_file_targets` 默认 `100`（防止宽泛 glob 匹配大量无关文件）
10. 构建 `tlsConfig`：`InsecureSkipVerify = true`，如有 `server_name` 则设置 `ServerName`
11. 区分 `file_targets` 中的精确路径和 glob 模式（复用 `filter.HasMeta`）

## Gather() 逻辑

```
Gather(q):
    wg, se = WaitGroup, Semaphore(concurrency)

    // 远程目标
    for each target in remote_targets:
        wg.Add(1)
        go:
            se.Acquire()
            defer se.Release(), wg.Done(), recover()
            checkRemote(q, target)

    // 文件目标：精确路径 + glob 动态解析
    resolvedFiles = resolveFileTargets()
    if len(resolvedFiles) > max_file_targets:
        产出 Warning 事件: "file_targets resolved to N files, exceeding max_file_targets(M)"
        resolvedFiles = resolvedFiles[:max_file_targets]
    for each filePath in resolvedFiles:
        wg.Add(1)
        go:
            se.Acquire()
            defer se.Release(), wg.Done(), recover()
            checkFile(q, filePath)

    wg.Wait()
```

### resolveFileTargets()

```
resolveFileTargets():
    seen = {}
    files = []

    // 精确路径：直接加入（即使不存在，checkFile 会产出 Critical）
    for path in explicitFilePaths:
        if !seen[path]:
            files = append(files, path)
            seen[path] = true

    // glob 模式：动态展开
    for pattern in fileGlobPatterns:
        matches = filepath.Glob(pattern)
        for m in matches:
            if !m.IsDir() && !seen[m]:
                files = append(files, m)
                seen[m] = true

    return files
```

### checkRemote(q, target)

```
checkRemote(q, target):
    event = buildEvent("cert::remote_expiry", target)

    // 确定 SNI（优先级：per-target > instance-level > hostname）
    sni = targetSNI[target] || ins.ServerName || hostname(target)
    tlsCfg = ins.tlsConfig.Clone()
    tlsCfg.ServerName = sni
    event._attr_cert_sni = sni

    // 建立连接
    conn = net.DialTimeout("tcp", target, timeout)
    if error:
        event → Critical: "TLS connection to <target> failed: <error>"
        return

    // STARTTLS 协商（如配置）
    if ins.StartTLS != "":
        handler = starttlsHandlers[ins.StartTLS]
        handler(conn, timeout)
        if error:
            event → Critical: "<protocol> STARTTLS negotiation with <target> failed: <error>"
            return

    // TLS 握手
    tlsConn = tls.Client(conn, tlsCfg)
    tlsConn.HandshakeContext(ctx)
    if error:
        event → Critical: "TLS handshake with <target> failed: <error>"
        return
    defer tlsConn.Close()

    certs = tlsConn.ConnectionState().PeerCertificates
    if len(certs) == 0:
        event → Critical: "no peer certificates from <target>"
        return

    // 取最早过期的证书
    cert, certIdx = earliestExpiry(certs)
    evaluateExpiry(event, cert, certIdx, len(certs), ins.RemoteExpiry)
    q.PushFront(event)
```

### checkFile(q, target)

```
checkFile(q, target):
    event = buildEvent("cert::file_expiry", target)

    data, err = os.ReadFile(target)
    if err:
        if os.IsNotExist(err):
            event → Critical: "certificate file not found: <target>"
        else:
            event → Critical: "failed to read <target>: <error>"
        return

    if len(data) > maxCertFileSize:
        event → Critical: "file too large (<size>), likely not a certificate: <target>"
        return

    certs, err = parseCerts(data)
    if err || len(certs) == 0:
        event → Critical: "no valid certificates found in <target>: <error>"
        return

    cert, certIdx = earliestExpiry(certs)
    evaluateExpiry(event, cert, certIdx, len(certs), ins.FileExpiry)
    q.PushFront(event)
```

### evaluateExpiry(event, cert, certIdx, chainLen, check)

```
evaluateExpiry(event, cert, certIdx, chainLen, check):
    expiry = cert.NotAfter
    timeUntil = time.Until(expiry)

    // 附加 _attr_ 标签
    event._attr_cert_subject = cert.Subject.String()
    event._attr_cert_issuer = cert.Issuer.String()
    event._attr_cert_serial = formatSerial(cert.SerialNumber)
    event._attr_cert_sha256 = sha256Fingerprint(cert.Raw)
    event._attr_cert_not_before = cert.NotBefore.Format("2006-01-02 15:04:05")
    event._attr_cert_expires_at = expiry.Format("2006-01-02 15:04:05")
    if timeUntil >= 0:
        event._attr_time_until_expiry = humanDuration(timeUntil)       // "106d 12h"
    else:
        event._attr_time_until_expiry = "-" + humanDuration(-timeUntil) // "-2d 6h"
    event._attr_cert_dns_names = join(cert.DNSNames, ", ")
    event._attr_cert_chain_count = strconv.Itoa(chainLen)
    event._attr_warn_within = check.WarnWithin.HumanString()
    event._attr_critical_within = check.CriticalWithin.HumanString()

    // 判断是否为中间证书（非链中第一张）
    isIntermediate = certIdx > 0

    if time.Now().Before(cert.NotBefore):
        // 尚未生效：无条件 Critical
        event → Critical: "cert not yet valid (starts at <NotBefore>)"
    else if timeUntil < 0:
        // 已过期：无条件 Critical
        if isIntermediate:
            event → Critical: "intermediate cert <subject> expired <abs(timeUntil)> ago"
        else:
            event → Critical: "cert expired <abs(timeUntil)> ago (expired at <expiry>)"
    else if critical_within > 0 && timeUntil <= critical_within:
        if isIntermediate:
            event → Critical: "intermediate cert <subject> expires in <timeUntil>"
        else:
            event → Critical: "cert expires in <timeUntil>, within critical threshold"
    else if warn_within > 0 && timeUntil <= warn_within:
        if isIntermediate:
            event → Warning: "intermediate cert <subject> expires in <timeUntil>"
        else:
            event → Warning: "cert expires in <timeUntil>, within warning threshold"
    else:
        event → Ok: "cert expires at <expiry>, everything is ok"
```

### 关键行为

1. **每个 target 独立产出事件**，一个 target 连接失败不影响其他（原则 13）
2. **已过期证书无条件 Critical**，不受阈值配置影响
3. **尚未生效证书无条件 Critical**（NotBefore 在未来）
4. **中间证书区分**：Description 中注明 "intermediate cert" 前缀，提示用户非直接管理的证书
5. **开箱即用默认值**：只填 targets 即可获得 30d/7d 告警
6. **连接失败 = Critical**（含具体错误），不会静默失效（原则 7）
7. **并发控制**：`semaphore` 限制同时连接数，防止大量 targets 导致 fd 耗尽
8. **Panic recovery**：每个 goroutine 有 `defer recover()`
9. **per-target SNI**：同一 instance 内不同 target 可使用不同 SNI

### Description 示例

- 证书即将过期：`cert expires in 5 days 3 hours, within critical threshold 7 days`
- 证书已过期：`cert expired 2 days 6 hours ago (expired at 2026-02-26 00:00:00)`
- 证书尚未生效：`cert not yet valid (starts at 2026-04-01 00:00:00)`
- 证书健康：`cert expires at 2026-12-01 23:59:59, everything is ok`
- 中间证书即将过期：`intermediate cert CN=Let's Encrypt Authority X3 expires in 25 days 4 hours`
- 连接失败：`TLS connection to db.example.com:3306 failed: connection refused`
- STARTTLS 失败：`SMTP STARTTLS negotiation with mail.example.com:25 failed: unexpected response "454 TLS not available"`
- 文件不存在：`certificate file not found: /etc/ssl/certs/app.pem`
- 文件解析失败：`no valid certificates found in /etc/ssl/certs/app.pem: x509: malformed certificate`
- 无对端证书：`no peer certificates from example.com:443`

## 默认配置关键决策

| 决策 | 值 | 理由 |
| --- | --- | --- |
| warn_within | `720h`（30 天） | Let's Encrypt 标准续期窗口；给运维 30 天响应时间 |
| critical_within | `168h`（7 天） | 一周内过期属紧急 |
| timeout | `10s` | 覆盖慢 DNS + 跨洋 TLS 握手；本地连接毫秒级 |
| concurrency | `10` | 10 个并发连接足以快速检查大量 target；防止 fd 暴涨 |
| max_file_targets | `100` | 防止宽泛 glob 意外匹配大量非证书文件；正常场景远低于此值 |
| starttls | `""`（直接 TLS） | 绝大多数场景是直接 TLS |
| InsecureSkipVerify | `true`（硬编码） | 确保自签/过期/链缺失也能获取证书信息 |
| 端口默认值 | `443` | TLS 最常见端口 |
| for_duration | `0` | 证书过期是确定性状态，无需持续确认 |

## 与 http 插件的关系

| 特性 | cert | http |
| --- | --- | --- |
| 检查范围 | **所有 TLS 协议** + 本地证书文件 | 仅 HTTPS 端点 |
| 连接方式 | `tls.Dial` / STARTTLS | `http.Client.Do` |
| 连接验证 | 不验证（InsecureSkipVerify=true） | 完整验证（连通性检查） |
| 证书来源 | TLS 握手 + 本地文件 | HTTP 响应的 TLS 状态 |
| 适用场景 | MySQL TLS、gRPC、Redis TLS、邮件服务器、磁盘证书 | Web 站点 HTTPS |
| 其他检查 | 无（专注过期） | 连通性、响应时间、状态码、响应体 |

两者互补：
- HTTPS 站点 → 用 `http` 插件（一次检查覆盖连通性 + 证书 + 状态码）
- 非 HTTP TLS 服务 / 磁盘证书 → 用 `cert` 插件
- 如果只关心证书过期而不关心 HTTP 功能 → 也可用 `cert` 插件（更轻量）

## 与 Prometheus blackbox_exporter 的关系

很多用户同时使用 Prometheus，可能会问"为什么不直接用 blackbox_exporter"。

| 特性 | catpaw cert | blackbox_exporter |
| --- | --- | --- |
| 输出类型 | **分级事件**（直接告警） | 指标（需额外配 PromQL 规则） |
| 文件模式 | 支持（含 glob） | 不支持 |
| STARTTLS | 支持（可扩展） | 不支持 |
| per-target SNI | 支持 | 需为每个 target 配不同 module |
| 集成门槛 | 填 targets 即可（开箱即用） | 需配 scrape config + alerting rules |
| 依赖栈 | 独立运行 | 依赖 Prometheus + Alertmanager |
| 适用场景 | 不想维护 Prometheus 栈、或需要即时告警 | 已有 Prometheus 生态的环境 |

两者不冲突——如果已有 Prometheus 且证书指标已采集，catpaw cert 的价值在于提供更精确的事件语义（intermediate vs leaf、not-yet-valid 检测）和对本地文件的覆盖。

## 跨平台兼容性

| 平台 | 远程模式 | 文件模式 | STARTTLS |
| --- | --- | --- | --- |
| Linux | 完整支持 | 完整支持 | 完整支持 |
| macOS | 完整支持 | 完整支持 | 完整支持 |
| Windows | 完整支持 | 完整支持 | 完整支持 |

全部基于标准库，无平台特定代码。

## 文件结构

```
plugins/cert/
    design.md             # 本文档
    cert.go               # 主逻辑
    cert_test.go          # 测试

conf.d/p.cert/
    cert.toml             # 默认配置
```

## 默认配置文件

```toml
[[instances]]
## ===== 最小可用示例（30 秒跑起来）=====
## 1) remote_targets 填远程 TLS 服务地址（host:port）
## 2) file_targets 填本地证书文件路径（支持 glob 模式）
## 3) 默认 30 天 Warning、7 天 Critical
## 两种模式可以同时使用，也可以只用其中一种
## 例子：
## remote_targets = ["example.com:443", "db.example.com:3306"]
## file_targets = ["/etc/ssl/certs/app.pem", "/etc/nginx/ssl/*.crt"]

## 远程 TLS 检查目标
## 格式：host:port（端口省略默认 443）
## 需要指定 SNI 时：host:port@sni（如 "10.0.0.1:443@api.example.com"）
remote_targets = [
    # "example.com:443",
    # "db.example.com:3306",
    # "redis.internal:6380",
    # "10.0.0.1:443@api.example.com",
]

## 本地证书文件（支持 PEM 和 DER 格式，自动检测）
## 支持 glob 模式，如 "/etc/letsencrypt/live/*/fullchain.pem"
file_targets = [
    # "/etc/ssl/certs/app.pem",
    # "/etc/nginx/ssl/*.crt",
    # "/etc/letsencrypt/live/*/fullchain.pem",
]

## STARTTLS 协议（默认空 = 直接 TLS）
## 设为 "smtp" 用于检查 SMTP 邮件服务器（25/587 端口）
# starttls = ""

## SNI 覆盖（默认从 target hostname 自动提取）
## 作为所有 remote_targets 的默认 SNI，per-target @sni 优先级更高
# server_name = ""

## 连接超时（默认 10s）
# timeout = "10s"

## 并发连接数（默认 10）
# concurrency = 10

## glob 模式解析的最大文件数（默认 100）
## 超过此限制会产出 Warning 事件并截断
# max_file_targets = 100

## 采集间隔（证书过期变化慢，可设较长间隔）
interval = "1h"

## 远程证书过期检测
## warn_within: 距过期多久开始 Warning（默认 30 天）
## critical_within: 距过期多久升级 Critical（默认 7 天）
## 同时检测已过期和尚未生效（NotBefore 在未来）的证书
[instances.remote_expiry]
warn_within = "720h"
critical_within = "168h"
# title_rule = "[check] [target]"

## 文件证书过期检测（阈值含义同上）
[instances.file_expiry]
warn_within = "720h"
critical_within = "168h"
# title_rule = "[check] [target]"

[instances.alerting]
for_duration = 0
repeat_interval = "4h"
repeat_number = 0
# disabled = false
# disable_recovery_notification = false
```
