# 生产可观测性

PR14 把此前的 OpenTelemetry 埋点从 no-op 全局 Provider 接到生产链路：进程通过
OTLP/gRPC 向 Collector 输出 trace 和 metrics，Collector 执行 memory limiter 与 batch，
Prometheus 抓取 Collector 的 metrics exporter，Grafana 自动加载平台 dashboard。

## Compose 验收

```bash
docker compose up --build -d
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:9090/-/healthy
curl http://127.0.0.1:3000/api/health
curl 'http://127.0.0.1:9090/api/v1/query?query=up'
```

Grafana 默认 dashboard 位于 `Agent Platform / tRPC Agent Service`。Compose 开放匿名
Viewer 只是本机验收方式，不应直接暴露公网。Prometheus 使用命名卷保存 15 天数据；任何
清理卷的操作都必须由运维人员明确执行。

## Trace

HTTP Server middleware 为 Admin、Gateway、企业微信和飞书入口统一提取 W3C
`traceparent`。上下文继续穿过 Inbox、Redis Streams、Worker、Runner、model、Tool、
Session/Memory 和 Outbox。`TRPC_AGENT_TRACE_SAMPLE_RATIO` 使用 0..1 的 parent-based
采样，默认 0.1；上游已经采样的 parent 会保持采样决定。

Compose 的 Collector 把 trace 发给 `debug` exporter，只用于证明链路连通，不提供长期
查询。生产必须将 traces pipeline 的 exporter 换成 Tempo、Jaeger 或托管 OTLP，并根据
租户合规策略配置采样和保留期。OTLP header 只能使用标准 OpenTelemetry Secret 环境变量
或 Secret 挂载，不能写进租户 YAML。

## Metrics 与基数

指标只使用 `tenant.id`、`app.id`、`channel`、`operation`、`status` 以及有限的
`queue/domain/backend` 标签。user、session、request、message、trace ID 不进入标签。
当前包括：

- 请求量、操作延迟、模型首事件耗时、token 与成本；
- IM 投递成功/失败；
- 按租户的 Inbox、Outbox、DLQ 深度；
- 活跃 Worker；
- 平台 PostgreSQL 健康和 ping 延迟；
- 审计保留任务成功/失败。

生产告警至少覆盖错误率、模型 p95/p99、DLQ 非零、队列持续增长、无活跃 Worker、数据库
健康为零、Collector export failure 和审计保留任务失败。

## 审计保留

每个节点都可以启动 Retention Worker，但 PostgreSQL advisory lock 保证一轮只有一个节点
执行。Worker 只读取 `tenants.current_config_version` 指向的不可变配置；disabled tenant 或
`retention_days <= 0` 不删除。删除条件始终包含 `tenant_id` 和截止时间，租户之间不会互相
清理。

当前审计表是在线 PostgreSQL 存储。WORM 对象归档、法律保全和外置 SIEM 属于后续能力，
不能把 Prometheus 或 Collector debug exporter 当作审计归档。
