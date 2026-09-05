# PR24 Kubernetes Demo 验收报告

- 日期：2026-09-05T13:42:20Z
- Git 基线：adfd3bbdefd76f93a707f2bf118f96d7912f4b5b+candidate
- Kubernetes context：kind-trpc-agent-pr24
- 镜像：sha256:36792783ff18（仅记录不可逆摘要）
- 拓扑：Gateway 3 副本、Worker 3 副本、PostgreSQL/Redis StatefulSet、Mock Model、OTel Collector
- 容量冒烟：health 100 请求、Runner 20 请求，失败 0，接入 p95 8.307ms，health p95 7.761ms；20 条均在 PostgreSQL Inbox 完成
- 单 Pod 恢复：通过
- PostgreSQL/Redis 故障与 readiness 恢复：通过
- Model 请求重试/Collector 故障恢复：通过
- Sender retry/DLQ 回归：通过
- PDB live status、HPA contract/admission、滚动升级与 rollback：通过
- PostgreSQL 重启后配置版本：1 → 1
- PVC：2 个，验收脚本未删除 namespace、PVC 或数据卷

报告不包含 Secret、用户消息正文、数据库密码、完整镜像 ID 或外部标识。
