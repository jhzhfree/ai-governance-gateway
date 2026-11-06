# V1.0 企业网关交付跟踪

状态：开发中，未发布 V1.0。分支 `feature/v1.0-enterprise-gateway` 从主线 b4e4bc983f3058cd88ec0cc5ddcf0649d2014300 创建。

## 基线与原则

保留现有租户/项目授权、OIDC 与本地身份、客户密钥、PostgreSQL 预算账本、内容审计、设备准入、Web/CLI/TUI。历史实现状态见 p0-p1-delivery.md，本文件不把历史验证结果冒充本轮复验。

引入 Bifrost Core v1.14.1 作为待接入的可替换执行引擎，独立模块位于 modules/engine。权威身份、费用、设备与审计仍由本项目负责。任何请求只能走一个执行引擎，一次物理尝试只由一层决定重试；不得并行发送两份真实请求做影子比较。

不直接合并 new-api 等项目代码；借鉴设计与操作流程。发布前逐项核验实际分发依赖及许可证，不用某个顶层许可证代替全依赖审查。

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

## 执行顺序

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
