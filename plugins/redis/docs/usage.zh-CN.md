# Redis 插件使用说明

## 概述

`redis` 插件提供 Redis 语义层的监控和诊断能力，而不只是通用 TCP 连通性检测。

它支持：

- standalone Redis
- 主从 Redis
- Redis Cluster

这个插件围绕两个原则设计：

- 默认配置尽量开箱即用
- 周期采集必须保持轻量，不能给 Redis 增加明显负担

因此，默认只开启少量低成本检查，其他阈值类检查由用户按需打开。

## 支持的检查项

### 默认检查

- `redis::connectivity`
- `redis::cluster_state`（仅在目标被识别为 Redis Cluster 节点时自动开启）
- `redis::cluster_topology`（仅在目标被识别为 Redis Cluster 节点时自动开启）

### 可选检查

- `redis::response_time`
- `redis::role`
- `redis::repl_lag`
- `redis::connected_clients`
- `redis::blocked_clients`
- `redis::used_memory`
- `redis::used_memory_pct`
- `redis::rejected_connections`
- `redis::master_link_status`
- `redis::connected_slaves`
- `redis::evicted_keys`
- `redis::expired_keys`
- `redis::instantaneous_ops_per_sec`
- `redis::persistence`

## 诊断工具

Redis 插件还会为 AI 诊断和 `inspect` 注册只读诊断工具：

- `redis_info`
- `redis_cluster_info`
- `redis_slowlog`
- `redis_client_list`
- `redis_config_get`
- `redis_latency`
- `redis_memory_analysis`
- `redis_bigkeys_scan`

其中 `redis_bigkeys_scan` 是诊断专用工具，不会进入周期采集路径。

## 快速开始

最小配置：

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"
```

这个配置会启用：

- `redis::connectivity`
- 如果目标是 Redis Cluster 节点，还会自动启用默认的 cluster 检查

测试运行：

```bash
./catpaw -test -plugins redis
```

## 基础配置字段

常用字段：

- `targets`：Redis 地址，支持 `host` 或 `host:port`
- `concurrency`：单个 instance 的并发检查数，默认 `10`
- `timeout`：建连和写超时，默认 `3s`
- `read_timeout`：读超时，默认 `2s`
- `username`：Redis ACL 用户名
- `password`：Redis 密码
- `db`：数据库编号，默认 `0`
- `mode`：`auto` / `standalone` / `cluster`，默认 `auto`
- `cluster_name`：可选的 cluster 标签，仅用于事件聚合
- `use_tls` 及相关 TLS 字段
- `interval`
- `labels`

如果 `targets` 没有显式带端口，会自动补成 `:6379`。

## Cluster 模式说明

### `mode = "auto"`

这是默认值，也是推荐值。

行为：

- 先读取 `INFO server`
- 如果 `redis_mode=cluster`，自动启用：
  - `redis::cluster_state`
  - `redis::cluster_topology`
- 如果不是 cluster，自动跳过 cluster 检查

### `mode = "standalone"`

适用于明确不是 Redis Cluster 的实例。这样可以避免连 cluster 探测都不做，开销最小。

### `mode = "cluster"`

适用于目标必须是 cluster 节点的场景。如果实际不是 cluster，catpaw 会产出明确的错误事件。

## 为什么有些检查默认关闭

下面这些检查和业务流量、拓扑策略、容量模型强相关，所以默认关闭：

- `redis::repl_lag`
- `redis::used_memory`
- `redis::used_memory_pct`
- `redis::connected_clients`
- `redis::connected_slaves`

这样可以避免默认配置在不同 Redis 场景下产生大量误报。

## 常见配置示例

### 1. standalone 或最小可用监控

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"
```

### 2. master 节点健康检查

```toml
[[instances]]
targets = ["10.0.0.10:6379"]
password = "your-password"

[instances.role]
expect = "master"
severity = "Warning"

[instances.connected_slaves]
warn_lt = 2
critical_lt = 1

[instances.persistence]
enabled = true
severity = "Critical"
```

### 3. replica 节点健康检查

```toml
[[instances]]
targets = ["10.0.0.11:6379"]
password = "your-password"

[instances.role]
expect = "slave"
severity = "Warning"

[instances.master_link_status]
expect = "up"
severity = "Warning"
```

### 4. Redis Cluster 节点默认硬故障检查

```toml
[[instances]]
targets = ["10.0.0.20:6379"]
password = "your-password"
mode = "auto"
cluster_name = "prod-cache"
```

### 5. Redis Cluster 节点开启可选阈值检查

```toml
[[instances]]
targets = ["10.0.0.20:6379"]
password = "your-password"
mode = "auto"
cluster_name = "prod-cache"

[instances.repl_lag]
warn_ge = "1MB"
critical_ge = "10MB"

[instances.used_memory_pct]
warn_ge = 80
critical_ge = 90
```

## 检查项说明

### `redis::repl_lag`

- 单位是“字节偏移差”，不是时间
- replica 视角：`master_repl_offset - slave_repl_offset`
- master 视角：所有 replica 中的最大 offset 差

### `redis::used_memory_pct`

- 只有在 `maxmemory > 0` 时才有意义
- 如果 `maxmemory = 0`，插件会输出 `Ok` 并说明该检查已跳过

### delta 型计数器

下面这些检查使用采集周期增量，而不是 Redis 生命周期累计值：

- `redis::rejected_connections`
- `redis::evicted_keys`
- `redis::expired_keys`

第一次成功采集只建立 baseline，不会直接告警。

## TLS

示例：

```toml
[[instances]]
targets = ["redis.example.com:6380"]
password = "your-password"
use_tls = true
tls_ca = "/etc/catpaw/ca.pem"
tls_server_name = "redis.example.com"
```

可用 TLS 字段：

- `use_tls`
- `tls_ca`
- `tls_cert`
- `tls_key`
- `tls_key_pwd`
- `tls_server_name`
- `insecure_skip_verify`
- `tls_min_version`
- `tls_max_version`

## Partials

可以用 `partials` 复用认证、TLS、超时和公共阈值：

```toml
[[partials]]
id = "prod"
password = "your-password"
timeout = "3s"
read_timeout = "2s"

[partials.connectivity]
severity = "Critical"

[partials.cluster_state]
severity = "Critical"

[[instances]]
targets = ["10.0.0.20:6379"]
partial = "prod"
cluster_name = "prod-cache"
```

## 相关文档

- [`../README.md`](../README.md)
- [`design.md`](./design.md)
- [`test-plan.md`](./test-plan.md)
- [`cluster-test-plan.md`](./cluster-test-plan.md)
