# secmod 插件设计

## 概述

检查 Linux 安全模块（SELinux / AppArmor）的运行状态是否符合用户配置的期望基线。当实际状态偏离期望时产出告警事件。

**核心原则：插件不带立场。** 不同环境对安全模块有不同策略——有的组织要求 SELinux 必须 enforcing（等保合规），有的环境故意 disabled（兼容性原因）。插件只回答"当前状态是否与你期望的一致"，不判断期望本身对错。

最典型的场景：运维人员临时 `setenforce 0` 排障后忘记恢复，导致安全策略长期失效。这类"临时操作变永久"的配置漂移，正是 catpaw 擅长捕获的。

**定位**：安全基线一致性检查。与 sysctl 插件互补——sysctl 检查内核参数基线，secmod 检查安全模块基线。

**参考**：Nagios `check_selinux`、Sensu `check-selinux`、CIS Benchmark SELinux/AppArmor 检查项。

## 检查维度

| 维度 | check label | 说明 |
| --- | --- | --- |
| SELinux 模式 | `secmod::selinux_mode` | 实际 SELinux 模式是否与期望一致 |
| AppArmor 状态 | `secmod::apparmor_enabled` | AppArmor 是否处于期望的启用/禁用状态 |

两个维度独立配置、独立产出事件。用户可以只配一个、也可以两个都配、也可以两个都不配（expect 留空则跳过该检查）。

- **target label** 固定为 `"selinux"` / `"apparmor"`
- **默认 title_rule** 为 `"[check] [target]"`

## 数据来源

### SELinux

**读取优先级**（逐级兜底）：

1. `/sys/fs/selinux/enforce` — SELinux 虚拟文件系统
   - 文件存在：内容为 `1`（enforcing）或 `0`（permissive）
   - 文件不存在：SELinux 未启用或未编译进内核 → 状态为 `disabled`
2. `/etc/selinux/config` 中的 `SELINUX=` 行 — 仅作为参考，不直接使用
   - 该文件反映的是**开机配置**而非**运行时状态**，`setenforce 0` 不会改这个文件

**不用 `getenforce` 命令**：
- 与项目的其他插件一致（conntrack、filefd、sysctl 都读文件而非执行命令）
- 不依赖 `policycoreutils` 包是否安装
- 读 `/sys/fs/selinux/enforce` 是纯内存操作

**状态映射**：

| 文件状态 | 映射值 |
| --- | --- |
| `/sys/fs/selinux/enforce` 存在，内容为 `1` | `enforcing` |
| `/sys/fs/selinux/enforce` 存在，内容为 `0` | `permissive` |
| `/sys/fs/selinux/enforce` 不存在 | `disabled` |

### AppArmor

**读取**：`/sys/module/apparmor/parameters/enabled`

| 文件状态 | 映射值 |
| --- | --- |
| 文件存在，内容为 `Y` | `yes` |
| 文件存在，内容为 `N` | `no` |
| 文件不存在 | `no`（模块未加载） |

## 结构体设计

```go
type EnforceModeCheck struct {
    Expect    string `toml:"expect"`
    Severity  string `toml:"severity"`
    TitleRule string `toml:"title_rule"`
}

type AppArmorCheck struct {
    Expect    string `toml:"expect"`
    Severity  string `toml:"severity"`
    TitleRule string `toml:"title_rule"`
}

type Instance struct {
    config.InternalConfig

    EnforceMode EnforceModeCheck `toml:"enforce_mode"`
    AppArmor    AppArmorCheck    `toml:"apparmor_enabled"`
}

type SecmodPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

不需要：
- `Timeout` — 读 `/sys/` 是纯内存操作，不会 hang
- `inFlight` — 同理
- `Concurrency` — 最多两项检查，串行即可

## _attr_ 标签

### enforce_mode

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_actual` | `permissive` | 实际 SELinux 模式 |
| `_attr_expect` | `enforcing` | 期望的模式 |

### apparmor_enabled

| 标签 | 示例值 | 说明 |
| --- | --- | --- |
| `_attr_actual` | `no` | AppArmor 实际状态 |
| `_attr_expect` | `yes` | 期望的状态 |

## Init() 校验

```
Init():
    1. if runtime.GOOS != "linux":
           return error: "secmod plugin only supports linux"

    2. enforceModeEnabled = enforce_mode.expect != ""
       apparmorEnabled = apparmor_enabled.expect != ""

    3. if enforceModeEnabled:
       a. normalize expect to lowercase
       b. if expect not in ["enforcing", "permissive", "disabled"]:
              return error
       c. normalize severity: default "Warning"
       d. validate severity: must be Info/Warning/Critical

    4. if apparmorEnabled:
       a. normalize expect to lowercase
       b. if expect not in ["yes", "no"]:
              return error
       c. normalize severity: default "Warning"
       d. validate severity: must be Info/Warning/Critical
```

**expect 为空 → 跳过该检查**。这是核心设计：用户不配 expect 就表示"我不关心这个维度"，插件不产出任何事件。

## Gather() 逻辑

```
Gather(q):
    if enforce_mode.expect != "":
        checkEnforceMode(q)

    if apparmor_enabled.expect != "":
        checkAppArmor(q)


checkEnforceMode(q):
    actual, err = readSELinuxMode()
    if err:
        push Critical target="selinux"
            "failed to read SELinux status: <err>"
        return

    event = buildEvent("selinux", enforce_mode)
    event._attr_actual = actual
    event._attr_expect = enforce_mode.expect

    if actual == enforce_mode.expect:
        event.Ok "SELinux mode is enforcing, matches expectation"
    else:
        event.severity "SELinux mode is permissive, expected enforcing"

    q.PushFront(event)


readSELinuxMode() (string, error):
    data, err = os.ReadFile("/sys/fs/selinux/enforce")
    if errors.Is(err, os.ErrNotExist):
        return "disabled", nil
    if err:
        return "", err
    switch TrimSpace(data):
        "1": return "enforcing", nil
        "0": return "permissive", nil
        default: return "", fmt.Errorf("unexpected value %q", data)


checkAppArmor(q):
    actual, err = readAppArmorStatus()
    if err:
        push Critical target="apparmor"
            "failed to read AppArmor status: <err>"
        return

    event = buildEvent("apparmor", apparmor)
    event._attr_actual = actual
    event._attr_expect = apparmor.expect

    if actual == apparmor.expect:
        event.Ok "AppArmor is yes, matches expectation"
    else:
        event.severity "AppArmor is no, expected yes"

    q.PushFront(event)


readAppArmorStatus() (string, error):
    data, err = os.ReadFile("/sys/module/apparmor/parameters/enabled")
    if errors.Is(err, os.ErrNotExist):
        return "no", nil
    if err:
        return "", err
    switch TrimSpace(data):
        "Y": return "yes", nil
        "N": return "no", nil
        default: return "", fmt.Errorf("unexpected value %q", data)
```

### 关键行为

1. **expect 为空 → 静默跳过**——不产出事件，不计为错误。
2. **两个维度独立**——SELinux 读取失败不影响 AppArmor 检查（原则 13）。
3. **模块不存在 ≠ 读取失败**——`/sys/fs/selinux/enforce` 不存在是合法状态（`disabled`），不是错误。
4. **文件内容异常才报 Critical**——如 `/sys/fs/selinux/enforce` 内容既不是 `0` 也不是 `1`。

## Description 示例

SELinux：
- 匹配：`SELinux mode is enforcing, matches expectation`
- 不匹配：`SELinux mode is permissive, expected enforcing`
- 禁用不匹配：`SELinux mode is disabled, expected enforcing`
- 读取失败：`failed to read SELinux status: permission denied`

AppArmor：
- 匹配：`AppArmor is yes, matches expectation`
- 不匹配：`AppArmor is no, expected yes`

## 默认配置建议

| 决策 | 值 | 理由 |
| --- | --- | --- |
| enforce_mode.expect | `""` (空) | 不预设立场，用户显式配置才启用 |
| apparmor_enabled.expect | `""` (空) | 同上 |
| severity | `"Warning"` | 安全模块状态偏离是隐患但通常不立即导致故障 |
| interval | `"60s"` | 安全模块状态极少变化 |
| for_duration | `0` | 状态变化是确定性的 |
| repeat_interval | `"30m"` | 长期问题，适度提醒 |
| repeat_number | `3` | 防止噪音 |

## 与其他插件的关系

| 场景 | 推荐插件 | 说明 |
| --- | --- | --- |
| 安全模块状态偏离 | **secmod** | SELinux/AppArmor 基线检查 |
| 内核参数被重置 | sysctl | 另一类配置漂移 |
| 服务状态异常 | systemd | 服务级检查 |

三者组合可以覆盖"系统配置基线"的大部分场景。

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 读取 `/sys/fs/selinux/` 和 `/sys/module/apparmor/` |
| macOS | 不支持 | Init 返回错误 |
| Windows | 不支持 | Init 返回错误 |

## 文件结构

```
plugins/secmod/
    design.md          # 本文档
    secmod.go          # 主逻辑（仅 Linux）
    secmod_test.go     # 测试

conf.d/p.secmod/
    secmod.toml        # 默认配置
```

通过 `runtime.GOOS` 在 Init 中限制为 Linux，无需 build tags。

## 默认配置文件示例

```toml
[[instances]]
## ===== 安全模块基线检查 =====
## 检查 SELinux / AppArmor 的运行状态是否符合期望
## expect 留空表示不检查该维度（插件不带立场，由用户决定期望状态）
## severity 支持 Info / Warning / Critical，默认 Warning

interval = "60s"

## SELinux 模式检查
## expect 可选值：enforcing / permissive / disabled
## 留空则不检查 SELinux
[instances.enforce_mode]
# expect = "enforcing"
# severity = "Warning"
# title_rule = "[check] [target]"

## AppArmor 状态检查
## expect 可选值：yes / no
## 留空则不检查 AppArmor
[instances.apparmor_enabled]
# expect = "yes"
# severity = "Warning"
# title_rule = "[check] [target]"

[instances.alerting]
for_duration = 0
repeat_interval = "30m"
repeat_number = 3
# disabled = false
# disable_recovery_notification = false
```
