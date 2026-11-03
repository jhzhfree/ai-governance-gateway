# 开源架构参考与采用边界

本清单服务于技术选型和设计评审。仓库链接为上游入口，尚未完成逐项源码审计、版本验证或许可证核查，不代表已引入依赖。实际采用前须固定 tag/commit，记录对应 LICENSE、NOTICE、企业功能边界与验证证据；不能把当前主分支能力当作目标版本承诺。

进展（2026-11-03）：已完成八个项目固定提交的定向源码检查、根许可证初查和部分运行验证，详见[研究报告](research/findings.md)及[固定源码索引](research/sources.md)。这不等于完整审计或全部运行通过；具体证据与未验证项以报告为准。

## 1. 核心参考库

| 优先级 | 项目 | 重点阅读与借鉴 | 本工程采用边界 |
| --- | --- | --- | --- |
| P0 | [Bifrost](https://github.com/maximhq/bifrost) | Go AI 网关、供应商抽象、请求转换、流式处理与扩展点 | 首先对照 Go 推理链路设计；性能结论需本地重测，治理能力需核对具体版本与授权范围 |
| P0 | [LiteLLM](https://github.com/BerriAI/litellm) | 多供应商兼容、虚拟密钥、路由、用量与预算产品模型 | 作为协议和治理场景参考；保留本工程 Go 技术栈，预算硬限额需独立验证并发与故障语义 |
| P0 | [TDesign React](https://github.com/Tencent/tdesign-react) | 腾讯开源组件、资源表格、表单、抽屉、主题与无障碍接口 | Web 首选组件库候选；业务页面需按统一封装规范构建，不能仅靠组件库保证无障碍 |
| P0 | [Bubble Tea](https://github.com/charmbracelet/bubbletea) | model/update/view、异步命令、输入与程序退出 | TUI 主框架候选；先验证网络取消、窗口缩放和终端恢复 |
| P1 | [Dify](https://github.com/langgenius/dify) | 工作空间、供应商配置、调试反馈、应用接入的产品组织方式 | 借鉴任务流程；不移植工作流引擎，不默认允许源码复用，先核对其许可证附加条件 |
| P1 | [Coze Studio](https://github.com/coze-dev/coze-studio) | 扣子开源平台的工作空间、资源组织、调试与发布流程 | 用于产品信息架构研究；其编排功能与工程复杂度不纳入网关首期 |
| P1 | [Casdoor](https://github.com/casdoor/casdoor) | 身份提供方接入、组织与登录配置、OIDC 集成 | 身份服务候选，独立于本工程业务角色；必须实测终端授权流程及生产部署约束 |
| P1 | [OpenTelemetry Collector](https://github.com/open-telemetry/opentelemetry-collector) | 遥测管线、批处理、导出与处理器边界 | 作为观测基础设施候选；日志脱敏与敏感字段排除仍由应用负责 |

推荐重点研究 Bifrost 与 LiteLLM，分别回答“Go 网关如何组织”和“AI 治理需要覆盖哪些场景”；Web 以 TDesign 为组件基础，以扣子和 Dify 为流程参考。阅读参考库不等于整库 fork，直接依赖仅在契约和运行验证通过后确定。

## 2. 配套工程参考

| 项目 | 用途与选择理由 |
| --- | --- |
| [chi](https://github.com/go-chi/chi) | 标准 net/http 兼容路由、中间件与上下文传递；避免将业务规则绑定路由框架 |
| [pgx](https://github.com/jackc/pgx)、[sqlc](https://github.com/sqlc-dev/sqlc) | PostgreSQL 事务及连接管理、SQL 类型生成；重点验证金额类型、事务封装和组织作用域查询 |
| [TanStack Query](https://github.com/TanStack/query)、[React Router](https://github.com/remix-run/react-router) | 服务端状态、缓存失效与路由；明确 URL 状态和本地表单状态的边界 |
| [Cobra](https://github.com/spf13/cobra) | CLI 子命令、帮助、参数与退出行为；交互 TUI 与非交互 JSON 输出共用 API 客户端 |
| [Bubbles](https://github.com/charmbracelet/bubbles)、[Lip Gloss](https://github.com/charmbracelet/lipgloss) | TUI 表格、列表、输入及终端布局；与 Bubble Tea 锁定兼容版本 |
| [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen)、[openapi-typescript](https://github.com/openapi-ts/openapi-typescript) | 从管理 OpenAPI 生成 Go/TypeScript 契约代码；需验证 schema 支持范围及生成差异稳定性 |
| [Playwright](https://github.com/microsoft/playwright) | 首次接入、权限和调用排障的浏览器端到端测试 |

## 3. M0 研究任务与交付物

1. 网关：对照 Bifrost/LiteLLM，整理供应商接口、SSE 事件、取消、超时、usage 缺失和错误映射；标明借鉴来源到 commit 和文件路径。产出供应商适配器 ADR 与兼容矩阵，不复制未知授权代码。
2. 治理：研究虚拟密钥和预算模型，设计本工程的 PostgreSQL 原子预留与幂等结算。用并发及崩溃恢复测试决定是否满足硬限额，而非照搬“支持预算”的功能描述。
3. Web：用 TDesign 制作项目列表、密钥创建、调用详情三个最小页面，检验路由筛选、表单错误、主题、键盘和窄屏。对照 Coze Studio/Dify 的首次接入流程，记录保留与舍弃的交互及原因。
4. 终端：基于 Bubble Tea/Cobra 接同一管理 API，实现项目切换和记录查询；验证授权、取消、无颜色、缩放、退出恢复及 JSON 输出。
5. 身份：选择现有组织 IdP；若缺失，再评估 Casdoor 等候选。验证 OIDC 会话、角色映射及 Device Flow 或浏览器 PKCE，避免自研密码系统。

每项形成一页评估：需求匹配、固定版本、许可证、依赖成本、部署条件、失败场景、验证命令与结果、最终决定。只将真正通过验证的库纳入依赖清单。

## 4. 许可证与供应链要求

- 参考设计、使用依赖、复制源码、分发修改版本分别评估，不能混为同一种授权方式。
- 按选定 commit 检查根目录和子模块许可证、NOTICE、商标及素材条件；企业模块和第三方组件可能有不同条款。尤其不能假定 Dify 或其他 AI 平台全部属于无附加限制的宽松许可。
- 直接采用或复制时记录来源、版本、文件、修改和适用义务，保留要求的声明。授权不明确时仅保留需求层面的研究结论，选择许可清晰的替代实现。
- 采用固定版本与完整性校验，检查维护状态、安全公告和依赖树，生成 SBOM；不将参考项目加入本工程运行依赖，除非已作明确采用决定。
