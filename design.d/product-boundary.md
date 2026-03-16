# 产品边界：诊断 vs 指标采集

## 核心定位

catpaw 是**诊断 Agent**，不是指标采集器。

- catpaw 的核心价值是 **AI 驱动的故障诊断**，不是替代 Prometheus 生态的指标采集链路
- 指标采集由 Prometheus + Exporter 生态负责（node-exporter、redis-exporter、mysqld-exporter 等）
- catpaw 关注的是**异常事件**和**根因分析**，不是历史指标趋势

## 决策：服务认证信息的处理

### 问题

catpaw 部署在所有机器上，但机器上运行的 Redis、MySQL 等服务需要认证才能深度诊断。用户可能已经在 redis-exporter、mysqld-exporter 中配置过认证信息，在 catpaw 中再配一遍有重复感。

### 决策结论

**catpaw 独立配置服务认证信息，仅用于诊断，不做指标采集。**

理由：

1. **诊断 ≠ 采集** — 采集是持续的、周期性的（每 15s/30s），诊断是按需的、事件触发的。两者需要的信息也不同：采集需要标准化 metrics，诊断需要 `INFO`、`SLOWLOG`、`SHOW PROCESSLIST` 等命令的原始输出
2. **价值感知强** — 多配一行密码，换来的是 AI 自动诊断 Redis/MySQL 问题的能力。Exporter 只告诉你"Redis 慢了"，catpaw 告诉你"Redis 慢了是因为有个 3MB 的大 key 导致 SLOWLOG 里全是 HGETALL"
3. **解耦生命周期** — catpaw 不依赖 exporter 是否部署、是否运行。即使 exporter 挂了，catpaw 依然能独立诊断

### 明确不做

- **不吸收 exporter 代码** — redis-exporter、mysqld-exporter 各有几千行代码，版本适配和 bug 修复全要跟进，维护成本极高
- **不做指标采集** — 不产出 Prometheus metrics，不暴露 `/metrics` 端点，不做持续性数据采集
- **不替代 exporter** — catpaw 和 exporter 是互补关系，不是竞争关系

### 配置方式

服务认证信息配置在插件 instance 级别，语义明确——这是诊断用的连接信息：

```toml
# conf.d/p.redis/redis.toml
[[instances]]
targets = ["127.0.0.1:6379"]
password = "${REDIS_PASSWORD}"

  [instances.diagnose]
  enabled = true
```

### 后续可优化

作为降低配置摩擦的增强（非主路径），可考虑：

- 自动发现本机运行的 exporter 进程，解析其启动参数中的认证信息
- 自动发现本机运行的服务进程，尝试无认证连接（部分 Redis 实例无密码）

这些作为便利功能，不作为核心依赖——exporter 的配置格式不稳定，解析容易出错。

## 与 Exporter 的关系矩阵

| 维度 | Exporter | catpaw |
|------|----------|--------|
| 目的 | 持续采集指标，喂给 Prometheus | 检测异常 + AI 诊断根因 |
| 频率 | 周期性（15s/30s） | 事件触发（告警时） |
| 输出 | Prometheus metrics | Event（告警）+ 诊断报告 |
| 连接方式 | 长期保持或高频短连 | 按需建连，诊断完即断 |
| 数据类型 | 标准化数值指标 | 诊断命令原始输出（INFO、SLOWLOG 等） |
| 故障时影响 | 指标缺失，告警规则失效 | 诊断不可用，但告警不受影响 |

## 指导原则

1. catpaw 的任何功能不应与 Prometheus + Exporter 生态的指标采集能力重叠
2. catpaw 对服务的连接仅用于按需诊断，不做持续性数据拉取
3. 诊断功能是锦上添花，采集功能留给专业工具——catpaw 的护城河是**诊断深度**，不是采集广度
