# ADR 0006：大陆供应商契约与凭证快照绑定

状态：本地合成契约与受控工厂接入；主程序未开放执行器切换。

## 固定供应商配置

Config.Supplier 由服务端选择，支持 aliyun、volcengine、zhipu；空值保留既有通用 OpenAI 适配。正式配置固定服务 origin，拒绝替换 origin、额外路径、查询或未知供应商。测试例外仍要求显式 TestLoopback 与字面回环地址。

| 供应商 | 固定 Chat 路径 | 服务端参数 | SSE 用量请求 |
| --- | --- | --- | --- |
| aliyun | /compatible-mode/v1/chat/completions | enable_thinking=false | include_usage=true |
| volcengine | /api/v3/chat/completions | thinking.type=disabled | include_usage=true |
| zhipu | /api/paas/v4/chat/completions | thinking.type=disabled | 不发送 stream_options |

这些是与根 provider 目录对齐的显式兼容契约，不代表真实账号、模型版本、现价或地域驻留已验收。正式 origin 保留既有根目录端点：dashscope.aliyuncs.com、ark.cn-beijing.volces.com、open.bigmodel.cn。

## 非流式转换差异

第一轮实际 HTTP 测试发现，SDK 通用 OpenAI 转换把 max_tokens 改为 max_completion_tokens，并改写了思考模式参数。不能仅通过设置 ExtraParams 就认为供应商预算上限已传达。

对固定三家配置，先执行现有严格请求白名单，再构造服务端参数明确的请求体。非流式通过 SDK 的 RawRequestBody 功能发送此已校验请求，SDK 仍负责单次 HTTP 执行与响应转换；流式使用 ADR 0005 的直接接收器。两条路径复用同一 wireBody 构造逻辑，保持 max_tokens 原值。客户端提供 thinking、enable_thinking、stream_options、base_url、provider、api_key 或 max_completion_tokens 都拒绝，不能借原始请求功能绕过白名单。

没有启用 SDK 内部治理/脱敏插件；根网关的权限、关键字、审计和账本仍在原位置。原始请求模式不用于任意客户端正文透传。通用 OpenAI 模式继续原 SDK 转换，未扩大工具/多模态等主网关支持范围。

## 工厂与生命周期

根 Route 提供仅限进程内使用的 ExecutionSettings，包含受信模型、供应商、输出上限与凭证。凭证字段不参与 JSON，配置、设置与执行对象普通格式化均隐藏凭证。

NewSupplier 从有效 Route 创建拥有执行器的 SupplierBinding，不能从客户端请求选择供应商或目标地址。Close 停止接纳并调用执行器的取消/回收逻辑，重复 Close 安全。输出上限低于 16 的路由拒绝，保留当前执行契约。

价格身份继续排除凭证，避免换密钥改变价格快照；新增不对外暴露的 ExecutionIdentity，将路由/价格指纹与凭证共同纳入绑定比较。旧绑定面对新凭证 Route 会在派发前拒绝，即使价格指纹相同。已取得旧 Route 的在途请求使用其原快照；这不是自动轮换、分布式即时撤销或共享池。

下一步由有界共享池负责按快照复用、发布后创建新实例、排空旧实例、资源回收和过载行为。当前工厂不自动读取数据库、不订阅发布事件，也未安装到 main。不能将手动创建新绑定并关闭旧绑定的测试扩大为生产自动轮换已完成。

## 验证

三家 × JSON/SSE 六种真实本地 HTTP 场景核对根目录路径、Authorization、model、max_tokens=32、思考模式、stream_options、单次派发与用量=5。另验证服务端地址拒绝和客户端参数覆盖零派发。

工厂测试对三家分别创建旧/新凭证快照，核验价格不变、执行身份改变、旧绑定拒绝新快照、旧实例关闭后拒绝接纳、新实例仅使用新凭证，并检查 JSON/普通格式化不包含合成凭证。没有真实模型调用。
