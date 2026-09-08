# 双 IM 可复现契约验收

- 状态：PASS
- 范围：企业微信、飞书
- 入口：`scripts/dual_im_contract_acceptance.sh`
- 外部依赖：无；只使用合成回调、离线 Mock Model 和内存测试后端
- 最近复验：2026-09-08，基线 `e290260`

单条自动化用例分别生成企业微信和飞书加密回调，验证协议验签与解密后进入共享 Inbox；
两条消息故意使用相同外部用户 ID 和消息 ID，但属于不同租户和 binding。随后执行真实
Worker Processor 与 tRPC-Agent-Go Runner，检查 Session Event、Summary、Memory 和 Outbox，
最后通过模拟 HTTP Transport 调用两种平台 Sender。重复回调只产生一次 Runner 执行和一次回复。

测试输出只包含用例状态和聚合计数，不打印合成或真实消息正文、media key、下载地址与凭据。
这项测试提供无需平台账号即可复现的协议证据；真实企业微信和飞书 E2E 仍属于人工平台验收，
二者不能互相替代。
