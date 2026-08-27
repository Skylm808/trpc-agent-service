# 文档目录

- [总体架构设计（方案提交入口）](architecture.md)：系统拓扑、核心时序、最小数据模型、多后端、预期效果和时间规划。
- [生产风险清单](risks.md)：17 项生产风险、缓解措施和演练方法。
- [数据模型](data-model.md)：PostgreSQL 表、配置版本和迁移约束。
- [多节点消息运行时](message-runtime.md)：Inbox、fencing、提交顺序和 Outbox。
- [Runtime Bundle](runtime.md)：tRPC-Agent-Go Runner 的构建、版本和生命周期。
- [治理、审计与可观测性](governance.md)：权限、预算、审批、脱敏和 tracing。
- [企业微信 Adapter](wecom.md)：回调协议、身份映射和主动发送。
- [PostgreSQL + Redis 部署](deployment.md)：Compose 启动、验证、密钥和生产拓扑边界。
