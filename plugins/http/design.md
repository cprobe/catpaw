# http 插件设计

## 概述

检查 HTTP/HTTPS 端点的可达性、响应时间、证书有效期、状态码和响应体内容。

**核心场景**：

1. **Web 服务不可达**：应用挂了、网络中断、DNS 故障
2. **HTTPS 证书即将过期**：证书到期后浏览器报错，用户无法访问
3. **应用异常但端口仍在**：进程存活但返回 500、响应体不包含预期内容
4. **响应变慢**：后端性能退化，用户体验下降

**参考**：Nagios `check_http`、Blackbox Exporter。

## 检查维度

| 维度 | check label | target | 说明 |
| --- | --- | --- | --- |
| 连通性 | `http::connectivity` | URL | HTTP 请求能否成功完成 |
| 响应时间 | `http::response_time` | URL | 响应耗时是否超过阈值 |
| 证书有效期 | `http::cert_expiry` | URL | HTTPS 证书距过期的天数 |
| 状态码 | `http::status_code` | URL | 响应状态码是否符合预期 |
| 响应体 | `http::response_body` | URL | 响应体是否包含预期内容 |

- **每个 target URL 独立产出事件**
- 支持并发检查（`concurrency`，默认 10）
- 支持 Partials 配置复用（多个 instance 共享 HTTP 参数）

## 结构体设计

```go
type Instance struct {
    config.InternalConfig
    Targets      []string          // 目标 URL 列表
    Concurrency  int               // 并发数，默认 10
    Connectivity ConnectivityCheck // 连通性检查，默认 severity = Critical
    ResponseTime ResponseTimeCheck // 响应时间阈值
    CertExpiry   CertExpiryCheck   // 证书到期提前告警
    StatusCode   StatusCodeCheck   // 期望状态码（支持 glob，如 "2*"）
    ResponseBody ResponseBodyCheck // 期望响应体内容（substring 或 regex）
    config.HTTPConfig              // HTTP 客户端参数（method/proxy/headers/TLS/auth 等）
}
```

## Init() 校验

1. target URL 必须是 `http://` 或 `https://` 开头
2. `response_time` 和 `cert_expiry` 阈值需满足 warn < critical
3. `status_code.expect` 编译为 filter 模式（支持 glob，如 `["2*", "301"]`）
4. `response_body` 的 `expect_substring` 和 `expect_regex` 互斥
5. `headers` 必须是偶数个元素（key-value 对）
6. HTTPS target 自动启用 TLS

## Gather() 逻辑

对每个 target 并发执行：

1. 构建 HTTP 请求（method/headers/auth/payload）
2. **connectivity 检查**：发起请求，失败 → severity 告警
3. **response_time 检查**：响应耗时比对阈值
4. **cert_expiry 检查**：仅 HTTPS，提取最早到期证书的过期时间
5. **status_code 检查**：响应码是否匹配期望模式
6. **response_body 检查**：响应体是否包含期望子串/正则

### 安全防护

- 响应体最多读取 1MB（`maxBodyReadSize`）
- 告警描述中响应体最多展示 1KB
- panic recovery 保护每个 goroutine

## 高级功能

- **Partials**：多个 instance 共享 HTTP 配置（proxy、timeout、headers 等），避免重复
- **自定义 HTTP 客户端**：支持 proxy、源接口（`interface`）、自定义 CA、跳过 TLS 验证、禁止重定向
- **BasicAuth**：支持用户名/密码认证
- **自定义 Method + Payload**：可用于 POST 健康检查端点

## 跨平台兼容性

| 平台 | 支持 |
| --- | --- |
| Linux | 完整支持 |
| macOS | 完整支持 |
| Windows | 完整支持 |
