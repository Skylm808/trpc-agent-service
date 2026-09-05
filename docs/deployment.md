# PostgreSQL + Redis 部署与验证

## 最小可运行环境

根目录的 `docker-compose.yml` 启动 PostgreSQL、Redis、一次性 migration、兼容模式的合并 Gateway/Worker，以及 OpenTelemetry Collector、Tempo、Prometheus、Grafana；opt-in `multinode` profile 另提供一个 Gateway 和两个 Worker 的 PR20 验收拓扑。Kubernetes 基线使用 `--role gateway` 与 `--role worker` 拆成独立 Deployment。服务启动时把 `configs/example.yaml` 当作**种子**：租户在控制面还没有任何已发布版本时才写入 version 1；一旦通过 Admin API 发布过新版本，数据库就是唯一事实源，重启不再校验文件与数据库一致，也不要求重建环境。启动文件中的版本号超过数据库已发布版本时拒绝启动，避免节点使用未发布配置。

首次启动前执行 `cp .env.example .env`，然后只在本机 `.env` 中填写
`DEEPSEEK_API_KEY`。生产 Runtime 使用 tRPC-Agent-Go 的 OpenAI-compatible Model
访问 DeepSeek；`mock` provider 会被生产组合拒绝。

```bash
docker compose up --build -d
docker compose ps
```

默认 HTTP token 为 `local-secret`，只用于本机联调：

```bash
curl -H 'Authorization: Bearer local-secret' \
  -H 'X-Channel-Binding: demo-http' \
  -H 'Content-Type: application/json' \
  -d '{"channel":"http","from":"demo-user","message_id":"demo-1","text":"calculate 6*7"}' \
  http://127.0.0.1:8080/v1/gateway/messages
```

查询结果时使用响应里的 `request_id`：

```bash
curl -H 'Authorization: Bearer local-secret' \
  -H 'X-Channel-Binding: demo-http' \
  'http://127.0.0.1:8080/v1/gateway/status?request_id=demo/demo-http/demo-1'
```

这条链路实际使用 PostgreSQL 保存 Inbox、tRPC-Agent-Go Session/Memory、平台 Event/Summary/Memory 投影、Artifact、Outbox 和 Audit；Redis 负责 lease、fencing token 和 SSE 事件总线。同一个 `message_id` 再次发送会命中 PostgreSQL 唯一键并返回 `duplicate=true`。

## 配置发布（PR9 生产控制面）

Admin API 与 Gateway 共用 HTTP 端口，所有 `/v1/tenants/{tenant_id}/configs*` 请求必须携带
`TRPC_AGENT_ADMIN_TOKENS` 中配置的 Bearer 凭据，且凭据的租户 scope 必须覆盖 URL 中的租户
（格式：`名称=令牌:租户-a,租户-b;ops=令牌2:*`；未配置时拒绝一切请求）。支持
`validate`、`publish`（`expected_version` 乐观锁）、`list`、`current` 和 `rollback`
（`expected_version` + `target_version`）。发布和回滚都创建新的不可变版本并写审计日志；
响应只含版本元数据，不包含配置原文或 SecretRef 解析值。

发布后无需 `docker compose down -v`、无需删除数据卷、无需修改启动文件：入站路由、
企业微信回调绑定、Runtime 快照和出站 Delivery Sender 都从控制面数据库解析。新请求
立即使用新版本 Bundle，处理中的请求和它产生的 Outbox 回复继续使用入口钉住的旧版本，
旧 Bundle 在引用归零后被 drain 和 Close；disabled 租户、App 或 Binding 从下一个请求起
被拒绝。新版本 Bundle 初始化失败时，钉住该版本的请求进入 Inbox 重试；旧 Bundle 只继续
服务原本钉住旧版本的请求，禁止新版本请求静默回退到旧模型、工具或配置。

## 配置和密钥

直接运行二进制需要设置：

- `TRPC_AGENT_POSTGRES_DSN`：完整 PostgreSQL DSN，只从环境或 Secret 挂载注入；
- `TRPC_AGENT_REDIS_URL`：Redis URL；
- `TRPC_AGENT_NODE_ID`：节点唯一标识；未设置时使用 hostname，Kubernetes 推荐注入 Pod UID；
- `TRPC_AGENT_WORKER_CONCURRENCY`：每节点 Worker 并发，默认 8，生产合法范围 1–256；非法值启动失败；
- `TRPC_AGENT_SHUTDOWN_TIMEOUT`：收到 SIGTERM 后排空组件的总上限，默认 10 秒、合法范围 1 秒到 10 分钟；Kubernetes 基线设置 100 秒并保留 preStop/退出余量；
- `DEEPSEEK_API_KEY`：DeepSeek API Key，由模型配置中的 SecretRef 引用；
- `TRPC_AGENT_GATEWAY_TOKEN_<BINDING_ID>`：HTTP Channel token；
- `TRPC_AGENT_ADMIN_TOKENS`：Admin API 管理员凭据（`名称=令牌:租户列表`，`;` 分隔，`*` 表示全部租户）；未配置时 Admin API 拒绝一切请求；
- `OTEL_EXPORTER_OTLP_ENDPOINT`：OTLP/gRPC Collector 地址；Compose 固定为内部 `otel-collector:4317`；
- `TRPC_AGENT_TRACE_SAMPLE_RATIO`：0 到 1 的 parent-based trace 采样率，Compose 默认 0.1；
- 企业微信启用后，还需要配置文件所引用的 `WECOM_CALLBACK_TOKEN`、`WECOM_APP_SECRET`
  和 `WECOM_ENCODING_AES_KEY`。Delivery Worker 会自动使用应用 Secret 获取 access token。
- 飞书启用后，还需要配置文件所引用的 `FEISHU_VERIFICATION_TOKEN`、`FEISHU_APP_SECRET`
  和可选的 `FEISHU_ENCRYPT_KEY`。Delivery Worker 自动使用 App Secret 获取并缓存
  tenant_access_token。飞书回调地址为 `/channels/feishu/{binding_id}`，协议细节见
  [飞书 Channel Adapter](feishu.md)。

Compose 中的默认数据库密码和 HTTP token 只供本地使用。共享环境应通过 `.env`、Docker Secret 或外部密钥系统覆盖，不能提交真实值。

Prometheus 位于 `http://127.0.0.1:9090`，Tempo 位于 `http://127.0.0.1:3200`，Grafana 位于 `http://127.0.0.1:3000`。Compose 的 Grafana 仅开放匿名 Viewer，仍只适合本机；共享或公网部署必须在反向代理层增加身份认证。指标、trace 和保留策略见[生产可观测性](observability.md)。

## 生产推荐拓扑

Kubernetes 基线已把 Gateway 与 Runner Worker 分开扩容：Gateway 挂载 Channel/Admin HTTP，Worker 只消费 Redis Stream。Outbox Delivery、Storage migration Worker 和审计 retention 当前仍随 Worker 运行，尚未成为独立角色。PostgreSQL 与 Redis 使用托管高可用集群。migration schema 仍作为单独 Job 执行；迁移在事务内获取 PostgreSQL advisory lock，业务 Pod 不负责建表。Session 和 Memory 不保存在 Worker 本地，因此不要求 sticky session。

默认 Compose 保留 Gateway 与 Worker 合并的 `all` 兼容进程；PR20 `multinode` profile 使用
独立 `gateway`、`worker-a`、`worker-b`，共享同一 PostgreSQL、Redis 和现有命名卷。Gateway
不构造 Runner，Worker 不暴露回调端口。生产路径使用 Redis Streams consumer group，
不依赖进程内 dispatcher 或 sticky session。每个 Worker 从 PostgreSQL 竞争捞取到期 retry
和 lease 已过期的 Inbox；进程在 ACK 后任意位置崩溃，其他节点都能生成新 claim 并重新投递。
claim 使用 `SKIP LOCKED`，同 session 按 `inbox_seq` 提交，超过最大尝试次数进入 DLQ；旧 token
在调用模型前或写入时被拒绝。

企业微信与飞书 Outbox 已装配 PostgreSQL 多节点 claim、租约、重试、DLQ、结果不确定隔离和
Redis 跨节点限流。默认 Compose 使用 `all`；多节点 profile 和 Kubernetes 中 Delivery 随 Worker 运行。
独立 Delivery Deployment 需要扩展新的受约束角色和专属探针，当前尚未实现。

跨节点请求状态、取消意图、预算、人工审批和节点心跳均保存在共享后端；取消命令另用 Redis
Pub/Sub 做低延迟通知。PR17 已提供受认证、租户隔离并带审计的 `uncertain` / DLQ Admin
运维 API，操作流程见[消息故障恢复](message-recovery.md)；Web 运维页面仍未实现。
PR18 已实现按租户动态 Runner 并发配额，PR19 已拆分 Gateway/Worker，PR20 已加入 Compose
多节点验收。企业微信与飞书真实 E2E 均已人工通过；新环境仍需部署方配置真实账号、公网
HTTPS 回调和平台网络策略。尚未生产化的部分包括 Delivery/maintenance 独立角色和基于队列
自定义指标的自动扩缩容控制器。

## PR20 多节点 Compose 验收

必须复用既有项目名以复用命名卷；下面的命令只创建或更新明确服务，不执行 `down -v`：

```bash
export TRPC_AGENT_COMPOSE_PROJECT=trpc-agent-service-pr14-check
docker compose -p "$TRPC_AGENT_COMPOSE_PROJECT" --profile multinode \
  up -d --build gateway worker-a worker-b
./scripts/pr20_multinode_acceptance.sh
```

脚本默认只读。需要跑独立测试 tenant 的 PostgreSQL/Redis 集成检查时显式设置
`TRPC_AGENT_ACCEPTANCE_RUN_INTEGRATION=1`；只做定点 Worker 重启与持久性检查可设置
`TRPC_AGENT_ACCEPTANCE_RESTART_WORKER=1`。需要产生模型成本的合成消息、双 Worker 分布和
定点重启检查时，再提供测试 tenant/binding/token 并设置
`TRPC_AGENT_ACCEPTANCE_RUN_MESSAGES=1`。若同一项目仍运行旧 `service --role all`，主动故障验收
还需设置 `TRPC_AGENT_ACCEPTANCE_ISOLATE_TOPOLOGY=1`；脚本会临时停止该明确服务并在退出时恢复。
除 `service` 和 `worker-a` 外不会停止其他容器，更不会停止数据库、清空表或删除卷。证据记录
使用[脱敏报告模板](production-acceptance-report.md)。

共享后端集成测试不需要暴露 PostgreSQL/Redis 到宿主机：

```bash
docker compose --profile test run --rm --build integration-test
```

测试覆盖 Redis 双节点 consumer group、PostgreSQL 状态/取消、节点 ID 冲突与重启、共享预算/
审批、跨节点租户并发配额、Storage Migration checkpoint/cutover，以及 Inbox/Outbox/配置版本回归；使用唯一测试作用域，可在保留数据卷时重复执行。

## Storage Router 与迁移

PR12 支持把 Session/Summary、Memory、Artifact 分别路由到不同 PostgreSQL 集群。每个外部目标都必须先运行同版本 migration；DSN 只能由 `credential: SecretRef` 提供。启动和 Admin validate/publish 会执行连接与表检查，缺少凭据、目标不可达或 schema 不完整都会拒绝启动/发布。

PR13 增加 PGVector/Qdrant Knowledge 和 S3-compatible Artifact。启用 Knowledge 或 S3 的配置在 Admin publish 时会解析 SecretRef 并执行有界连接预检；失败时保留当前发布版本和 Runtime Bundle。具体配置见 [Knowledge/RAG 与 S3 Artifact](knowledge.md)。

外部 Memory 使用 `type: external`、固定 HTTPS `endpoint` 和 Bearer `credential`；每个请求由
Adapter 注入可信 tenant/App scope。Audit 主存储仍是 PostgreSQL，可把 `migration_target` 配成
`external`，将脱敏后的审计 envelope 同步 POST 到 WORM 归档。两类客户端都有超时、响应大小
限制，错误不会回显 endpoint、Secret 或正文。

PR16 增加生产 MCP Registry 与固定 HTTPS JSON 业务工具。Admin validate/publish 和进程启动会对启用的 MCP 服务执行 Initialize/ListTools；运行中发布新版本时，新 Bundle 建立独立连接，旧 Bundle 引用归零后关闭旧连接。MCP 与业务工具凭据只通过 SecretRef 注入，部署环境需提供对应 secret；配置与网络边界见 [生产 MCP 与业务工具](mcp-tools.md)。

迁移不能直接修改 endpoint。先发布带 `migration_target` 的双写配置，再调用受认证的 migration API backfill；任务 completed 后才发布下一版本完成 cutover。旧源库保留只读回滚窗口，不会自动清理。命令与故障恢复步骤见 [Storage Router 与迁移](storage-migrations.md)。

## Kubernetes、容量和验收

Kubernetes 有序部署与 Secret 合约见 [manifest 说明](../deploy/kubernetes/README.md)。
`/healthz` 只用于 liveness；`/readyz` 有界检查 PostgreSQL/Redis，用于 Service readiness。
容量估算和工具见[容量测试](capacity.md)，故障注入见[演练手册](fault-drills.md)，上线硬门禁见
[生产验收](production-acceptance.md)。HPA 的 CPU/内存只是初始基线，不能代替 Provider 配额、
完整执行耗时、queue depth 和 Outbox backlog 的容量判定。

停止环境但保留数据：

```bash
docker compose down
```

数据卷清理由环境所有者单独审批，本手册不把删除数据作为部署或验收步骤。
