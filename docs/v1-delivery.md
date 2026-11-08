# V1.0 企业网关交付跟踪

本分支 `feature/v1-single-process-commercial` 的 [复用优先规划 V2](single-process-commercial-plan.md)已获用户批准，进入实施。以下旧双进程状态和历史证据不等于新执行器验收。

## 单进程分支首阶段（2026-11-07，本地验证通过）

- 固定产品入口 `gateway-engine` 的 Bifrost 默认值与禁止旧引擎规则，共享数据库自动迁移、身份、路由、预算、审计与关闭流程；未新增数据库迁移，仍未完成执行完成事件的持久闭环。
- 重整控制台分组导航、每页任务说明、可滚动侧栏、移动端菜单与键盘焦点。沿用原页面业务操作与后端授权；不以侧栏可见性代替权限。
- 概览人民币按元展示且保留微元精度；模拟预算仍显示模拟单位。移除由币种推断供应商地域的状态文案。
- 新增入口拒绝旧引擎回归、微元显示边界和移动导航测试。`AIGW_VERIFY_WEB=1 bash scripts/verify.sh` 退出 0：嵌套引擎和根包 race/vet、真实隔离 PostgreSQL、冻结 Web 安装、契约/类型/Lint/构建、20 项 Playwright 检查通过；桌面与移动截图复查通过。唯一顶层 SKIP 是只供子进程启动的 `TestNewAPIControlHelper`，相应进程场景已运行，未以缺数据库/夹具的跳过当作通过。
- 产品二进制 `--help` 与 native/newapi 拒绝启动冒烟通过。未执行真实供应商、企业 IdP、真实终端或目标机器验收；约 572 KB 的 Web 主脚本仍有构建体积提示，按页面加载优化留待后续。
- 依赖来源与 71 个第三方 Go 模块文件证据见 [复用核验](compliance/reuse-review.md)，尚未完成商业发行许可审查。本轮完整回归沿用既有 `newapi-governance-server-channels` 合成夹具，其构建元数据为 `fb0c225` 加未提交修改；已另从干净的 `934d85c3cd6f8eb0ef51514cb833ce5719cc6531` 重建 `newapi-governance-server-pin934d`（`vcs.modified=false`），后续复验切换该可追溯夹具。两者都不是实际供应商验收。
- 远端提交 `14bb1a8f0ad0c52daab445a68560d618887dec27` 已核对；[CI 42421913639](https://github.com/jhzhfree/ai-governance-gateway/actions/runs/42421913639) 已 success。

## 单进程配置状态与候选安装包（2026-11-07，本地验证通过）

- 供应商状态在复用原管理权限与凭证写入的前提下返回 `execution_engine=bifrost`，就绪同时依赖已发布配置和实际引擎；执行池失效不会仍显示路由已就绪。控制台展示单进程执行方式，区分配置状态与实际供应商验收。
- Linux 候选包改为构建嵌套模块的产品入口，保留现有迁移/身份/审计/CLI/host-agent 工具；生成 format-2 清单、实际二进制 build info 和两个 Go 模块的锁定依赖记录。支持核查旧包但不再生成旧数据面产品包。
- 候选包同时收集实际链接 Go 依赖、Go 运行时与已安装 npm 包的顶层许可/NOTICE，并明确缺失及未清查项；验证器要求 Core 许可文件存在、版本和哈希一致。不是完整许可审查或标准 SBOM。
- 本阶段改动未新增数据库迁移。验证器增加错误入口、引擎混选、替换 SDK、版本/校验和不一致、缺失清单等归档拒绝场景；新源码完整回归改用干净的 `934d85c` 合成夹具。
- 使用干净 `934d85c` 夹具的 `AIGW_VERIFY_WEB=1 bash scripts/verify.sh` 退出 0：嵌套与根包 race/vet、真实隔离数据库、契约生成检查、冻结 Web 安装/构建和 20 项浏览器检查通过。子进程 helper 的顶层 SKIP 与首阶段相同，不是省略进程验收；未在本地额外启用 SDK 专用门禁，远端 CI 按工作流执行。
- `bash scripts/verify-release.sh` 在固定 Go 1.27.3、Node 24.19.0 下通过，包含归档负向场景、真实 Linux/amd64 包构建、源提交/依赖/许可文件哈希核验，以及网关/主机代理/迁移工具的启动帮助检查和旧引擎拒绝。e82b7f4 本地候选收集 327 个组件、455 个文件；8 个组件没有顶层许可文件，清单明确待审，不作为许可清查完成证据。
- 早期校验发现 Go 生成类型漏更新及默认 Node 24.22.1 与打包固定版本不同，已同步 Go/TS 类型并采用校验过官方 SHA-256 的 Node 24.19.0 打包；没有放宽生成或版本门禁。常规浏览器回归使用环境支持的 Node 24.22.1，固定 Node 的包内 Web 资源在最终构建时另行对照。
- 第二阶段已推送并核对远端 `87835cede619055ddaffa72a73de5d1f38d33eb9`；[CI 42424226281](https://github.com/jhzhfree/ai-governance-gateway/actions/runs/42424226281) 在前端下一阶段开始时仍在运行。该提交最终 Linux/amd64 候选包 SHA-256 为 `77ca5807a743e2e80a2e0fdf546bdbe64eab84393aa2c4372995ba894b57a7d3`；归档和实际二进制冒烟通过，包内 3 个 Web 文件逐个与该阶段浏览器测试资源哈希一致。
- 完整协议迁移、持久完成证据、正式许可审查和生产验收仍未完成。

## 控制台按需加载与异常恢复（2026-11-07，本地验证通过）

- 沿用 React 的 lazy/Suspense，将控制台、独立审计入口及渠道/费用/终端/审计/系统等管理页按需加载；未增加依赖，也未改变管理 API 或账务状态。页面资源或渲染失败时保留侧栏与上下文，提供明确的重新加载入口；不暴露原始异常，不自动刷新或重放未确认操作。
- 固定 Node 24.19.0 的冻结安装、API 契约、类型、ESLint/Prettier、生产构建通过。最大压缩前 JS 分块从前阶段 572.28 KB 降至 263.16 KB，构建不再提示超过 500 KB；这是分块体积，不代表总下载量同比下降或生产性能 SLA。
- 真实隔离 PostgreSQL/OIDC、干净 `934d85c` 原生对照夹具与本地身份的浏览器回归共 21 项通过（19＋1＋1），Go fixture race 总计 121.099 秒，无跳过。新增请求拦截验证系统页资源首次访问才下载、等待提示、下载失败仍能切换导航、恢复网络后整页重载可用；没有以静态 mock 页面代替业务验证。桌面/移动截图复查通过。
- 本阶段仅 Web 与文档变更，未重复无变更的完整后端；上一阶段完整门禁记录见上。构建日志 `/tmp/aigw-ui-lazy-build.log`，浏览器日志 `/tmp/aigw-ui-lazy-browser.log`。新的远端 CI 和最终候选包须按各自提交核验，不能沿用旧包证明新资源已打包。
- 已核对远端 `258bd3b1d0ce7271791ea14a021d8d12ae4047f7` 和 [CI 42424778155](https://github.com/jhzhfree/ai-governance-gateway/actions/runs/42424778155) success；上一阶段 [CI 42424226281](https://github.com/jhzhfree/ai-governance-gateway/actions/runs/42424226281) 也已 success。该提交候选包 SHA-256 为 `16d8f0a98d0557399bb98a436837f8d6716f37c6e109c0f90449db83fc9af11a`，480 个文件和 28 个 Web 资源逐个核验，实际入口冒烟通过；这是本地开发候选，不是生产发布。

## 单进程完成证据与崩溃结算（2026-11-07—08，本地验证通过）

- 追加迁移 0032，保存执行前的不可重复绑定、原始数值用量和原子结算回执；价格快照不含供应商密钥或凭证衍生摘要。产品工厂必须接入持久 Store，单进程 Chat JSON/SSE 沿用唯一预算账本。
- JSON 完成或 SSE 实际 `[DONE]` 后，用量先同步落库再交给治理 Handler；预算变更和回执在同一事务提交。后台按原快照恢复已知费用，不重放供应商请求、不伪造客户端成功；无完成证据继续保留未知占额。详见 [ADR 0010](../modules/engine/docs/adr/0010-durable-chat-completion.md)。
- 针对性实测覆盖：12 路并发幂等结算、错误关联/摘要、字节不同的有效重复证据拒绝、坏事件不阻塞其他恢复、Dispatch 与 Bind 之间的崩溃窗口、绑定/事件/回执写入失败、客户端取消后已知费用恢复。JSON/SSE 独立子进程在结算事务中 SIGKILL，再由新进程恢复；供应商实际调用数保持 1。
- 真实 PostgreSQL 逻辑备份恢复演练通过，涵盖原始完成事件、已提交回执和待结算事件；旧版本恢复后仍按追加迁移升级。原始字节、表数据及序列摘要核对后继续幂等结算。
- 本阶段发现并修正：故障触发器应在子进程就绪后注入并在恢复启动前移除，否则会被正常结构漂移检查拒绝；完成后取消应保持 `canceled` 而非 `failed`；并发迁移测试的历史版本数量从 31 更新为 32。初次完整回归因最后一项失败，不计入通过；修正后的针对性检查与最终完整门禁均已通过。
- 最终代码复查收紧 JSON 完成契约：原始单候选必须明确给出受支持的结束原因，不能仅凭 HTTP 200 和用量计数生成完成事件。新增适配器负向测试及治理回归，单独重跑最终嵌套模块完整门禁；该修正不改变根模块或前端代码。
- 最终 `AIGW_VERIFY_WEB=1 AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh` 退出 0，使用固定 Go 1.27.3、Node 24.19.0、真实隔离 PostgreSQL 和干净 `934d85c` 对照夹具：模块/契约/格式/vet/race、冻结 Web 安装/构建、21 项浏览器检查（125.127 秒）和逻辑备份恢复通过。唯一顶层 SKIP 仍是子进程 helper，对应进程测试实际执行；本地未额外启用 SDK 专用门禁。日志 `/tmp/aigw-completion-final-verify.log`。JSON 边界最终修正后的 `bash modules/engine/scripts/verify.sh` 也退出 0（适配器 19.445 秒、桥接 44.000 秒，日志 `/tmp/aigw-completion-finish-verify.log`）；根业务和 Web 在补验期间未再修改。
- 当前仍限文本 Chat、统一输入/输出单价；控制台完成证据展示、其他三类 API、工具/内联图片与缓存细分计费按后续阶段推进。真实供应商账单、目标机器、企业身份/终端、商业发行审查和生产发布不在本轮开发验证中。
- 已核对远端提交 `1603aacbbdc589c99a677b1370cfdc73ed1f7979`；[CI 42428349399](https://github.com/jhzhfree/ai-governance-gateway/actions/runs/42428349399) 的主测试和 Linux 候选包任务均已 success。

## 完成证据查询与费用分析（2026-11-08，本地验证通过）

- 调用列表/详情复用原项目权限，新增可选 `embedded_completion`，区分未取得完成证据、已记录事件和已生成回执；返回冻结费率、数量、金额及时间，不返回原始用量字节、配置摘要、凭证或内容。不覆盖原 attempt 结果，无嵌入绑定的历史记录不推测新引擎。
- 成本报表和账单比对优先采用与回执匹配的原始数量，解决进程退出后已恢复费用但 attempt 数量为空的问题。无回执时沿用原 attempt 语义；两来源不会重复累计，金额仍来自唯一预算账本，读取不发起结算。
- Web 详情增加独立证据卡片，展示元/微元精度和冻结单价，明确回执不代表客户端成功。CLI/TUI 与生成的 Go/TS 契约同步。操作手册及账务专题同步新增状态说明，保留原身份与内容审计权限。
- 针对性真实数据库/管理 API 检查通过：三种证据状态、跨项目/组织拒绝、viewer 查看、推理 Key 拒绝、迟到恢复后报表更新且不重复计量、账单识别恢复后的用量差异。CLI/TUI 实际 API 检查也通过。
- 首次新增 API 测试使用了只支持 MOCK 的历史组织辅助函数，改为显式 CNY 项目后通过；首次浏览器发现旧“待核对”标签对比度为 2.82:1，已加深该页面的警示文字颜色，保留 axe 门禁。定向浏览器检查通过后优化窄屏卡片布局；定向筛选运行不计为完整回归。最终 `AIGW_VERIFY_WEB=1 bash scripts/verify.sh` 退出 0：嵌套/根模块、真实 PostgreSQL、race/vet、契约、固定 Node 24.19.0 冻结安装/构建及 22 项浏览器检查（20＋1＋1，133.138 秒）通过。后端 HTTP API 346.756 秒、newapibridge 547.722 秒；唯一顶层 SKIP 是实际子进程测试使用的 helper。本地未另启用 SDK/备份恢复可选门禁，上一阶段有恢复验证。日志 `/tmp/aigw-evidence-full-verify.log`。最终补充的双来源不重复计量及历史记录不推测引擎测试另验通过（7.322 秒）；未改变上述已验证的业务实现。桌面/窄屏截图复查通过。
- 当前未新增数据库迁移或第三方依赖；未调用真实模型、修改生产配置或完成外部商业发行验收。

## 单进程受控 Responses（2026-11-08，本地检查通过）

- 上一费用证据阶段已核对远端提交 `293c273cda26243fe9b75ab904042525a64ef116`；[CI 42430508026](https://github.com/jhzhfree/ai-governance-gateway/actions/runs/42430508026) 主测试与 Linux 候选包任务均已 success。
- 复用已有 Responses 严格文本转换及治理 Handler，增加持久执行能力检查，当前快照及关闭状态逐层复核。实际 SDK 发出一个 Chat 请求，持久证据仍为原始 `chat_v1`，管理卡片明确标注上游计量协议；无新增迁移/依赖。详见 [ADR 0011](../modules/engine/docs/adr/0011-embedded-responses.md)。
- Chat/Responses 治理矩阵 44 个子场景及两入口各 JSON/SSE 的四个实际强杀恢复场景通过（72.113 秒），覆盖拒绝、断流、证据/回执故障和取消，恢复不重放。真实 HTTP 屏障检查证明完成前可收到文本；能力随发布/关闭撤销，针对性 race 检查通过（3.956 秒）。首次实时测试误用了不存在的审计状态 `sealed`，改为实际 `complete` 后通过，未改生产审计逻辑。
- 固定 OpenAI Python 3.46.0 连接真实嵌入式链路，JSON、低层 SSE、高级 stream、文本块及服务端状态拒绝通过；缺失原始用量时不能取得成功结果，费用保持未知且不重试。该测试接入根可选 SDK 门禁，旧数据面 SDK 仍独立保留。
- 代码复查发现嵌入式执行尚缺派发前输入 Token 估算，当前仅有请求结构/大小和返回原始用量上界校验；下一阶段优先补准入，不把预估视作供应商计费事实。
- `AIGW_VERIFY_WEB=1 AIGW_VERIFY_SDK=1 bash scripts/verify.sh` 已运行至全部检查结束。固定 Go 1.27.3、Node 24.19.0、真实隔离 PostgreSQL、干净 `934d85c` 对照夹具下：执行模块完整 race/vet（桥接 74.653 秒）、根后端（HTTP API 339.803 秒、旧桥接 539.671 秒）、冻结 Web 安装/契约/类型/Lint/构建、22 项浏览器（125.084 秒）、新增嵌入式 SDK（6.708 秒）及历史 SDK/协议检查（68.198 秒）均给出通过结果，日志 `/tmp/aigw-responses-full-verify.log`。唯一顶层 SKIP 仍为实际子进程套件使用的 helper。执行接口中途丢失会话句柄，未直接取回外层 shell 退出码；已核对日志包含最后一项 SDK 包的 PASS/ok 且脚本进程结束，不将最初句柄失效当作成功依据。后续远端 CI 独立核验确切提交。
- 本轮无新迁移，未重复可选备份恢复；之前 0032 有恢复证据。无新第三方依赖，来源说明与 146 个文档相对链接核对通过。四协议或 V1.0 尚未全部完成；真实供应商与账单、目标机器、身份/终端及商业发行仍为外部验收。

---

以下为历史双进程分支记录，不是当前单进程产品状态。该历史控制面分支为 `feature/newapi-foundation-validation`，配套 new-api 数据面为 `feature/enterprise-governance-integration`。此前 `feature/v1.0-enterprise-gateway` 从主线 b4e4bc983f3058cd88ec0cc5ddcf0649d2014300 创建；下文保留其历史阶段证据。当前接入与部署约束以 [new-api 集成](new-api-integration.md) 的最新章节为准。

## 基线与原则

保留现有租户/项目授权、OIDC 与本地身份、客户密钥、PostgreSQL 预算账本、内容审计、设备准入、Web/CLI/TUI。历史实现状态见 p0-p1-delivery.md，本文件不把历史验证结果冒充本轮复验。

当前显式 `--execution-engine=newapi` 使用独立 new-api 数据面，企业控制面负责权威身份、预算、设备与审计。已接入受控 Chat（文本、客户端函数、审核内联图片）、实时 SSE、Embeddings、无状态文本 Responses（JSON/实时 SSE）、Messages（文本 JSON／显式审核 Anthropic 实时 SSE）子集、缓存读写及时长计费、持久完成 outbox 与恢复回执；分协议限制见集成文档。Bifrost Core v1.14.1 的历史可替换执行模块保留在 modules/engine。任何请求只能走一个执行引擎；不得并行发送两份真实请求做影子比较。默认启动路径及生产切流仍需按交付门禁批准。

控制面不复制 new-api 核心代码；独立 fork 通过受控内部协议提供路由与供应商适配。发布前逐项核验实际分发依赖及许可证，不用某个顶层许可证代替全依赖审查。

## V1 范围与门禁

| 工作包 | 当前基线 | V1 退出条件 |
| --- | --- | --- |
| 执行引擎 | 隔离 SDK、严格 SSE 与治理桥接；main 未切换 | 主治理链路接入、协议白名单、工具与流式计量、取消/过载/关闭、真实供应商受控验收 |
| Token 与身份 | OIDC、本地身份、项目 Key、权限及撤销已有实现 | 全链路跨租户拒绝、撤销及时性、身份生命周期回归；本地身份不是自建 OIDC Provider |
| 费用治理 | 两级预算、预留/结算/未知、价格版本、报表/对账已有实现 | 新引擎沿用唯一账本，重复/迟到/缺失 usage 不误结算，崩溃恢复、官方账单样本及差异处置 |
| 终端管控 | 设备登记、DPoP、短期会话、Linux 代理已有实现 | 新链路不绕过设备策略；明确 Agent 与出口控制边界；真实终端状态接口验收 |
| 内容审计与护栏准备 | 加密检查点、恢复、权限与关键字规则已有实现 | 新引擎请求/响应/工具内容完整关联，可靠接收与故障策略、保留删除、受控查看与日志无秘密 |
| 性能与可扩展 | 本地指标和共享治理状态已有实现 | 可复现压测、长流/内存/连接回收、双节点故障测试、按账务/审计启用状态分列成本 |
| 企业发布 | 版本迁移与本地开发脚本已有实现 | 安装升级回滚、备份恢复、运维与安全文档、依赖清单、缺陷分级与发布清单 |

## 当前确认的 V1 公共协议范围

2026-11-07 用户确认：先以现有四类受控 API 完成 V1 验收，不在此轮新增图像生成／编辑、音视频或其他原生协议。

| 公共入口 | 此轮验收范围 | 明确排除 |
| --- | --- | --- |
| Chat Completions | 文本 JSON／实时 SSE、审核客户端函数、审核内联图片 | 托管工具、外部图片 URL、图像生成、音视频 |
| Embeddings | 有界文本批量、float 向量、原始 input-only 计量 | 任意二进制输入、其他编码、输出文本护栏 |
| Responses | 无状态文本、字符串／有界消息列表、JSON／实时 SSE | 服务端状态／检索、工具、媒体、developer／phase 语义 |
| Messages | 纯文本字符串／text 块、JSON；显式审核 Anthropic 实时 SSE | 工具、媒体、thinking、beta、缓存控制和引用字段 |

兼容范围以严格请求校验和接入文档为准，不能标为完整 OpenAI／Anthropic／Claude Code 协议兼容。四类入口共用身份、项目 Key、设备准入、预算和审计，不以单个协议通过代表其他协议通过。后续协议扩展另行规划。

开发环境与发布验收分开：本机合成上游、真实隔离数据库、进程故障、SDK 和浏览器结果是开发证据。真实供应商及账单、企业 IdP／终端、目标 32 GiB 机器（CPU 规格待澄清）、跨主机／代理流式传输、商业授权、生产切流均仍需外部条件和相应验收，不发布 V1.0 标签冒充完成。

## 历史执行顺序

1. 纳入执行模块、统一验证入口并提交推送；保留原供应商链路。
2. 加固执行模块的输入/计量/生命周期契约，提交实测证据；明确不支持能力的拒绝行为。
3. 通过可替换接口接入现有授权→设备→预算预留→持久 attempt→执行→审计→结算闭环；用合成供应商验证安全和故障场景，禁止默认绕开治理。
4. 完成产品操作、真实环境联调、容量、安全与交付演练，才可打 V1.0 标签。

每个阶段验证、提交、推送后再推进下一阶段。测试失败须修复或记录阻塞，不省略数据库集成检查，不以测试数量宣告整个产品完成。

## 本轮证据

阶段 1（2026-11-06）：已纳入固定发布版执行模块，并加入根验证脚本与 CI 缓存。`source /workspace/.onboarding/env.sh; AIGW_GO_TEST_TIMEOUT=600s GOMAXPROCS=4 bash scripts/verify.sh` 最终退出码 0，覆盖模块校验、Go API 契约、vet、race 和真实本地 PostgreSQL 隔离 schema；没有跳过集成测试。引擎模块 12 个顶层契约测试另含 2 个流取消子场景通过。Web 未变更，本轮未重跑浏览器测试；远端 CI 状态另行核验，不以本地结果代替。

首次全量验证失败：云环境的本地 PostgreSQL 未运行，且原 PTY 测试关闭 master 早于读取线程消费终端退出序列。启动已有专用测试库后修正 PTY 测试同步，保留 raw mode 与退出序列断言；该测试额外连续 20 次 race 验证通过，完整后端重跑通过。日志位于云环境 `/workspace/.onboarding/logs/v1-stage1-verify-final.log`，不提交环境日志。

未部署、未做生产迁移、未调用付费模型。

## 第二阶段：请求边界与费用证据加固

已增加文本 Chat/function tools 的显式协议白名单、重复关键字段拒绝、单候选限制、消息/工具大小限制、服务端输出上限和用量算术一致性校验。修复 SDK 会提高过小输出上限的集成风险：小于 16 直接拒绝，并断言 16 的实际派发值不变。未支持的文件 URL/多模态输入不进入 SDK。工具历史传递增加实际 HTTP 检查。详见 `modules/engine/docs/adr/0002-bounded-chat-contract.md`。

2026-11-06 完整根验证再次退出码 0：治理主模块 206 个顶层测试与引擎模块 17 个顶层测试通过，包含 race、真实本地 PostgreSQL 隔离 schema、模块/格式/Go API 契约/vet；无失败和跳过。日志为 `/workspace/.onboarding/logs/v1-stage2-verify.log`。Web 未改动，未重跑浏览器。

仍未接入主治理链路，未声称已完成 V1.0。阶段 1 已推送提交 `72a444c894c3191acc5417b1770c015a6846466b` 并通过远端 ref 校验；GitHub Actions API 查询返回 Forbidden，远端 CI 结果未确认。

## 第三阶段：随版本启动自动升级数据库

按用户新增要求，默认 `--schema-mode=auto`。在 HTTP 监听、身份初始化及后台任务之前，应用内嵌 SQL、验证历史与最终结构。复用整批事务、schema 级互斥锁，保留迁移校验和、结构漂移和数据库版本过新拒绝机制。迁移/锁等待默认 5 分钟且可配置，后续身份初始化拥有独立超时。

保留 `--schema-mode=check` 和独立 migrate 命令供分离权限部署；旧 migrate/init-schema true 参数继续兼容。启动日志显式记录升级过程。未修改任何已发布迁移，数据库版本仍为 0023。自动化测试覆盖新库/重复、并发实例、0022→0023 和账务数据保留、check 拒绝待升级、版本/校验和/漂移拒绝、锁超时、失败整批回滚及再次启动重试。2026-11-06 完整验证脚本退出码 0：治理主模块 212 个顶层测试、执行模块 17 个顶层测试通过，含真实 PostgreSQL、race、vet、模块与 API 契约检查，无失败和跳过；日志 `/workspace/.onboarding/logs/v1-stage3-verify.log`。Web 未修改，未重复浏览器验收。

部署仍须备份并排空不兼容旧实例；自动迁移不等于自动备份、无停机混合版本升级或数据库自动降级。详见数据库升级手册。本轮只操作专用测试数据库的隔离 schema，没有生产数据库升级。

## 第四阶段：执行接纳与关闭

引擎增加显式并发上限（默认 4，可配置 1—1024）、无排队超载拒绝、停止接纳、取消在途、等待回收、幂等并发关闭。返回明确 ErrOverloaded/ErrClosed，派发数用合成 HTTP 上游核验；不自动重试、结算或退款。2026-11-06 完整验证退出码 0：治理主模块 212 个顶层测试、引擎 20 个顶层测试通过，真实 PostgreSQL/race/vet/模块与 API 契约均通过，无失败或跳过。过载/关闭两个场景额外连续 20 轮 race 验证通过。日志为 `/workspace/.onboarding/logs/v1-stage4-verify.log` 和 `v1-stage4-lifecycle.log`。此阶段仍非主治理链路接入，不能将隔离测试当成生产容量。

## CI 包级超时修复

2026-11-06 核验 f88f78a 的远程 run 42239408967：环境准备成功，internal/httpapi 整包累计 120 秒超时，并非正在运行约 1 秒的所有权测试被证明卡死。此前本地命令覆盖为 600 秒，而 CI 使用脚本的旧默认 120 秒。统一脚本默认值为 600 秒并打印实际配置，保持 race、数据库集成及单操作期限不变，详见 [CI 验证说明](ci-verification.md)。

修复后的本地完整验证主动 unset AIGW_GO_TEST_TIMEOUT，以默认配置运行 AIGW_VERIFY_WEB=1：退出码 0，后端 212 个顶层测试、执行引擎 20 个测试、16 个 OIDC＋1 个本地身份浏览器场景均通过，无失败或跳过；包括 API 契约、vet/race、冻结前端安装、类型/Lint/构建。internal/httpapi 153.986 秒，浏览器 Go fixture 合计 68.416 秒。日志 `/workspace/.onboarding/logs/v1-ci-timeout-fix.log`。该记录是推送前本地证据，新提交远程 CI 需另外核验。

## CI 后续：按钮状态切换对比度

3ecc453 的远程运行 42245393535 确认后端包级超时修复有效（httpapi 144.353 秒通过），随后在账单/预算通知两个浏览器无障碍场景失败。补充节点诊断后在本地完整回归复现：刷新按钮恢复可用时，组件库仍在对背景做 200ms 色彩过渡，白字瞬时对比度不足。取消按钮颜色过渡，保留阴影和变换；通过 `:root .t-button` 确保应用规则不被后加载的组件基础样式覆盖。

新增逐帧状态切换回归，现有整页 axe 零违规断言不变。中间失败均保留在环境日志，未将失败重试记为首次通过。后端 212 个测试及引擎 20 个测试在完整脚本中通过；最后仅 CSS 优先级修正后重新执行冻结安装、API 契约、类型/Lint/生产构建和两套真实浏览器 fixture，最终 16 个 OIDC＋1 个本地身份场景通过（61.395 秒，退出码 0，`/workspace/.onboarding/logs/v1-ci-contrast-final-web.log`）。浏览器使用与 CI 一致的 Chrome for Testing 156.0.8078.4。新提交远端结果需单独核验，尚不因此宣称 V1 完成。

## 第五阶段：受治理执行桥接

基线 b48aec6 的远程 run 42251492160 已确认 success：httpapi 143.168 秒，16＋1 浏览器场景通过。该证据属于此前 CI 修复，不代替本阶段验证。

本阶段新增主网关 ProviderExecutor 接缝与模块 gatewaybridge.Binding；使用真实 PostgreSQL/项目 Key/准入/熔断/账本/attempt/加密审计，执行本地 Bifrost 上游。公开 execution 契约，SDK 类型仍隔离；现有主程序未切换，路由和凭证轮换未验收。数据库无结构变更，保留启动自动迁移。

联调发现 SDK 聚合重复 usage 且可能修正 total，原实现不能证明费用来源。新增捕获帧校验、无缓冲背压和 Body.Close 回收，并补充拒绝/异常结算测试。SDK 捕获仍存在解析丢弃与队列截断边界，生产切换保持阻塞，不能将本次受控通过扩大为全流完整性已证明。详见 [当前功能盘点](v1-current-status.md) 与 [ADR 0004](../modules/engine/docs/adr/0004-governed-execution-bridge.md)。验证结果见下。

完整本地命令 `source /workspace/.onboarding/env.sh; AIGW_VERIFY_WEB=1 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：主模块 212 个顶层测试、执行模块 26 个顶层测试（其中桥接集成含 12 个场景）、16 个 OIDC＋1 个本地身份浏览器场景通过，无失败或跳过。模块校验、Go API 契约、vet/race、真实隔离 PostgreSQL、前端冻结安装/契约/类型/Lint/构建均通过；httpapi 153.686 秒、浏览器 67.465 秒。日志 `/workspace/.onboarding/logs/v1-stage5-verify.log`。仅合成上游、专用测试库；未部署、未操作生产数据库、未调用真实付费模型。远程 CI 需在推送后另外核验。

## 第六阶段：原始 SSE 完整性

新增直接、逐帧、有界的 SSE 接收层，流式请求不再经过可能丢弃/截断事件的 SDK 流解析器；非流式仍用固定 SDK。用量暂存到有效 DONE 后才成为证据。复用 SDK 公开网络防护，保留 TLS/SSRF/代理策略、无重定向/重试、背压及关闭回收。受治理联调增加畸形帧、超大非转发帧、用量后断流，预期均为 uncertain，详见 [ADR 0005](../modules/engine/docs/adr/0005-strict-stream-reception.md)。不改数据库，不开放主程序切换。

本地 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：主模块 212 个顶层测试、执行模块 32 个顶层测试（桥接内含 15 个治理场景）通过，含真实隔离 PostgreSQL、race、vet、模块校验和 Go API 契约检查，无失败或跳过。httpapi 159.992 秒。关闭/取消/过载另连续 20 轮 race 通过。日志 `/workspace/.onboarding/logs/v1-stage6-verify.log` 与 `v1-stage6-lifecycle.log`。Web 与主启动路径未修改，本轮未重跑本地浏览器；未调用付费上游或操作生产库。

前一提交 18836df 的远端 run 42291747120 已确认 success：治理桥接 11.642 秒、httpapi 159.688 秒、16＋1 浏览器场景通过，无失败/跳过。该远端证据不代替本次新提交的远端结果。

## 第七阶段：供应商契约与凭证绑定

新增三家固定 origin/Chat 路径与服务端参数，JSON/SSE 共用请求构造。实际 HTTP 测试发现 SDK 会改写 token 字段和思考模式，已对三家采用白名单校验后的确定请求体，未放开客户端原始透传。新增 Route 执行设置与不对外暴露的凭证绑定身份、NewSupplier 工厂和关闭接纳控制；价格版本不受换密钥影响，旧绑定拒绝新凭证快照。详见 [ADR 0006](../modules/engine/docs/adr/0006-mainland-supplier-bindings.md)。自动轮换、共享池与主程序启用尚未完成，数据库无变更。

本地 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：主模块 212 个顶层测试、执行模块 36 个顶层测试（适配器 31、桥接 5）通过，包括三家 × JSON/SSE 契约、三家凭证快照隔离、真实隔离 PostgreSQL、race、vet、模块校验与 Go API 契约，无失败或跳过。httpapi 159.759 秒；日志 `/workspace/.onboarding/logs/v1-stage7-verify.log`。本轮未修改 Web 和主启动路径，未重跑本地浏览器；未调用真实付费上游或操作生产库。推送前，上一提交 86fe914 的远端 run 42294000854 仍为 in_progress，本提交的远端结果也须独立核验。

## DeepSeek 扩展阶段

新增第四家供应商，贯通主网关和候选执行器的固定路由/JSON/SSE、凭证托管、模型授权、attempt、账单导入和控制台。追加迁移 0024 扩充供应商约束，保留自动启动升级。候选模块核验缓存分项总和，不推算缓存优惠/分时价；详情见 [DeepSeek 接入范围](deepseek.md)。完整根脚本中后端 214 个顶层测试、执行模块 36 个顶层测试通过，含真实隔离 PostgreSQL、race/vet、API 生成检查；httpapi 167.579 秒，无跳过。初次浏览器测试有 1 个失败：供应商编辑场景仍使用新增前的第 4 行序号，修正为第 5 行后重新执行冻结安装、Web 契约/类型/Lint/构建和两套浏览器 fixture，最终 16＋1 场景全部通过，退出码 0（浏览器 72.590 秒）。日志 `deepseek-verify.log`、`deepseek-web-final.log` 位于 `/workspace/.onboarding/logs/`；未把第一次失败记作通过。仅合成上游与专用测试库，无真实付费调用。

基线 4aff9bc 的远端 run 42295834384 已确认 success；该结果仅属于上阶段，DeepSeek 新提交需独立验证。

## 下一轮：有界执行实例池

新增 Pool：完整集合原子发布、单调代次拒绝旧发布、快照复用、新旧实例排空、现用/排空/构建总容量上限和全局请求并发上限。Body EOF/错误/Close/context 取消及打开失败释放租约，Close 取消并等待回收。原 15 个治理集成场景改经池与供应商工厂；初次测试暴露旧 fixture 路径/凭证断言，修正后重验。详见 [ADR 0007](../modules/engine/docs/adr/0007-bounded-execution-pool.md)。数据库自动发布联动、主程序切换与生产容量验收尚未完成，本轮无数据库或 Web 变更。

完整根 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：主模块 214 个顶层测试、执行模块当时 42 个顶层测试通过，真实隔离 PostgreSQL、race/vet、API 契约与模块校验通过，无跳过。随后仅执行模块补充排队发布可取消行为，重新运行模块完整脚本退出码 0，最终 43 个顶层测试（31 适配器＋12 桥接）通过，含 15 个治理场景；全部 7 个 Pool 测试另连续 20 轮 race 通过。日志为 `/workspace/.onboarding/logs/pool-verify-final.log`、`pool-engine-final.log`、`pool-lifecycle-final.log`。本轮 Web 未修改；DeepSeek 阶段已有 16＋1 浏览器最终通过证据。推送前 DeepSeek 提交 13166b0 的远端 run 42297449378 仍在运行，远端结果须独立核验。

## 一致快照与受管发布阶段

基线 13166b0、4947841 的远程运行 42297449378、42298576145 均已确认 success。

修复根 Manager 的配置/凭证分开读取，改为 REPEATABLE READ 只读事务；固定环境凭证读取结果。新增 Managed 将一致快照自动发布到池，串行读取＋发布避免迟到覆盖；数据库/解密/容量失败后拒绝缓存接纳，原租约排空，后续有效刷新恢复。同步路由刷新触发，非后台推送；事务内模型查询复用原连接。main 尚未切换，无数据库或 Web 结构变更。详见 [ADR 0008](../modules/engine/docs/adr/0008-managed-snapshot-publication.md)。

根 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：主模块 215 个顶层测试、执行模块 46 个测试通过，包含真实隔离 PostgreSQL、race/vet、模块与 API 契约检查，无跳过。最后补充关闭等待源读取及过期路由完整集合边界后，重新运行执行模块完整脚本和 provider 包 race 测试均退出码 0；三个 Managed 测试另连续 20 轮 race 通过。日志在 `/workspace/.onboarding/logs/managed-verify.log`、`managed-engine-final.log`、`managed-provider-final.log`、`managed-race.log`。Web 未修改，本轮未重跑本地浏览器；远端 CI 需单独核验。仅合成上游与专用测试库，无付费上游、生产操作或 V1 发布。

## 独立引擎入口与生命周期阶段

基线 9946054 远端运行 42306383261 已确认 success。迁移原启动/健康/升级实现与测试至 internal/gatewayapp，保留原命令薄入口；新增独立 gateway-engine 命令和显式 bifrost 选择，默认仍 native。成对安装路由与执行器，readiness 包含真实引擎状态，启动失败清理已建实例，不回退。新增请求关闭门禁、10 秒排空＋超时取消后的 15 秒清理等待、维护任务退出回收。数据库无新增迁移，自动升级保留。详见 [入口说明](engine-entrypoint.md) 与 [ADR 0009](../modules/engine/docs/adr/0009-explicit-engine-entrypoint.md)。完整 `AIGW_VERIFY_WEB=1 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：主模块 219 个顶层测试、执行模块 47 个测试，以及 16 个 OIDC＋1 个本地身份浏览器场景通过，无失败或跳过。含真实隔离 PostgreSQL、race/vet、API 契约、模块校验、嵌套入口链接构建、前端冻结安装/类型/Lint/生产构建；httpapi 169.609 秒，浏览器 71.168 秒。最后仅增强测试中资源 context 排空/回收的明确断言，启动/初始化失败/强制关闭三项测试连续 10 轮 race 通过（26.015 秒）。两个实际二进制均构建并验证帮助信息，原生二进制拒绝 bifrost 选择，根命令依赖列表无 Bifrost/engine 模块。日志 `/workspace/.onboarding/logs/entry-verify.log`、`entry-lifecycle-final.log`、`native-entry-rejection.log`、`engine-entry-help-final.log`。没有生产启动、付费上游调用或 V1 发布；新提交远端 CI 需独立核验。

## 故障回归与性能基线阶段

基线 ed44d09 的远程 run 42309539136 已确认 success。本阶段新增两个真实 OS 进程共享 PostgreSQL 的崩溃回归：SIGKILL 后租约及未知费用占额保持，测试推进租约后验证账本/审计恢复，存活进程服务其他项目且无重放；恢复函数由父测试调用，不宣称完整应用维护任务自动接管。新增约 12 秒持续 SSE 回归，验证 24 帧、关联 ID 与最终一次用量确认。新增可重复基准脚本，对照原生/受管执行器、内容审计开关、1/4 工作者，全部检查结算余额及审计链。覆盖和测量数据见 [可靠性说明](engine-reliability.md)。不改变生产实现、Web 或数据库 schema，保留启动自动迁移。

完整根 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：主模块 219 个顶层测试、执行模块 49 个测试（32 适配器＋16 桥接＋1 入口）通过，含真实隔离 PostgreSQL、race/vet、API 契约、模块校验及独立入口链接构建，无失败或跳过；httpapi 180.845 秒。随后双进程回归连续 5 轮 race 通过（19.769 秒），包含存活子进程正常退出断言。独立基准脚本 8 组、每组 50 次全部成功（12.121 秒），50 样本尾延迟只用于回归观察，不能当作生产容量。首轮基准末尾与完整测试并行，未采用该轮数值；记录的是门禁结束后的独立重测。日志在 `/workspace/.onboarding/logs/reliability-verify.log`、`cluster-repeat-final.log`、`governance-benchmark-final.log`。Web 未修改，本轮未重复本地浏览器；上一阶段已有浏览器及远端 CI 通过证据。本阶段新提交的远端 CI 需独立核验。无真实模型调用、生产数据库操作或 V1.0 发布。

## Linux 系统管理阶段

基线 86a46fe 的远端运行 42311493795 已确认 success。本轮按 Linux 虚拟机／物理机、NetworkManager 部署方向增加系统管理：CPU、内存、接口收发和 DNS 观测；独立 Unix socket 主机代理、UID 校验与网卡白名单；普通活动以太网 IPv4/DNS 临时应用、确认持久化和检查点回退。平台管理员按 issuer/subject 单独授权，变更意图先落审计，再执行并记录结果；未知结果不自动重放。追加 0025 系统审计迁移，保留启动自动升级、失败事务回滚及漂移检查。控制台提供预览、倒计时、确认/回退和操作记录。部署步骤与边界见 [系统管理](system-management.md)。

最终后端脚本 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 日志包含全部 232 个主模块顶层测试通过，执行模块各包通过（49 个测试），无失败或跳过；包含真实隔离 PostgreSQL、race/vet、契约及模块检查，httpapi 206.137 秒。最后补强的只读过期观测断言另连续 5 轮 race 通过。冻结依赖安装、前端契约/类型/Lint/构建通过，17 个 OIDC 和 1 个本地登录浏览器场景通过；随后新增确认后复读 DNS 断言，17 个 OIDC 场景再次通过（81.638 秒）。私有 D-Bus 的真实线协议序列化、Unix socket 身份检查及代理构建均通过。日志保存在 `/workspace/.onboarding/logs/system-backend-final.log`、`system-readonly-expiry.log`、`system-web-final.log`、`system-browser-confirm-final.log`、`system-dbus-wire.log`。

初次验证发现并修复嵌套模块依赖校验缺失、未来迁移版本测试固定编号与新版本冲突，以及浏览器表格滚动区域不可键盘聚焦；未将失败轮计入通过。NetworkManager 测试使用合成服务；当前环境没有真实 NetworkManager，未安装特权服务或修改开发机网卡/DNS。真实 Linux 虚拟机的 DHCP/静态地址、失联后回退、进程退出与系统重启边界仍须验收；此阶段不构成 V1.0 生产发布。新提交远端 CI 需独立核验。

## 系统管理通信与采样加固阶段

基线 59e5fee。修复必要内存/负载字段缺失被误报为零、计数溢出与非有限浮点数问题；失败采样不更新 CPU 差分基线。代理通信限制响应头 16 KiB、正文 1 MiB，关闭自动压缩，拒绝有效 JSON 后的额外内容、截断和超限响应。成功写结果必须匹配操作 ID、预期状态，临时应用还校验目标接口和非零截止时间；无法验证时返回不可用，由既有控制面记为 unknown，不自动重放。没有新增迁移或修改公开 API/UI，启动自动升级保持至 0025。

新增真实 Unix socket 异常响应与监控异常输入测试；最终 systemhost race 回归通过（1.275 秒），最后结果校验代码通过 vet。17 个 OIDC 浏览器场景全部通过（74.106 秒），包含真实 PostgreSQL/OIDC、Unix 代理 fixture 和确认后复读 DNS。本轮未修改前端，未重复冻结安装/构建及本地登录浏览器；上一阶段已有对应通过证据。日志位于 `/workspace/.onboarding/logs/system-hardening-targeted.log`、`system-hardening-browser.log`。完整 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0，234 个主模块顶层测试和执行模块各包（49 个测试）通过，无失败或跳过，覆盖真实隔离 PostgreSQL、race/vet、模块和 API 契约；日志 `system-hardening-verify.log`。最后追加的响应结果绑定校验另经上述 systemhost race/vet 与浏览器流程复验。

仍未进行真实 NetworkManager 虚拟机网络切换及失联回退验收，没有修改开发机网络或发布生产版本。

## 数据库逻辑备份恢复演练阶段

基线 ab758da；系统管理提交 59e5fee 的远端 run 42316462913 已确认 success，ab758da 的 run 42318108140 在本轮检查时仍在运行。新增独立 `scripts/verify-restore.sh`，并通过 `AIGW_VERIFY_RESTORE=1` 接入根门禁；普通 CI 不默认启用，不能宣称每次 CI 已进行备份恢复。演练只处理 testutil 新建的随机 schema，保留源 fixture 后恢复，不接受生产 schema 或外部备份输入。无新迁移，自动启动升级保持至 0025。详见 [备份恢复说明](backup-restore.md)。

两条真实 PostgreSQL 17.11 逻辑备份/恢复路径分别覆盖当前 0025 和构造的 0024 fixture；比较全部 fixture 表和序列摘要、检查截断备份失败无残留、执行旧备份恢复后的升级，并验证费用 settled/reserved/dispatched/uncertain 状态和占用保留、系统审计 identity 继续写入。首轮发现 libpq 不展开 PGDATABASE 中的完整 URI，已改为受限 URI 字段到子进程环境映射，凭证不进入 argv/输出；首次失败不计入通过。独立 race 演练最终通过（3.695 秒），脚本缺少测试数据库的失败分支及带 restore_e2e 标签的 vet 通过。

没有运行真实密钥解密、生产备份、跨主机或大数据量恢复；没有修改 Web 或执行付费模型调用。本轮不重复浏览器测试，上一轮已有 17 个 OIDC 场景通过证据。完整 `AIGW_VERIFY_RESTORE=1 AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0：234 个主模块顶层测试、执行模块各包（49 个测试）和 1 个恢复顶层测试（2 条版本路径）全部通过，无失败或跳过，包含真实 PostgreSQL、race/vet、模块与 API 契约。完整证据日志 `/workspace/.onboarding/logs/restore-stage-verify.log`，独立演练日志 `restore-drill.log`。

## Linux 原生候选发行打包阶段

基线 252aa28。新增 `scripts/package-linux.sh` 与无解压校验器：从 HEAD 归档快照构建原生 Linux amd64/arm64 候选包，固定 Go/Node、冻结 Web 依赖、只读 Go 模块；包含六个生产命令、控制台、无凭证配置示例、部署文档、版本清单与载荷校验和。拒绝覆盖既有路径，完成清单校验后才以独占方式写出目标包。没有集成 Bifrost 独立入口、安装服务、生产迁移、正式标签或外部发行。详见 [Linux 候选包](linux-package.md)。

本机 amd64 已用固定 Go 1.27.3、Node 24.19.0 对基线源码成功构建包并验证完整性；最初环境 Node 24.22.1 被明确拒绝，之后使用官方校验和核验的固定版本重试。冻结 npm 安装、Web 契约/类型/Lint/生产构建及六个 Go 二进制构建通过，网关与主机代理帮助命令退出码 0；迁移工具保持现有用法错误行为（`--help` 显示用法并退出 1），仅核验该预期，不计为成功执行迁移。校验器的正常包、内容篡改、额外/缺失文件、绝对/上级路径、符号链接、重复文件、不可执行权限等子场景通过；同名包拒绝覆盖后原包仍校验通过。构建日志 `/workspace/.onboarding/logs/package-checked.log`，归档留在本地 `.onboarding`，不提交二进制或凭证。

本轮没有修改业务 Go/Web 代码，不重复完整后端或浏览器回归；上一阶段完整后端与恢复门禁有通过证据。arm64 未实机验证，尚无签名、可重复构建一致性证明、完整 SBOM/授权审查或真实安装升级验收，不能视为 V1.0 正式发行。

## 候选发行持续集成门禁阶段

基线 3c043db。前阶段 ab758da 的远端 run 42318108140 已确认 success。新增 `scripts/verify-release.sh` 和依赖主测试成功的 `package` CI job：完整构建本提交原生候选包、绑定源 commit、验证清单并对本地刚构建的三个命令进行无服务启动的帮助/用法冒烟检查。权限保持 contents:read，无上传发行物或生产部署。

强化归档校验：成员数量上限、根/子目录与文件过宽权限拒绝、重复目录/文件路径拒绝、源 commit 类型与预期值检查。15 个正常/负向子场景通过，含篡改、额外/缺失文件、危险路径/链接、重复/冲突、权限、错误 commit/清单类型及超过 10000 个成员。工作流 YAML 解析、job 依赖和只读权限断言通过。

本地固定 Go 1.27.3、Node 24.19.0 下 `bash scripts/verify-release.sh` 退出 0，涵盖冻结 npm、Web 契约/类型/Lint/构建、六个 Go 命令构建、两次包验证与三个 CLI 冒烟，日志 `/workspace/.onboarding/logs/release-gate.log`。最后补充的归档负向场景单独重验通过。本轮未改业务 Go/Web，不重复完整后端/浏览器；新 CI 两个 job 的实际结果需推送后独立确认。没有执行数据库迁移、真实网卡变更或 V1.0 正式发行。

## 产品文档交付阶段

以 47c8f8c 为实现基线，新增产品文档中心、17 个导航页面操作手册、技术规格与部署验收手册。操作手册覆盖角色、首次调用、字段/步骤、成功与未知状态、异常排查；规格区分已实现能力与未验收的性能/外部系统承诺；部署手册包含配置分组、默认启动升级与分离权限模式、19 项角色/故障验收、备份恢复及签收要求。历史 product-handbook/web-console 增加版本提示，根 README 与当前状态提供新入口。

核验了当前页面导航、组织交接/密码校验、预算错误及 UTC 轮转、供应商/设备/审计/系统配置和命令实现。修正文档中的密码长度口径为至少 12 个 Unicode 字符、最多 256 UTF-8 字节；明确交接仅面向已有成员、24 小时有效期、原 owner 降为 admin。新文档全部相对文件链接存在，17 个导航页面均有独立操作章节，Markdown 差异检查通过。页面布局是明确标注的文字示意，未伪造截图或实机验收证据。

本轮仅修改文档，没有业务代码、数据库或 Web 行为变更，不重复运行后端/浏览器测试。未将当前分支标为正式 V1.0 或作生产容量/合规承诺。

## new-api 底座验证第一阶段（独立分支）

企业分支 `feature/newapi-foundation-validation` 从 f3db909 创建；new-api 分支 `feature/enterprise-governance-integration` 固定 e034390，尚未修改该 fork 代码。新增 `internal/newapibridge` 实验执行器：独立服务凭证、固定 origin/模型映射、请求/attempt 关联、证据版本/来源/物理次数严格检查、有界并发、取消与关闭，不自动重试或跟随重定向。原生产入口没有启用该执行器；普通 new-api 尚不具备此证据契约，必须先完成后端实际事实与持久事件改造，不能伪造 header。见 [落地计划](new-api-integration.md)。

完整 `AIGW_VERIFY_WEB=0 GOMAXPROCS=4 bash scripts/verify.sh` 退出码 0，239 个主模块顶层测试及执行模块各包（49 个测试）通过，无失败或跳过，含真实隔离 PostgreSQL、race/vet、API/模块检查。最后补强 origin 边界与预算拒绝子场景后，newapibridge 全包 race 再验通过（6.728 秒），vet 通过。有效 JSON/SSE 结算、缺证据/缺 usage/缺 DONE 保留 uncertain 占额、额度不足不派发均有回归。日志 `/workspace/.onboarding/logs/newapi-foundation-verify.log`、`newapi-bridge-budget-final.log`。未修改 Web，本轮不重复浏览器。

实际 new-api 在当前 Go 1.27.3 环境构建和 --help 检查成功，日志 `newapi-validation-build.log`、`newapi-validation-help.log`；不是其完整数据库/协议测试通过。未启动外部管理服务、未接入真实供应商或复制 new-api 代码进企业主模块，未发布商业版本。真实双进程推理与后端证据/outbox 仍待下一阶段。


## 统一供应商渠道管理（本阶段）

新增企业控制台到真实 new-api 的窄管理接口，独立服务身份、平台权限、目标 origin 白名单、密钥只写、版本冲突检测、事务审计及幂等回执。企业侧迁移 0031 持久保存非秘密操作历史与审核目录；newapi 模式不传 --providers 时启用数据库目录，单执行器保留并发上限和在途配置。新增渠道/编辑/启停/凭据轮换、目录审核发布及故障回执核查页面。支持类型 1/43/14；高级协议覆盖及其他渠道类型只读，不声称完整替代原生控制台。数据面固定 `934d85c3cd6f8eb0ef51514cb833ce5719cc6531`，控制面基线 07a4bb2；本阶段是开发候选交付，不包含生产切流。

验证已完成：真实 SQLite 3.50.4、MySQL 8.4.13、PostgreSQL 17.11 渠道写入／冲突／回执／审计回滚／撤销与内存缓存刷新；三库 fresh 和官方发布 706144769e5bed5a47b78721e3bb11da3cf4c0b9 的升级／重复启动。`go test -race ./controller -run '^TestGovernance(ChannelManagementDatabaseMatrix|JournalDatabaseMatrix)$' -count=1 -timeout=5m -v` 通过，日志 `unified-channels-native-matrix-final.log`；缓存启用追加矩阵日志 `unified-channels-cache-matrix.log`。已审核渠道的高优先级竞争、模型权限拒绝和分组能力拒绝通过。浏览器连接真实 OIDC、PostgreSQL 和原生子进程，创建渠道→显式审核→项目授权→实际合成推理→停用的流程连续 5 次通过，日志 `unified-channels-browser-final.log`。

数据面完整本地 race 回归（common、dto、middleware、model、relay/common、relay/channel、OpenAI／Claude／DeepSeek 适配器、service、controller）通过，controller 包 689.987 秒，原生可执行文件构建通过；`unified-channels-native-full-final.log` / `unified-channels-native-build.log`。Responses／Messages 的固定 SDK 与协议专项通过，74.319 秒，`unified-channels-sdk.log`。首次原生全包检查发现本轮测试未恢复全局分组配置，已补充清理；另两个测试要求空的外部测试库，因此最终全包按 CI 使用 SQLite，三库路径使用专用隔离矩阵，未将该首次失败算作通过。

控制面首次完整门禁的模块/API/vet 和其余后端包通过；HTTP API 458.220 秒，迁移包 228.028 秒。newapibridge 在累计 600 秒触发整包上限，当时最后一个 Messages 流恢复场景刚开始（0 秒），此前子场景已通过；该次门禁退出 1，不记为完整通过。将本地/CI 的默认包级上限统一为 900 秒，未改变单请求、故障场景或生产超时；受影响套件以 900 秒独立复验；本提交完整门禁结果以关联远端 CI 为准。

控制面冻结 npm 安装、Go/TS OpenAPI 一致性、TypeScript、ESLint、Prettier、生产构建通过；最终真实浏览器 17 项组织 OIDC＋1 项统一 new-api 渠道＋1 项本地身份场景全部通过（92.380 秒）。当前与旧版 PostgreSQL 备份恢复两条路径通过（5.161 秒）。数据面新提交远端 CI [42380813055](https://github.com/jhzhfree/new-api/actions/runs/42380813055) 已 success；控制面绑定该确切版本后独立执行远端门禁。真实供应商、生产目标主机及商业分发验收不在本阶段测试结论内。
