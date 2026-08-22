# PR4/PR5：多节点消息运行时与 OpenClaw-compatible durable HTTP Gateway

## 组件边界

`gateway/openclaw` 只负责认证、协议解析和服务端路由；`idempotency`
负责持久化 Inbox claim；`dispatcher` 只提供单节点同 session 排队；
`sessioncoord` 负责跨节点 lease、fencing 和写入顺序；`worker` 才能取得固定版本的
Runtime Bundle 并调用 tRPC-Agent-Go `Runner`。因此 Gateway 和 Worker 都可以水平扩展，
不依赖负载均衡器 sticky session。

```text
OpenClaw/IM callback
  -> credential + binding -> tenant/app/config_version
  -> canonical user/session
  -> PostgreSQL Inbox unique claim + inbox_seq
  -> fast HTTP 202
  -> per-session dispatcher
  -> Redis lease (INCR fencing token)
  -> Runtime Manager -> LLMAgent -> Tool -> Runner events
  -> fenced event/state
  -> fenced Summary/Memory projection
  -> fenced Outbox
  -> Inbox completed
```

进程内 `MemoryStore`、`Coordinator` 和 `MemoryWriteStore` 是离线示例与确定性测试实现。
生产部署必须使用 `SQLStore` 的 PostgreSQL claim、`RedisCoordinator`，以及
`SQLWriteStore`（或等价的 `WriteStore + FenceValidator` 共享事务后端）。`FencedSessionService` 的
“校验后调用 delegate”只适用于同一原子后端；生产 Session Adapter 必须在同一数据库
事务或 Lua 脚本中完成 `last_fence = token` 校验与 event/state 修改，不能把一次远端
预检查当作事务隔离。

## 顺序和故障语义

1. Inbox 唯一键是 `(tenant_id, binding_id, external_message_id)`。首次 claim 在
   PostgreSQL serializable 事务和 session advisory lock 下分配 `inbox_seq`。
2. 不同 session 可并行；同 session 的 `CommitTurn` 只接受
   `inbox_seq = last_event_seq + 1`。不同节点乱序竞争时，后序消息得到
   `ErrOutOfOrder` 并进入 Inbox retry，而不是越过前序消息。
3. Redis 使用独立持久化计数器 `INCR` 生成 fencing token。lease 续期和释放都比较
   `owner|token`；旧 Worker 即使在 GC pause 或网络恢复后继续执行，也不能写 event、
   state、summary、memory 或 outbox。
4. Worker 的提交顺序固定为 event/state → summary/memory → outbox → Inbox completed。
   Outbox 用 `(tenant_id, dedupe_key)` 幂等，Memory 用 source event 幂等，Summary 用
   version/cutoff CAS。任何阶段失败都会保留可重试 Inbox；已提交步骤重复执行不会产生
   第二条消息或第二份 Memory。
5. Runtime 请求固定携带 ingress 时的 `config_version`。旧版本 Bundle 在已有请求释放
   lease 前不会关闭；新请求只进入新版本。固定版本已经被清除时请求失败并重试/进入
   DLQ，禁止悄悄使用当前版本。

## OpenClaw 兼容 HTTP

这里实现的是 OpenClaw 文本协议的 durable callback profile，不是上游 `gwclient`
的逐字节替代：消息持久化后返回 `202 Accepted`，而不是等待完整回复后返回 `200`。
多模态 DTO 目前也是 text-only 子集。

端点：

- `POST /v1/gateway/messages`：持久化成功即返回 `202`，不等待模型和工具。
- `POST /v1/gateway/messages:stream`：Runner 产生事件时立即输出 `run.started`、
  `run.progress`、`message.delta`、`message.completed`、`run.completed` 或终态事件。
- `GET /healthz`、`GET /v1/gateway/status`。
- `POST /v1/gateway/cancel`：取消本节点当前活跃的 Runner，并将 Inbox 以 CAS 标记为
  `canceled`；未知或已结束请求返回 `404`。生产多节点部署必须把取消命令送到持有
  request 的 Worker（例如 Redis command bus），不能依赖 Gateway 进程内查找。

本地 `Hub` 和 `Registry` 仅供单进程开发。Gateway 与 Worker 分离或水平扩展时，SSE
事件应使用 `RedisEventBus`（或等价共享 Pub/Sub），status 应投影到共享持久化后端；
因此客户端不需要 sticky session。

进程内 Hub 对 delta 使用有界、非阻塞缓冲；慢客户端可能丢失中间 delta，但终态
`message.completed`/`run.completed` 会优先投递。需要完整回放时，应从共享 event log
按 request ID 重放，而不是把 Pub/Sub 当作事实存储。

客户端通过 `Authorization: Bearer ...` 和 `X-Channel-Binding` 认证。token 只参与
常量时间比较，不写入消息、日志或 trace。服务端根据凭证解析 tenant/app；外部
`from` 映射为 canonical user，单聊、群聊、thread 分别使用 `DirectSessionID`、
`GroupSessionID`、`ThreadSessionID`。客户端提交 `session_id` 会被拒绝，避免跨租户
或跨会话注入。`X-Trace-ID` 从 callback 一直写入 Runner 请求、event 和 Outbox；
若缺失则由 Gateway 生成。

## 当前协议范围

DTO 保留 `content_parts`、图片/文件 URL、model 和 extensions，PR5 的可执行输入仅接受
文本。真正的企业微信/Telegram Adapter 应先验签，再转换成相同 `InboundMessage`；
出站层从 Outbox 做平台长度切片、限流、失败退避和 DLQ。HTTP 层不会把图片/文件 URL
直接交给模型或工具，以免形成 SSRF 通道。
