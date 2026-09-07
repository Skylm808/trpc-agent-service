# 多节点 Compose 验收报告

- 日期：2026-09-07T14:36:54Z
- Git 基线：`05aa0a13bba79e9a553d8e7a890b968e8c633399+candidate`
- Compose project：`trpc-agent-service-pr14-check`
- 拓扑：Gateway 1、Worker 2、共享 PostgreSQL/Redis
- 数据卷操作：未执行 `down -v`、`volume rm`、数据库清空或卷重建

| 验收项 | 结果 | 脱敏证据 |
| --- | --- | --- |
| Gateway health/readiness | PASS | `/healthz`、`/readyz` |
| Worker 不暴露回调端口 | PASS | 两个 Worker 均无宿主端口 |
| Worker 唯一注册和心跳 | PASS | 存活 Worker 数为 2 |
| Redis Streams 可用 | PASS | 共享 Stream 可读 |
| PostgreSQL Session/Event/Memory 可用 | PASS | 只比较聚合计数 |
| 双 Worker 竞争、崩溃接管和旧 token 拒绝 | PASS | 隔离 PostgreSQL/Redis 集成测试 |
| 同 session 顺序和跨租户隔离 | PASS | 隔离集成测试 |
| 定点停止 `worker-a` 后存活节点保持健康 | PASS | 仅 stop/start 明确 Worker |
| `worker-a` 重新注册 | PASS | 恢复后存活 Worker 数为 2 |
| 配置和持久数据不回退 | PASS | before/after 聚合值未下降 |
| Secret、正文和数据库密码未输出 | PASS | 验收脚本仅输出状态和聚合结果 |

本轮没有发送真实模型消息，也没有读取 IM 或模型凭据。“消息由不同 Worker 消费”由真实 Redis/PostgreSQL 隔离集成测试验证；企业微信和飞书真实平台 E2E 沿用此前人工 PASS 结论。若需再次进行产生模型费用的主动消息验收，必须显式提供独立测试 tenant/binding，并设置脚本要求的 opt-in 环境变量。

结论：多节点 Compose 基础生产拓扑 **GO**。报告不包含用户消息正文、回调 payload、SecretRef key、下载 URL、DSN、密码或真实外部身份。
