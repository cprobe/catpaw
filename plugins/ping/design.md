# ping 插件设计

## 概述

ICMP ping 检查目标主机的可达性、丢包率和往返延迟（RTT）。

**核心场景**：

1. **主机不可达**：服务器宕机、网络中断、路由黑洞
2. **网络质量差**：丢包率高、延迟大、抖动严重
3. **跨地域连通性**：监控多个数据中心之间的网络可达性

**与 net/http 插件的关系**：ping 工作在 ICMP 层（L3），验证"主机是否活着"；net 工作在 TCP/UDP 层（L4），验证"端口是否在监听"；http 工作在应用层（L7），验证"服务是否正常响应"。

**参考**：Nagios `check_ping`、Prometheus Blackbox Exporter ICMP probe。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 连通性 | `ping::connectivity` | 目标地址 | ICMP 能否到达目标 |
| 丢包率 | `ping::packet_loss` | 目标地址 | 丢包率是否超过阈值 |
| 往返延迟 | `ping::rtt` | 目标地址 | 平均 RTT 是否超过阈值 |

- **每个 target 独立事件**
- 支持并发检查（`concurrency`，默认 10）
- 支持 Partials 配置复用

## 实现方式

使用 `github.com/prometheus-community/pro-bing` 库发送 ICMP 包，需要 `CAP_NET_RAW` 权限（或 root）。

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Targets      []string        // 目标地址（IP 或域名）
    Concurrency  int             // 并发数，默认 10
    Count        int             // 每次检查发送的包数，默认 5
    PingInterval config.Duration // 包间间隔，最小 200ms
    Timeout      config.Duration // 超时，默认 3s（自动调整为 >= count × interval）
    Interface    string          // 指定发包网卡（IP 或接口名）
    IPv6         *bool           // 是否使用 IPv6
    Size         *int            // ICMP payload 大小，默认 56 字节
    Connectivity ConnectivityCheck // 连通性检查，默认 Critical
    PacketLoss   PacketLossCheck   // 丢包率阈值
    Rtt          RttCheck          // RTT 阈值
}
```

## Init() 校验

1. `count` 默认 5，`ping_interval` 最小 200ms
2. `timeout` 自动调整为 >= `count × ping_interval`（防止还没发完就超时）
3. `interface` 支持 IP 地址或网卡名，网卡名会解析为对应 IP（IPv4/IPv6 自动选择）
4. 阈值校验：warn < critical

## Gather() 逻辑

1. 并发对每个 target 发送 `count` 个 ICMP 包
2. **connectivity 检查**：收到 0 包 → severity 告警；ping 执行异常 → severity 告警
3. **packet_loss 检查**（如配置）：丢包率比对阈值
4. **rtt 检查**（如配置）：平均 RTT 比对阈值

### 权限要求

Linux 需要 `CAP_NET_RAW`：

```bash
sudo setcap cap_net_raw=+ep /path/to/catpaw
```

## 跨平台兼容性

| 平台 | 支持 | 说明 |
| --- | --- | --- |
| Linux | 完整支持 | 需要 CAP_NET_RAW |
| macOS | 完整支持 | 需要 root |
| Windows | 完整支持 | 需要管理员权限 |
