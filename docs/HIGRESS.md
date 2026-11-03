# Higress集成与版本冻结

## 当前状态

已实现治理服务到Higress的OpenAI文本非流式HTTP契约，提供Compose与ext-auth配置体。当前执行环境无Docker，**没有启动真实Higress**，也未拉取镜像、验证Wasm版本或连接真实供应商。

Higress镜像不硬编码latest，不伪造镜像digest。操作人员在能访问镜像仓库的环境选择稳定版本，核查发行记录、漏洞及目标CPU架构，记录repo/tag、digest、上游commit和插件digest。随后设置：

```text
NODE_IMAGE=<Node24镜像仓库>@sha256:<实际摘要>
HIGRESS_IMAGE=<Higress all-in-one镜像仓库>@sha256:<实际摘要>
```

`node --env-file=.env scripts/check-release.ts`检查引用格式，不能验证镜像真实性。当前未完成生产镜像冻结。

## alpha真实调用配置步骤

1. 使用deploy/compose.yaml启动，访问本机8001控制台；不要向公网开放。
2. 配置供应商地址和API Key，设置模型别名`approved-chat`；密钥仅存Higress受保护配置。
3. 创建`/v1/chat/completions`内部模型路由，启用key-auth等已核验认证插件，仅允许治理服务持有的`HIGRESS_ACCESS_TOKEN`。
4. 治理服务HIGRESS_URL指向内部Higress 8080。外部调用者只访问8090，不能直连8080。禁止将Higress供应商Key当作员工凭证。
5. 对照真实供应商调整policy费率。alpha不支持复杂usage，含缓存/推理计费的模型应先完成适配，否则只能用于协议测试。
6. 执行烟测、正常调用/异常调用/撤销/重复请求验证并收集真实usage字段。

Compose并不自动创建模型路由、Key或策略，必须完成以上配置。Higress all-in-one作为预研形态，生产需按资源与恢复要求重新设计。

## ext-auth模板的适用范围

deploy/ext-auth.example.yaml基于官方forward_auth字段，调用GET鉴权接口，失败默认拒绝。仅用于未来“客户端直接进入Higress”的身份实验。它没有预算预占或usage回调，不能替代当前治理适配服务。不能直接在alpha的内部路由套用此模板，因为内部Higress凭证不属于外部服务主体凭证。

正式插件需在请求前执行身份、模型、数据域和预算决策；在上游结束时提交可信usage事件。禁止仅靠客户端回报usage结算。插件超时、重试和崩溃需有outbox/恢复机制，不能预占后忘记结算。

## 实际集成验收表

| 项目 | 当前证据 | 待补 |
|---|---|---|
| 服务HTTP契约 | fixture自动测试 | 真实Higress版本与配置 |
| 身份forward_auth | 接口单测、配置示例 | 插件实测及Header覆盖 |
| 上游Key隔离 | HTTP测试 | 实际插件和错误日志检查 |
| 真实模型 | 无 | 供应商/私有模型及版本矩阵 |
| ARM/C86 | 无 | 指定硬件和OS安装、性能 |
| 性能 | 无容量结论 | 长连接、预算和日志分层压测 |

参考：[Higress仓库](https://github.com/higress-group/higress)、[ext-auth官方文档](https://higress.io/en/docs/latest/plugins/authentication/ext-auth/)。模板字段应按冻结的插件版本再次核对。
