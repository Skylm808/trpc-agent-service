# Knowledge/RAG 与 S3 Artifact

PR13 提供最小生产闭环：管理员把文本写入租户/App 的知识库，服务调用 OpenAI-compatible Embedding 并写入 PGVector 或 Qdrant；启用了 `knowledge_search` 的 Agent 可在运行时检索。它不是完整的文档管理系统，不包含 PDF/OCR、网页抓取、MCP 或业务 Tool。

## Knowledge 配置

Knowledge 默认关闭。启用时必须同时配置 embedding、共享向量后端，并显式允许工具：

```yaml
tools:
  allow: [calculator, knowledge_search]
knowledge:
  enabled: true
  embedding:
    provider: openai-compatible
    model: text-embedding-3-small
    base_url: https://embedding.example/v1
    api_key: {provider: env, key: EMBEDDING_API_KEY}
    dimensions: 1536
  max_results: 8
  min_score: 0.2
storage:
  knowledge:
    type: qdrant
    endpoint: grpcs://qdrant.example:6334
    namespace: support_docs
    credential: {provider: env, key: QDRANT_API_KEY}
```

PGVector 使用 `type: postgres`；其目标 PostgreSQL 必须预装 `vector` extension，SecretRef 可像其他路由一样解析独立 DSN。发布阶段会建立并检查对应 table/collection。物理名称由配置 namespace 加 tenant/App 摘要派生，检索还会强制添加可信 `tenant_id` 与 `app_id` metadata filter；请求体里的同名字段会被覆盖。

Admin 路径继续使用 `TRPC_AGENT_ADMIN_TOKENS` 的 Bearer 认证和 URL tenant scope：

```http
POST /v1/tenants/demo/apps/assistant/knowledge/documents
Authorization: Bearer <admin-token>
Content-Type: application/json

{"document_id":"runbook-1","name":"Runbook","content":"...","metadata":{"topic":"operations"}}
```

文本上限 1 MiB，按 Unicode rune 分片并带重叠；chunk ID 由 tenant、App、document ID 和序号确定，重复 ingest 是 upsert。检索端点是同一路径下的 `/search`，请求字段为 `query`、`max_results`、`min_score`。API 不接受 tenant_id、后端 endpoint 或凭据。

## S3-compatible Artifact

Artifact 路由可使用 `type: s3`，`endpoint` 是 HTTPS S3 API 地址，`namespace` 是 bucket。Credential SecretRef 解析出的值是 JSON，字段为 `access_key_id`、`secret_access_key`、`region`，MinIO 还应设置 `path_style: true`；可选 `session_token`。真实 JSON 只存在密钥提供方，不写入 YAML、日志、trace、审计或 API 响应。

官方 S3 Artifact Adapter 的 revision 分配本身不是并发安全的，因此平台在共享 PostgreSQL 上取得按 app/user/session/filename 派生的 advisory lock，再执行 list-and-put。多个 Worker 节点仍能得到单调 revision，不需要 sticky session。

PostgreSQL Artifact 迁往 S3 时使用现有 `migration_target` 流程。新 Bundle 同步双写，Migration Worker 按租户/App 扫描 PostgreSQL revision 并写入 S3；若外部写入成功后 Worker 丢失 lease，重放会先比对目标 revision，再补平台 ledger，不会创建额外 revision。S3 目标冲突、缺失旧 revision 或内容不一致会让任务失败，禁止 cutover。当前不支持 S3 反向枚举迁回 PostgreSQL，也不支持 PGVector/Qdrant 之间自动迁移；需要重新 ingest 到新索引并做召回验证后再发布切换。
