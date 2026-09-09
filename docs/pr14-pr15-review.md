# PR #14/#15 取舍记录

本文记录对 [PR #14](https://github.com/liuzengh/trpc-agent-service/pull/14) 和 [PR #15](https://github.com/liuzengh/trpc-agent-service/pull/15) 的代码对比结果。目标是吸收能直接提升当前实现的生产能力，而不是替换已经稳定的多租户 Gateway、Inbox/Outbox、Worker 和企业微信/飞书适配器。

## 已有等价能力

| PR 中的做法 | 当前实现 | 结论 |
| --- | --- | --- |
| 入站去重、跨节点队列、Session fencing、取消与恢复 | `trpcservice/idempotency`、`cluster`、`sessioncoord`、`worker` | 已有，不重复引入第二套消息管线 |
| 企业微信/飞书签名、加密、UTF-8 分片、token 刷新、Outbox 重试 | `trpcservice/channels/wecom`、`channels/feishu`、`delivery` | 已有，保留现有协议边界 |
| SecretRef、工具白名单、预算、审计脱敏、trace 传播 | `secret`、`tool`、`policy`、`audit`、`metrics` | 已有，当前实现还增加了租户作用域与工具执行账本 |
| 健康检查、readiness、优雅关闭、PostgreSQL migration | `gateway/openclaw`、`app`、`repository`、`migrations` | 已有，继续由现有 CI 验证 |

## 本次吸收的局部改进

1. **外部 HTTP 凭据边界**：Secret Vault/KMS 和外部 Memory 适配器现在复制调用方 `http.Client`，强制禁用重定向。这样保留自定义 Transport/Timeout 的同时，避免 bearer/token header 被重定向到未审核主机。
2. **配置版本缓存上限**：`config.PublishedCache` 默认最多保留 256 个不可变版本，可通过 `NewPublishedCacheWithLimit` 为测试或受限部署设置更小上限。淘汰后从控制面重新加载，不改变版本语义，避免长期发布导致进程内存无限增长。
3. **回归验证**：新增重定向拒绝和缓存淘汰测试；真实 YAML、IM secret 和外部服务仍由本地环境/集成 CI 提供，不写入仓库。

## 暂不引入的内容

- PR #14 的完整新 Gateway/Storage/Worker 目录树：与当前已完成的 durable Inbox/Outbox 和 storage router 重复，整体移植会造成大规模重构。
- 飞书事件长连接、企业微信 AI-Bot 长连接：当前题目范围是企业微信回调与飞书事件订阅，新增长连接属于通道产品选择，不是当前 P0/P1 缺口；如上线需要，作为独立适配器评审。
- WebUI 专用 Redis Pub/Sub：当前生产验收链路以企业微信/飞书 Outbox 为准，WebUI 不是本次 IM 交付边界。
- PR 中仅用于演示的默认管理员凭据、明文 secret 或未经脱敏的“真实 E2E 已通过”声明。

## 验收边界

本次变更不改变现有数据模型和租户路由。验证重点是：配置版本仍能按租户和版本读取；外部 Secret/Memory 请求遇到 3xx 时不会跟随；企业微信/飞书现有协议测试和 CI 继续作为主验收入口。
