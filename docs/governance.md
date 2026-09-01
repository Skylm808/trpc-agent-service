# 租户治理、审计与可观测性

运行时固定执行以下链路：binding 凭证认证、租户路由、canonical identity、Inbox
幂等 claim、身份/预算校验、工具可见性、工具执行与审批、输出脱敏、租户审计。
`Processor.Policy` 是必需依赖；缺失时请求 fail closed。工具同时受 tRPC-Agent-Go 的
`WithToolFilter`、`WithToolExecutionFilter`、`WithToolPermissionPolicy` 和最终
`Guarded.Call` 保护，直接调用不能绕开 tenant/request scope。

`require_approval` 工具不会自动执行。Worker 发布 `run.approval_required`，审批方使用
认证后的 `POST /v1/gateway/approve` 提交 `request_id` 与 `tool_name`；共享审批存储唤醒
持有请求的 Worker，Worker 调用 guarded tool，并用 `model.NewToolMessage` 在原 session
续跑模型。本地 `MemoryApprovals` 仅供测试；多节点生产环境必须实现共享、原子、带过期
时间和审批人审计的 ApprovalStore。

预算先按输入估算原子预留，再按模型 usage reconciliation。request token 超限会取消
Runner。配置月度成本预算时必须提供 provider 对应的 `CostMicrosPerToken`（或生产
BudgetStore 的等价动态计价）；无法计价时 fail closed，禁止把未知成本当作零成本。

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

## 监控指标

| 指标 | 建议维度 | 用途 |
| --- | --- | --- |
| `agent_requests_total`, `agent_errors_total` | tenant, app, channel, status | 请求量、租户错误率和灰度判断 |
| `model_first_token_seconds`, `model_duration_seconds` | tenant, app, provider, model, status | 模型首包与完整调用耗时 |
| `tool_duration_seconds`, `tool_calls_total` | tenant, app, tool, decision, status | Tool 性能、拒绝和审批比例 |
| `im_callback_seconds`, `im_delivery_total` | tenant, channel, binding, status | 回调耗时、投递成功率和平台限流 |
| `model_tokens_total`, `tenant_cost_micros_total` | tenant, app, provider, model | token 消耗、预算和租户成本 |
| `storage_operation_seconds` | tenant, domain, backend, operation, status | Session/Memory/Storage 后端延迟 |
| `inbox_backlog`, `outbox_backlog`, `dlq_total` | tenant, channel, status | 排队、投递和故障恢复状态 |
| `stale_fence_rejections_total` | tenant, app | 检测 lease 转移和旧 Worker 写入 |

`binding_id` 只有在绑定数量受控时才能作为 metrics 标签，否则只进入 trace。延迟指标使用 histogram，并按部署基线设置 p95/p99 告警；错误率、队列等待和成本同时按租户及全局聚合。

## 审计字段

每条审计记录至少包含 `tenant_id`、`channel`、`user_id`、`session_id`、`agent_name`、`tool_name`、`decision`、`latency_ms`、`error_type`、`cost` 和 `trace_id`，并建议保留 `request_id`、`event_id`、`config_version`、`policy_version` 与脱敏后的 `details_json`。密钥、Authorization header、Cookie、模型原始请求及未获授权的消息正文不得写入日志、trace 或错误报告。
