# 生产验收

生产验收以可复现证据为准，不以“Pod 已启动”为准。部署固定 commit/image digest，使用外部 Secret 管理、托管 PostgreSQL/Redis、独立 migration Job 和 TLS/WAF 入口。先运行只读检查：

```bash
export TRPC_AGENT_ACCEPTANCE_BASE_URL=https://agent.example.com
./scripts/production_acceptance.sh
```

脚本验证 `/healthz` 和会实际 Ping PostgreSQL/Redis 的 `/readyz`。Admin 检查是可选的，token 只从环境读取且响应保存到权限受控的临时目录：

```bash
export TRPC_AGENT_ACCEPTANCE_ADMIN_TOKEN='<from secret manager>'
export TRPC_AGENT_ACCEPTANCE_TENANT='acceptance-tenant'
./scripts/production_acceptance.sh
```

只有在已确认模型成本和测试租户后，才显式启用一条真实 Runner 消息。它不替代企业微信/飞书平台回调验收：

```bash
export TRPC_AGENT_ACCEPTANCE_GATEWAY_TOKEN='<from secret manager>'
export TRPC_AGENT_ACCEPTANCE_BINDING='acceptance-binding'
export TRPC_AGENT_ACCEPTANCE_RUN_MESSAGE=1
./scripts/production_acceptance.sh
```

## 发布门禁

- `./check.sh`、PostgreSQL 集成测试、Kubernetes manifest 校验和镜像构建均通过；migration Job 先完成，业务 Deployment 后发布。
- 三副本跨节点/可用区调度，PDB 与滚动更新生效；readiness 失败不会触发 liveness 重启风暴。
- Admin 未认证/越权、并发 publish、rollback 不可变性和 SecretRef 脱敏测试通过；当前配置版本与变更单一致。
- Session/Memory 为共享 PostgreSQL，调度/限流为 Redis；缺失或初始化失败必须 fail fast，不能出现 InMemory 回退。
- 容量测试达到批准的错误率、完整执行 p95、队列和数据库阈值，并留出一个 Pod 失效余量。
- 单 Pod、PostgreSQL、Redis、模型、Sender 和 Collector 故障场景有演练记录；告警能在目标时间内到达值班人员。
- 企业微信真实回调、幂等、Runner、Outbox 回复完成回归。飞书在账号接入前保持 binding disabled；启用前必须另做真实事件订阅、验签、token 刷新、单聊/群聊和文件消息验收。
- 日志、trace、HTTP 错误、审计和验收产物抽查无模型 Key、IM Secret/Token、EncodingAESKey、数据库密码或用户敏感正文。

任一硬门禁失败都停止发布并保留上一镜像和上一不可变配置版本。配置问题用 Admin rollback 创建新版本；二进制问题使用 Kubernetes rollout undo。两者都不得删除或重建 PostgreSQL/Redis 数据卷。
