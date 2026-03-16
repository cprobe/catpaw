# 插件开发指南

catpaw 采用插件化架构，新增插件只需实现几个接口并注册即可。

## 目录结构

```
plugins/
└── myplugin/
    └── myplugin.go
```

对于简单插件，这样的单文件结构已经足够；对于 remote 类、诊断能力较强、
支持多 target / partial / accessor / cluster 扩展的插件，建议参考
[`plugins/redis/`](../plugins/redis/README.md) 的拆分方式。

## 步骤

### 1. 定义 Instance 和 Plugin 结构体

```go
package myplugin

import (
    "github.com/cprobe/catpaw/config"
    "github.com/cprobe/catpaw/pkg/safe"
    "github.com/cprobe/catpaw/plugins"
    "github.com/cprobe/catpaw/types"
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
        })

        // ... 执行检查逻辑 ...

        if somethingWrong {
            event.SetEventStatus(types.EventStatusCritical)
            event.SetDescription("something went wrong")
            event.SetAttrs(map[string]string{"response_time": "150ms"})
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
_ "github.com/cprobe/catpaw/plugins/myplugin"
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

### Attrs（展示属性）

动态度量数据（如响应时间、使用率、阈值等）使用 `event.SetAttrs(map[string]string{...})` 设置，不参与 AlertKey 计算。

### EventStatus

- `types.EventStatusOk` — 正常（触发恢复）
- `types.EventStatusWarning` — 警告
- `types.EventStatusCritical` — 严重
- `types.EventStatusInfo` — 信息

### Description

纯文本，简洁描述当前状态。不要使用 Markdown。

## 多维度检查

一个 Instance 可以检查多个维度（如 disk 插件同时检查空间使用率、inode 使用率、可写性）。每个维度产出独立的 Event，通过不同的 `check` label 区分。

## Remote 插件参考实现

下面这些插件适合用来参考不同复杂度的 remote 采集模式：

- `plugins/ping/`：简单 remote target、多 target 基础模式
- `plugins/http/`：远端请求 + richer attrs 的模式
- [`plugins/redis/`](../plugins/redis/README.md)：**推荐标杆**

`plugins/redis/` 适合作为后续 MySQL、MongoDB 等 remote 类插件的模板，
因为它已经覆盖了这些常见需求：

- 多 target 并发采集
- `inFlight` 防重入和 hung 检测
- partial 配置复用
- `Init()` 集中校验与默认值
- Accessor 抽象（Gather 与 Diagnose 共用）
- 诊断工具、PreCollector、DiagnoseHints
- 单元测试、协议级 fake server 测试、integration 测试

## Partial 模式

如果多个 Instance 共享大量相同配置（如 ping、net、http），可实现 `IApplyPartials` 接口，通过 `[[partials]]` 定义模板，Instance 通过 `partial = "id"` 引用。
