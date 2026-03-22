# catpaw 博客索引

本目录存放 catpaw 项目的推广博客文章。写新博客前请先阅读本索引，了解已有文章的主题和角度，避免重复。

## 文章列表

| # | 文件 | 标题 | 主题角度 | 目标读者 |
|---|------|------|----------|----------|
| 1 | [catpaw-intro.md](catpaw-intro.md) | catpaw：会自己看病的监控 Agent | 项目全景介绍：定位、架构、三种 AI 能力（自动诊断/Chat/巡检）、27 个插件总览、5 分钟快速体验、AI 编程实践、社区贡献指南 | 泛技术人群，对 AI + 监控感兴趣的开发者/运维 |
| 2 | [linux-silent-killers.md](linux-silent-killers.md) | 那些你不知道自己需要监控的 Linux 暗坑 | 深度技术内容：拆解 8 个 Linux 内核层"沉默杀手"（conntrack、ARP 邻居表、sysctl 漂移、listen 队列溢出、CLOSE_WAIT、fd 耗尽、网卡 error/drop、挂载点漂移），每个附带故障原理和 catpaw 监控配置 | SRE / Linux 运维，有生产环境排障经验 |
| 3 | [chat-troubleshooting.md](chat-troubleshooting.md) | 不记命令也能排障：catpaw chat 实战手册 | 实战教程：12 个高频排障场景（CPU/OOM/磁盘/网络/CLOSE_WAIT/进程/内核/证书/重传/容器/listen 溢出/日志），每个对比"传统命令行" vs "catpaw chat 一句话"，展示 AI 调用 90+ 诊断工具的完整过程 | 开发者 / 初中级运维，排障时常需要 Google 命令的人 |
| 4 | [alert-to-root-cause.md](alert-to-root-cause.md) | 告警发出来之后，谁来查根因？ | 深度方法论 + 产品落地：解释为什么告警只解决了 20% 的问题，拆解完整的"异常检测 -> 聚合 -> 诊断 -> 报告"闭环，并用磁盘/conntrack 场景展示 catpaw 如何把根因初筛自动化 | On-call SRE、值班开发、平台工程师 |
| 5 | [prometheus-and-check-agent.md](prometheus-and-check-agent.md) | Prometheus 很强，但它不负责半夜帮你查机器 | 认知校准：解释 metrics、alerting、check、RCA 的分层，说明 catpaw 不是替代 Prometheus，而是补上主机侧异常检测和告警后第一轮根因分析 | SRE、平台工程师、负责值班的后端开发 |
| 6 | [redis-what-to-monitor.md](redis-what-to-monitor.md) | Redis 出问题时，你到底该先看什么？ | 垂直场景专题：按单机、主从、集群拆解 Redis 真正值得监控的语义层风险，解释哪些检查该周期跑、哪些只适合诊断时按需触发，并结合 catpaw Redis 插件给出最小可用配置 | 中间件 SRE、后端开发、缓存平台维护者 |
| 7 | [drift-and-inspection.md](drift-and-inspection.md) | 比故障更可怕的，是系统正在悄悄漂移 | 预防式治理专题：解释为什么成熟的 SRE 不能只等告警，围绕 sysctl、mount、secmod、ntp 讲基线检查和主动巡检的价值，并结合 `catpaw inspect` 说明变更后如何快速做健康确认 | 系统运维、平台工程师、负责主机基线和变更治理的 SRE |
| 8 | [monitor-your-monitoring.md](monitor-your-monitoring.md) | 监控你的监控系统：Prometheus 挂了之后，谁来发现？ | 场景方案文：围绕 Prometheus / Nightingale / Alertmanager 等监控基础设施，拆解“进程、端口、HTTP、磁盘、日志、时间、规则链路”这几类最关键的自监控项，并说明 catpaw 为什么适合作为这层独立哨兵 | 平台工程师、可观测性团队、负责监控平台稳定性的 SRE |

## 规划文档

- [content-roadmap.md](content-roadmap.md)：面向 SRE / DEV 的后续博客路线图、选题优先级和分发建议
- [alert-to-root-cause-outline.md](alert-to-root-cause-outline.md)：优先文章 1 提纲，主打"告警到根因"闭环
- [prometheus-and-check-agent-outline.md](prometheus-and-check-agent-outline.md)：优先文章 2 提纲，主打 catpaw 与 Prometheus 的互补关系
- [social-posts.md](social-posts.md)：基于最近 5 篇新文拆出的短帖文案，可直接发朋友圈 / X / LinkedIn

## 写作原则

- 每篇博客应有**独立的主题角度**，与已有文章形成差异化
- 面向目标读者，技术深度与读者背景匹配
- 优先选择有教育价值的主题——即使读者不用 catpaw，读完也能学到东西
- catpaw 的推广应自然融入内容，而非硬广
