# ADR 0015：原生 Messages 文本 JSON

状态：本地完整后端/Web/SDK/恢复门禁通过；远端提交与 CI 状态见根交付跟踪。真实供应商兼容仍待外部验收。

复用 Bifrost v1.14.1 公开的 OpenAI→Bifrost→Anthropic 请求转换、Anthropic→Bifrost 响应转换；沿用现有有界 JSON 接收、企业治理和完成日志。参考 [DeepSeek Messages](https://api-docs.deepseek.com/api/create-message/) 与[兼容说明](https://api-docs.deepseek.com/guides/anthropic_api/)（2026-11-08 核对）。不复制 SDK 实现，未新增或更新模块锁定项；实际链接依赖以候选包清单为准。

新增服务端路由 API `messages`，仅固定 DeepSeek `/anthropic/v1/messages`，使用现有凭证；不接受客户端地址、beta 或代理工具头，不接受会静默映射为其他模型的 claude* 上游别名。必须显式设置原始缓存命中费率，写费率为不适用的 0，无 TTL；强制 thinking disabled，保留审核 max_tokens。文本请求和历史复用当前严格子集，工具、媒体、缓存控制和其他字段拒绝。此阶段原生路由所有 SSE 在派发前拒绝，下一阶段再接入明确的起始用量与 message_stop 完成契约。

原始 usage 必须包含未命中 input_tokens、cache_read_input_tokens、明确为 0 的 cache_creation_input_tokens、output_tokens；允许 standard service_tier，不接受额外服务端工具或计费类别。输入总量为前三类之和，原始字节不补字段；不将现有 Chat 计数缺失写入量当作 Messages 证明。SDK 只做内容/协议转换，最终数量与原始解析比较。

0035 追加独立 deepseek_messages_v1 绑定（本阶段非流式）和可空原始写入量列；旧 Chat 命中/Embedding 历史保持 null。派发费率、原始投影、Hash、回执与恢复沿用现有事务，缺失证据继续保留未知费用占额。公共 Messages、Chat/Responses JSON 可使用同一明确审核的原生路由；路由 API 决定上游协议，公共入口不越权切换物理地址。

验收须覆盖真实隔离数据库升级回滚、JSON/SDK 有界转换、模型与身份权限、缓存原始字段、预算、内容审计、回执与实际强杀恢复、浏览器发布和固定 Anthropic SDK；全套检查及远端提交见根交付跟踪。真实供应商、目标设备/身份/容量及商业分发审查仍单独验收。

后续显式审核的原生实时流按 [ADR 0016](0016-embedded-messages-realtime.md) 和追加迁移 0036 实施；以上 JSON-only 描述保留为本决策初始范围。
