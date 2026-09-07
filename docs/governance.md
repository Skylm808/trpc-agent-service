# 租户治理、审计与可观测性

运行时固定执行以下链路：binding 凭证认证、租户路由、canonical identity、Inbox
幂等 claim、binding 级用户/群聊 ACL、身份/预算校验、工具可见性、工具执行与审批、输出脱敏、租户审计。
`Processor.Policy` 是必需依赖；缺失时请求 fail closed。工具同时受 tRPC-Agent-Go 的
`WithToolFilter`、`WithToolExecutionFilter`、`WithToolPermissionPolicy` 和最终
`Guarded.Call` 保护，直接调用不能绕开 tenant/request scope。

MCP 与 HTTPS 业务工具也进入同一条链路。配置发布只把显式命名且列入
`tools.allow` 的工具放进版本固定 Catalog；远端 metadata 在安全包装后继续保留，所有
结果、callback payload 和 metadata 在进入 Agent Event 或审计前递归脱敏。MCP 与业务
接口的原始错误正文不会返回给模型或写入审计。

`require_approval` 工具不会自动执行。Worker 发布 `run.approval_required`，审批方使用
认证后的 `POST /v1/gateway/approve` 提交 `request_id` 与 `tool_name`；共享审批存储唤醒
持有请求的 Worker，Worker 调用 guarded tool，并用 `model.NewToolMessage` 在原 session
续跑模型。本地 `MemoryApprovals` 仅供测试；多节点生产环境必须实现共享、原子、带过期
时间和审批人审计的 ApprovalStore。

预算先按输入估算原子预留，再按模型 usage reconciliation；request token 超限会取消
Runner。当前生产组合器使用统一的固定微成本系数，因此 token 预算和并发扣减已经生效，
但不能作为不同模型的精确账单。要启用可靠的月度成本治理，仍需在不可变模型配置中增加
版本化的输入/输出价格，并把计算后的实际成本写入 Metrics 和 Audit。

审计遵循 tenant `AuditPolicy`：`enabled=false` 不写；`store_content=false` 不保存错误
正文；`redact_fields` 在结构化字段写入前生效；`RetentionDays` 由定时任务调用
`audit.RetentionWorker` 自动执行 `audit.PruneTenant`，多节点使用 PostgreSQL advisory lock
串行化每轮清理。审计写使用两秒 deadline，当前选择 fail-open 并记录低基数
`operation=audit,status=failed` 指标，避免审计后端拖死消息处理。

生产入口由 `otelhttp` 从 HTTP `traceparent` 提取并传播到队列，覆盖 callback、Inbox、
lease、Runner、model stream、Tool、Session、Summary/Memory 和 Outbox。metrics 标签只允许
tenant、app、channel、operation、status；request/user/session/message ID 只允许进入
trace/audit。SDK 经 OTLP/gRPC 输出到 Collector，Collector 再向 Prometheus 暴露指标；默认与 tenant redactor 会处理日志、SSE error、Inbox last_error、回复和
审计 details，tRPC-Agent-Go 的上下文 logger 也在 Bundle 构建时安装脱敏包装。

`allowed_users` 和 `allowed_chats` 使用经过验签后得到的平台稳定 ID，并随不可变配置版本进入
Worker。两个列表都为空时兼容现有绑定并允许全部；`allowed_users` 限制单聊和群聊发送者，
`allowed_chats` 限制群聊。仅设置 chat 白名单时直聊默认拒绝。拒绝在 Runtime Bundle/Runner
创建前完成，Inbox 标记为 `rejected`，审计只保存脱敏身份和 `decision=deny`，错误不回显名单或
外部用户 ID。相同用户或群 ID 在不同租户独立判断，不能复用另一租户的 ACL。

## 监控指标

| 指标 | 建议维度 | 用途 |
| --- | --- | --- |
| `agent.requests`, `agent.operation.duration` | tenant, app, channel, operation, status | 请求量、错误率和模型/Tool/治理操作耗时 |
| `agent.model.first_token.duration` | tenant, app, channel, operation, status | 模型首事件耗时 |
| `agent.im.delivery` | tenant, app, channel, status | IM 投递成功率和平台限流结果 |
| `agent.tokens` | tenant, app, operation, status | 已接入的 token 消耗与预算核对 |
| `agent.cost` | tenant, app, operation, status | 指标已注册；接入版本化模型价格后记录租户实际成本 |
| `agent.storage.operation.duration`, `agent.storage.operation.errors` | tenant, app, domain, backend, operation, status | 真实 Session/Memory Adapter 调用延迟和错误率 |
| `agent.queue.depth`, `agent.outbox.backlog` | tenant, queue, status | Inbox/Outbox/DLQ 排队与故障恢复状态 |
| `agent.worker.live`, `agent.storage.health`, `agent.storage.healthcheck.duration` | domain, backend, operation, status | Worker 存活与平台 PostgreSQL 健康检查 |

`binding_id` 只有在绑定数量受控时才能作为 metrics 标签，否则只进入 trace。延迟指标使用 histogram，并按部署基线设置 p95/p99 告警；错误率和队列等待可以按租户及全局聚合，精确成本聚合需先完成模型价格闭环。

## 审计字段

每条审计记录包含 `tenant_id`、`channel`、`user_id`、`session_id`、`agent_name`、`tool_name`、`decision`、`latency_ms`、`error_type`、`cost_micros` 和 `trace_id`，并保留 `request_id`、`event_id` 与脱敏后的 `details_json`。当前 `cost_micros` 在价格闭环完成前为 0；`config_version` 和 `policy_version` 仍需补充到审计表。密钥、Authorization header、Cookie、模型原始请求及未获授权的消息正文不得写入日志、trace 或错误报告。
