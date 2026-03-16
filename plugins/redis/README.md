# Redis 插件文档

这个目录包含 Redis 插件的实现代码、配置示例，以及面向单机 Redis 和 Redis 集群的验证资产。

## 文档索引

- [`docs/usage.md`](./docs/usage.md)：使用说明
- [`docs/design.md`](./docs/design.md)：设计说明、适用范围、默认策略和实现模型
- [`testdata/master-replica/docker-compose.yml`](./testdata/master-replica/docker-compose.yml)：主从测试环境
- [`testdata/cluster/docker-compose.yml`](./testdata/cluster/docker-compose.yml)：Redis 集群测试环境

## 代码结构

| 文件 | 作用 |
| --- | --- |
| [`redis.go`](./redis.go) | 包入口与插件注册 |
| [`types.go`](./types.go) | 常量定义，以及 `Plugin` / `Instance` / `Partial` 结构体 |
| [`config.go`](./config.go) | `partial` 合并、`Init` 校验与配置归一化 |
| [`gather.go`](./gather.go) | 多目标采集、单目标流程与卡死处理 |
| [`checks.go`](./checks.go) | 节点级检查，例如角色、内存、持久化 |
| [`cluster.go`](./cluster.go) | 集群相关检查与拓扑解析 |
| [`accessor.go`](./accessor.go) | Redis 连接、RESP 协议处理与 `INFO` 解析 |
| [`diagnose.go`](./diagnose.go) | AI 诊断工具、预采集器与诊断提示 |
| [`redis_test.go`](./redis_test.go) | 基于 fake Redis server 的单元测试 |
| [`diagnose_test.go`](./diagnose_test.go) | 诊断工具注册与行为测试 |
| [`redis_integration_test.go`](./redis_integration_test.go) | 通过 `integration` 构建标签启用的集成测试 |

## 插件覆盖范围

- 单机 Redis 和主从部署
- 使用保守默认策略的 Redis 集群硬故障检查
- 可选的业务相关检查，例如复制延迟和 `maxmemory` 使用率
- 面向集群拓扑和大 key 的按需诊断工具

## 插件明确不做的事

- 不替代 `redis-exporter`
- 不暴露 Prometheus 指标
- 不执行 `SCAN`、`MEMORY USAGE` 这类周期性重扫描
- 默认不假设必须满足某种拓扑策略，例如固定副本数
