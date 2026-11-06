# DeepSeek 接入范围

本阶段新增第四家固定供应商 `deepseek`，原生主网关与候选执行模块均支持受限文本 Chat JSON/SSE。固定地址 `https://api.deepseek.com/chat/completions`，凭证环境变量 `AIGW_DEEPSEEK_API_KEY`，或通过既有供应商管理页加密托管；公开模型别名与实际模型名仍由管理员发布配置映射。示例 `deepseek-flash` 仅用于展示映射，示例费率与驻留证据不可直接上线。

协议依据（2026-11-06 查阅）：[DeepSeek Chat Completions](https://api-docs.deepseek.com/api/create-chat-completion/)。服务端固定 `thinking.type=disabled`，保留 `max_tokens`，SSE 请求 `include_usage=true`；支持最后一个 choice 同时带 finish_reason 与 usage 的格式。客户端不能覆盖思考模式、上游地址、凭证、供应商或用量请求开关。工具、多模态、思考输出尚未开放。

缓存 hit/miss 计数作为原始证据校验：出现时必须成对、非负、合计等于 prompt_tokens。候选执行模块不把这些字段推算成折扣或零费用；目前内部费用仍按管理员发布的输入/输出单价结算，不等同供应商实际账单。管理员须选用覆盖适用时段的保守预算费率；缓存优惠、分时价格和官方账单精确复算仍待费用模型扩展。最终账单可经标准 CSV 导入与现有双人复核流程处理，不能直接覆盖已确认账本。

域名和供应商品牌不证明数据驻留。部署及模型仍须有效的大陆驻留审查证据，过期拒绝派发；不允许客户端任意端点或自动跨供应商回退。

数据库追加 `0024_deepseek_supplier.sql`，仅扩充五张既有表的供应商 CHECK 约束。启动前自动升级、事务锁、校验和、结构漂移和新版本拒绝保持。升级测试包含 v23→v24、故障回滚、不丢旧预算以及历史升级链。没有自动降级，也未操作生产库。

验证使用本地合成上游、真实隔离 PostgreSQL 和浏览器 fixture；不代表真实 DeepSeek 账号、现价、生产容量已验收。执行模块尚未替换主程序原生路径。
