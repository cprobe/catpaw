# net 插件设计

## 概述

检查 TCP/UDP 端口的连通性和响应时间。支持发送自定义内容并验证响应，适用于非 HTTP 协议的网络服务检查。

**核心场景**：

1. **TCP 端口不可达**：服务进程挂了、防火墙规则变更、网络分区
2. **服务响应异常**：端口可连但服务内部故障，返回内容不符合预期（如 Redis `PONG` 检查）
3. **响应变慢**：网络拥塞或服务负载过高

**与 http/ping 插件的关系**：ping 检查 ICMP 层可达性，http 检查 HTTP 协议层，net 检查 TCP/UDP 传输层。三者从不同层面覆盖网络连通性。

**参考**：Nagios `check_tcp` / `check_udp`。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 连通性 | `net::connectivity` | host:port | TCP/UDP 连接能否建立并通过 send/expect 验证 |
| 响应时间 | `net::response_time` | host:port | 从连接到收到预期响应的总耗时 |

- **每个 target 独立事件**
- 支持并发检查（`concurrency`，默认 10）
- 支持 Partials 配置复用

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Targets      []string        // 目标地址列表（host:port）
    Concurrency  int             // 并发数，默认 10
    Timeout      config.Duration // 连接超时，默认 1s
    ReadTimeout  config.Duration // 读超时，默认 1s
    Protocol     string          // "tcp"（默认）或 "udp"
    Send         string          // 可选：连接后发送的内容
    Expect       string          // 可选：期望响应包含的内容
    Connectivity ConnectivityCheck // 连通性检查，默认 severity = Critical
    ResponseTime ResponseTimeCheck // 响应时间阈值
}
```

## Init() 校验

1. `protocol` 必须是 `tcp` 或 `udp`
2. UDP 协议必须同时配置 `send` 和 `expect`（UDP 无连接，不发数据无法判断服务是否存活）
3. targets 必须是 `host:port` 格式，host 为空时自动补 `localhost`
4. `response_time` 阈值：warn < critical

## Gather() 逻辑

### TCP

1. `net.DialTimeout` 建立连接
2. 如配了 `send`：写入内容
3. 如配了 `expect`：读取响应（上限 64KB），验证是否包含期望字符串
4. 连接成功 → emit Ok；失败/不匹配 → emit severity
5. 如配了 response_time 阈值：检查总耗时

### UDP

1. `net.DialUDP` + 发送 `send` 内容
2. 带 `read_timeout` 读取响应
3. 验证响应是否包含 `expect`

## 跨平台兼容性

| 平台 | 支持 |
| --- | --- |
| Linux | 完整支持 |
| macOS | 完整支持 |
| Windows | 完整支持 |
