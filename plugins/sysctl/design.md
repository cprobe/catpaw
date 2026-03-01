# sysctl 插件设计

## 概述

检查 Linux 内核参数（sysctl）的实际值是否符合用户定义的期望基线。当参数值偏离预期时产出告警事件。

内核参数漂移是一类极其隐蔽的生产隐患。运维人员调优了 `net.core.somaxconn`、`vm.swappiness`、`net.ipv4.ip_forward` 等参数，但服务器重启、内核升级、配置文件路径错误（`/etc/sysctl.conf` vs `/etc/sysctl.d/`）等原因都可能导致参数被静默重置为默认值。问题在于：参数变了不会立即报错，往往在流量高峰或特定场景下才暴露——而此时排查方向很难第一时间指向"内核参数变了"。

**定位**：配置漂移检测。不是采集指标趋势，而是回答"我的内核调优还在吗？"这个问题。与 conntrack、sockstat 等插件互补——那些插件从症状发现问题（表满、队列溢出），sysctl 从源头预防问题（参数是否到位）。

**参考**：Nagios `check_sysctl`、Sensu `check-sysctl`、Chef/Ansible sysctl 断言。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| 参数基线比对 | `sysctl::param_check` | 逐个参数检查，不匹配的参数产出告警 |

- **target label** 为被检查的参数 key（如 `"net.core.somaxconn"`）
- **默认 title_rule** 为 `"[check] [target]"`
- **每个参数独立产出事件**——参数之间互不干扰，一个参数读取失败不影响其他参数的检查（原则 13）

### 为什么每个参数独立一个事件

考虑过两种方案：

| 方案 | 优点 | 缺点 |
| --- | --- | --- |
| 所有参数合并一个事件 | 事件数少 | 一个参数恢复无法单独 Ok；Description 要塞所有不匹配的参数，冗长 |
| **每个参数一个事件** | 独立恢复；AlertKey 天然区分；Description 精准 | 配置 10 个参数就可能有 10 个事件 |

选择**每个参数独立事件**：sysctl 检查的参数数量通常在 5~20 个，事件数可控；且每个参数的修复和恢复是独立的，合并反而模糊了告警语义。

## 数据来源

### `/proc/sys/` 虚拟文件系统

sysctl key 直接映射到 `/proc/sys/` 路径：

```
net.core.somaxconn  →  /proc/sys/net/core/somaxconn
vm.swappiness       →  /proc/sys/vm/swappiness
net.ipv4.ip_forward →  /proc/sys/net/ipv4/ip_forward
```

转换规则：将 `.` 替换为 `/`，前缀 `/proc/sys/`。

读取方式：`os.ReadFile` + `strings.TrimSpace`，获取的是字符串值。

**为什么不用 `sysctl` 命令**：
- `/proc/sys/` 读取是纯内存操作，无进程创建开销
- 不依赖 `procps` 包是否安装
- 与项目中 conntrack、filefd 等插件的实现方式一致

### 值的类型处理

`/proc/sys/` 下的值都是文本。大多数是整数（如 `65535`），但也有字符串型的（如 `net.ipv4.tcp_congestion_control` 的值是 `cubic`）。

插件统一以**字符串**读取，在比较时：
- 如果 op 是 `eq` 或 `ne`：字符串比较
- 如果 op 是 `ge`、`le`、`gt`、`lt`：尝试将两侧都解析为数值（`strconv.ParseFloat`），解析失败则报错

## 比较操作

| op | 含义 | 说明 |
| --- | --- | --- |
| `eq` | 等于 | 字符串精确比较，最常用 |
| `ne` | 不等于 | 确保参数不是某个危险值 |
| `ge` | 大于等于 | 数值比较，如 somaxconn >= 65535 |
| `le` | 小于等于 | 数值比较，如 swappiness <= 10 |
| `gt` | 大于 | 数值比较 |
| `lt` | 小于 | 数值比较 |

默认 op 为 `eq`（不配则等于精确匹配）。

## 告警级别

每个参数条目可单独配置 `severity`，默认 `Warning`。

```toml
params = [
  { key = "net.ipv4.ip_forward", expect = "1", severity = "Critical" },
  { key = "vm.swappiness", expect = "10", op = "le" },
]
```

设计考量：
- 默认 `Warning` 而非 `Critical`——参数偏离通常不是紧急故障，而是潜在隐患（原则 6）
- 允许 per-param 配置 severity——`ip_forward` 被关了可能立刻断网（Critical），`swappiness` 偏高只是性能影响（Warning）

## 结构体设计

```go
type ParamSpec struct {
    Key      string `toml:"key"`
    Expect   string `toml:"expect"`
    Op       string `toml:"op"`
    Severity string `toml:"severity"`
}

type ParamCheck struct {
    Params    []ParamSpec `toml:"params"`
    TitleRule string      `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    ParamCheck ParamCheck `toml:"param_check"`
}

type SysctlPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

不需要：
- `Timeout` — 读 `/proc/sys/` 是纯内存操作，不会 hang
- `inFlight` — 同理，不涉及阻塞操作（原则 9：适用于可能 hang 的场景）
- `Concurrency` — 参数数量有限（通常 < 50），串行遍历即可

## _attr_ 标签

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_actual` | `128` | 参数的实际值 |
| `_attr_expect` | `65535` | 配置的期望值 |
| `_attr_op` | `ge` | 比较操作 |

Ok 事件也携带完整 `_attr_`，便于巡检确认参数值。

## Init() 校验

```
Init():
    1. if runtime.GOOS != "linux":
           return error: "sysctl plugin only supports linux"

    2. if len(params) == 0:
           return error: "param_check.params must not be empty"

    3. for each param:
       a. if key is empty:
              return error: "param key must not be empty"

       b. if expect is empty:
              return error: "param '%s' expect value must not be empty"

       c. normalize op:
          - if op is "": set op = "eq"
          - if op not in ["eq", "ne", "ge", "le", "gt", "lt"]:
                return error

       d. if op in ["ge", "le", "gt", "lt"]:
          - try ParseFloat(expect) — if fails:
                return error: "param '%s' expect value '%s' must be numeric for op '%s'"

       e. normalize severity:
          - if severity is "": set severity = "Warning"
          - if not valid severity:
                return error

       f. validate key format:
          - must not contain "/" or ".." (防止路径穿越)
          - must not start with "."
```

### key 校验的安全考量

用户配置的 `key` 会被转换为文件路径（`/proc/sys/<key with . → />`）。必须防止路径穿越：
- 拒绝包含 `/` 的 key（合法 sysctl key 用 `.` 分隔）
- 拒绝包含 `..` 的 key
- 拒绝 `.` 开头的 key

## Gather() 逻辑

```
Gather(q):
    for each param in params:
        path = "/proc/sys/" + strings.ReplaceAll(param.key, ".", "/")
        actual, err = readFile(path)

        if err:
            if os.IsNotExist(err):
                // 参数不存在，可能是模块未加载或内核版本不支持
                push param.severity target=param.key
                    "parameter not found: /proc/sys path does not exist"
            else:
                push Critical target=param.key
                    "failed to read parameter: <err>"
            continue

        actual = strings.TrimSpace(actual)
        match, err = compare(actual, param.expect, param.op)

        if err:
            push Critical target=param.key
                "comparison error: <err>"
            continue

        event = buildEvent(param)
        if match:
            event.SetEventStatus(Ok)
            event.SetDescription("net.core.somaxconn = 65535, matches expectation (ge 65535)")
        else:
            event.SetEventStatus(param.severity)
            event.SetDescription("net.core.somaxconn = 128, expected ge 65535")

        q.PushFront(event)
```

### compare 函数

```
compare(actual, expect, op) (bool, error):
    switch op:
        "eq": return actual == expect, nil
        "ne": return actual != expect, nil
        "ge", "le", "gt", "lt":
            actualNum, err1 = ParseFloat(actual)
            expectNum, err2 = ParseFloat(expect)
            if err1: return false, "actual value '%s' is not numeric"
            if err2: return false, "expect value '%s' is not numeric"
            // expect 已在 Init 校验过，但运行时仍做防御
            switch op:
                "ge": return actualNum >= expectNum, nil
                "le": return actualNum <= expectNum, nil
                "gt": return actualNum > expectNum, nil
                "lt": return actualNum < expectNum, nil
```

### 关键行为

1. **每个参数独立产出事件**——一个参数失败不影响其他参数（原则 13）。
2. **参数不存在不是静默跳过**——用户既然配了，就期望它存在。不存在说明模块未加载或 key 拼错了，应该告知用户（原则 7）。
3. **参数不存在时使用 param 自身的 severity 而非硬编码 Critical**——因为参数不存在的严重程度取决于参数本身的重要性。
4. **读取失败（非 NotExist）使用 Critical**——权限不足等系统级问题，需要关注。
5. **数值比较时实际值不是数字也报错**——如果 `/proc/sys/` 文件内容格式异常，这本身就是问题。

## Description 示例

- 参数匹配：`net.core.somaxconn = 65535, matches expectation (ge 65535)`
- 参数不匹配：`net.core.somaxconn = 128, expected ge 65535`
- 字符串不匹配：`net.ipv4.tcp_congestion_control = reno, expected eq cubic`
- 参数不存在：`parameter not found: /proc/sys path does not exist`
- 读取失败：`failed to read parameter: permission denied`

## 默认配置建议

| 决策 | 值 | 理由 |
| --- | --- | --- |
| interval | `"60s"` | 内核参数极少变化，60s 足够 |
| severity | `"Warning"` | 参数偏离通常是隐患而非紧急故障 |
| op | `"eq"` | 最常用的精确匹配 |
| for_duration | `0` | 参数变化是确定性的，不需要持续确认 |
| repeat_interval | `"30m"` | 基线偏离是长期问题，不需要频繁提醒 |
| repeat_number | `3` | 适度提醒后停止，防止噪音 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 某个内核参数被重置 | **sysctl** | 源头检测，参数级别的基线比对 |
| 连接跟踪表满 | conntrack | 症状检测，sysctl 可预防（检查 nf_conntrack_max） |
| listen 队列溢出 | sockstat | 症状检测，sysctl 可预防（检查 somaxconn） |
| 文件描述符不足 | filefd / procfd | 症状检测，sysctl 可预防（检查 fs.file-max） |

**推荐组合**：sysctl 作为"治未病"的基线守卫，配合 conntrack、sockstat 等"治已病"的症状检测。

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/proc/sys/` |
| macOS | 不支持 | Init 返回错误。macOS 有 `sysctl` 命令但路径机制不同 |
| Windows | 不支持 | Init 返回错误。Windows 无 sysctl 概念 |

## 文件结构

```
plugins/sysctl/
    design.md        # 本文档
    sysctl.go        # 主逻辑（仅 Linux）
    sysctl_test.go   # 测试（Init 校验、compare 函数、key 路径转换）

conf.d/p.sysctl/
    sysctl.toml      # 默认配置
```

通过 `runtime.GOOS` 在 Init 中限制为 Linux，无需 build tags。

## 默认配置文件示例

```toml
[[instances]]
## ===== 最小可用示例（60 秒跑起来）=====
## 检查内核参数是否符合期望基线
## 防止重启、升级后调优参数被静默重置
## 比较操作：eq（等于）、ne（不等于）、ge（>=）、le（<=）、gt（>）、lt（<）
## 告警级别：默认 Warning，可 per-param 配置为 Critical

interval = "60s"

[instances.param_check]
# title_rule = "[check] [target]"
params = [
  { key = "net.core.somaxconn", expect = "65535", op = "ge" },
  { key = "vm.swappiness", expect = "10", op = "le" },
  # { key = "net.ipv4.ip_forward", expect = "1", severity = "Critical" },
  # { key = "net.ipv4.tcp_tw_reuse", expect = "1" },
  # { key = "net.ipv4.tcp_max_syn_backlog", expect = "65535", op = "ge" },
  # { key = "fs.file-max", expect = "1000000", op = "ge" },
]

[instances.alerting]
for_duration = 0
repeat_interval = "30m"
repeat_number = 3
# disabled = false
# disable_recovery_notification = false
```
