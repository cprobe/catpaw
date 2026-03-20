# 远程控制安全设计

## 背景与威胁模型

catpaw 通过 WebSocket 长连接接入 catpaw-server，支持远程诊断（inspect/diagnose）和交互式 Chat。这带来了安全挑战：

- **Server 是所有 Agent 的控制面**：Server 被攻陷可能影响所有 Agent
- **长连接持续暴露攻击面**：不同于 SSH 的按需连接，WebSocket 长连接意味着攻击窗口永远开着

### 核心设计决策：远程模式禁用 Shell

catpaw 的定位是**故障诊断，不是远程管理**。AI 的职责是分析问题、给出建议，而非直接修复。因此：

- **远程 session（inspect/diagnose/chat）：只暴露结构化诊断工具，不注册 `exec_shell`**
- **本地 `catpaw chat`：保留 shell 能力（有人在终端确认）**

这一决策从根本上消除了"远程任意命令执行"的攻击面，使安全模型大幅简化。

### 为什么可行

catpaw 已有 70+ 结构化诊断工具，覆盖 CPU/内存/磁盘/网络/进程/内核/容器/日志/连接追踪等维度。AI 在本地 chat 中实际使用 `exec_shell` 的场景，绝大多数已被现有工具覆盖：

| AI 可能用 shell 做的事 | 已有工具覆盖 |
|---|---|
| `df -h` | `disk_usage` |
| `ps aux \| grep xxx` | `cpu_top_processes`, `proc_threads` |
| `dmesg \| tail` | `dmesg_recent` |
| `ss -tlnp` | `ss_detail`, `listen_overflow` |
| `journalctl -u xxx` | `journal_query` |
| `cat /some/log \| grep error` | `log_grep`, `log_tail` |

真正需要 shell 而工具覆盖不了的场景极少，且通常是**写操作**（restart 服务、kill 进程、修改配置）——这些本来就不该远程自动执行。

遇到工具不够用时的解决路径：
1. **加 tool**：发现缺什么就补一个诊断工具，比维护 shell 安全体系成本低得多
2. **本地 chat 兜底**：真需要 shell 的复杂排障，用户 SSH 上去跑 `catpaw chat`，有人在回路确认

### 行业对比

| | Ansible | SaltStack | Puppet | catpaw |
|---|---------|-----------|--------|--------|
| 连接模型 | Push/SSH | 长连接/ZeroMQ | Pull/HTTPS | 长连接/WebSocket |
| 认证 | SSH Key (per-host) | Per-Minion RSA Key | mTLS/PKI | 共享 Token → Per-Agent Key |
| 常驻 Agent | 无 | 有 | 有 | 有 |
| 命令来源 | 人写 Playbook | 人触发模块 | 声明式 Catalog | AI + 结构化工具 |
| Raw Shell | 有 | 有 (cmd.run) | 弱化 | **远程禁用，本地保留** |

借鉴要点：
- **来自 Puppet**：结构化操作优于 raw shell，catpaw 的 70+ 诊断工具就是"资源类型"
- **来自 SaltStack**：Per-Agent Key 替代共享 Token，Server 侧 ACL
- **来自 SaltStack 教训（CVE-2020-11651）**：中心化控制面认证漏洞 = 全部 Agent 沦陷，认证必须严谨
- **catpaw 独创**：远程完全禁用 shell，因为 AI 生成的命令不可预测，结构化工具是唯一安全的远程执行路径

---

## 安全架构

```
┌─────────────────────────────────────────────────────────┐
│                  第三层：纵深防御                          │
│     低权限运行 · 工具调用审计 · 异常检测                   │
├─────────────────────────────────────────────────────────┤
│                  第二层：传输与认证加固                     │
│     Per-Agent Key · mTLS · Session 签名 · Token 轮转     │
├─────────────────────────────────────────────────────────┤
│                  第一层：远程禁用 Shell                    │
│     只暴露结构化诊断工具 · 工具只读 · 参数受限             │
└─────────────────────────────────────────────────────────┘
```

---

## 第一层：远程禁用 Shell + 工具安全

### 实现方式

远程 session 构建 Chat 工具集时，不注册 `exec_shell`：

```go
// 远程 chat session：只注册诊断工具，不注册 exec_shell
func buildRemoteChatToolSet(registry *ToolRegistry) []Tool {
    tools := buildDiagnoseToolSet(registry) // 70+ 结构化工具
    // 不调用 addShellTool()
    return tools
}

// 本地 chat session：诊断工具 + exec_shell
func buildLocalChatToolSet(registry *ToolRegistry) []Tool {
    tools := buildDiagnoseToolSet(registry)
    tools = append(tools, shellTool) // 仅本地有
    return tools
}
```

### 工具安全保障

结构化工具天然比 raw shell 安全，但仍需保障：

**1. 工具只读原则**

所有诊断工具只做读取操作，不注册任何写操作：

| 插件 | 安全工具（只读） | 危险操作（不注册） |
|------|-----------------|-------------------|
| redis | INFO, SLOWLOG GET, CLIENT LIST, CONFIG GET | DEL, FLUSHDB, CONFIG SET, SHUTDOWN |
| mysql | SHOW STATUS/PROCESSLIST, EXPLAIN | DROP, ALTER, UPDATE, DELETE |
| disk | iostat, df, /proc/diskstats | mkfs, fdisk, dd |
| cpu | /proc/stat, top -bn1 | kill, renice |
| os | dmesg, uptime, uname | shutdown, reboot |

**2. 参数校验**

`call_tool()` 执行前校验参数符合工具定义的 schema，拒绝未声明的参数。

**3. 敏感信息过滤**

工具返回值中过滤敏感字段（如 Redis 的 requirepass、masterauth 等）。

**4. 输出截断**

所有工具返回值有大小上限（32KB），防止大量数据发送给 AI API。

---

## 第二层：传输与认证加固

### 2.1 Per-Agent Key（替代共享 Token）

**现状**：所有 Agent 使用同一个 `agent_token`，一个泄露全部受影响。

**目标**：每个 Agent 拥有独立密钥，支持单独吊销。

```
Agent 首次启动:
  1. Agent 生成 Ed25519 密钥对，存储在 state.d/agent_key
  2. Agent 发送公钥 + 临时注册 token 到 Server
  3. Server 管理员审核后接受（类似 salt-key -a）
  4. 后续通信使用 Agent 私钥签名，Server 用已接受的公钥验证

吊销:
  - Server 删除某 Agent 的公钥 → 该 Agent 立即失去连接能力
  - 不影响其他 Agent
```

### 2.2 mTLS（双向 TLS 认证）

**目标**：Agent 和 Server 互相验证身份，防止中间人攻击。

```toml
[server]
# Agent 证书（由组织 CA 签发）
tls_cert = "/etc/catpaw/agent.crt"
tls_key  = "/etc/catpaw/agent.key"
# Server CA 证书（用于验证 Server 身份）
ca_file  = "/etc/catpaw/ca.crt"
# 生产环境必须关闭
tls_skip_verify = false
```

证书管理方案选项:
- **自建 CA**：Server 内置简易 CA，Agent 首次连接时签发证书（类似 Puppet）
- **外部 CA**：使用组织已有的 PKI 基础设施
- **短期证书**：证书有效期短（如 24h），自动轮转，降低泄露影响

### 2.3 Session 签名

**目标**：Agent 验证 session_start 确实来自合法 Server，而非重放攻击。

```json
{
  "type": "session_start",
  "payload": {
    "session_id": "sess_xyz",
    "session_type": "chat",
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

### 2.4 Token 轮转

**目标**：即使 Token 泄露，影响窗口有限。

- Server 定期生成新 Token，通过已认证连接下发给 Agent
- Agent 平滑切换到新 Token
- 旧 Token 在宽限期后失效

---

## 第三层：纵深防御

### 3.1 低权限运行

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

### 3.2 工具调用审计日志

所有远程工具调用写入本地审计日志：

```go
type ToolAuditEntry struct {
    Timestamp   time.Time         `json:"timestamp"`
    SessionID   string            `json:"session_id"`
    SessionType string            `json:"session_type"` // inspect/diagnose/chat
    UserName    string            `json:"user_name"`    // 来自 session_start
    ToolName    string            `json:"tool_name"`
    Args        map[string]string `json:"args"`
    DurationMs  int64             `json:"duration_ms"`
    Error       string            `json:"error,omitempty"`
    SourceIP    string            `json:"source_ip"`    // Server 地址
}
```

审计日志:
- 写入 `state.d/tool_audit.log`，append-only，JSONL 格式
- Agent 侧记录，Server 无法篡改或删除
- 包含完整上下文：谁（user_name）、从哪（source_ip）、调了什么工具（tool_name + args）

### 3.3 Server 侧 ACL

Server 维护权限矩阵，控制用户对 Agent 的操作范围：

```
用户 × Agent × 操作类型（inspect / diagnose / chat）
```

例如：
- 运维人员 A 可以对所有 Agent 发起 inspect 和 diagnose
- 开发人员 B 只能对其负责的服务 Agent 发起 chat
- 只读审计员 C 只能查看诊断报告，不能发起任何 session

### 3.4 异常检测

监控异常的工具调用模式：
- 短时间内大量工具调用（可能的自动化攻击）
- 同一用户对大量 Agent 批量发起 session（可能的横向探测）
- 非工作时间的异常操作

---

## 实施优先级

### P0：安全底线

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| 远程禁用 shell | 远程 session 不注册 `exec_shell`，从代码层面杜绝 | **低** |
| 工具调用审计日志 | 所有远程工具调用写本地 JSONL 日志 | 低 |

### P1：短期加固

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| Per-Agent Key | 替代共享 Token，每 Agent 独立密钥 | 中 |
| Server 侧 ACL | 用户 × Agent × 操作类型的权限矩阵 | 中 |
| Session 签名 | 防重放攻击 | 低 |

### P2：中期完善

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| mTLS | Agent 和 Server 双向证书认证 | 高 |
| 短期证书 + 自动轮转 | 降低证书泄露影响 | 高 |
| 审计日志转发 | Agent 审计日志定期上报 Server 归档 | 低 |

### P3：长期演进

| 任务 | 说明 | 复杂度 |
|------|------|--------|
| 低权限运行 + Capabilities | 专用用户 + 最小 Linux capabilities | 中 |
| 异常检测 | 异常工具调用模式触发告警 | 中 |
| AI 行为监控 | 检测 AI prompt 注入尝试 | 高 |

---

## 设计原则总结

1. **诊断不是管理**：远程只暴露只读诊断工具，不提供 shell。这是最根本的安全决策
2. **结构化优于 raw shell**：70+ 诊断工具是 catpaw 的"资源类型"，参数受限、输出可控、行为可预测
3. **缺工具就加工具**：遇到诊断盲区，正确的做法是补一个结构化工具，而非开放 shell 口子
4. **本地保留 shell**：`catpaw chat` 本地使用仍有 shell 能力（有人确认），满足深度排障需求
5. **纵深防御**：认证、ACL、审计多层叠加，单层失效不致全面沦陷
6. **可审计**：所有远程操作留痕，Agent 侧记录不可被 Server 篡改
7. **优雅降级**：安全机制故障时倾向于拒绝（fail-close），而非放行
