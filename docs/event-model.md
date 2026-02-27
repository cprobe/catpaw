# 事件数据模型

catpaw 的核心产出是**事件（Event）**。每个插件在每次采集时，根据检查结果产出一个或多个事件，由 engine 处理后推送到 FlashDuty。

## Event 结构

```json
{
  "event_time": 1708934400,
  "event_status": "Critical",
  "alert_key": "a1b2c3d4e5f6...",
  "labels": {
    "from_plugin": "disk",
    "from_agent": "catpaw",
    "from_hostname": "web-01",
    "from_hostip": "10.0.0.1",
    "check": "disk::space_usage",
    "target": "/data",
    "_attr_used_percent": "94.2%",
    "_attr_device": "/dev/sda1"
  },
  "title_rule": "[check] [target]",
  "description": "disk usage 94.2% >= critical threshold 90%"
}
```

## 字段说明

| 字段 | 说明 |
|---|---|
| `event_time` | 事件产生的 Unix 时间戳 |
| `event_status` | 事件级别：`Critical` / `Warning` / `Info` / `Ok` |
| `alert_key` | 告警唯一标识，用于 FlashDuty 的告警去重和恢复关联 |
| `labels` | 键值对标签，承载事件的所有结构化数据 |
| `title_rule` | 标题模板，FlashDuty 用 `[label_key]` 语法从 labels 中取值渲染 |
| `description` | 纯文本描述，人类可读的事件摘要 |

## Labels 设计

Labels 分为两类：

### 身份标签（参与 AlertKey 计算）

这些标签决定了一个告警的"身份"，相同身份标签组合的事件会被 FlashDuty 归为同一条告警：

- `from_plugin` — 产出事件的插件名
- `from_agent` — 固定为 `catpaw`
- `from_hostname` — 主机名
- `from_hostip` — 主机 IP
- `check` — 检查维度，格式为 `plugin::dimension`（如 `disk::space_usage`）
- `target` — 检查对象（如挂载点 `/data`、URL、进程名等）
- `protocol` — 协议（net 插件特有）
- `method` — HTTP 方法（http 插件特有）
- 用户自定义标签（通过配置 `labels = { env = "production" }` 添加）

### 属性标签（`_attr_` 前缀，不参与 AlertKey 计算）

以 `_attr_` 为前缀的标签携带动态的度量数据和上下文信息，每次采集值可能不同：

- `_attr_used_percent` — 磁盘使用率
- `_attr_response_time` — 响应时间
- `_attr_packet_loss` — 丢包率
- `_attr_warn_threshold` / `_attr_critical_threshold` — 告警阈值
- 等等

这种设计的好处：
1. FlashDuty 可以通过 Labels 获取所有结构化数据，在不同通知渠道（短信、邮件、IM）灵活渲染
2. AlertKey 保持稳定，动态度量值的变化不会产生新的告警条目
3. Description 保持为人类可读的纯文本摘要

## AlertKey 生成规则

AlertKey 是所有**非 `_attr_` 前缀**标签的排序拼接后的 MD5 值：

```
sort labels by key → for each non-_attr_ key: "key:value:" → MD5(concatenated string)
```

相同 AlertKey 的事件被视为同一条告警。当事件状态从异常变为 `Ok` 时，触发恢复通知。

## 告警生命周期

```
[首次告警] → 缓存事件，根据 for_duration 决定是否立即发送
     ↓
[持续告警] → 根据 repeat_interval 和 repeat_number 控制重复通知
     ↓
[恢复(Ok)] → 清除缓存，发送恢复通知（除非 disable_recovery_notification=true）
```

### Alerting 配置参数

| 参数 | 说明 |
|---|---|
| `for_duration` | 持续多久才发送首次告警（默认 0，立即发送） |
| `repeat_interval` | 重复通知间隔 |
| `repeat_number` | 最大通知次数（0 = 不限制） |
| `disabled` | 是否禁用告警（只采集不告警） |
| `disable_recovery_notification` | 是否禁用恢复通知 |
