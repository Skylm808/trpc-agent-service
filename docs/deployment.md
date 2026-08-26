# PostgreSQL + Redis 部署与验证

## 最小可运行环境

根目录的 `docker-compose.yml` 启动四个组件：PostgreSQL、Redis、一次性 migration 和合并部署的 Gateway/Worker。服务启动时会把 `configs/example.yaml` 中的租户、App 和 Channel Binding 写入控制面表；配置文件版本和数据库已发布版本不一致时拒绝启动，避免节点使用未发布配置。

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

## 配置和密钥

直接运行二进制需要设置：

- `TRPC_AGENT_POSTGRES_DSN`：完整 PostgreSQL DSN，只从环境或 Secret 挂载注入；
- `TRPC_AGENT_REDIS_URL`：Redis URL；
- `TRPC_AGENT_NODE_ID`：节点唯一标识；未设置时使用 hostname，Kubernetes 推荐注入 Pod UID；
- `DEEPSEEK_API_KEY`：DeepSeek API Key，由模型配置中的 SecretRef 引用；
- `TRPC_AGENT_GATEWAY_TOKEN_<BINDING_ID>`：HTTP Channel token；
- 企业微信启用后，还需要配置文件所引用的 callback token、应用 Secret 和 EncodingAESKey。

Compose 中的默认数据库密码和 HTTP token 只供本地使用。共享环境应通过 `.env`、Docker Secret 或外部密钥系统覆盖，不能提交真实值。

## 生产推荐拓扑

生产环境把 Gateway、Worker、Outbox Delivery Worker、Channel Adapter 和 Admin API 分开扩容，PostgreSQL 与 Redis 使用托管高可用集群。migration 作为单独 Job 执行；迁移在事务内获取 PostgreSQL advisory lock，业务 Pod 不负责建表。Session 和 Memory 不保存在 Worker 本地，因此不要求 sticky session。

当前 Compose 是单进程最小链路：Gateway 到 Worker 的快速路径仍使用进程内 dispatcher，
但每个节点都会从 PostgreSQL 竞争捞取到期 retry 和 lease 已过期的 Inbox，因此进程在
ACK 后、执行前崩溃，重启或其他节点可恢复任务。claim 使用 `SKIP LOCKED`，同 session
按 `inbox_seq` 执行，超过最大尝试次数进入 DLQ；旧 token 在调用模型前会被拒绝。

仍未生产化的部分包括：Gateway 与 Worker 独立进程之间的即时共享队列、跨节点请求状态、
共享预算与人工审批存储，以及 Outbox claim、重试、DLQ 投递进程。当前 Outbox 只持久化为
`pending`；真实企业微信主动回复应在 Outbox 消费进程装配后验收。

停止环境但保留数据：

```bash
docker compose down
```

连同本地测试数据卷一起清理：

```bash
docker compose down -v
```
