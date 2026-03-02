# Redis 插件使用说明

## 概述

`redis` 插件用于监控 Redis 的可用性、延迟、主从复制状态、客户端压力、内存压力、运行期计数器以及持久化健康。

支持的检查项：

- `redis::connectivity`
- `redis::response_time`
- `redis::role`
- `redis::connected_clients`
- `redis::blocked_clients`
- `redis::used_memory`
- `redis::rejected_connections`
- `redis::master_link_status`
- `redis::connected_slaves`
- `redis::evicted_keys`
- `redis::expired_keys`
- `redis::instantaneous_ops_per_sec`
- `redis::persistence`

## 快速开始

在 `conf.d/p.redis/redis.toml` 中创建配置：

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"

[instances.role]
expect = "master"

[instances.used_memory]
warn_ge = "512MB"
critical_ge = "1GB"

[instances.alerting]
for_duration = 0
repeat_interval = "5m"
repeat_number = 3
```

测试运行：

```bash
./catpaw -test -plugins redis
```

## 基础配置

每个 Redis instance 支持这些常用字段：

- `targets`：Redis 地址，支持 `host` 或 `host:port`
- `concurrency`：单个 instance 的并发检查数，默认 `10`
- `timeout`：建连和写超时，默认 `3s`
- `read_timeout`：读超时，默认 `2s`
- `username`：Redis ACL 用户名
- `password`：Redis 密码
- `db`：数据库编号，默认 `0`
- `use_tls` 及相关 TLS 字段
- `interval`：采集间隔
- `labels`：追加到事件中的自定义标签

如果 `targets` 没有显式带端口，会自动补成 `:6379`。

## 认证

仅密码认证：

```toml
[[instances]]
targets = ["redis.example.com:6379"]
password = "your-password"
```

ACL 认证：

```toml
[[instances]]
targets = ["redis.example.com:6379"]
username = "monitor"
password = "your-password"
```

选择非默认数据库：

```toml
[[instances]]
targets = ["redis.example.com:6379"]
password = "your-password"
db = 2
```

## TLS

适用于 Redis 原生 TLS 或代理场景：

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

## 常见配置示例

### 1. 最小可用监控

```toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "your-password"
```

这个配置只启用 `redis::connectivity`。

### 2. Master 节点健康检查

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

### 3. Replica 节点健康检查

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

### 4. 内存与客户端压力监控

```toml
[[instances]]
targets = ["10.0.0.10:6379"]
password = "your-password"

[instances.connected_clients]
warn_ge = 500
critical_ge = 1000

[instances.blocked_clients]
warn_ge = 1
critical_ge = 5

[instances.used_memory]
warn_ge = "8GB"
critical_ge = "10GB"
```

### 5. 运行期计数器监控

```toml
[[instances]]
targets = ["10.0.0.10:6379"]
password = "your-password"

[instances.rejected_connections]
warn_ge = 1
critical_ge = 10

[instances.evicted_keys]
warn_ge = 10
critical_ge = 100

[instances.expired_keys]
warn_ge = 100
critical_ge = 1000

[instances.instantaneous_ops_per_sec]
warn_ge = 5000
critical_ge = 20000
```

说明：

- `rejected_connections` 使用 `INFO stats` 中的累计值。
- `evicted_keys` 和 `expired_keys` 使用采集周期内的 delta，不是 Redis 启动以来累计值。

## 使用 Partials 复用配置

可以用 `partials` 复用认证、TLS、超时和公共阈值：

```toml
[[partials]]
id = "prod"
password = "your-password"
timeout = "3s"
read_timeout = "2s"

[partials.connectivity]
severity = "Critical"

[partials.persistence]
enabled = true
severity = "Critical"

[[instances]]
targets = ["10.0.0.10:6379"]
partial = "prod"

[instances.role]
expect = "master"

[[instances]]
targets = ["10.0.0.11:6379"]
partial = "prod"

[instances.role]
expect = "slave"

[instances.master_link_status]
expect = "up"
```

## 检查项说明

### `redis::connectivity`

- 始终启用
- 建连后按需执行 `AUTH` / `SELECT`，最后执行 `PING`
- 默认严重级别为 `Critical`

### `redis::response_time`

- 统计从建连到收到 `PONG` 的总耗时
- 配置了 `warn_ge` 或 `critical_ge` 才会启用

### `redis::role`

- 读取 `INFO replication`
- `expect` 可选值：`master`、`slave`、`replica`
- `replica` 会在内部归一化为 `slave`

### `redis::connected_clients`

- 读取 `INFO clients` 中的 `connected_clients`

### `redis::blocked_clients`

- 读取 `INFO clients` 中的 `blocked_clients`

### `redis::used_memory`

- 读取 `INFO memory` 中的 `used_memory`
- 如果 Redis 返回 `maxmemory`，事件里会附带相关标签

### `redis::rejected_connections`

- 读取 `INFO stats` 中的 `rejected_connections`
- 常用于发现 `maxclients` 耗尽或资源压力

### `redis::master_link_status`

- 适用于 replica 节点
- 常见期望值为 `up`

### `redis::connected_slaves`

- 适用于 master 节点
- 使用 `warn_lt` / `critical_lt` 判断副本数是否偏低

### `redis::evicted_keys`

- 读取 `INFO stats` 中的 `evicted_keys`
- 检查的是采集周期内 delta
- 首次采集只建立 baseline

### `redis::expired_keys`

- 读取 `INFO stats` 中的 `expired_keys`
- 检查的是采集周期内 delta
- 首次采集只建立 baseline

### `redis::instantaneous_ops_per_sec`

- 读取 `INFO stats` 中的 `instantaneous_ops_per_sec`

### `redis::persistence`

- 读取 `INFO persistence`
- 以下情况会告警：
  - `loading = 1`
  - `rdb_last_bgsave_status != ok`
  - `aof_enabled = 1` 且 `aof_last_write_status != ok`

## 运维建议

- 对 master 节点重点启用 `role` 和 `connected_slaves`。
- 对 replica 节点重点启用 `role` 和 `master_link_status`。
- `used_memory` 阈值应结合真实 `maxmemory` 配置设置。
- `rejected_connections` 一旦增长，通常已经是运行期问题，不建议长期忽略。
- `evicted_keys`、`expired_keys` 和 `instantaneous_ops_per_sec` 需要结合实际业务流量和采集间隔调优。
- 如果 Redis 承担数据持久化职责，建议启用 `persistence`。

## 故障排查

### 认证失败

现象：

- `redis::connectivity` 报 `WRONGPASS`
- Redis 可达，但认证失败

重点检查：

- `password`
- `username`
- 监控账号的 ACL 权限

### TLS 失败

现象：

- 在 `PING` 之前就连接失败

重点检查：

- `use_tls`
- CA / 证书 / 私钥文件
- `tls_server_name`
- 是否错误使用了 `insecure_skip_verify`

### 角色不匹配

现象：

- `redis::role` 提示实际角色和期望角色不一致

重点检查：

- 当前是否发生过主从切换
- sentinel / 编排系统是否已经调整拓扑
- 当前监控配置是否仍然匹配真实角色

### `evicted_keys` 或 `expired_keys` 没有告警

这不一定是问题：

- 第一次采集只建立 baseline
- `evicted_keys` 需要真实的内存压力
- `expired_keys` 依赖采集周期内确实发生 TTL 到期

## 相关文档

- [`design.md`](./design.md)
- [`docker-compose.yml`](./docker-compose.yml)
- [`test-plan.md`](./test-plan.md)
- [`test-report.md`](./test-report.md)
- [`test-report.zh-CN.md`](./test-report.zh-CN.md)
