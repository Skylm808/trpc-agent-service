# Kubernetes 验收报告模板

- 日期：
- Git commit / image digest：
- Kubernetes context / 版本：
- Gateway / Worker 副本数：
- PostgreSQL / Redis 拓扑与 PVC 数：
- 容量场景、请求数、并发数、错误率、p95：
- 完整合成消息链路：通过 / 失败
- 单 Gateway Pod：通过 / 失败
- 单 Worker Pod：通过 / 失败
- Redis：通过 / 失败
- PostgreSQL：通过 / 失败
- Model：通过 / 失败
- Sender retry / DLQ：通过 / 失败
- Collector：通过 / 失败
- PDB / HPA：通过 / 失败
- 滚动升级 / rollback：通过 / 失败
- 配置版本重启前后：
- PVC 验收前后：
- 回滚点：
- 剩余风险与负责人：

只记录聚合指标、不可逆摘要和结论。不得粘贴 Secret、SecretRef 解析值、数据库 DSN/密码、
IM token、模型密钥、下载 URL、media key、用户消息正文、文件正文或含上述内容的日志。
