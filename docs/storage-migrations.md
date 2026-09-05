# Storage Router 与数据迁移

Storage Router 支持 Session/Summary、Memory、Artifact、Knowledge 和 Audit 独立选路。迁移任务覆盖 PostgreSQL 数据域、PGVector ↔ Qdrant Knowledge，以及 PostgreSQL ↔ S3 Artifact；Memory 还可选固定 HTTPS 外部服务，Audit 可同步追加到外部 WORM 归档。

## 安全迁移流程

1. 在目标 PostgreSQL 上使用相同版本的 `--migrate-only` 初始化 schema。不要在业务 Pod 内自动建表。
2. 发布新配置版本，在待迁移域保留当前主路由并添加 `migration_target`。Session 和 Summary 的主路由与目标必须完全一致。
3. 新请求开始读主库、同步双写目标库。目标写失败会让请求失败并进入现有 Inbox 重试，不会把双写错误当成功。
4. 通过 Admin API 创建 backfill。API 只接收 `app_id`、`domain` 和 `expected_version`；source/target 从服务端已发布配置读取。
5. Migration Worker 分批复制并更新 checkpoint。节点退出或 lease 过期后其他节点接管；目标端 ledger 保证同一源行重放幂等。
6. 状态达到 `completed` 后，发布下一配置版本，把原 migration target 设为主路由并移除 `migration_target`。控制面核对旧版本、目标身份、完成状态和行数后才允许 cutover。
7. 保留旧库作为只读回滚窗口。因为 cutover 后旧库不再接收写入，普通配置 rollback 会被拒绝；需要回退时必须反向执行同一流程。

计划迁移：

```http
POST /v1/tenants/demo/storage/migrations
Authorization: Bearer <admin-token>
Content-Type: application/json

{"app_id":"assistant","domain":"memory","expected_version":3}
```

查询和取消：

```http
GET  /v1/tenants/demo/storage/migrations
GET  /v1/tenants/demo/storage/migrations/{migration_id}
POST /v1/tenants/demo/storage/migrations/{migration_id}/cancel
```

取消只允许尚未运行或已经失败的任务。运行中的 batch 会有界结束并保存 checkpoint，不能在目标事务中途强行终止。响应只包含租户、App、配置版本、domain、状态、行数、尝试次数和时间，不包含 endpoint 配置正文、SecretRef 或解析后的 DSN。

## 一致性与故障恢复

- Session/Summary 使用同一路由，避免事件在一个集群而摘要在另一个集群。
- Backfill 只复制 canonical app_name 或显式 tenant/app 范围内的数据；两个租户使用相同用户、session 或文件名也不会互相读取。
- PostgreSQL 目标数据与 `storage_migration_items` 在一个事务中提交。S3 无法参与 SQL 事务，因此外部写成功后的重放会按指定 revision 读取并比对内容，再补平台 PostgreSQL ledger；冲突时 fail closed，不会继续创建 revision。
- `migration_jobs` 使用 owner/token/lease 精确更新；过期 Worker 无权提交新 checkpoint。
- 错误只保存 Go error type，不保存驱动错误正文，避免 DSN、数据库地址或密码进入 API、审计和日志。
- Knowledge 以 `runtime_knowledge_documents` 文档目录为事实源，在目标 PGVector/Qdrant
  重新 embedding/upsert；Artifact 以不含正文的版本目录枚举 S3 对象。两者都校验 checksum，
  源内容在 checkpoint 后变化会 fail closed。
- 完成校验是源快照行数与已处理行数。大规模生产切换仍应在回滚窗口内额外执行召回对比、
  Artifact 抽样读取和业务只读校验。

`domain` 可选 `session`、`memory`、`artifact`、`knowledge`。每项任务都从已发布配置读取
source/target，Admin 请求不能注入 endpoint 或 SecretRef。
