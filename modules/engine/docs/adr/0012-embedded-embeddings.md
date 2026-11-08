# ADR 0012：单进程文本向量、输入准入与 input-only 完成证据

状态：开发实现与专项验证；完整门禁、提交和远端 CI 以根 `docs/v1-delivery.md` 为准。没有真实付费供应商调用，不代表正式商业发行。

## 复用与接缝

复用本仓 `EmbeddingRequest`、`NormalizeEmbedding`、已有 Handler、预算、内容审计、实例池和管理表单。新增 `NewEmbedded`/`NewEmbeddedManager`，单进程入口显式选择；原 `New` 原生执行器仍拒绝向量配置，new-api 审核渠道条件不变。发布时检查能力，输入-only 路由强制输出单价/上限为零、禁止流/工具/图片/缓存费率。当前绑定和执行快照均包含 API，过期、关闭或不同协议拒绝。

公共 `/v1/embeddings` 在单进程路径须同时具备持久 Store 与当前执行器声明的能力；检查能力不派发，Open 再复核。项目 Key、模型授权、设备证明、预算、输入策略和审计使用同一治理链。客户端仅提供项目密钥与已授权别名；供应商密钥由管理员托管，不接受客户端渠道/地址/重试覆盖。

## 审核接口范围

| 供应商 | 固定文本路径 | 网关限制 |
| --- | --- | --- |
| 阿里云 | `/compatible-mode/v1/embeddings` | 1–10 个非空文本，显式维度仅 64/128/256/512/768/1024/1536/2048；具体型号仍须匹配 |
| 火山引擎 | `/api/v3/embeddings` | 1–16 个非空文本，不接受 dimensions；不是多模态向量接口 |
| 智谱 | `/api/paas/v4/embeddings` | 1–16 个非空文本，显式维度限制为 256–2048；具体型号仍须匹配 |

仅浮点向量；字符串或字符串数组输入，单项最多 16 KiB、合计 64 KiB。拒绝 token-ID、base64、任意媒体、未知请求字段、DeepSeek 向量配置。原供应商固定域名/HTTPS/SSRF 防护不放宽。路径和参数范围依据[阿里云](https://www.alibabacloud.com/help/en/model-studio/embedding-interfaces-compatible-with-openai)、[火山文本接口](https://api.volcengine.com/api-docs/view?action=Embeddings&serviceCode=ark&version=2024-01-01)、[智谱](https://docs.bigmodel.cn/cn/guide/models/embedding/embedding-3)官方文档定向核对；不是实际账号/模型上限验收。

固定 OpenAI Python SDK 调用时须显式 `encoding_format="float"`；其默认 base64 行为不在此子集。响应最大 1 MiB，向量必须为非空有限数值、索引唯一且齐全、批次数与维度一致；超过响应上限的实际组合需减小批量。向量响应不作文本输出护栏，仍保留已有输入审计和调用元数据语义。

## 为什么使用受限接收器

首先尝试固定 Bifrost Core v1.14.1 的 EmbeddingRequest。故障测试发现 Core `providers/utils.HandleProviderResponse` 会通过 `compactRawJSON` 压缩 JSON，再返回 RawResponse；大量空白可先被接收后缩短，不能把事后长度断言当作原始网络字节上限。因此与已有严格 SSE 接收器一致，复用 SDK 的 SSRF-safe HTTP client、生命周期、固定配置和本仓向量规范化器，在适配边界接收原始响应，不改上游、不在失败后再发第二次请求。

接收器要求 HTTP 200、application/json、identity 编码，不跟随重定向；头部等待 10 秒、每次读空闲 3 秒、整体请求最长 120 秒。在 JSON 解析前最多读取 1 MiB+1 字节；超限拒绝。响应对象、向量项目和 usage 拒绝重复字段、null 数值和不支持的结构。只输出规范化内容和完整原始 numeric usage；密钥、原始供应商错误、完整原始响应不进入完成表。向量路由不建立闲置 Chat SDK 池。

该发现也说明：此前 Chat 非流式 SDK capture 保留原数值与字段顺序，但会压缩空白，不能声称是网络字节逐字取证或解析前大小限制。后续 [ADR 0013](0013-bounded-json-reception.md) 已实施 Chat JSON 接收收紧，验收以交付记录为准；SSE 已使用原始受限接收器。本阶段不修改已发行 Chat 事件字节或摘要。

## 派发前输入准入

Chat/Responses 文本预估采用 UTF-8 字节数加固定消息封装余量；向量采用所有文本字节数加每项封装余量。超过审核 `max_input_tokens` 时返回 `estimated_input_limit_exceeded`，在预算预留和供应商派发前拒绝，Open 再复核。不会用 Unicode 字符数漏算多字节文本，也不把工具/图片当普通文本估算。能力查询与上限错误分开，超限仍返回准确的准入原因。

这是保守的本地准入估算，不是供应商 tokenizer、模板或实际账单的精确复现。预算继续按配置完整上界预留，结算仍按原始用量；供应商计量超过审核上界时保持未知费用，不自动免费。模型审核与真实账单对账仍必需。

## 持久结算与升级

追加 0033，不改已发布 0032。`embeddings_v1` 使用原始 prompt_tokens/total_tokens（必须正整数且相等），可接受供应商显式 completion_tokens=0，其他明细拒绝。输出零是输入-only 协议投影，不能写进未包含该字段的原始对象。Bind 固定协议/输入价/上界；Record 在锁定绑定后按协议解析，记录原始字节与 SHA-256；Settle/Recover 依绑定协议重验，预算与回执原子提交。重复事件须字节一致；重复恢复不重复收费，不调用供应商。

启动默认自动升级至 0033 后才监听。32→33 事务失败回滚约束与版本记录，保留旧 Chat 事件/费用回执；重复迁移、结构漂移、备份恢复均走现有门禁。旧二进制不能读取较新 schema，应摘流排空后升级，回退须使用验证过的备份方案，不自动降级。

管理页新增单进程接口能力选择、仅输入价格和只读零输出字段；调用详情和 CLI/TUI 明确标注向量计量，统计/对账沿用唯一回执来源。结算成功不推断客户端或审计成功。

## 验证范围

合成固定三供应商路径、原始字节保留、请求拒绝、重复/缺失/超界用量、非法向量、超大原始响应、取消和零重试；真实 PostgreSQL 的 16 个向量治理场景；独立进程在事件已提交、回执事务未提交时强杀，再启动恢复；32→33 升级/回滚/漂移和逻辑备份恢复；固定 Python SDK；真实 OIDC/数据库/浏览器发布模型并调用实际嵌入式治理链、查看费用和桌面/窄屏卡片。完整结果与首次失败的修正见交付跟踪。

Messages、工具/图片、缓存细分、统一审核渠道和持久链 20 并发继续按 V1 规划推进。目标验收机、真实供应商/身份/终端、全量权属与许可和生产发布单列验收。
