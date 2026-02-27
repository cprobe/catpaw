# 插件开发指南

catpaw 采用插件化架构，新增插件只需实现几个接口并注册即可。

## 目录结构

```
plugins/
└── myplugin/
    └── myplugin.go
```

## 步骤

### 1. 定义 Instance 和 Plugin 结构体

```go
package myplugin

import (
    "flashcat.cloud/catpaw/config"
    "flashcat.cloud/catpaw/pkg/safe"
    "flashcat.cloud/catpaw/plugins"
    "flashcat.cloud/catpaw/types"
)

const pluginName = "myplugin"

type Instance struct {
    config.InternalConfig

    // 插件特有的配置字段
    Targets []string `toml:"targets"`
    // ...
}

type MyPlugin struct {
    config.InternalConfig
    Instances []*Instance `toml:"instances"`
}
```

`config.InternalConfig` 内嵌了 `Labels`、`Interval`、`Alerting` 等通用字段，无需重复定义。

### 2. 实现必要的接口

```go
// 注册插件（在 init 中完成）
func init() {
    plugins.Add(pluginName, func() plugins.Plugin {
        return &MyPlugin{}
    })
}

// 返回所有 instance
func (p *MyPlugin) GetInstances() []plugins.Instance {
    ret := make([]plugins.Instance, len(p.Instances))
    for i := 0; i < len(p.Instances); i++ {
        ret[i] = p.Instances[i]
    }
    return ret
}

// 初始化（可选，校验配置、设置默认值）
func (ins *Instance) Init() error {
    if len(ins.Targets) == 0 {
        return nil // 没有配置则跳过
    }
    return nil
}

// 核心采集逻辑
func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
    for _, target := range ins.Targets {
        event := types.BuildEvent(map[string]string{
            "check":  "myplugin::health",
            "target": target,
        }).SetTitleRule("[check] [target]")

        // ... 执行检查逻辑 ...

        if somethingWrong {
            event.SetEventStatus(types.EventStatusCritical)
            event.SetDescription("something went wrong")
        } else {
            event.SetDescription("everything is ok")
        }

        q.PushFront(event)
    }
}
```

### 3. 在 agent.go 中注册 import

在 `agent/agent.go` 的 import 块中添加：

```go
_ "flashcat.cloud/catpaw/plugins/myplugin"
```

### 4. 创建配置文件

在 `conf.d/p.myplugin/myplugin.toml` 中创建默认配置：

```toml
[[instances]]
targets = ["example"]
interval = "30s"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 3
# disabled = false
# disable_recovery_notification = false
```

## 关键约定

### Labels

- `check` — 必须设置，格式为 `pluginName::dimension`
- `target` — 检查对象的标识
- 动态数据使用 `types.AttrPrefix` 前缀（如 `_attr_response_time`）

### EventStatus

- `types.EventStatusOk` — 正常（触发恢复）
- `types.EventStatusWarning` — 警告
- `types.EventStatusCritical` — 严重
- `types.EventStatusInfo` — 信息

### Description

纯文本，简洁描述当前状态。不要使用 Markdown。

### TitleRule

通常为 `"[check] [target]"`，FlashDuty 会从 Labels 中取值渲染为告警标题。

## 多维度检查

一个 Instance 可以检查多个维度（如 disk 插件同时检查空间使用率、inode 使用率、可写性）。每个维度产出独立的 Event，通过不同的 `check` label 区分。

## Partial 模式

如果多个 Instance 共享大量相同配置（如 ping、net、http），可实现 `IApplyPartials` 接口，通过 `[[partials]]` 定义模板，Instance 通过 `partial = "id"` 引用。
