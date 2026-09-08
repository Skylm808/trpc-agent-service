# 关键模块覆盖率

- 状态：PASS
- 入口：`scripts/coverage_acceptance.sh`
- 口径：默认短测，不依赖真实 PostgreSQL、Redis 或外部平台
- 最近复验：2026-09-08，基线 `e290260`

| 模块 | 调整前 | 当前 | CI 最低门槛 |
| --- | ---: | ---: | ---: |
| `recovery` | 0.0% | 20.3% | 15% |
| `backend` | 7.5% | 26.9% | 20% |
| `storage` | 14.7% | 19.8% | 18% |
| `worker` | 23.8% | 59.9% | 50% |

新增测试覆盖恢复状态约束、后端 fail-closed、Storage 路由边界，以及 Worker 从 claim、
Runner 到 Event/Summary/Memory/Outbox 的成功链路。PostgreSQL/Redis 集成测试由 CI 的独立
容器步骤执行，不计入这组短测百分比。
