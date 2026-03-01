# dns 插件设计

## 概述

检查 DNS 域名解析是否正常、解析结果是否符合预期、响应时间是否在可接受范围内。

**核心场景**：

1. **DNS 服务故障**：域名无法解析，应用报 `no such host` 错误
2. **DNS 劫持/污染**：域名被解析到错误 IP（CDN 切换遗漏、DNS 缓存投毒）
3. **DNS 响应缓慢**：DNS 服务器过载，解析耗时过长拖慢应用请求

**参考**：Nagios `check_dns`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 解析成功性 | `dns::resolution` | 域名 | 域名能否被成功解析 |
| 解析结果 | `dns::expected_ips` | 域名 | 解析出的 IP 是否包含期望值 |
| 响应时间 | `dns::response_time` | 域名 | DNS 查询耗时是否超过阈值 |

- **每个 target 独立产出事件**——多个域名并发检查，互不影响
- 支持自定义 DNS 服务器（`servers`），默认使用系统 DNS
- 支持并发检查（`concurrency`，默认 10）

## 数据来源

使用 Go 标准库 `net.Resolver.LookupHost`。自定义 DNS 服务器时通过 `PreferGo: true` + 自定义 `Dial` 实现，轮询多个 server 地址。

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Targets      []string        // 必填：要检查的域名列表
    Servers      []string        // 可选：自定义 DNS 服务器（IP 或 IP:port）
    ExpectedIPs  []string        // 可选：期望解析到的 IP（任一命中即正常）
    Timeout      config.Duration // 默认 5s
    Concurrency  int             // 默认 10
    Resolution   ResolutionCheck // 解析失败时的 severity，默认 Critical
    ResponseTime ResponseTimeCheck // 响应时间阈值
}
```

## Init() 校验

1. `targets` 不能为空，且每个元素不能为空白
2. `resolution.severity` 默认 Critical
3. `response_time` 阈值校验：warn < critical
4. `servers` 中的地址必须是合法 IP 或 IP:port
5. `expected_ips` 中的每个 IP 必须是合法格式

## Gather() 逻辑

对每个 target 并发执行：

1. 带 timeout 的 `LookupHost` 解析域名
2. **resolution 检查**：解析失败 → severity 告警；解析成功 → Ok（附带解析出的 IP 和耗时）
3. **expected_ips 检查**（如配置）：解析出的 IP 列表中是否包含任一预期 IP
4. **response_time 检查**（如配置）：响应耗时是否超过阈值

### DNS 错误分类

代码对 `net.DNSError` 做了细粒度分类：
- `IsNotFound` → NXDOMAIN（域名不存在）
- `IsTimeout` → 查询超时
- `IsTemporary` → 临时故障

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 自定义 server 通过 Go 纯 DNS 实现 |
| macOS | 完整支持 | 自定义 server 可能受 cgo resolver 限制（会自动降级） |
| Windows | 部分支持 | 自定义 server 在 Windows 上可能不生效（Go resolver 限制） |
