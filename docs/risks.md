# 生产风险清单

这份清单用于上线评审和故障演练。风险不是泛泛的可能性，每一项都对应当前架构中的具体状态或外部依赖。

| 编号 | 风险与触发条件 | 影响 | 缓解措施 | 监测与演练 |
| --- | --- | --- | --- | --- |
| R1 | Worker 发生长时间 GC、网络分区或 lease 续约失败，旧节点恢复后继续执行 | 同一 session 被两个节点写入，状态倒退或重复回复 | Redis 生成单调 fencing token；Session/Event/Outbox 写入在事务内校验 `last_fence`；旧 token 一律拒绝 | 统计 stale-fence rejection；故障演练中暂停 Worker 后转移 lease |
| R2 | 企业微信或飞书因超时重复投递，同一消息被多个 Gateway 同时接收 | 模型与工具重复调用，产生重复费用或副作用 | Inbox 唯一键包含 `tenant_id + binding_id + external_message_id`；首次 claim 才能入队；事件消息使用稳定派生 ID | 监控 duplicate claim 比例；并发回放同一 MsgId |
| R3 | 同一 session 的多条消息在队列中乱序到达 | 后发消息覆盖前序 state，Summary 截断位置错误 | 入站分配单调 `inbox_seq`；提交要求等于 `last_event_seq + 1`；乱序请求退避重试 | `out_of_order` 指标和热点 session 告警；随机打乱队列消息测试 |
| R4 | PostgreSQL 短暂不可用、连接池耗尽或热点 session 行锁竞争 | Gateway 无法 claim，Worker 无法提交，队列积压 | Gateway 快速返回可重试错误；连接池限额和超时；按租户限流；热点 session 串行消费；跨可用区主备 | 监控连接池等待、事务 p99、deadlock、Inbox backlog；主库切换演练 |
| R5 | Redis 主从切换、数据丢失或集群不可用 | lease、命令总线或热点缓存失效，Worker 暂停调度 | Redis 开启 AOF 和多副本；fence 最终仍由 PostgreSQL 写入校验；Redis 不作为 Session 事实来源；不可安全取 lease 时停止执行 | 监控 failover、AOF 延迟和 lease renewal；断开 Redis 验证 fail-closed |
| R6 | 模型超时、客户端断开后事件 channel 无人消费，或 Tool goroutine 忽略取消 | goroutine/连接泄漏，节点并发逐步耗尽 | 贯穿 `context.Context`；调用 `ManagedRunner.Cancel`；有界排空事件 channel；Tool 禁止启动无托管后台任务 | goroutine、活跃 Runner、取消耗时指标；注入永不返回的模型/工具 |
| R7 | Tool 已产生外部副作用，但 Worker 在记录结果前崩溃，重试再次调用 | 重复转账、重复建单或重复通知 | 危险 Tool 二次确认；传递 request/tool-call 幂等键；外部系统采用幂等写；不具备幂等能力的 Tool 标记为不可自动重试 | 审计 tool decision 和 call ID；崩溃点故障注入 |
| R8 | IM 回调超过平台时限、回复超过长度或触发频率限制 | 平台重复回调，回复丢失或账号被限流 | 回调只做验签、Inbox claim 和入队；回复从 Outbox 异步发送；按平台分片、租户限流、指数退避和 DLQ | 回调耗时、投递成功率、429/45009、Outbox backlog 告警 |
| R9 | 模型 API key、IM token、数据库密码出现在配置、错误或 trace 中 | 凭据泄露，攻击者跨租户调用外部服务 | 配置只保存 SecretRef；Vault/KMS/挂载文件解析；结构化日志字段白名单；错误和审计写入前统一脱敏 | CI 扫描 secret pattern；用 canary secret 跑日志/trace 泄漏测试 |
| R10 | Repository 查询漏写 `tenant_id`，或向量检索未加 tenant filter | 跨租户读取 Session、Memory、Knowledge 或审计数据 | Repository 方法强制 tenant 参数；复合 PK/FK；可选 PostgreSQL RLS；向量 collection/namespace 与 metadata 双重隔离 | 双租户相同 ID 测试；SQL 静态检查；定期越权演练 |
| R11 | 审计后端延迟或不可用 | fail-open 时审计缺口，fail-closed 时业务整体不可用 | 审计设置独立超时和缓冲；默认业务 fail-open 并告警；监管租户可选择 fail-closed；审计事件另存不可变归档 | audit failure、丢弃数和延迟告警；分别演练两种策略 |
| R12 | Provider 未返回 usage、价格表过期或预算并发扣减不原子 | 成本超支，租户账单不准 | 请求前预留预算，完成后 reconciliation；未知价格 fail-closed；价格版本化；BudgetStore 用原子 CAS | 预算拒绝率、usage 缺失、预留与实扣差异告警 |
| R13 | Redis 到 SQL、向量库或对象存储迁移中途失败，切换后发现数据缺失 | Session 不完整、召回下降或 Artifact 丢失 | snapshot、dual write、catch-up、checksum/抽检、原子 cutover；保留回滚窗口和源端只读副本 | migration lag、校验差异和双写失败告警；切换/回滚演练 |
| R14 | 向量索引异步更新、删除延迟或过滤条件错误 | Agent 短暂读取旧知识，甚至召回已撤销内容 | SQL 保存文档与索引版本；检索要求 tenant/version/status filter；删除使用 tombstone；高敏知识切换前等待索引水位 | 索引 lag、版本命中率、删除后召回测试 |
| R15 | Outbox 发送成功后，节点在写 sent 状态前崩溃 | IM 用户收到重复回复 | Outbox dedupe key 固定；发送状态 CAS；能使用平台幂等参数时同时传递；无法确认结果的消息进入人工可见状态，不盲目高频重发 | sending 超时、重复投递反馈和 DLQ 告警；在发送后注入崩溃 |
| R16 | 新配置、模型或工具策略发布后出现错误，旧请求仍在运行 | 新旧行为混杂，错误版本扩大影响 | Bundle 以 config version 固定；按租户灰度；新请求进新版本、旧请求 drain；回滚创建新版本且保留审计 | 按版本统计错误率/成本；灰度阈值自动停止；执行租户级回滚演练 |
| R17 | trace/metrics 标签携带 user、session、request 等高基数字段或原始内容 | Collector 内存上涨、监控成本失控、PII 泄露 | metrics 只保留 tenant/app/channel/operation/status；高基数字段只进受控 trace/audit；采样和保留期按租户设置 | Collector 队列、基数和导出失败监控；遥测字段审计 |

上线前至少完成 R1、R2、R4、R6、R8、R9、R10、R15 的故障注入或回放测试。其余风险按所启用的后端和租户合规等级进入发布检查表。
