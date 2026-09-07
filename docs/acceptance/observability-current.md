# 可观测性验收报告

- 日期：2026-09-07
- Git 基线：`05aa0a13bba79e9a553d8e7a890b968e8c633399+candidate`
- Compose project：`trpc-agent-service-pr14-check`
- 数据卷：保留；仅重启 Prometheus 以加载新增规则

| 验收项 | 结果 |
| --- | --- |
| Tempo OTLP exporter | PASS |
| Grafana Tempo 数据源 | PASS |
| Prometheus 配置与六条规则 | PASS |
| 错误率、DLQ、积压、无 Worker、PostgreSQL 告警 | PASS |
| 模型 usage 缺失告警 | PASS |
| Tempo、Prometheus、Grafana live health | PASS |
| Trace/Metric 脱敏结构测试 | PASS |

本轮 live 验收只读取健康状态、数据源 UID 和告警名称，没有发送业务消息，也没有保存 Trace payload、Metric 样本、用户正文、Header、SecretRef、DSN 或凭据。
