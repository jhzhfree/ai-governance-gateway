# ai-governance-gateway

AI 治理网关：面向团队的统一模型接入、访问控制、预算限额和调用审计平台。

当前状态：已实现预算/SSE、组织/项目权限、项目密钥，以及带 OIDC 服务端会话的 Web 控制台、只读 CLI/TUI 与 Device Flow 登录。另已实现百炼北京、火山方舟北京、智谱国内服务的受控适配与人民币计价；真实账号、模型参数、价格与大陆数据驻留仍待验收。已补充共享限流、熔断、运行状态、标准账单人工对账、项目密钥模型发现与 Web 成员管理。企业 IdP、原生钥匙串、真实账单及生产部署尚未验收，整体仍为开发集成版本。

- [正式本地身份与离线初始化](docs/local-identity.md)
- [预算运营与关键字匹配](docs/budget-and-keywords.md)
- [P0/P1 交付跟踪](docs/p0-p1-delivery.md)
- [真实供应商页面配置与应用接入](docs/provider-management.md)
- [Ubuntu 24.04 环境搭建、开发与部署](docs/deployment/ubuntu-24.04.md)
- [开发环境安装记录（本机实测）](docs/dev-environment-setup.md)
- [开发调试与构建发布命令](docs/dev-and-release-commands.md)
- [全局产品说明（开发与发布基线）](docs/product-handbook.md)
- [深层技术可行性评估（Token 治理 + 内容审计 + 软硬一体）](docs/technical-feasibility.md)
- [项目密钥模型发现与成员管理](docs/model-discovery-and-members.md)
- [加密内容审计与维护](docs/content-audit.md)
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

供应商 API Key 现支持页面录入/替换与加密存储（迁移 0013），配置前完成独立主密钥初始化，详见[页面凭证配置](docs/provider-credentials.md)。

部署级指标：见 [境内 Prometheus 监控配置](docs/monitoring.md)。`AIGW_METRICS_TOKEN` 为空时关闭 `/metrics`，启用后需要独立抓取令牌。

成本统计：见 [Web/CLI/终端 UI 成本报表](docs/cost-reports.md)，分别统计已结算、估算终结与开放占额，CSV 导出留痕。
