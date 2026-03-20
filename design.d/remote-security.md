# 远程控制安全设计

## 背景与威胁模型

catpaw 通过 WebSocket 长连接接入 catpaw-server，支持远程诊断（inspect/diagnose）和交互式 Chat（含 shell 执行）。这带来了显著的安全挑战：

- **Server 是所有 Agent 的控制面**：Server 被攻陷 = 所有 Agent 沦陷
- **AI 生成的命令不可预测**：不同于 Ansible/Salt 的确定性 Playbook，AI 产出的 shell 命令无法提前审计
- **长连接持续暴露攻击面**：不同于 SSH 的按需连接，WebSocket 长连接意味着攻击窗口永远开着

### 当前状态（问题）

| 问题 | 位置 | 风险 |
|------|------|------|
| `remoteShellExecutor` 始终返回 `approved=true` | `agent/agent.go` | 远程 shell 无人确认，Server 可执行任意命令 |
| `allow_shell` 由 Server 单方面控制 | `session_handler.go` | Agent 侧无策略覆盖能力 |
| 共享静态 Token 认证 | `server/conn.go` | Token 泄露影响所有 Agent |
| 无 Agent 侧 ACL | 全局 | Server 下发什么就执行什么 |
| 无本地审计日志 | 全局 | 事后无法追溯远程操作 |

### 攻击场景

1. **Server 入侵**：攻击者获取 Server 权限 → 向任意 Agent 发送 `session_start{type:"chat", allow_shell:true}` → 通过 AI 或直接构造 `exec_shell` → 获得所有 Agent 的 shell 权限
2. **Token 泄露/MITM**：共享 Token 被截获 → 伪造 Server → 下发恶意 session
3. **AI Prompt 注入**：恶意用户通过 Chat 输入诱导 AI 执行危险命令，远程场景下无人类确认
4. **横向移动**：攻陷一台 Agent 后，利用相同 Token 冒充其他 Agent 或探测 Server

### 行业对比

| | Ansible | SaltStack | Puppet | catpaw (当前) |
|---|---------|-----------|--------|--------------|
| 连接模型 | Push/SSH | 长连接/ZeroMQ | Pull/HTTPS | 长连接/WebSocket |
| 认证 | SSH Key (per-host) | Per-Minion RSA Key | mTLS/PKI | **共享 Token** |
| 常驻 Agent | 无 | 有 | 有 | 有 |
| 命令来源 | 人写 Playbook | 人触发模块 | 声明式 Catalog | **AI 动态生成** |
| Raw Shell | 有 | 有 (cmd.run) | 弱化 | 有，**无限制** |
| Agent 侧防护 | 无 Agent | 无 | 无(声明式) | **无** |
| ACL | SSH 权限 | client_acl | Node Classification | **无** |

catpaw 的安全挑战比上述工具都大：AI 命令不可预测 + 长连接持续暴露。需要在 Agent 侧建立独立的信任决策能力——这是其他工具不需要的。

---

## 分层安全架构

核心原则：**Agent 不无条件信任 Server，安全决策权留在 Agent 侧。**

```
┌─────────────────────────────────────────────────────────┐
│                  第四层：纵深防御                          │
│     低权限运行 · 受限 Shell · 速率限制 · 审计日志          │
├─────────────────────────────────────────────────────────┤
│                  第三层：传输与认证加固                     │
│     Per-Agent Key · mTLS · Session 签名 · Token 轮转     │
├─────────────────────────────────────────────────────────┤
│                  第二层：远程确认协议                       │
│     Human-in-the-Loop · 超时自动拒绝                     │
├─────────────────────────────────────────────────────────┤
│                  第一层：Agent 侧命令分级策略               │
│     白名单(Safe) · 需确认(Guarded) · 黑名单(Blocked)     │
└─────────────────────────────────────────────────────────┘
```

---

## 第一层：Agent 侧命令分级策略引擎

**目标**：Agent 本地定义命令安全策略，Server 无法覆盖或绕过。

### 命令分级

```
┌──────────┬────────────────────────────────────────┬──────────────┐
│  级别     │  说明                                  │  处理方式     │
├──────────┼────────────────────────────────────────┼──────────────┤
│  Safe    │  只读诊断命令，不改变系统状态              │  自动批准     │
│  Guarded │  有限度的写操作或敏感读取                  │  远程用户确认  │
│  Blocked │  高危命令，可能造成不可逆破坏              │  Agent 直接拒绝│
└──────────┴────────────────────────────────────────┴──────────────┘
```

### Safe 白名单（只读诊断命令）

以下命令模式自动批准，Server 无需额外确认：

```
# 系统信息
df, du, free, uptime, uname, hostname, whoami, id, date
cat /proc/*, cat /sys/*, sysctl -a
lsblk, lscpu, lspci, lsmod, lsof
top -bn1, ps aux, vmstat, iostat, mpstat, sar
dmesg, journalctl (只读)

# 网络诊断
ss, netstat, ip addr, ip route, ip neigh
ping, traceroute, dig, nslookup, curl (GET only)
iptables -L, nft list

# 服务状态
systemctl status, systemctl list-units, systemctl list-timers
docker ps, docker inspect, docker logs (只读)

# 文件查看（限定路径）
cat, head, tail, less, wc, grep, awk, sed (不带 -i)
ls, find, stat, file
```

### Blocked 黑名单（硬编码 + 本地配置，不可被 Server 覆盖）

```
# 系统破坏
rm -rf /, rm -rf /*, dd if=* of=/dev/*
mkfs, fdisk, parted (写模式)
shutdown, reboot, halt, poweroff, init 0

# 权限与用户
chmod 777, chown root, usermod, useradd, userdel, passwd
visudo, sudoers 修改

# 网络破坏
iptables -F, iptables -X, nft flush
ifconfig * down, ip link set * down (非 lo)

# 内核与引导
insmod, rmmod, modprobe (写)
grub 修改, bootloader 修改

# 数据破坏
DROP DATABASE, DROP TABLE, FLUSHALL, FLUSHDB
truncate (数据库)

# 管道到 shell (防注入)
| bash, | sh, | zsh, $(...), `...` (动态执行)
```

### 策略引擎配置

```toml
# conf.d/config.toml

[server.security]
# 是否允许远程 shell（全局开关，默认关闭）
allow_remote_shell = false

# 命令分级策略（Agent 侧，Server 无法覆盖）
# 额外的 Safe 命令模式（正则），追加到内置白名单
safe_patterns = [
    "^kubectl get ",
    "^helm status ",
]

# 额外的 Blocked 命令模式（正则），追加到内置黑名单
blocked_patterns = [
    "^kubectl delete ",
    "^helm uninstall ",
]

# Guarded 命令确认超时（超时自动拒绝）
shell_approval_timeout = "30s"

# 每分钟最大 shell 命令数（速率限制）
shell_rate_limit = 10
```

### 策略引擎伪代码

```go
type ShellPolicy struct {
    safePatterns    []*regexp.Regexp  // 内置 + 配置追加
    blockedPatterns []*regexp.Regexp  // 内置 + 配置追加
    rateLimit       rate.Limiter
    approvalTimeout time.Duration
}

func (p *ShellPolicy) Evaluate(command string) ShellDecision {
    normalized := normalizeCommand(command)

    // 黑名单优先级最高
    for _, pat := range p.blockedPatterns {
        if pat.MatchString(normalized) {
            return Blocked
        }
    }

    // 检测管道注入
    if containsShellInjection(normalized) {
        return Blocked
    }

    // 速率限制
    if !p.rateLimit.Allow() {
        return RateLimited
    }

    // 白名单匹配
    for _, pat := range p.safePatterns {
        if pat.MatchString(normalized) {
            return Safe
        }
    }

    // 未匹配任何规则 → Guarded（需要远程用户确认）
    return Guarded
}
```

---

## 第二层：远程确认协议（Human-in-the-Loop）

**目标**：Guarded 命令通过 WebSocket 协议传回 Server，由用户在 Web UI 上确认。

### 协议流程

```
AI 请求执行: systemctl restart nginx
  │
  ▼
Agent ShellPolicy.Evaluate()
  │
  ├─ Safe → 直接执行，返回结果
  ├─ Blocked → 拒绝，返回 "command blocked by agent policy"
  ├─ RateLimited → 拒绝，返回 "shell rate limit exceeded"
  │
  └─ Guarded → 进入确认流程:
       │
       ▼
     Agent 发送 session_output:
       {
         "stage": "shell_approval_request",
         "metadata": {
           "command": "systemctl restart nginx",
           "approval_id": "apr_abc123",
           "timeout_sec": 30
         }
       }
       │
       ▼
     Server 展示给用户:
       ┌──────────────────────────────────┐
       │  AI requests command:            │
       │  systemctl restart nginx         │
       │                                  │
       │  [Approve]  [Reject]  [Edit]     │
       └──────────────────────────────────┘
       │
       ▼
     Server 发送 session_input:
       {
         "type": "shell_decision",
         "approval_id": "apr_abc123",
         "approved": true,             // or false
         "edited_command": "..."       // 可选，用户修改后的命令
       }
       │
       ▼
     Agent 收到决定:
       ├─ approved=true → 执行（原命令或 edited_command）
       ├─ approved=false → 返回 "user rejected command"
       └─ 超时未响应 → 自动拒绝
```

### 超时机制

- 默认 30 秒超时，可配置
- 超时后 Agent 自动拒绝，不会无限等待
- AI 收到拒绝后可选择换用其他诊断工具继续分析

---

## 第三层：传输与认证加固

### 3.1 Per-Agent Key（替代共享 Token）

**现状**：所有 Agent 使用同一个 `agent_token`，一个泄露全部受影响。

**目标**：每个 Agent 拥有独立密钥，支持单独吊销。

```
Agent 首次启动:
  1. Agent 生成 RSA/Ed25519 密钥对，存储在 state.d/agent_key
  2. Agent 发送公钥 + 临时注册 token 到 Server
  3. Server 管理员审核后接受（类似 salt-key -a）
  4. 后续通信使用 Agent 私钥签名，Server 用已接受的公钥验证

吊销:
  - Server 删除某 Agent 的公钥 → 该 Agent 立即失去连接能力
  - 不影响其他 Agent
```

### 3.2 mTLS（双向 TLS 认证）

**目标**：Agent 和 Server 互相验证身份，防止中间人攻击。

```toml
[server]
# Agent 证书（由组织 CA 签发）
tls_cert = "/etc/catpaw/agent.crt"
tls_key  = "/etc/catpaw/agent.key"
# Server CA 证书（用于验证 Server 身份）
ca_file  = "/etc/catpaw/ca.crt"
# 禁用 tls_skip_verify（生产环境必须关闭）
tls_skip_verify = false
```

证书管理方案选项:
- **自建 CA**：Server 内置简易 CA，Agent 首次连接时签发证书（类似 Puppet）
- **外部 CA**：使用组织已有的 PKI 基础设施
- **短期证书**：证书有效期短（如 24h），自动轮转，降低泄露影响

### 3.3 Session 签名

**目标**：Agent 验证 session_start 确实来自合法 Server，而非重放攻击。

```json
// Server 下发 session_start 时附带签名
{
  "type": "session_start",
  "payload": {
    "session_id": "sess_xyz",
    "session_type": "chat",
    "params": { "allow_shell": true },
    "timestamp": 1711000000,
    "nonce": "random_nonce_123"
  },
  "signature": "base64_hmac_sha256(...)"
}
```

Agent 验证:
1. 检查 timestamp 在合理范围内（防重放）
2. 检查 nonce 未被使用过（防重放）
3. 验证 signature（使用预共享密钥或 Server 公钥）

### 3.4 Token 轮转

**目标**：即使 Token 泄露，影响窗口有限。

- Server 定期生成新 Token，通过已认证连接下发给 Agent
- Agent 平滑切换到新 Token
- 旧 Token 在宽限期后失效

---

## 第四层：纵深防御

### 4.1 低权限运行

```bash
# 创建专用用户
useradd -r -s /sbin/nologin catpaw

# 授予必要的能力（而非 root）
setcap cap_net_raw,cap_sys_ptrace+ep /usr/local/bin/catpaw

# 以 catpaw 用户运行
systemctl edit catpaw
[Service]
User=catpaw
Group=catpaw
```

即使 shell 被攻破，攻击者只获得低权限 shell，无法直接提权。

### 4.2 受限 Shell 环境

远程 shell 命令在受限环境中执行：

```go
func execShellRestricted(ctx context.Context, command string) (string, error) {
    cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)

    // 限制环境变量（移除敏感变量）
    cmd.Env = filterEnv(os.Environ(), []string{
        "PATH", "HOME", "LANG", "LC_ALL", "TERM",
    })

    // 限制资源
    cmd.SysProcAttr = &syscall.SysProcAttr{
        // Linux: 使用 unshare 隔离命名空间（可选）
    }

    // 输出大小限制（已有：256KB capture, 4KB to AI）
    // 执行时间限制（已有：toolTimeout）
    ...
}
```

### 4.3 命令执行速率限制

```toml
[server.security]
# 每分钟最大 shell 命令数
shell_rate_limit = 10
# 每小时最大 shell 命令数
shell_hourly_limit = 100
# 每天最大 shell 命令数
shell_daily_limit = 500
```

防止自动化攻击通过大量 shell 命令快速造成破坏。

### 4.4 本地不可变审计日志

```go
type ShellAuditEntry struct {
    Timestamp   time.Time `json:"timestamp"`
    SessionID   string    `json:"session_id"`
    UserName    string    `json:"user_name"`    // 来自 session_start
    Command     string    `json:"command"`
    Decision    string    `json:"decision"`     // safe/guarded/blocked/approved/rejected/timeout
    Output      string    `json:"output"`       // 截断后的输出
    DurationMs  int64     `json:"duration_ms"`
    ExitCode    int       `json:"exit_code"`
    SourceIP    string    `json:"source_ip"`    // Server 地址
}
```

审计日志:
- 写入 `state.d/shell_audit.log`，append-only
- 每条记录独立一行 JSON（JSONL 格式），便于分析
- Agent 侧记录，Server 无法篡改或删除
- 包含完整上下文：谁（user_name）、从哪（source_ip）、干了什么（command）、结果如何（decision + output）

---

## 实施优先级

### P0：紧急修复（安全底线）

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| 远程 shell 默认关闭 | `allow_remote_shell` 默认 `false`，必须显式开启 | 低 |
| Agent 侧命令分级策略引擎 | Safe/Guarded/Blocked 三级，内置白名单和黑名单 | 中 |
| 远程确认协议 | Guarded 命令走 WebSocket 回传确认流程 | 中 |
| 本地审计日志 | 所有远程 shell 操作写本地 JSONL 日志 | 低 |

### P1：短期加固

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| Per-Agent Key | 替代共享 Token，每 Agent 独立密钥 | 中 |
| Server 侧 ACL | 用户 x Agent x 操作类型 的权限矩阵 | 中 |
| Shell 速率限制 | 分钟/小时/天三级限流 | 低 |
| Session 签名 | 防重放攻击 | 低 |

### P2：中期完善

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| mTLS | Agent 和 Server 双向证书认证 | 高 |
| 短期证书 + 自动轮转 | 降低证书泄露影响 | 高 |
| 受限 Shell 环境 | 环境变量过滤、资源限制 | 中 |
| 审计日志转发 | Agent 审计日志定期上报 Server 归档 | 低 |

### P3：长期演进

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| 低权限运行 + Capabilities | 专用用户 + 最小 Linux capabilities | 中 |
| 命名空间隔离 | shell 命令在独立 namespace 中执行 | 高 |
| 审计告警 | 异常操作模式（高频 shell、Blocked 命中）触发告警 | 中 |
| AI 行为监控 | 检测 AI 输出中的异常命令模式（可能的 prompt 注入） | 高 |

---

## 设计原则总结

1. **Agent 侧策略不可被 Server 覆盖**：黑名单硬编码 + 本地配置，Server 无法绕过
2. **默认安全**：远程 shell 默认关闭；未匹配规则的命令默认进入 Guarded（需确认）
3. **纵深防御**：认证、策略、确认、审计多层叠加，单层失效不致全面沦陷
4. **Human-in-the-Loop**：高危操作必须有人类确认，AI 不是最终决策者
5. **最小权限**：Agent 只暴露必要能力，shell 是 escape hatch 而非常规路径
6. **可审计**：所有远程操作留痕，Agent 侧记录不可被 Server 篡改
7. **优雅降级**：安全机制故障时倾向于拒绝（fail-close），而非放行
