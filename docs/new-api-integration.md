# new-api 底座落地：隔离验证与阶段进展

## 分支与当前状态

- 企业工程：`feature/newapi-foundation-validation`，从 `f3db909` 创建。
- new-api fork：`feature/enterprise-governance-integration`，从 `e03439033238722c0b8d4437a052445f3aeeed83` 创建；第一阶段未修改该仓库，后续原始用量事实实现见下方阶段进展。
- 路线：企业入口保留身份、项目、预算、终端与审计；new-api 承接协议/渠道执行，通过明确的用量和物理尝试契约对接。
- 当前已提供显式 `--execution-engine=newapi`、可信入口、持久派发/outbox、不可变价格/渠道绑定、恢复消费、自动迁移至 0027 和 readiness。默认执行器未切换。客户端为受限文本 Chat Completions，上游已验证 OpenAI 兼容、DeepSeek 与 Anthropic Messages，SSE 通过 v2 完成证明实时返回内容；部署配置和验收见后文“已接通”章节。
- 本文保留历史阶段证据；历史章节中的待办不代表当前状态。生产发布门禁（目标部署流式延迟、容量、跨主机/真实账号联合验收等）尚未全部完成。

## 已落地的接入边界

1. 仅使用独立内部服务 Token；不复制用户 Authorization、Cookie 或 route 中的厂商秘密。
2. 固定 origin 和 `/internal/governance/v1/chat/completions` 路径，模型由网关别名映射到经配置的后端模型名。没有 URL 参数/用户信息/子路径支持；HTTPS 默认必需，仅显式测试选项允许 loopback IP HTTP。
3. 配置中的 Token 不进入 JSON 或常用格式化输出。模型映射在创建时复制，调用者不能事后修改映射。
4. Validate 不发请求；Open 仍由原治理链在预算预留、attempt 和 dispatch 记录后调用。认证、模型授权和预算并未搬到客户端。
5. HTTP 不跟随重定向，不做应用层自动重试；响应头上限 16 KiB，连接/响应时间有界；并发持有到 Body 关闭/结束，Close 取消并回收在途流。
6. JSON/SSE 正文继续经过现有严格解析、内容治理和结算链。响应头通过不替代完整 usage 和流结束检查。

## 当前证据契约 v1

企业入口发送 `X-Aigw-Request-Id`、`X-Aigw-Attempt-Id` 与审核过的 `X-Aigw-Channel-Id`。后端成功响应必须各出现一次：

| 响应头 | 初期要求 |
| --- | --- |
| X-Aigw-Evidence-Version | `1` |
| X-Aigw-Request-Id | 与本次业务 request_id 完全一致 |
| X-Aigw-Attempt-Id | 与本次网关 attempt_id 完全一致 |
| X-Aigw-Usage-Source | `upstream`，不允许将估算结果作为实际用量 |
| X-Aigw-Physical-Attempts | `1`，首期不允许透明内部重试 |

这些头是 fork 内部协议，**不是 stock new-api 的已存在功能**。普通兼容响应没有该证据时，适配器明确失败，派发后的企业费用保持 uncertain，而非退款或当成零消费。

响应头本身不是密码学证明或持久账单，不能由反向代理无条件补齐，也不能透传上游/用户伪造值。本分支已在 new-api 内部基于实际执行事实生成，并配合可信服务身份、持久事件、严格原始用量来源与回归验证。流式 header 的声明只有在最终正文证据完整时才可用于结算；它不能提前证明流会完成。

## 第一阶段测试证据及限制（历史）

新增五个顶层回归，测试用专用合成 HTTP 服务模拟未来契约，不伪装为 new-api 实机联调：

- 服务凭证/模型映射隔离、缺证据/错 attempt/重复证据/估算/多次物理尝试的拒绝、307 不跟随且无重试。
- 流持有并发槽位，超额立即拒绝；关闭执行器取消流并回收，关闭后不能接纳。
- origin/凭证边界及秘密格式化/JSON 输出检查。
- 真实隔离 PostgreSQL + 原 gateway.Handler：预算不足不联系后端且不创建预留；有效 JSON 费用结算；缺少证据时返回失败，状态 uncertain，保留 288 微元占额，不计作成功费用。
- 真实数据库下 SSE：完整 usage + DONE 才结算；缺 usage 或缺 DONE 保留占额。测试金额均为合成规则。

实际 new-api checkout 本轮已 `go build -mod=readonly` 成功并通过 `--help` 启动检查。使用当前云环境 Go 1.27.3 编译（源码声明最低 1.26.10），不是该项目固定发布工具链/完整测试矩阵的验证。没有启动对外 HTTP 管理端、没有配置真实凭证、没有进行真实模型调用；未运行 new-api 的 SQLite/MySQL/PostgreSQL 完整测试矩阵。

## 最初实施顺序与完成条件（历史计划）

1. **后端证据与尝试约束**：在 new-api 验证分支实现仅可信内部入口可用的模式；固定路由版本、关闭内部重试/跨组回退；禁止估算回填被标成原始用量。不能只添加静态 header。
2. **持久事件契约**：在物理派发前记录 attempt；完成/失败、用量来源和重复投递通过 outbox/幂等消费与恢复核验。控制面数据库失败时不能丢失已经产生的费用。
3. **真实进程联调**：new-api + 合成供应商 + 企业网关 + 专用数据库；验证凭证拒绝、渠道重试配置、缺失/冲突 usage、断流、双方 SIGKILL、重复事件与重新启动。比较物理调用次数与账本。
4. **目录和凭证解耦**：当前企业 Route 仍由原供应商配置校验创建；需要新独立来源目录，让控制面只持有价格/模型与证据，不再要求它保存由 new-api 托管的厂商秘密。不能用虚假凭证填充正式配置绕过校验。
5. **显式启动入口**：上述门禁通过后，增加 `newapi` 执行模式、配置校验/readiness、密钥轮换、关闭排空和平台 UI。默认行为只能在发布决策后变更。
6. **企业能力扩展**：逐协议支持 Responses/工具/多模态，补齐对应审计与费用上界后开放，不能因 new-api 能转发就自动放行。

## 商业与发布边界

没有复制 new-api 源码进入企业模块；采用独立 HTTP 集成不自动构成闭源授权结论。AGPLv3/NOTICE 与书面商业许可仍按 [底座评估](new-api-foundation-assessment.md) 处理，商业发行前确认。当前工作是开发验证，不发布新产品包，不移除署名，不提交真实密钥，也不迁移生产数据库。


## 阶段进展：原始用量事实（2026-11-07）

new-api 分支已提交并推送 [5edfa3883](https://github.com/jhzhfree/new-api/commit/5edfa3883c8a733553970365b84474a28de9fc12)：

- OpenAI 兼容 JSON/SSE 处理器将原始 prompt/completion/total counters 保存为独立事实，先于用量估算和供应商字段归一化；不保存正文或凭证。每次尝试清空旧事实。
- 缺失、null、负数、分数、超出 int32 上限、合计不一致的 counters 不构成证据。明确的零与缺失值分开处理。
- 流完成事实来自上游扫描器的 DONE、无错误和协议完成状态；不是下游由格式转换器补出的 DONE。
- 公共响应头复制路径中的上游 `X-Aigw-*` 全部过滤，防止兼容供应商或中间代理冒充治理执行事实。当前尚不生成可信响应凭证头。
- 原有计费逻辑保持不变。这些事实不是持久凭证、不是物理调用次数证明，也不代表已启用生产接入；后续必须核对返回正文与原始事实，不能把存在 `UpstreamUsageEvidence` 当成可结算条件。

验证：OpenAI 适配层与 relay/common 全包竞态回归通过；controller/service 普通全包回归通过；new-api 可执行文件重新构建成功。使用 Go 1.27.3，未更改数据库模型、迁移或 SQL，本轮不宣称完成三数据库兼容验收。

扩大竞态回归未全绿，并已在独立 checkout 的原始基线 `e03439033238722c0b8d4437a052445f3aeeed83` 复现两个问题：`TestPreConsumePolicyDatabaseMatrix/sqlite` 清理 fixture 时写 RedisEnabled，与延迟的 quota cache 更新读取竞争；视频轮询并发写 logger 的普通全局计数器。前者不是新功能的数据库验证通过凭据，后者需要作为生产并发质量问题修复。不能将有这些失败的整体 race 命令写成成功。


### 并发质量修复与单次调用保护

日志并发修复已提交 [cc51bebad](https://github.com/jhzhfree/new-api/commit/cc51bebad6c0398cde10f42b9609f4760b070dbb)。计数器和轮换调度状态改为原子操作；视频轮询测试在派发前保存不可变任务 ID，避免断言与 ORM 写回同一个任务对象竞争。修复后 `go test -race ./service -count=1` 通过。

单次调用保护及分支 CI 已提交并推送 [22623f75a](https://github.com/jhzhfree/new-api/commit/22623f75a662a0f111b64e479a5bfe66ed572a44)。新增的 `relay/common.SingleAttempt` 只由程序中的可信执行上下文持有，不从请求头或 JSON 构造。绑定后，控制器的重试上限为 0，HTTP 执行采用一次性 HTTP/1 连接，禁止重定向、请求体重放、Cookie jar、未验证的 TLS 和不可审计的自定义 Transport。克隆的 TLS ALPN 同时限制为 HTTP/1，避免只改 Go 协议开关但仍协商到 HTTP/2。相同 guard 第二次派发直接拒绝。

限制：**当前鉴权/路由入口尚未创建该 guard，因此普通 new-api 请求的行为没有切换。** 这不是持久化幂等机制，不覆盖进程重启、任务协议或 WebSocket；新的内部入口必须完成持久派发记录、身份校验和协议限定后才能使用。一次性连接也尚未达到最终性能目标，不能作为吞吐优化已经完成的证据。

合成 TLS 上游开启 HTTP/2，验证网关实际只使用 HTTP/1；成功、503、307 与接收请求后断连四种情形都只收到一次物理请求，重定向目标不被访问，第二次调用被拒绝。测试另外修复了原有并行 header 测试修改全局 Gin mode 的竞争，保留并行执行与全部业务断言。

最终执行边界竞态回归命令：`go test -race ./relay/common ./relay/channel ./relay/channel/openai ./service -count=1`。四包通过，JSON 结果记录 796 条 pass（含子测试）、2 条 skip；跳过的是原有固定价格计费 MySQL/PostgreSQL 可选矩阵，**不计作已验证的数据库兼容性**。该阶段的控制器完整竞态套件仍有 Redis fixture 问题；后续修复与独立验证见下一节。

为该 fork 分支增加独立 GitHub Actions 验证流程：执行上述竞态范围、controller 普通功能回归和构建；没有部署或发布步骤。Web embed 占位仅服务于 CI 编译检查，不是可交付产品 UI。


### 控制器竞态缺口闭环与三库基线

[本阶段提交 df9de6d5f](https://github.com/jhzhfree/new-api/commit/df9de6d5f) 在测试 fixture 切换全局配置、关闭数据库之前等待已经提交的后台任务结束，避免异步缓存更新读取被恢复的配置。未改动生产数据库查询、模型或迁移，也没有降低原有断言或关闭竞态检测。

- `go test -race ./controller -count=1 -timeout=15m`：通过，478.884 秒；JSON 记录 1467 条 pass（含子测试）、36 条可选环境 skip。这个结果不代表所有可选数据库/外部服务矩阵均已覆盖。
- 单独为预扣费和固定价格矩阵配置全部所需 DSN，执行 `go test -race ./controller ./service -run 'TestPreConsumePolicyDatabaseMatrix|TestFixedPriceBillingDatabaseMatrix' -count=1`：116 条 pass（含子测试），**0 skip、0 fail**。
- 实际数据库版本：SQLite 3.50.4、MySQL 8.4.13、PostgreSQL 17.11。固定价格矩阵的 MySQL/PostgreSQL 主库和日志库分别隔离；所有实例均为本地合成数据测试环境，不使用生产数据库。
- 环境变量须区分：预扣费使用 `TEST_MYSQL_DSN` / `TEST_POSTGRES_DSN`；固定价格使用 `TEST_FIXED_MYSQL_DSN` / `TEST_FIXED_POSTGRES_DSN`，独立日志库另用 `TEST_FIXED_MYSQL_LOG_DSN` / `TEST_FIXED_POSTGRES_LOG_DSN`。只设置前一对不会启用后一套矩阵。凭证不进入仓库。
- CI 已升级为 controller 完整竞态检查，包超时 15 分钟，任务预算 30 分钟，覆盖原有较慢的密码校验测试。此前 `22623f75a` 的[远端 CI 已通过](https://github.com/jhzhfree/new-api/actions/runs/42325936340)；新的提交须以对应 run 结果为准。

本阶段是执行事实、单次调用保护和测试基础的落地。**整个底座替换尚未完成**：可信入口尚未绑定 guard，持久 attempt/outbox、故障恢复与双进程联调、控制面模型目录和厂商凭证解耦、正式启动配置/readiness/UI 尚待实现。本轮三库计费测试也不替代新增 schema 后必须进行的“旧版本升级、重复启动、数据保留”专项验收。

### 持久派发记录与事务事件（2026-11-07，下一轮改造）

new-api 提交 [18f7df9d2](https://github.com/jhzhfree/new-api/commit/18f7df9d2) 增加主库中的 `GovernanceAttempt` / `GovernanceEvent`，经原生启动迁移创建。它们只保存执行标识、摘要、渠道、模型和计数，不保存请求正文或凭证；独立日志库不参与此事务。

- Prepare 仅允许首次插入。Dispatch 将 prepared 条件更新为 dispatched，成功提交后才允许真实 HTTP 传输；两个独立 guard 竞争同一 attempt，只能有一个派发。持久提交失败或结果不确定时，不允许该 guard 再试。
- Complete 在一个事务中更新完成状态并插入 outbox；事件写入失败会回滚状态。相同计数重复完成幂等，冲突计数拒绝。计数必须非负、在 int32 范围内且总量一致，但原始用量来源仍须由未来可信调用方核验。
- Cancel 可撤销 prepared，或为尚未接纳的 attempt 写入取消记录，阻止迟到的 Prepare；dispatched 不允许当作未执行取消。派发提交后、网络调用前的进程崩溃仍然属于未知结果，不能自动退款或重新派发。
- 查询、拉取和确认事件按稳定服务主体 OwnerID 隔离，不能用可轮换 Token ID 代替主体。待确认事件每次最多 100 条，使用 `(owner_id, acknowledged_at, created_at, attempt_id)` 联合索引。消费方须先提交自身幂等账本，再确认事件；消费服务尚未实现。
- attempt 记录暂不清理：在加入可验证的接纳有效期之前，删除会使迟到请求重新获准执行。该阶段不宣称完成长期容量和归档设计。
- 真实传输并发测试发现共享 HTTP Transport 的 TLS 惰性初始化竞争，已改为先 Clone，再检查私有 TLS 配置，竞态回归通过。

升级基线为上游发布版本 [v1.0.0-rc.48](https://github.com/QuantumNous/new-api/releases/tag/v1.0.0-rc.48)，固定提交 `706144769e5bed5a47b78721e3bb11da3cf4c0b9`。fixture 在该版本模块中编译并执行其真实启动迁移，生成停用的合成用户、Token 与哨兵数据；随后当前版本启动迁移三次，验证额度、关联、密钥摘要、唯一约束、派发状态和事件保留。fixture 只接受本地隔离测试目标，不输出凭证。

实际验收（Go 1.27.3）：

- 配置 `TEST_MYSQL_DSN`、`TEST_POSTGRES_DSN`、`TEST_GOVERNANCE_BASELINE_BINARY` 后执行 `go test -json -race ./controller -run TestGovernanceJournalDatabaseMatrix -count=1 -timeout=5m`：SQLite 3.50.4、MySQL 8.4.13、PostgreSQL 17.11 的 fresh / baseline_upgrade 共六个场景全部通过，0 skip、0 fail。PostgreSQL 索引顺序直接检查数据库定义，避免驱动无序元数据造成误判。
- `go test -json -race ./model ./relay/channel ./relay/common ./service ./controller -count=1 -timeout=15m`：五包通过，controller 503.651 秒。此全包运行早于最后的联合索引调整，之后上述六场景专项已重新通过；全包运行中的可选环境跳过不作为三库验证证据。
- `go build -mod=readonly -o /workspace/.onboarding/new-api-validation .`：通过。
- 分支 CI 增加固定发布版本 fixture 构建和 model 竞态检查；CI 默认覆盖 SQLite 全新安装与升级，MySQL/PostgreSQL 的结果来自上述本地真实数据库，不能声称远端 CI 已覆盖三库。

**接入边界仍未切换。** 这些是已接入实际 HTTP 传输调用路径并完成专项验证的内部组件，普通 new-api 请求尚未绑定持久 guard。下一阶段必须实现可信入口身份与请求摘要绑定、原始 usage 完成钩子、控制面幂等事件消费及恢复对账，再完成真实双进程故障联调。目录/凭证解耦、启动模式、readiness 和平台 UI 也尚未完成。new-api 保留原生 master 节点 GORM 迁移机制；没有把它描述为企业控制面已有的编号、校验和与版本拒绝迁移机制。

### 已接通：可信入口、恢复消费与显式启动

数据面提交 [d914f98b6](https://github.com/jhzhfree/new-api/commit/d914f98b64e40a9f7c255e0055ce51266286dc51) 及 [9d3a35f15](https://github.com/jhzhfree/new-api/commit/9d3a35f1551b6f021aae2cb2a53eee23326954ea) 接通实际路由、原生 TokenAuth、渠道选择和 Relay。此前“入口尚未绑定”的阶段状态已由本节替代。

- 入口为 `POST /internal/governance/v1/chat/completions`。仅部署配置的专用服务用户可访问；令牌验证、停用、过期、模型/IP 限制沿用原生机制。身份不从请求 JSON 推导。拒绝 Cookie、query 凭证、多个 Authorization、通过令牌后缀指定渠道及伪造的转发 TLS 头。
- 指定 `GOVERNANCE_SERVICE_USER_ID` 后注册内部路由；未配置则不注册。服务账户应专用于一个企业控制面集群，不使用管理员个人凭证。专用服务 Token 在普通 `/v1` 等推理入口被拒绝，避免绕过持久治理链。轮换 Token 时保留同一个服务用户；原生服务账户额度也须监控，耗尽或停用会阻止执行及恢复访问。
- 治理入口校验实际 TLS 连接。new-api 新增 `SERVER_TLS_CERT_FILE` / `SERVER_TLS_KEY_FILE`，成对配置以启用直接 TLS，最低 TLS 1.2；配置错误阻止启动。`GOVERNANCE_ALLOW_LOOPBACK_HTTP=true` 只允许实际 loopback 对端的明文测试连接，不信任 `X-Forwarded-Proto`。
- 本版允许 **OpenAI 兼容、DeepSeek 和 Anthropic 渠道类型**，客户端保持 Chat Completions 文本入口；其他原生适配器在执行前拒绝。管理员可在 new-api 维护兼容厂商的 endpoint、模型映射与真实密钥。不能把原生支持的全部协议视为已经过企业治理验收。
- 请求正文上限 1 MiB；治理入口最多同时处理 32 个请求；响应上限 4 MiB。只接收 model/messages/max_tokens/stream/stream_options，进一步协议约束沿用控制面和原生校验。
- 完成钩子核对实际原始计数与返回 JSON/SSE；缺失、重复、冲突、估算、异常结束不发可信证据。非流式成功时先提交状态/outbox，再发送正文和证据头。SSE v2 提前传输内容，完成标记与 trailer 必须等待持久提交；旧 v1 保留缓冲兼容路径。容量、延迟和跨主机故障仍须在目标部署验收。
- `GET /internal/governance/v1/events` 返回当前服务主体最多 100 个未确认事件，带 `X-Aigw-Events-Version: 1`；`POST /internal/governance/v1/events/:id/ack` 幂等确认；`POST /internal/governance/v1/attempts/:id/cancel` 只允许取消尚未派发的匹配尝试。事件包含执行元数据与计数，不包含正文和可用凭证。

控制面新增编号迁移 `0026_newapi_execution.sql`，仍由启动自动升级及既有事务锁、校验和、漂移检查保护：

1. 发起网络请求前，将 attempt/request/backend/model/channel 绑定到不可变价格、输入上限和本次输出上限。相同 attempt 不允许重新取得派发权；目录不再要求控制面持有厂商密钥。
2. 每五秒按认证内部接口拉取事件。完成/取消证据必须与本地绑定匹配；按持久价格快照计价，不读取重启后的新价格。
3. 恢复收据与预算结算同一 PostgreSQL 事务提交，再向数据面确认。重复事件不重复扣费；写收据失败会回滚账本。无法验证的事件保留，不自动确认；恢复失败使 readiness 失败。
4. 本地绑定超过三分钟、仍无收据时，按有界批次请求数据面取消。只有数据面原子证明不存在/尚未派发并写入取消事件后，才结算零费用；已派发则拒绝取消。轮询时间戳轮转批次，避免旧的未知请求长期占据前 100 条。迟到请求被永久取消记录拒绝。
5. 接收失败但数据面已经持久完成的请求可补记费用。数据面已派发却没有完成凭据时继续保留占额；崩溃或超时不等于未执行，不自动退款/重新请求。

显式启动示例（所有实际密钥从部署秘密管理注入，不放入命令行或 Git）：

```sh
# 企业进程还需要既有数据库、身份、审计加密和 HTTPS 部署配置。
export AIGW_NEWAPI_ORIGIN=https://newapi.internal.example
# AIGW_NEWAPI_SERVICE_TOKEN 由秘密管理系统注入，使用原生 sk- Token。
./aigw-gateway --execution-engine=newapi --providers=/etc/aigw/reviewed-models.json
```

`reviewed-models.json` 沿用现有 provider.Config：保留明确价格、上下文/输出上限和驻留依据；`upstream_model` 表示 new-api 中可选择的模型名称；每项还必须填写正整数 `newapi_channel_id`，绑定审核过的实际渠道。渠道选择不匹配时派发前拒绝，恢复事件也须匹配该 ID。渠道 ID 纳入价格/路由指纹；同名模型不能自动切换到另一个渠道。渠道内部 endpoint、映射、覆盖参数和账号的变更仍须按变更管理重新审核，建议新建渠道 ID 后更新审核目录；不能把 ID 相同当作第三方驻留证明。原生直连方法禁止使用这种无厂商凭证目录。首版目录以文件发布，修改后受控重启；供应商配置页不会代替 new-api 渠道管理。项目模型授权、Token、预算、终端准入、关键词和内容审计仍走原有企业 Handler。

### 本轮验收与尚未完成的发布门禁

- new-api 相关 controller/common/relay/channel/router 竞态回归通过；全 controller 本轮运行 592.003 秒。最后的治理专项与 TLS 专项再次通过；完整回归的可选数据库 skip 不视作三库证据。
- 完整配置三库及发布 fixture 后，治理专项再次覆盖 SQLite 3.50.4、MySQL 8.4.13、PostgreSQL 17.11 六个 fresh/upgrade 场景，无跳过。真实原生 TokenAuth/Distribute/Relay 验证无效与停用 Token、重复 attempt、未支持渠道不调用上游。
- 企业迁移专项验证 v25→v26、连续启动、历史占额保留、失败 DDL/历史回滚和新表漂移阻断。恢复收据插入失败时预算同步回滚，并发重复投递只结算一次。
- 双进程联调使用实际 new-api 路由/迁移/鉴权/Relay 和企业 Handler/恢复器；授权与模型上游为合成 fixture，不是生产身份/供应商验收。JSON 与 SSE 均通过。控制面在数据面持久完成、成功响应尚被故障代理扣留时被 SIGKILL，重启恢复三笔结算，共 48 微元，物理请求仍为 3 次。
- 数据面在供应商已观察到第 4 次请求、尚无完成证据时被 SIGKILL；同一 SQLite 数据重启后，保留 1 笔 uncertain、288 微元占额，既有支出仍 48 微元，物理请求仍为 4 次。fixture 显式设置原生重试次数为 3，治理模式仍不重试。
- CI 固定使用 new-api `7d06da50e22340da362b2d21c9dd8f7cb4a44d7e` 构建进程 fixture，设置 `TEST_NEWAPI_SERVER_BINARY` 才计作完成该联调；不配置时的 skip 不是验收通过。

默认执行器仍为 native；本轮没有切换生产流量、发布 V1.0 标签或宣称 AGPL 商业许可问题已解决。目标部署实时流式延迟验收、生产容量与跨主机故障、真实 IdP/终端/供应商账号、安装升级和备份恢复的联合验收仍属于发布门禁。后续协议扩展必须逐项提供用量与审计证据，不能直接开放全部原生功能。

鉴权设计参考 [OWASP Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html) 与 [Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)，采用服务器身份校验、真实 TLS、拒绝 Cookie 与替代凭证入口、状态验证和通用错误响应。本轮未进行完整 ASVS 合规认证。

取消证明的新增回归包括三库上 Cancel/Dispatch 并发竞争不可能同时成功，以及双进程中“控制面记录超期、数据面尚未接纳”的取消与迟到重放拒绝。已派发的未知请求占额保持不变。恢复扫描使用未完成绑定的部分索引，完成收据提交后退出扫描集合。

安全参考版本为 [OWASP ASVS 5.0.0](https://asvs.owasp.org/)；上述控制是本轮具体实现与测试范围，不代表完整标准认证。

最终数据面契约还包括 [11f8ee659](https://github.com/jhzhfree/new-api/commit/11f8ee659cc40190e3a10b7cfdfda6e4ee7f88a3) 的渠道绑定/取消证明，以及 [773c954a5](https://github.com/jhzhfree/new-api/commit/773c954a584a1935e9ffc4c3b0b1f5e17f05aac5) 的专用服务凭证入口约束。后者 middleware 全包竞态回归通过（10.059 秒）；取消/派发并发、真实原生 Relay 与三库升级专项通过。最终联调为两个带竞态检测的独立进程，恢复演练还包含缺席尝试的零费用结算和迟到重放拒绝。


最终控制面门禁已通过：设置固定 new-api 竞态 fixture 的 `TEST_NEWAPI_SERVER_BINARY`，并执行 `AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0。覆盖嵌套引擎模块、模块校验、契约生成一致性、vet、全部后端竞态测试、真实 PostgreSQL 迁移/事务与逻辑备份恢复。双进程专项 15.00 秒，恢复演练包 4.801 秒；全套中 `TestNewAPIControlHelper` 在父进程跳过是子进程入口占位，真正的 `TestNewAPITwoProcessRecovery` 已执行并通过，不把该占位 skip 当作验收。

`cmd/gateway` 构建及 `--help` 检查通过，实际发布二进制名为 `aigw-gateway`；`aigw` 是管理 CLI，不应混用启动命令。本地本轮没有重新运行浏览器套件，远端 CI 仍执行既有 Web 与 Linux 候选包门禁；以对应提交的 run 结果为准。


### 渠道请求边界补强

数据面 `5b324b904` 在治理请求发送前拒绝参数/请求头覆盖、系统提示词注入、透传、格式/思考内容变换、高级自定义配置及全局 Chat→Responses 转换，避免改变已预留的输出 Token 上限或转发服务凭证。检查使用 Distribute 建立的实际请求上下文，而不是首轮仅含 ID/类型的渠道摘要。普通 new-api 请求不受此限制。渠道模型映射和 endpoint 仍须经独立变更审核。

真实 TokenAuth/Distribute/Relay 回归证明上述被拒配置不会调用供应商，尝试转为 cancelled；治理与模型列表组合竞态测试通过（6.009 秒）。同时修复治理测试未恢复全局模型/分组费率造成的后续模型列表测试污染。此前 native CI 42333217441 的失败属于该污染，不算通过；后续结果以最新提交对应 CI 为准。

渠道边界补强后再次执行固定竞态数据面 fixture 的 `AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0；包含完整后端、双进程故障恢复和两种版本备份恢复。远端全套 UI/安装包门禁须查看该提交的 Actions 结果。


### 实时 SSE v2

控制面以 `X-Aigw-Stream-Version: 2` 请求实时流。数据面先发送内容块，保持 4 MiB 总量和 64 KiB 行边界；先前的完整响应核验仍保留。只有原始用量匹配且完成状态/outbox 已持久提交，才发送 `[DONE]` 和 `X-Aigw-Completion: completed` HTTP trailer。错误、取消、截断或提交失败不产生成功结束标记。控制面逐块经过既有解析、关键词策略与内容审计，但扣留 `[DONE]`，直到 EOF 及唯一正确 trailer 验证成功后才允许结算成功。

不支持 trailer 的代理会使成功终态校验失败，费用由持久事件恢复核对；不能去掉该校验来兼容代理。旧数据面 v1 已完成后缓冲响应仍可接收。控制面未向用户承诺 trailer，客户端看到的仍是标准 Chat SSE。实时输出无法撤回已发内容；要求全篇阻断/脱敏的策略仍遵循现有流式拒绝边界。

真实双进程故障测试新增上游完成屏障：客户端收到内容时只允许前一笔请求已结算，释放屏障后才收到 `[DONE]`；原有 SIGKILL 恢复、未知派发保留占额、迟到重放拒绝继续通过。专项分别验证缺失/重复/失败/正文伪造 trailer、异常尾部及截断，均不能放行 DONE。首块测试是同步顺序断言，不虚构生产 TTFT 或吞吐数据。

实时 SSE 阶段验证：完整 `AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh` 退出码 0；覆盖全部后端 race/vet、契约和逻辑恢复，固定 new-api 竞态 fixture 的双进程测试实际执行。数据面另外禁用治理请求的后台 ping，保证输出 writer 独占；最新 fixture 的 bridge 全包竞态检查再次执行。原始迁移 0026 未修改。


### DeepSeek / Anthropic 上游与独立供应商标识

- new-api `02fb5353a` 开放类型 1/43/14（OpenAI/DeepSeek/Anthropic）的受控文本请求，复用现有转换器。所有渠道在真实网络派发前核对转换后的 `max_tokens` 或 `max_completion_tokens`：必须存在唯一、正数且不超过批准上限的值；不满足时不取得派发权、不调用供应商。治理入口另拒绝零值、缺失和超过 1,000,000 的上限。
- Claude 原始计数在转换/估算之前采集，要求 message_start、累计 message_delta 用量及 message_stop；缺字段、计数倒退、重复起始、终止后数据、错误或额外服务端工具计费不会产生完成证据。缓存读取/创建与普通输入相加后与转换出的 Chat usage 核对。当前目录只有统一输入/输出两种费率，缓存分档价格并未实现；须使用经审核的统一治理费率并通过账单对账核查实际费用。
- 官方协议语义参考 [Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming) 与 [DeepSeek Chat Completions](https://api-docs.deepseek.com/api/create-chat-completion/)。DeepSeek 联调包含“结束 choice 与 usage 同一最后块”的形态。
- `newapi` 目录允许独立的供应商标识（小写字母开头，字母/数字/下划线/短横线，最长 64），必须绑定审核渠道、价格、输入/输出边界和有效驻留证据。无需也不能借用其他厂商名称。此规则不开放原生直连或原生凭证管理的厂商白名单。
- 追加迁移 `0027_governed_suppliers.sql`，保留历史数据并让执行、费用分析和标准账单保存真实标识。账单导入只允许原有四家，或当前审核目录中的标识，或该项目历史执行中已经记录的标识；仍需原有管理员权限、双人审核和原子审计。供应商名称本身不证明驻留、合同或授权。
- 这里的 Anthropic 是**上游协议兼容性**，不代表官方服务已获大陆驻留认可，也没有新增公网 `/v1/messages`、Responses、工具、多模态或 Gemini API。用户侧仍为既有文本 Chat 入口。真实供应商/账户/驻留/费率必须单独验收。
- 三协议双进程矩阵实际跑 JSON、实时 SSE、控制面 SIGKILL 恢复、数据面 SIGKILL 保留占额和无派发取消/迟到拒绝；Anthropic 场景使用独立审核供应商标识贯穿执行记录与恢复。另有真实三库 fresh/发布升级矩阵，原生治理专项通过（34.756 秒），适配器包完整竞态检查通过。

审核目录中的新增供应商条目示例（放在既有 `models` 数组内；以下为合成值，不是实际报价或驻留证明）：

```json
{
  "id": "reviewed-compatible/text-model",
  "provider": "reviewed-compatible",
  "upstream_model": "reviewed-model-alias",
  "newapi_channel_id": 17,
  "price_version": "reviewed-v1",
  "input_per_million": 1000000,
  "output_per_million": 2000000,
  "max_input_tokens": 4096,
  "max_output_tokens": 1024,
  "residency": {
    "region": "cn-mainland",
    "evidence_ref": "replace-with-reviewed-evidence",
    "expires_at": "2030-01-01T00:00:00Z"
  }
}
```

渠道 ID 必须替换为已核验的真实数据面渠道；部署级驻留证据也仍为必填。既有四家供应商的原生凭证 API 不接受这个自定义标识；该条目仅供 `--execution-engine=newapi` 的文件审核目录使用。

协议与供应商标识阶段最终验证：设置固定的 new-api 竞态 fixture，执行 `AIGW_VERIFY_WEB=1 AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0。覆盖完整后端竞态与 vet、API 生成一致性、冻结前端依赖、类型/Lint/构建、真实 PostgreSQL/OIDC/本地登录浏览器以及当前/旧版备份恢复。三协议双进程测试实际执行，自定义供应商记录纳入当前版恢复快照。迁移 0027 追加发布，未改动历史 SQL。


### 有界事件分页与异常积压恢复

`GET /internal/governance/v1/events` 和新增的 `/events/after/:cursor` 均沿用服务主体鉴权，每页最多 100 条。事件载荷版本仍为 `X-Aigw-Events-Version: 1`，分页能力为 `X-Aigw-Paging-Version: 1`；满页返回 `X-Aigw-Next-Cursor`，非满页结束本轮扫描。游标以已有索引的 `(created_at, attempt_id)` 排序，只能引用同一服务主体的事件；事件已经确认也不影响游标继续使用。无需新增数据库列。

控制面每次最多消费一页，下一轮继续后续页；到末页后回到队首重试未确认事件。超过 100 条无法核对的事件不会永远挡住后面的合法结算；失败事件仍未确认、未删除，也不作零费用推断。一次扫描周期出现失败时，后续页即使成功也保持恢复不就绪；解决异常后可以恢复。当前 cursor 只在进程内保存，重启从队首开始；账本收据保证重复消费不会重复扣费。

启动时，鉴权/版本/数据库检查失败仍终止初始化；若可信事件已取得但存在待核对记录，则以 readiness=false 启动恢复工作器，继续分页并阻止新推理，避免反复启动永远停在第一页。该状态不等于已解决异常，管理员仍须核查来源、绑定或账单。部署升级先更新数据面（旧客户端仍能读载荷 v1），再更新要求分页能力的控制面。

验证包括三库六个 fresh/发布升级场景：205 条相同创建时间事件的分页、已确认游标、跨主体隔离；以及真实 PostgreSQL 下首批 100 条异常事件之后的合法结算、失败事件不确认、启动不就绪和工作器恢复。治理三库专项通过（35.088 秒）。

### 长流与恢复连接隔离

控制面推理连接上限仍由 `MaxConcurrent` 管理。事件读取、确认和无派发取消使用独立连接池（每主机最多 2 条连接），响应头超时 5 秒、单次 HTTP 请求超时 10 秒，保留服务令牌隔离、TLS 校验和禁止重定向。推理长流占满连接时，后台恢复不会因竞争同一连接池而失去就绪；恢复协议错误或未知费用仍按原规则失败关闭。

真实 HTTP/数据库专项将推理并发设为 1，让该连接持续输出未结束响应，在不结束推理的情况下完成事件扫描，并断言原占额仍是 dispatched。该验证证明恢复连接隔离，不替代生产容量、连接数和延迟压测。

### 实施验收与恢复处置

| 现象 | 检查与处置 | 不应执行的操作 |
| --- | --- | --- |
| 启动初始化失败 | 核对两端版本、内部 HTTPS、服务 Token 主体/权限、数据库迁移与驻留证据；先部署具有分页能力的数据面 | 禁用鉴权或证据检查以强行启动 |
| 启动成功但 readiness 不通过 | 核对待消费事件与本地 request/attempt/channel/价格绑定，修复配置或持久存储故障；工作器继续分页与幂等重试 | 手工确认未入账事件或删除未知尝试 |
| 客户端已收内容但未收到正常结束 | 检查代理是否保留 trailer、上游是否完整结束；通过账本和持久事件判定费用 | 把断流当作免费，自动再次调用供应商 |
| 数据面重启后存在已派发未知请求 | 保留 uncertain 占额，以供应商账单及现有审批流程核查；没有完成事实不能推断实际费用 | 重置 attempt 状态、复用 attempt 或直接释放占额 |
| 超过一页积压 | 保持消费者运行并观察后续账本进展；同一服务主体配置保持稳定 | 通过删除前页事件绕过错误 |

开发环境已验证实际双进程、真实数据库迁移/事务、三种合成上游协议及 Web 账单管理。上线仍需用户提供目标部署与真实账户，完成 IdP、终端防绕过、供应商原始用量/实际账单、目标代理 trailer、跨主机故障、容量、安装升级及密钥备份恢复联合验收，并确认 new-api 授权满足分发/商业化方案。上述项目未执行生产操作，也未以开发环境结果替代验收。后续章节扩展了客户端函数与有界内联图片；其他多模态和公共协议仍需独立契约及用量/内容审计测试。


### 继续改造的缺口与执行顺序

当前集成不是 new-api 全量能力镜像。按原始企业要求重新盘点：Token/预算/报表、准入、审计、系统管理和自动升级已有实现；真实企业 IdP、设备防绕过、目标部署与商业授权仍属于外部验收。可自主推进的协议扩展须同时提供请求边界、原始费用事实、完整可审计内容和失败关闭，不直接打开原生路由。

| 范围 | 当前治理缺口 | 推进顺序 |
| --- | --- | --- |
| 客户端函数调用 | 原文本 DTO/响应归一化丢失调用参数与结果；现由下节实现补齐 | 本阶段 |
| 图片理解输入 | 内容数组、图片边界、审计语义、图片费用事实与外部 URL 获取风险 | 已完成有界内联图片；语义护栏与细分价格仍待扩展 |
| Embeddings | 已实现有界文本批量、浮点向量、input-only 账本、审计与双进程恢复 | 真实目标模型/维度/账单仍待验收 |
| Responses / 原生 Messages / Gemini 公共 API | 多协议事件到同一账本和审计的完整映射 | 分协议推进，不能仅增加路由别名 |
| 音频、图片生成、视频/异步任务、供应商托管工具 | 秒/张/任务/工具等非 token 计费、异步完成与恢复、二进制审计 | 先设计新的计量与预留上界，当前继续拒绝 |
| 企业增强 | 自建标准 OIDC Provider、语义护栏、KMS/SIEM、集中工具授权、规模化归档 | 不把现有本地登录/关键词/客户端声明校验表述为这些能力 |

### 客户端函数调用与审计

显式 newapi 模式新增 `tools`、字符串 `tool_choice`（auto/none/required）、assistant `tool_calls` 和 role=tool / `tool_call_id`。最多 8 个唯一函数声明，每次最多 8 个调用；参数必须是有界 JSON 对象字符串（单个 16 KiB，响应合计 32 KiB），名称必须来自当前声明。历史调用和紧随其后的结果必须唯一对应，孤立、重复、未回答的调用在预算预留前拒绝。请求总上限仍为 64 KiB。

客户端 JSON 和 SSE 均支持。SSE 的文本内容继续实时返回，函数参数增量在网关有界汇总，确认 JSON 完整、函数/调用 ID 一致、finish_reason=tool_calls 后才输出完整调用块。客户端必须等待成功结束，按自己的参数 schema、权限和业务规则验证后执行；网关不执行函数，也不声称按任意 JSON Schema 验证了参数。不同增量可以重复一致的元数据，不能改变调用身份。旧 native/mock/Bifrost 执行路径继续拒绝此扩展，避免无声丢字段。

工具声明、历史调用/结果和归一化输出参数纳入既有项目显式开启的加密内容审计，查看授权、留痕、保留和截断标记沿用原机制。关键词检查工具描述、schema 字符串和解码后的参数，JSON 转义不能绕过检查；输出脱敏只修改 JSON 字符串值，函数名/对象键不能安全脱敏时阻断。SSE 仍只允许原有告警型输出策略，不宣称可撤回已发文本或完成通用提示注入防护。

费用仍来自供应商原始 input/output token 证据，参数/结果作为模型上下文计费；工具本身的执行费用不由网关计量。客户端声明仅限定模型可返回的名称，不替代集中工具授权。数据面仅接纳 function 类型，Claude 原始内容发现服务端工具块或非零工具计量时不给出 token-only 完成证明；未知费用保留占额。没有打开搜索、代码执行等供应商托管工具。

协议参考：[DeepSeek Chat 工具契约](https://api-docs.deepseek.com/api/create-chat-completion/) 和 [Anthropic 客户端工具生命周期](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls)。本阶段验证包括声明/历史关联、畸形与越界增量、三协议 JSON/SSE 与双进程故障、实际数据库中加密审计和工具参数关键词阻断/脱敏。均使用合成供应商，不替代真实账户及业务工具授权验收。

工具阶段最终门禁：固定 `7b3ccfb24` 数据面竞态 fixture，运行 `AIGW_VERIFY_WEB=1 AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0。三协议工具故障矩阵 41.58 秒，HTTP API 包 216.854 秒，17 项 OIDC 与 1 项本地登录浏览器测试、当前/旧版逻辑恢复全部执行通过。新增空工具数组回归保持旧入口拒绝行为。无需数据库迁移，仍为 v27。


### 有界图片理解输入与完成后工具交付

审核目录条目新增 `"allow_images": true`，默认关闭，仅显式 newapi 模式可开启。该能力进入不可变路由/价格身份；修改后须按目录发布流程重启，历史请求仍使用原价格快照。模型列表返回 `allow_images`，它表示已审核接入能力，不表示已经完成图片语义审核。原生配置/凭证管理页不会自动打开该功能。

客户端沿用 Chat Completions，在 user 消息中使用 `content` 数组：

```json
{"role":"user","content":[
  {"type":"text","text":"描述这张合成测试图"},
  {"type":"image_url","image_url":{"url":"data:image/png;base64,<base64-image>","detail":"auto"}}
]}
```

仅 PNG/JPEG 内联 data URI；每张解码后最多 256 KiB，每次最多 4 张、合计 512 KiB；单边最多 4096 像素、合计最多 16 Mi 像素。请求体上限 1 MiB，非图片请求仍限制 64 KiB。两端在媒体转换前检查类型、base64、头部尺寸与数量，拒绝远程 URL、SVG、音频、文件引用和未知内容类型。检查不分配完整像素缓冲，也不声称完成图片解码、恶意文件扫描或 OCR。图像内部损坏仍可能由供应商拒绝。

已验证 OpenAI 兼容和 Anthropic 两种图片上游转换；DeepSeek 渠道在派发前拒绝图片。工具结果中的图片、图片输出、图片生成/编辑、音频与视频未开放。文本数组支持仍经文本关键词检查；图像像素不通过文本关键词推断安全，语义图片护栏属于后续工作。

图片费用必须由完整的供应商原始 prompt/input token 计数覆盖，并使用经审核的统一输入费率；有独立图像费率或额外计量维度的模型不能套用此路径。预留仍使用目录的输入上下文上界与本次输出上限，结算核对原始计数；不是以图片字节或像素数估算最终账单。目录上界/费率必须真实反映目标模型，现已增加派发前本地输入估算准入，但它不是供应商精确 tokenizer 限额，不能将估算值作为真实账单证据。超出原计费边界的证据不会自动入账/退款，须核查并保持恢复失败关闭。

启用内容审计后，图片的消息/part 位置、MIME、字节数、宽高及 SHA-256 摘要放在加密请求记录开头，随后保存实际请求（含内联内容）。仍遵循项目同意、捕获上限与保留策略；记录被截断时显示 truncated，不能把摘要或截断的 base64 当作完整影像留存。未启用内容审计时，不新增图片正文留存。查看与留痕权限沿用现有独立审计授权。

同时进一步收紧函数 SSE：完整工具调用和关联终止帧须等待数据面完成证明、控制面结算和调用元数据完成后才输出；缺失/失败 trailer 不泄露完整可执行调用，费用保持 uncertain。客户端仍须等待成功结束及自己的 schema/权限校验后执行，内容审计最终封存失败仍会扣留 DONE。

专项已验证图片 JSON/SSE、两种协议转换、控制面/数据面 SIGKILL、原始图片输入计数结算、未知派发保留占额；合成图片场景三笔完成费用为 108 微元，故障第四笔仍保留 288 微元，物理调用不增加。另验证不审核即不派发、远程 URL/类型/尺寸/数量拒绝、加密审计摘要及截断标记、工具调用无完成证明不交付。

参考：[Anthropic 图片输入](https://platform.claude.com/docs/en/build-with-claude/vision)、[OpenAI 图片输入计量说明](https://developers.openai.com/api/docs/guides/image-cost-calculator)。这些协议资料不替代真实供应商账户、模型费率和图片驻留验收。

图片阶段最终门禁：固定 `98896c0d9` 数据面竞态 fixture，执行 `AIGW_VERIFY_WEB=1 AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0。图片双协议故障矩阵 27.37 秒，工具三协议回归 41.20 秒，17 项 OIDC/1 项本地登录浏览器及两种逻辑恢复全部通过。OpenAPI 内容联合类型同步生成 Go/TypeScript，锁定官方 oapi-codegen/runtime v1.8.0 及所需依赖。数据库仍为 v27，没有修改已发布迁移。


### 输入准入 v2 与滚动升级

控制面发送单值 `X-Aigw-Admission-Version: 2` 和 `X-Aigw-Max-Input-Tokens`（目录审核上限，1–2,000,000）。版本与上限加入请求指纹，不能沿用同一 attempt 更改授权。数据面对协议转换、模型映射和参数覆盖后的实际请求计量，超限或不可计量时不取得持久派发权，也不访问供应商。计量不受原生全局 CountToken 开关影响；支持正文、工具 schema/参数/结果、图片尺寸与 detail，绝不下载外部 URL。

该值是 tokenkit 的本地估算，不保证等于供应商 tokenizer 或隐含上下文计数。最终结算仍只使用完整原始用量；超出审核界限的实际证据保留占额、进入恢复失败关闭，不能用估算伪造完成事实。未知模型的图片规则属于估算 fallback，目标模型仍须逐一校准和审核。

先升级数据面，再升级控制面。新数据面保留旧版无准入头请求及历史 outbox 恢复，便于先行部署；新版控制面启动/健康检查要求事件端点宣告准入 v2，且每个 JSON/SSE 成功响应必须回显版本与相同输入上限。旧数据面缺少能力时控制面拒绝就绪。回滚控制面意味着回到旧版准入能力，不属于同等治理保障，不应在生产未审查时执行。

合成工具/图片故障用例的输入上限提升到 1024，以覆盖实际本地估算；第四笔未知派发保留 2080 微元（1024×2 + 8×4），不是此前 128 上限的 288 微元。三笔完成仍由原始用量结算：工具 48、图片 108 微元。没有数据库 schema 变化，仍为 v27。

输入准入阶段完整门禁：固定数据面 `f95a8968a4165d2ce3eb4a880c921787760cdbd8` 的专用竞态 fixture，执行 `TEST_NEWAPI_SERVER_BINARY=... AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0。含全部后端竞态、真实 PostgreSQL、三协议文本/工具与双协议图片双进程故障、当前及旧版逻辑恢复。此前本地误用主程序入口的运行已废弃，不计入证据；CI 固定使用专用 fixture 入口。


### 最终派发配置复核

原有入口已拒绝渠道参数/头部覆盖、系统提示注入、body passthrough、全局 Chat→Responses 和自定义协议转换。本阶段在转换完成、取得持久派发权之前追加最终复核：运行时头部覆盖、`tls_insecure_skip_verify`、模型映射标记或实际模型名与审核模型名不一致均拒绝。受控目录的 `upstream_model` 必须直接填写真实目标模型，不能依赖 new-api alias、自动模型后缀改写或模型映射完成隐藏切换。

配置拒绝不访问供应商，写入 cancelled 凭据供控制面恢复退款，不生成完成用量。已覆盖三类真实 relay 中的 model_mapping/TLS 配置拒绝及单元测试的运行时头部变更。此复核不冻结渠道端点、代理和凭证，也不替代配置版本化审核；这些变化仍需运营变更管理和驻留复审，不能声称已实现完整渠道配置指纹。

最终派发复核阶段：固定 `ba305e7754a1fe37a2d76050e440ec95387bfe89` 专用竞态 fixture，完整执行 `AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0；三协议文本/函数、双协议图片故障矩阵、全部后端和真实数据库恢复通过。无数据库或公共 API 变化。


### 审核目录工具白名单

显式 newapi 模型目录新增 `"allowed_tools": ["lookup", "describe"]`，最多 8 个唯一函数名，字符集与 Chat function 名一致。未配置或空列表默认禁止客户端工具；升级既有工具模型前，必须在审核目录明确列出允许名称。原生直连配置不接纳非空白名单，不能以该字段暗示原生工具支持。

声明及历史调用中的函数名都必须在当前模型白名单内；拒绝发生在预算预留、attempt 创建和供应商派发之前。名称排序后参与不可变路由/价格版本，配置源的切片修改不能改变已发布策略；新策略需要按目录发布流程重启，已在途请求和历史账本沿用原快照。撤销不会中断已经取得派发权的请求。

这是模型目录级的函数名准入，不是工具服务器、MCP 网关或用户/动作级 RBAC。它不认证函数实现，不批准业务对象或参数，不校验任意 JSON Schema。客户端仍须在执行前检查参数 schema、调用者权限和业务条件；托管搜索/代码执行工具仍拒绝。无需数据库迁移，仍为 v27。

工具白名单阶段完整门禁通过：固定 `ba305e775` 数据面 fixture，执行 `AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0，覆盖全部后端竞态、默认/未审核拒绝、策略不可变与版本、原始费用/加密审计、三协议函数及双协议图片故障矩阵、当前/旧版逻辑恢复。公共 API 类型未改变。


### 受控文本 Embeddings

模型目录显式设置 `"api": "embeddings"`，`output_per_million` 与 `max_output_tokens` 必须同时为 0；`input_per_million` 保持正的审核价格，`max_input_tokens` 是整批输入的审核上限。不可同时开启图片或客户端工具。公共模型管理目录返回 `api: embeddings`，省略该字段仍为 Chat。该模式仅走审核的 new-api OpenAI 兼容渠道（type 1），不因通用 DTO 存在就宣称 DeepSeek/Anthropic 提供 Embeddings。

```json
{"model":"reviewed-compatible/embedding","input":["第一条合成文本","第二条合成文本"],"encoding_format":"float","dimensions":1024}
```

请求发送至 `POST /v1/embeddings`，同样使用项目 Key/设备准入、模型权限、预算、限流熔断和审计链。允许单字符串或最多 16 个非空字符串，每项最多 16 KiB、完整请求体最多 64 KiB。拒绝 token-ID 数组、媒体、user 透传、stream、工具与未知字段。可选 dimensions 为 1–4096，目标模型是否支持维度参数必须审核；encoding_format 只允许 float。数据面在转换后按每项文本估算输入，每项估算不超过 8192，整批不超过目录上限；仍非供应商精确 tokenizer 或账单。

响应必须是 list，具有与输入数量一一对应的唯一 index、非空有限浮点向量、统一维度，指定 dimensions 时须相符。每个向量最多 4096 维，整个供应商响应最多 1 MiB；这些独立上限不保证所有最大值组合都可用，高维批量需要缩小批次。返回值按 index 排序并使用客户端模型别名，去除其他供应商字段。拒绝 base64 向量、缺少 index、缺项、重复索引、非数值、维度不一致和缺失/冲突用量。

原始 Embeddings 用量必须明确含 prompt_tokens/total_tokens，二者相等且为正；completion_tokens 缺省或显式 0 只在此协议成立，不能用于放宽 Chat 的完整证据要求。输入用量乘以审核输入价格结算，不虚构输出 token、输出费用或 max_tokens。协议名纳入请求指纹并由数据面响应回显；配置 Embeddings 的新版控制面还检查 `X-Aigw-Embeddings-Version: 1` 能力，旧数据面不能让它就绪。

启用项目内容审计后，加密请求记录标记 protocol=embeddings 并保存输入，响应保存归一化向量；均受捕获上限、截断、查看授权与保留策略约束。向量可能携带敏感信息，不能因不是自然语言而免于数据治理。输入关键词照常检查；存在任何 output/both 文本策略时在派发前返回 embedding_output_policy_unsupported，不对向量伪造文本审核，也不改写向量数值。向量结构验收与供应商费用事实分离：若供应商用量完整但结果格式不可交付，恢复账本仍可能据事实结算，不能无依据退款。

新增 v28 追加迁移，放宽执行绑定中的输出价/上限至 0 并要求两者成对为 0 或同时为正，保留既有 Chat 约束。启动自动升级、事务回滚、校验和与漂移检查保留；不能把回退二进制等同数据库自动降级。先升级数据面，再启动控制面升级数据库。历史 Chat 记录、价格快照和 outbox 恢复兼容。

已验证合成 Embeddings 两次正常完成、控制面完成证据前 SIGKILL 与重启恢复、数据面已派发后 SIGKILL 的未知占额、无派发取消墓碑。三笔完成分别 2 input token、合计 12 微元；第四笔未知派发保留 256 微元，不增加物理调用。生产能力仍需要真实供应商账号、目标模型维度、原始账单与容量验收。

协议参考：[OpenAI Embeddings API](https://developers.openai.com/api/reference/resources/embeddings/methods/create)。本产品限制比该通用接口更窄，不等于完整兼容其所有输入和编码格式。

Embeddings 数据面验证：relay/common、channel/OpenAI/Claude/DeepSeek、service 全部竞态回归通过；治理 controller 专项通过。持久账本三数据库新建/官方 rc48 基线升级矩阵 6 项全部通过（SQLite 3.50.4、MySQL 8.4.13、PostgreSQL 17.11），命令 `go test -race ./controller -run ^TestGovernanceJournalDatabaseMatrix$ -count=1 -timeout=180s -v`，实际运行 32.954 秒。模型包的早期过滤命令没有匹配测试，不计入验证证据。

Embeddings 控制面完整门禁：固定 `7d06da50e22340da362b2d21c9dd8f7cb4a44d7e` 专用竞态 fixture，执行 `AIGW_VERIFY_WEB=1 AIGW_VERIFY_RESTORE=1 bash scripts/verify.sh`，退出码 0。包含 v28 自动升级/事务回滚/约束漂移、全部后端竞态、Embedding/Chat/函数/图片双进程故障、17 项 OIDC 与 1 项本地登录浏览器（77.217 秒）、当前和旧版逻辑恢复（4.285 秒）。公开 OpenAPI 与 Go/TypeScript 类型一致。
