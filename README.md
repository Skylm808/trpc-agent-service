# 基于 tRPC-Agent-Go 设计多租户节点化 Agent 部署平台

## 背景和价值

企业在落地 Agent 应用时，通常不会只部署一个单体机器人，而是希望面向多个部门、多个业务线、多个 IM 入口和多个数据后端，构建一套可统一管理的 Agent 平台。例如：客服团队希望把 Agent 接入企业微信，研发团队希望接入内部群机器人，运营团队希望接入微信公众号或微信客服，不同租户又需要隔离会话、记忆、知识库、工具权限和审计日志。

[tRPC-Agent-Go](https://github.com/trpc-group/trpc-agent-go) 已经具备 Agent 编排（LLMAgent / GraphAgent / Chain / Parallel / Cycle）、Tool / MCP、Session、Memory、Knowledge、Artifact、Plugin / Guardrail、Telemetry、HTTP 服务化（OpenAI-compatible / AG-UI / A2A）、OpenClaw / IM 通道等能力。该题要求基于这些能力设计一个“多租户、可节点化部署、支持多后端数据同步、可接入微信 / 企业微信等 IM 软件”的生产级方案。

这个题目解决的业务痛点是：企业希望把 Agent 能力从单点 demo 扩展成平台化服务，同时满足租户隔离、弹性部署、数据一致性、IM 触达、审计合规和后端可替换等要求。它的价值在于把框架能力真正映射到企业级 Agent 平台架构，而不是只停留在单个 Agent 进程。

本题以 **tRPC-Agent-Go** 为实现框架，对称于基于 tRPC-Agent-Python 的同名题目。

### 任务描述

请设计一个基于 tRPC-Agent-Go 的多租户节点化 Agent 部署平台。平台需要支持多个租户创建和部署自己的 Agent，每个租户可以绑定不同 IM 通道、选择不同数据后端、配置不同工具权限和知识库，并允许多个 Agent 节点水平扩展。系统需要考虑跨节点会话路由、数据同步、后端适配、IM 消息接入、监控审计和故障恢复。

本项目的目标是按该设计完成完整工程实现，而不仅是提交架构设计。设计文档用于约束实现边界，仓库中的 Go 代码、迁移、Compose 部署和端到端链路必须与文档描述一致。

## 具体要求

### 多租户与节点部署

- 设计租户模型，至少包含 `tenant_id`、应用配置、模型配置、工具权限、IM 通道配置、数据后端配置、审计策略。
- 设计节点部署拓扑，说明 Agent Gateway、Agent Worker、Channel Adapter、Storage Adapter、Admin API、Telemetry Collector 等组件如何协作。可对照 tRPC-Agent-Go 中的 `runner.Runner`、`server/*`、`openclaw` Gateway 与 Channel 的职责划分。
- 支持多节点水平扩展，说明用户消息如何路由到正确租户和正确 session。
- 说明是否需要 sticky session；如果不需要，说明如何依赖共享 Session / Memory 后端（例如 `session/redis`、`session/mysql`、`session/postgres`）实现无状态 Worker。
- 设计租户隔离机制，包括配置隔离、数据隔离、工具权限隔离、日志脱敏和密钥管理。

### 数据同步与多后端支持

- 支持不同租户选择不同数据后端，例如 PostgreSQL、Redis、SQL、向量库、对象存储或外部 Memory 服务。tRPC-Agent-Go 已提供 Session（inmemory / redis / mysql / postgres / sqlite / mongodb 等）、Memory、Knowledge、Artifact 以及 `storage`（redis / mysql / postgres / s3 / qdrant / milvus 等）适配，方案需说明如何在平台层做租户级选择与路由。**生产运行时 Session / Memory 必须使用 PostgreSQL 等共享持久化后端**：多节点 Worker 依赖共享 Session / Memory 后端实现无状态运行；InMemory 只允许作为单元测试或离线开发辅助，不得作为生产方案、验收方案或多节点方案的默认值；生产配置缺少可识别的共享 Session 后端时服务必须启动失败，不允许静默回退到 tRPC-Agent-Go 的默认 InMemory Session。
- 设计统一的数据访问抽象，说明 Session、Memory、Summary、Artifact、Knowledge、Audit Log 分别如何存储。
- 设计数据同步策略，至少覆盖：
  - 多节点并发写入同一 session 的一致性。
  - Session event、state、summary 的更新顺序。
  - Memory 写入后的跨节点可见性。
  - 后端从 Redis 迁移到 SQL 或从本地向量库迁移到远端向量库时的数据迁移方案。
  - IM 消息重复投递时的幂等处理。
- 说明不同后端的一致性取舍，例如强一致、最终一致、读写延迟、成本和运维复杂度。
- 给出一个最小数据模型或表结构示例，至少包含 tenant、agent app、session、message/event、memory、summary、channel binding、audit log。

### IM 软件接入

- 设计 IM Channel Adapter，支持企业微信和飞书两类 IM 通道（微信客服、微信公众号等可在此模型上继续扩展）。可复用并扩展 tRPC-Agent-Go 的 OpenClaw Channel 模型。
- 说明外部 IM 消息如何转换为 tRPC-Agent-Go 的用户输入（`model.Message` / `runner.Runner.Run`），Agent Event 如何转换为 IM 回复、流式消息或卡片消息。
- 设计 IM 账号和租户绑定方式，包括 webhook URL、token、secret、回调验签、消息去重、用户身份映射。
- 说明群聊和单聊的 `session_id` 生成规则，以及用户跨群、跨租户时的隔离策略。
- 考虑 IM 平台限制，例如消息长度、频率限制、异步回复、图片 / 文件消息、撤回或失败重试。

### 治理、监控和安全

- 使用 Plugin / Guardrail / Callbacks 设计租户级治理策略，例如工具白名单、敏感信息脱敏、预算限制、危险工具二次确认、IM 用户权限校验。
- 设计监控指标，例如请求量、模型调用耗时、工具调用耗时、IM 投递成功率、错误率、token 消耗、每租户成本、Session 后端延迟。
- 说明如何接入 OpenTelemetry 或等价 tracing，要求 trace 能串起 IM callback、Runner 执行、Tool 调用、Session / Memory 读写和 IM 回复。
- 设计审计日志字段，至少包含 `tenant_id`、`channel`、`user_id`、`session_id`、`agent_name`、`tool_name`、`decision`、`latency`、`error_type`、`cost`、`trace_id`。
- 说明密钥管理和脱敏策略，IM token、模型 API key、数据库密码不能明文出现在日志、trace 或错误报告中。

### 故障恢复与运维

- 设计节点故障、IM 重试、数据库短暂不可用、模型超时、工具执行失败时的降级策略。Go 侧需同时说明 `context.Context` 取消、goroutine 生命周期和 Runner 事件通道排空，避免泄漏。
- 说明如何做灰度发布和租户级配置回滚。
- 说明如何做容量评估，例如每节点并发 session 数、平均 token 消耗、Redis / SQL QPS、IM 回调峰值。
- 设计最小可运行部署方案和生产推荐部署方案，可以使用 Docker Compose、Kubernetes 或等价部署方式描述。

### 交付物

- 一份架构设计文档，建议 2000 – 4000 字。
- 一张系统架构图，展示 Gateway、Worker、Channel Adapter、Storage Adapter、Plugin / Guardrail、Telemetry、数据库和 IM 平台之间的关系。
- 一张核心时序图，展示“企业微信用户发消息 → Agent 执行 → Tool 调用 → Session / Memory 写入 → IM 回复”的完整链路。
- 一份数据模型设计，包含核心表结构或 JSON schema。
- 一份数据同步和幂等策略说明。
- 一份多后端适配方案，说明 Redis / SQL / 向量库 / 对象存储分别适合存什么。
- 一份风险清单，列出至少 8 个生产风险及对应缓解措施。
- 一份基于该设计的 GitHub 实现代码。

## 题目难点

- 多租户隔离不是只加一个 `tenant_id` 字段，还涉及配置、权限、密钥、数据、日志、工具和成本隔离。
- 节点化部署要求 Agent Worker 尽量无状态，但 Agent 又天然依赖 Session、Memory、Summary 和工具上下文，需要设计可靠的共享状态层。
- IM 通道存在消息乱序、重复投递、响应超时、长度限制和身份映射问题，不能简单等同于 HTTP chat API。
- 不同后端的数据一致性能力不同，Redis、SQL、向量库、对象存储无法用同一种同步策略处理。
- Agent 执行链路包含模型、工具、MCP、知识库、沙箱和外部系统，监控和审计必须跨组件串联。
- 企业级平台必须考虑灰度、回滚、租户级限流、成本控制和合规审计。

## 验收标准

1. 架构方案必须覆盖多租户、节点化部署、数据同步、多后端支持、IM 接入、治理监控和故障恢复。
2. 数据模型必须能表达 tenant、agent、channel binding、session、event、memory、summary、audit log 的关系。
3. 必须说明至少两种 IM 通道的接入差异，其中至少包含微信或企业微信。
4. 必须说明至少三类后端的数据存储和同步策略，例如 Redis、SQL、向量库或对象存储。
5. 必须给出一条完整消息链路的时序说明，包含 `trace_id` 或 `request_id` 如何贯穿链路。
6. 必须列出至少 8 个生产风险和缓解措施。
7. 方案需要明确哪些能力可直接复用 tRPC-Agent-Go，哪些需要新增平台层模块。

## 可直接复用的 tRPC-Agent-Go 能力对照

| 平台需求 | 可复用的框架能力 | 需要新增的平台层 |
| --- | --- | --- |
| Agent 编排 | `agent/llmagent`、`agent/graph`、Chain / Parallel / Cycle | 租户级 Agent 注册、发布与路由 |
| 执行入口 | `runner.Runner`（流式 Event、context 取消） | 多租户 Worker 调度、无状态水平扩展 |
| Session / Memory / Artifact / Knowledge | `session`、`memory`、`artifact`、`knowledge` 及多后端实现 | 租户级后端选择、数据隔离与迁移 |
| Tool / MCP / Skill | `tool`、MCP Tool、`skill` | 租户工具白名单与密钥注入 |
| 治理 | Plugin / Guardrail / Callbacks | 租户策略下发、预算与审批 |
| 服务化 | `server/openai`、`server/agui`、`server/a2a`、`server/trpcagent` | 统一 Gateway、Admin API |
| IM 接入 | OpenClaw Gateway + Channel | 微信 / 企业微信等通道与租户绑定 |
| 可观测性 | OpenTelemetry tracing / metrics | 租户维度审计、成本与合规 |

## 代码目录

下面只是一个示范目录，用来说明平台需要覆盖的职责分层。实现时不必严格按这个结构组织代码，只要模块边界清晰、能对应到设计方案即可。

```txt
|-- README.md              # 说明文档，包含设计、安装、使用
|-- go.mod                 # Go module 定义
|-- build.sh               # 构建项目
|-- clean.sh               # 清理中间产物
|-- coverage.sh            # 运行单测覆盖率
|-- format.sh              # 格式化 Go 代码
|-- lint.sh                # 静态检查
|-- start.sh               # 启动服务
|-- stop.sh                # 停止服务
|-- data                   # 服务运行时数据
|-- docs                   # 各模块说明与架构设计文档
|-- cmd
|   `-- trpc-service       # 命令行入口，可直接启动服务
`-- trpcservice            # 源码
    |-- agent              # 基于 tRPC-Agent-Go 的 Agent 定义
    |-- channels           # 对接 IM 的 Channel Adapter
    |-- config             # 租户与节点配置
    |-- log                # 日志级别与脱敏
    |-- metrics            # 监控指标
    |-- skill              # 可运行的 Skill
    |-- tenant             # 多租户模型与隔离
    |-- tool               # 平台 Tool
    |-- version.go         # 版本信息
    |-- web                # 管理 / 对话页面
    `-- workspace          # 工作目录，包含本地、容器等沙箱环境
```

## 快速开始

```bash
git clone https://github.com/liuzengh/trpc-agent-service.git
cd trpc-agent-service

docker compose up --build
```

启动前复制 `.env.example` 为 `.env`，并设置真实 `DEEPSEEK_API_KEY`。`.env` 已被
Git 忽略，不能提交。Compose 会启动 PostgreSQL、Redis、一次性 migration 和使用
DeepSeek OpenAI-compatible API 的服务进程。默认 HTTP token 是
`local-secret`，仅用于本机联调：

```bash
curl -H 'Authorization: Bearer local-secret' \
  -H 'X-Channel-Binding: demo-http' \
  -H 'Content-Type: application/json' \
  -d '{"channel":"http","from":"demo-user","message_id":"m-1","text":"calculate 6*7"}' \
  http://127.0.0.1:8080/v1/gateway/messages
```

直接运行二进制时需要设置 `TRPC_AGENT_POSTGRES_DSN`、`TRPC_AGENT_REDIS_URL`、
`DEEPSEEK_API_KEY` 和通道凭据，再执行 `./start.sh`。PR3 的确定性 Runner 示例仍可用
`go run ./examples/quickstart` 单独运行，它不代表服务部署方式。

## 交付状态

设计中的组件按以下状态区分，避免把规划能力误读为已交付能力。

**已经实现并验证**：

- 企业微信真实端到端链路：回调验签/解密 → Inbox 幂等 → tRPC-Agent-Go Runner → DeepSeek → PostgreSQL Session/Memory/Event → Outbox → 企业微信回复。
- 多节点消息运行时：Redis Streams consumer group 即时调度、PostgreSQL Inbox/fencing/Outbox、崩溃恢复与 DLQ、共享状态/取消、共享预算/审批、节点心跳以及 Redis 跨节点限流与事件总线。
- 控制面数据模型与不可变配置版本、治理审计、OpenTelemetry 链路、Compose 最小部署。

**本 PR（PR9：生产控制面与动态配置发布）实现**：

- 生产 Admin API：`validate` / `publish` / `list` / `current` / `rollback`，全部要求 Bearer 认证与显式租户 scope，客户端无法通过请求体或参数切换租户。
- `expected_version` 乐观锁（并发发布只有一个成功，其余 409）、不可变版本（回滚创建新版本并记录 `rollback_of`、`created_by`、`content_hash`、`published_at`）。
- 发布/回滚审计日志（tenant、actor、action、版本、decision、error_type、latency、trace_id、timestamp）。
- 动态 Runtime Bundle 切换：入站路由、企业微信回调绑定、Runtime 快照和出站 Sender 都按请求钉住控制面版本；新请求使用新版本，旧请求及其 Outbox 回复继续使用旧版本，旧 Bundle 在引用归零后 drain 并 Close；切换失败沿用上一份有效配置并重试初始化。
- disabled 租户 / App / Binding 立即拒绝新请求；生产配置缺少共享 PostgreSQL 后端时 fail fast，Admin 发布非持久化存储配置会被直接拒绝。
- 配置发布后无需 `docker compose down -v`、无需删除数据卷、无需重建环境；启动文件只在首次启动时播种，之后数据库是唯一事实源。

**本 PR（PR10：飞书 Channel Adapter 与 Sender）实现**：

- 飞书事件订阅回调：URL verification 挑战应答、`X-Lark-Signature` 原始请求体签名校验、Verification Token 常量时间校验、Encrypt Key（AES-256-CBC）解密，事件订阅 v2 `im.message.receive_v1` 转换为统一 `gateway.InboundMessage`。
- 多租户消歧：多个租户可共享飞书 app_id 或 binding_id；加密回调以服务端 Encrypt Key 验签和解密后，再通过 Verification Token 与 app_id 唯一匹配，无法唯一匹配时返回 401；disabled 租户/App/Binding 返回 404。
- 身份与 session：open_id 优先（union_id 兜底，不用昵称），`user_id` 带 channel + binding 范围，单聊/群聊 session 稳定，与企业微信相同外部 ID 永不冲突。
- 消息解析：文本（剥离 @机器人提及占位）、图片/文件安全元数据（不含 image_key/file_key、不访问外部 URL，预留 `MediaDownloader` 扩展点），不支持的事件安全 ACK。
- 飞书 Sender：tenant_access_token 并发安全缓存、提前一分钟刷新、失效强制刷新重试一次；单聊/群聊文本回复、4096 字节 UTF-8 安全分片；可重试/永久/uncertain 错误分类；完整复用 Outbox、Delivery Worker、Redis 限流、重试和 DLQ。
- 动态配置：飞书 binding 的发布、禁用、回滚即时生效；Outbox 按入口钉住的 config_version 解析旧 Sender；canary secret 不出现在响应、日志、错误或审计中。
- 真实飞书端到端联调尚未执行：缺少飞书测试账号、公网回调域名与平台事件订阅配置，步骤见 [docs/feishu.md](docs/feishu.md)；本 PR 不声明真实 E2E 已通过。

**本 PR（PR11：跨节点共享调度与控制）实现**：

- Gateway 和 Inbox recovery poller 都把已 claim 的 `RunRequest` 投递到 Redis Streams consumer group；多个 Worker 节点竞争消费，PostgreSQL Inbox 仍是事实源并在流消息丢失或节点崩溃后恢复。
- PostgreSQL `run_statuses` 保存跨节点请求状态和取消意图；Redis cancel bus 把取消命令即时广播给持有 Runner 的节点，Worker 同时轮询持久化意图作为 Pub/Sub 丢失兜底。
- PostgreSQL 原子预算预留/核销和共享工具审批替代生产进程内 Store；不同 Gateway/Worker 节点可查询状态、批准工具并执行取消，不要求 sticky session。
- `worker_nodes` 保存唯一节点 ID、心跳、draining 和停止时间；重复的活跃 `TRPC_AGENT_NODE_ID` 启动失败。关闭时先标记 draining、停止接收；未完成请求释放/失去 lease 后由其他节点有界接管。
- PostgreSQL/Redis Compose 集成测试可用 `docker compose --profile test run --rm --build integration-test` 重复执行，不要求删除数据卷。

**本 PR（PR12：多后端 Storage Router 与数据迁移 Worker）实现**：

- Runtime Bundle 按不可变配置版本分别路由 Session/Summary、Memory 和 Artifact；各数据域可以使用平台 PostgreSQL或由 SecretRef 指向的独立 PostgreSQL 集群。外部连接池按 SecretRef 身份缓存并在服务关闭时释放。
- `migration_target` 启用读主库、同步双写目标库；Session 与 Summary 必须使用完全相同的主路由和迁移目标。目标写失败显式返回错误，不允许静默形成不可见的数据缺口。
- PostgreSQL `migration_jobs` 保存租户、App、配置版本、domain、claim lease、checkpoint、行数、重试和 error type；多个节点使用 `SKIP LOCKED` 竞争批量 backfill，目标端 ledger 与数据写入同事务，checkpoint 丢失后重放也不会重复插入。
- Admin API 支持计划、列表、查询和取消迁移。source/target 只取自已发布配置，客户端不能上传 DSN 或切换租户；响应、错误和审计均不返回 SecretRef 或解析后的数据库凭据。
- cutover 必须满足：旧版本已声明目标、对应迁移任务 completed、copied rows 不少于 source snapshot，并通过 expected version。任意改路由或直接回滚到可能陈旧的源库都会被拒绝，需先执行反向迁移。
- 服务启动、配置 validate/publish 会连接并检查所有路由所需表；SecretRef 缺失、目标不可达或未执行 schema migration 时 fail fast。完整操作步骤见 [Storage Router 与迁移](docs/storage-migrations.md)。

**本 PR（PR13：Knowledge/RAG 与对象存储）实现**：

- 可选的租户/App 级 Knowledge 配置使用 OpenAI-compatible Embedding，向量后端支持 PGVector 与 Qdrant；API Key 和 Qdrant Key 只允许通过 SecretRef 解析。
- 受 Admin 认证与 tenant scope 保护的文本 ingest/search API 完成 chunk、embedding、upsert 与检索；服务端强制覆盖 `tenant_id`/`app_id` metadata，并为每个租户/App 派生独立物理 table/collection，客户端不能扩大作用域。
- 启用 Knowledge 的 App 必须把 `knowledge_search` 加入工具白名单；该工具随不可变 Runtime Bundle 构建，新配置发布后新请求使用新索引配置，旧请求继续使用旧 Bundle 并有界关闭连接。
- Artifact 生产路由新增 S3-compatible（含 MinIO）。S3 revision 分配使用共享 PostgreSQL advisory lock 跨节点串行化；PostgreSQL → S3 支持双写、可恢复 backfill、外部写入后 ledger 对账与受控 cutover。
- 配置 validate/publish 和启动 preflight 会验证 PGVector/Qdrant、S3 凭据与可达性；初始化或切换失败不会替换上一份有效 Bundle，错误不包含解析后的凭据。配置和操作说明见 [Knowledge/RAG 与 S3 Artifact](docs/knowledge.md)。
- 本 PR 不交付 MCP、业务 Tool、复杂 PDF/OCR 文档流水线、Knowledge 跨向量后端自动迁移或真实飞书联调；这些不能写成已经实现。

**本 PR（PR14：持久化治理与可观测性）实现**：

- 生产进程安装 OpenTelemetry SDK Provider，经 OTLP/gRPC 向 Collector 导出 trace 与 metrics；所有 HTTP/企业微信/飞书入口统一提取 `traceparent`，关闭时有界 flush。
- Collector 使用内存保护和 batch pipeline，Prometheus 持久化 15 天指标，Grafana 自动 provision 数据源和租户请求、p95、Inbox/Outbox/DLQ、token/cost dashboard。
- 新增模型首事件耗时、低基数队列深度、活跃 Worker、PostgreSQL 健康与延迟指标；request/user/session/message ID 不进入 metrics label。
- 审计保留策略由后台 Worker 自动执行，多节点通过 PostgreSQL advisory lock 保证每轮只有一个节点清理；策略始终来自当前已发布配置。
- Compose 的 trace 默认由 Collector `debug` exporter 接收，便于验证但不作为长期存储；生产需要把该 exporter 替换为 Tempo、Jaeger 或托管 OTLP 后端。完整说明见 [生产可观测性](docs/observability.md)。

**后续计划**：

- PR15：Kubernetes、容量测试、故障演练和生产验收。

MCP、业务工具和复杂文档解析仍是未排期能力；本次 PR14 不把它们标成已完成。

## Admin API

生产 Admin API 与 Gateway 共用同一 HTTP 端口，由 `TRPC_AGENT_ADMIN_TOKENS`
配置管理员凭据，格式为 `名称=令牌:租户列表`，多个凭据用 `;` 分隔，`*` 表示全部租户：

```bash
export TRPC_AGENT_ADMIN_TOKENS='ops=change-me:demo;root=another-secret:*'
```

未配置该变量时 Admin API 拒绝一切请求（fail closed）。所有接口的租户范围只来自
URL 路径并校验凭据 scope：

```bash
# 校验配置（不在响应中返回配置原文或 SecretRef 真实值）
curl -X POST -H "Authorization: Bearer change-me" \
  --data-binary @tenant.yaml \
  'http://127.0.0.1:8080/v1/tenants/demo/configs/validate'

# 发布新版本（payload 中的 config_version 必须等于 expected_version + 1）
curl -X POST -H "Authorization: Bearer change-me" \
  --data-binary @tenant-v2.yaml \
  'http://127.0.0.1:8080/v1/tenants/demo/configs/publish?expected_version=1'

# 列出版本、查看当前发布版本、回滚到指定版本（创建新版本，不改写历史）
curl -H "Authorization: Bearer change-me" http://127.0.0.1:8080/v1/tenants/demo/configs
curl -H "Authorization: Bearer change-me" http://127.0.0.1:8080/v1/tenants/demo/configs/current
curl -X POST -H "Authorization: Bearer change-me" \
  'http://127.0.0.1:8080/v1/tenants/demo/configs/rollback?expected_version=2&target_version=1'
```

API 响应只包含版本元数据（`version`、`content_hash`、`created_by`、`published_at`、
`rollback_of`），永不返回配置原文或 SecretRef 解析值。发布后新请求立即使用新版本，
处理中的企业微信消息继续在旧 Bundle 上完成，不被中断。

总体设计从 [`docs/architecture.md`](docs/architecture.md) 开始，生产风险与缓解措施见
[`docs/risks.md`](docs/risks.md)。控制面数据模型见
[`docs/data-model.md`](docs/data-model.md)，Runtime
Bundle 的版本切换与生命周期约束见 [`docs/runtime.md`](docs/runtime.md)，多节点
Inbox/fencing/Outbox 与 OpenClaw HTTP 链路见
[`docs/message-runtime.md`](docs/message-runtime.md)，企业微信回调验签、消息规范化和
主动回复见 [`docs/wecom.md`](docs/wecom.md)，飞书事件订阅验签解密、身份映射和
Sender 见 [`docs/feishu.md`](docs/feishu.md)。
PostgreSQL + Redis 的 Compose 启动、验证和生产拓扑边界见
[`docs/deployment.md`](docs/deployment.md)。

PR7 的企业微信协议测试和 PR10 的飞书协议测试都不需要真实账号：

```bash
go test ./trpcservice/channels/wecom ./trpcservice/channels/feishu
```

Docker 可用时，可真实验证 PostgreSQL migration 的首次 up、重复 up、down 和再次 up：

```bash
./scripts/postgres_migrations_test.sh
```

停止 Compose：

```bash
docker compose down
```
