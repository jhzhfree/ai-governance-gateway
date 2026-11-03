# AI Governance Gateway / AI流量治理网关

版本：0.1.0-alpha.1。基于Higress的企业AI调用治理产品研发底座。

本仓库包含**可运行的治理API和预算账本**、Higress集成模板、企业级产品设计与实施计划。当前是单节点研发基线，不是生产合规成品。没有复制Higress源代码，也未嵌入其商业功能。真实Higress容器、SAC/DAS和设备证书尚未完成集成验收。

## 已实现

- 租户归属、管理员/服务调用凭证分离，随机密钥仅保存SHA-256摘要。
- 服务凭证签发、有效期与撤销；拒绝客户端伪造租户/主体头。
- 金额预算、原子预占、usage结算、追加账本、待对账与管理员核对。
- 请求幂等键防止同一次调用重复转发；重复返回409，**不缓存/重放正文**。
- 单一配置入口转发至Higress，客户端没有供应商Key；禁止重定向。
- 非流式文本的输入/输出固定词匹配，受控响应字段与安全错误。
- 元数据查询、操作审计、数据持久化、重启恢复。
- 自动化账本/HTTP测试、零第三方npm依赖的本地运行方式。

## 快速运行：无需Docker、无需供应商Key

要求Node.js 24.x；测试环境为24.22.1。使用Node原生TypeScript类型擦除与node:sqlite，执行测试不等于完整TypeScript静态类型检查。

```bash
npm test
npm run verify
npm run demo:init
node --env-file=.env scripts/mock-higress.ts
```

在另一个终端启动治理服务：

```bash
node --env-file=.env src/main.ts
```

再开终端执行不打印密钥的烟测：

```bash
node --env-file=.env scripts/smoke.ts
```

烟测依次创建项目预算、签发服务凭证、调用模型、检查元数据、撤销服务凭证并确认调用被拒绝。每次生成新的项目，预算单位为微元CNY，示例费率是演示数据。`.env`保存随机开发凭证，禁止提交或复制进诊断包。模拟上游只验证接口契约，不能替代Higress真实测试。

## 对接真实Higress

1. 根据[集成指南](docs/HIGRESS.md)部署并锁定Higress镜像。
2. 在Higress配置实际供应商Key、`approved-chat`模型别名和仅治理服务能访问的内部路由，配置key-auth使用独立网关凭证。
3. 设置`HIGRESS_URL`与`HIGRESS_ACCESS_TOKEN`，调整`config/policy.example.json`费率和输出上限。
4. 关闭模拟上游，重新运行烟测并核对真实供应商usage及账单。

不要同时在内部Higress路由上启用指向本服务的ext-auth，否则可能将内部凭证当外部凭证而拒绝请求。提供的ext-auth模板仅用于下一阶段前置Higress鉴权实验，不能单独实现预算结算。

## 本版边界

仅支持POST `/v1/chat/completions`、文本messages、非流式、显式输出上限。流式返回422；tools、多模态、Responses/Anthropic协议及未声明字段拒绝。未实现SSO、设备绑定、DAS/SAC、工作负载证书、自动Fallback、分布式HA、分级组织预算、完整管理UI。

预算输入使用带余量的字节估计，**不是准确分词或严格零超支保证**。只适配普通文本输入/输出Token示例费率，缓存、推理和多模态计费尚未适配。真实费用超出预占仍记录真实债务并阻止继续超额调用。未知usage不自动退款，需管理员根据上游证据对账。SQL写操作同步执行，只用于小规模单节点验证。

默认仅监听127.0.0.1；对外使用前必须补TLS边界。容器模板仅用于内网验证，不是生产安全交付。数据库和本地模型策略文件均属受保护配置。管理员初始凭证有效24小时，演示结束前完成验证；生产SSO与凭证轮换列入P1，勿靠删除生产库续期。

## 设计与开发入口

- [企业产品设计](docs/PRODUCT_DESIGN.md)
- [架构决策](docs/ARCHITECTURE.md)
- [阶段计划与验收](docs/DEVELOPMENT_PLAN.md)
- [接口规范](docs/API.md)
- [威胁模型与控制边界](docs/SECURITY.md)
- [部署与运维](docs/OPERATIONS.md)
- [验证记录](docs/VALIDATION.md)
- [GitHub交付说明](docs/GITHUB_HANDOFF.md)

原创代码暂不授予公开开源许可，参见COPYRIGHT。第三方项目许可独立适用，不能据此删除其版权、NOTICE或许可证。
