# 文章提纲：Prometheus 很强，但它不负责半夜帮你查机器

## 文章定位

- 主标题：Prometheus 很强，但它不负责半夜帮你查机器
- 备选标题：
  - Node Exporter 之后，为什么我还需要一个 check 型 Agent
  - 监控栈里缺的不是指标，而是异常判断和根因定位
  - 为什么大盘全绿，服务还是可能已经坏了
- 目标读者：SRE、平台工程师、负责值班的后端开发
- 文章目标：清楚解释 catpaw 与 Prometheus / Grafana / Alertmanager 的关系，避免被误解为重复造轮子

## 核心观点

Prometheus 擅长采集指标、保存时间序列、画趋势图和做规则告警；catpaw 擅长做主机侧异常判断、补监控盲区，并在告警触发后完成第一轮根因分析。两者不是替代关系，而是职责分层。

## 建议结构

### 1. 从一个常见误解切入

- 很多人第一次看到 catpaw，会问：
  - 这和 Prometheus / Node Exporter 有什么区别？
  - 我已经有 Grafana 了，还需要它吗？
- 直接给结论：
  - 如果你要的是指标趋势，Prometheus 非常合适
  - 如果你要的是"发现具体异常 + 帮你做第一轮排查"，Prometheus 并不直接解决

### 2. 指标采集和异常检测不是一回事

- 指标采集回答：系统现在有哪些数字
- 异常检测回答：哪些数字或状态已经构成问题
- 根因分析回答：这些问题最可能是怎么发生的

建议做一张三列对比表：

| 层次 | 典型工具 | 解决的问题 |
| --- | --- | --- |
| Metrics | Prometheus / Node Exporter | 指标采集、趋势分析 |
| Alerting | Alertmanager / 告警规则 | 何时通知 |
| Check + RCA | catpaw | 出了什么问题、先查什么 |

### 3. 为什么 exporter 指标不等于有效告警

这里可以复用 `linux-silent-killers.md` 的逻辑，挑 4 到 5 个例子：

- conntrack 表满了
- 邻居表满了
- sysctl 参数漂移
- listen 队列溢出
- CLOSE_WAIT / fd 耗尽

重点讲：

- 指标也许存在
- 但默认没人帮你变成面向故障的判断
- 更没人把这些判断串成下一步排查动作

### 4. catpaw 补的到底是哪一层

- 单机部署、单二进制、轻量
- 插件直接表达"检查项"
- 输出标准化事件，而不是一堆原始 metrics
- 可直接推给 Flashduty、PagerDuty、WebAPI 或 console
- 告警触发后可继续走 AI 诊断

这里可以用 README 里的插件表和通知表做支撑。

### 5. 一个更现实的监控栈长什么样

推荐给读者一个组合思路：

- Prometheus / Grafana：看趋势、看大盘、查历史
- catpaw：看主机侧风险点、值班告警、根因初筛
- PagerDuty / Flashduty：值班通知
- MCP：把 Prometheus、Jaeger 等历史上下文喂给 AI

这部分能帮助读者在脑中完成架构定位。

### 6. 什么时候特别适合加 catpaw

- 值班时经常需要 SSH 上机查命令
- 团队里不是每个人都熟 Linux 内核排障
- 已经有 Prometheus，但一些故障就是告不出来
- 希望把告警和第一轮分析一起发到 On-call 平台

### 7. 什么时候不必强推

- 团队只需要简单的 uptime / CPU / memory 指标
- 还没有任何值班流程
- 当前问题不是盲区，而是基础监控都没搭起来

这一段反而会增加可信度。

### 8. 收尾

- 最后把 catpaw 定位成：
  - 不是替代 Prometheus
  - 而是替代一部分"凌晨登录机器 + 手工排查"的工作
- CTA：
  - 先挑 3 个最容易出问题的插件接上
  - 比如 `conntrack`、`tcpstate`、`sysctl`

## 文中可直接引用的 repo 材料

- `README.md`：项目定位、插件列表、通知能力、MCP 能力
- `docs/dev-guide.md`：事件模型、诊断链路、MCP 注册方式
- `blog.d/linux-silent-killers.md`：监控盲区案例

## 写作注意点

- 不要把文章写成"Prometheus 不行"
- 重点是职责边界，不是工具对立
- 这篇最适合拿来做认知校准，减少误解和争论
