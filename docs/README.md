# 文档目录

- [总体架构设计（方案提交入口）](architecture.md)：系统拓扑、核心时序、最小数据模型、多后端、预期效果和时间规划。
- [生产风险清单](risks.md)：17 项生产风险、缓解措施和演练方法。
- [数据模型](data-model.md)：PostgreSQL 表、配置版本和迁移约束。
- [多节点消息运行时](message-runtime.md)：Inbox、fencing、提交顺序和 Outbox。
- [Runtime Bundle](runtime.md)：tRPC-Agent-Go Runner 的构建、版本和生命周期。
- [治理、审计与可观测性](governance.md)：权限、预算、审批、脱敏和 tracing。
- [生产可观测性](observability.md)：OTLP、Collector、Prometheus/Grafana、指标基数和审计保留。
- [容量测试与估算](capacity.md)：有界负载探针、完整执行容量模型与准入指标。
- [故障演练手册](fault-drills.md)：单 Pod、共享后端、模型、Sender 与 Collector 故障场景。
- [生产验收](production-acceptance.md)：只读 smoke、可选真实 Runner 消息与发布硬门禁。
- [企业微信 Adapter](wecom.md)：回调协议、身份映射和主动发送。
- [PostgreSQL + Redis 部署](deployment.md)：Compose 启动、验证、密钥和生产拓扑边界。
- [Kubernetes 部署](../deploy/kubernetes/README.md)：Kustomize、Secret 合约、migration 与滚动发布。
