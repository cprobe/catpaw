# systemd 插件设计

## 概述

检查 systemd unit 的运行状态是否符合预期。

**核心场景**：

1. **关键服务停了**：nginx、MySQL、Docker 等 service 意外停止
2. **Unit 被 mask**：管理员误操作 `systemctl mask` 导致服务无法启动
3. **Unit 不存在**：部署遗漏、包未安装

**与 procnum 插件的关系**：procnum 直接计数进程（不管它是否被 systemd 管理），systemd 插件检查 unit 的元数据（LoadState、ActiveState、SubState）。对于 systemd 管理的服务，systemd 插件能提供更丰富的状态信息（如 restart 次数、mask 状态）。

**参考**：Nagios `check_systemd`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| unit 状态 | `systemd::state` | unit 名 | ActiveState 是否等于期望值 |

- **每个 unit 独立事件**
- target 使用用户配置的 unit 名（非规范化后的名字）

## 数据来源

```bash
systemctl show <unit> --property=LoadState,ActiveState,SubState,Type,...
```

解析 `key=value` 格式的输出。查询的属性包括：
LoadState、ActiveState、SubState、Type、MainPID、NRestarts、Result、Description、FragmentPath、UnitFileState、ActiveEnterTimestamp。

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Units               []string        // 必填：要检查的 unit 名列表
    ExpectedActiveState string          // 期望的 ActiveState，默认 "active"
    Timeout             config.Duration // systemctl 超时，默认 5s
    Concurrency         int             // 并发数，默认 5
    State               StateCheck      // severity 和 title_rule
}
```

## Init() 校验

1. 仅 Linux 支持
2. `units` 不能为空，每个 unit 名不能为空白或包含空格
3. 检测 `systemctl` 是否存在
4. `expected_active_state` 默认 `"active"`
5. `state.severity` 默认 Critical

## Gather() 逻辑

对每个 unit 并发执行：

1. **规范化 unit 名**：不带已知后缀的自动补 `.service`（如 `nginx` → `nginx.service`）
2. 查询 unit 属性
3. **LoadState 判断**：
   - `not-found` → unit 不存在，告警
   - `masked` → unit 被禁用，告警
4. **ActiveState 判断**：
   - 等于预期值 → Ok
   - 不等于预期值 → severity 告警（附带 SubState、Result、最后活跃时间等信息）
5. oneshot/timer 类型 unit 在 expected_active_state = "active" 时记录 warn 日志（这类 unit 执行完会变 inactive）

### 事件 `_attr_` 标签

- `active_state`、`sub_state`、`load_state`
- `canonical_unit`（规范化后的名字）
- `type`（service 类型：simple/forking/oneshot 等）
- `main_pid`（非 0 时）
- `description`、`fragment_path`
- `active_enter_timestamp`、`n_restarts`（非 0 时）

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 依赖 systemctl |
| macOS | 不支持 | Init 返回错误 |
| Windows | 不支持 | Init 返回错误 |
