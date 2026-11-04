# ai-governance-gateway

AI 治理网关：面向团队的统一模型接入、访问控制、预算限额和调用审计平台。

当前状态：已实现预算/SSE、组织/项目权限、项目密钥，以及带 OIDC 服务端会话的 Web 控制台、只读 CLI/TUI 与 Device Flow 登录。另已实现百炼北京、火山方舟北京、智谱国内服务的受控适配与人民币计价；真实账号、模型参数、价格与大陆数据驻留仍待验收。已补充共享限流、熔断、运行状态和标准账单人工对账。企业 IdP、原生钥匙串、真实账单及生产部署尚未验收，整体仍为开发集成版本。

- [整体功能覆盖与剩余工作](docs/functional-coverage.md)
- [标准账单与人工对账](docs/billing-reconciliation.md)
- [共享熔断与运行状态](docs/supplier-resilience.md)
- [项目限流与故障核查](docs/traffic-and-recovery.md)
- [执行结果与 attempt 元数据](docs/execution-metadata.md)
- [三家大陆供应商与人民币计价](docs/mainland-providers.md)
- [调用账本查询与排障](docs/request-history.md)
- [CLI 与终端控制台](docs/terminal-client.md)
- [Web 控制台与浏览器会话](docs/web-console.md)
- [版本化数据库迁移](docs/database-migrations.md)

- [管理 API 与鉴权开发切片](docs/management-api.md)
- [OpenAPI 契约](api/openapi/management.json)

- [运行预算与 SSE 原型验证](docs/prototype.md)：`bash scripts/verify.sh`

- [工程分析与产品设计](docs/product-design.md)
- [系统与前端架构](docs/architecture.md)
- [开源架构参考](docs/open-source-references.md)
- [源码研究与验证结果](docs/research/findings.md)
- [代码与交付规范](docs/engineering.md)
- [开发计划与验收](docs/roadmap.md)

首期提供 Web 控制台、终端 TUI 和兼容 OpenAI 常用接口的网关。终端暂指命令行交互界面，移动端、桌面壳及 Agent 编排不在首期范围。
