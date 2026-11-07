# new-api 底座落地：隔离验证与阶段进展

## 分支与当前状态

- 企业工程：`feature/newapi-foundation-validation`，从 `f3db909` 创建。
- new-api fork：`feature/enterprise-governance-integration`，从 `e03439033238722c0b8d4437a052445f3aeeed83` 创建；第一阶段未修改该仓库，后续原始用量事实实现见下方阶段进展。
- 路线：企业入口保留身份、项目、预算、终端与审计；new-api 承接协议/渠道执行，通过明确的用量和物理尝试契约对接。
- 本阶段新增实验性 `internal/newapibridge`，实现 `gateway.ProviderExecutor`。**没有接入生产启动参数，没有切换默认执行器，也没有宣称普通 new-api 已满足下述契约。**

## 已落地的接入边界

1. 仅使用独立内部服务 Token；不复制用户 Authorization、Cookie 或 route 中的厂商秘密。
2. 固定 origin 和 `/v1/chat/completions` 路径，模型由网关别名映射到经配置的后端模型名。没有 URL 参数/用户信息/子路径支持；HTTPS 默认必需，仅显式测试选项允许 loopback IP HTTP。
3. 配置中的 Token 不进入 JSON 或常用格式化输出。模型映射在创建时复制，调用者不能事后修改映射。
4. Validate 不发请求；Open 仍由原治理链在预算预留、attempt 和 dispatch 记录后调用。认证、模型授权和预算并未搬到客户端。
5. HTTP 不跟随重定向，不做应用层自动重试；响应头上限 16 KiB，连接/响应时间有界；并发持有到 Body 关闭/结束，Close 取消并回收在途流。
6. JSON/SSE 正文继续经过现有严格解析、内容治理和结算链。响应头通过不替代完整 usage 和流结束检查。

## 待 new-api 实现的证据契约（草案 v1）

企业入口发送 `X-Aigw-Request-Id` 和 `X-Aigw-Attempt-Id`。后端成功响应必须各出现一次：

| 响应头 | 初期要求 |
| --- | --- |
| X-Aigw-Evidence-Version | `1` |
| X-Aigw-Request-Id | 与本次业务 request_id 完全一致 |
| X-Aigw-Attempt-Id | 与本次网关 attempt_id 完全一致 |
| X-Aigw-Usage-Source | `upstream`，不允许将估算结果作为实际用量 |
| X-Aigw-Physical-Attempts | `1`，首期不允许透明内部重试 |

这些头是我们拟定的内部协议，**不是 stock new-api 的已存在功能**。当前普通兼容响应没有该证据时，实验适配器明确失败，派发后的企业费用保持 uncertain，而非退款或当成零消费。

响应头本身不是密码学证明或持久账单，不能由反向代理无条件补齐，也不能透传上游/用户伪造值。第二阶段需要在 new-api 内部、基于实际执行事实生成，并配合可信服务身份、持久事件、严格原始用量来源与回归验证。流式 header 的声明只有在最终正文证据完整时才可用于结算；它不能提前证明流会完成。

## 目前测试证据及限制

新增五个顶层回归，测试用专用合成 HTTP 服务模拟未来契约，不伪装为 new-api 实机联调：

- 服务凭证/模型映射隔离、缺证据/错 attempt/重复证据/估算/多次物理尝试的拒绝、307 不跟随且无重试。
- 流持有并发槽位，超额立即拒绝；关闭执行器取消流并回收，关闭后不能接纳。
- origin/凭证边界及秘密格式化/JSON 输出检查。
- 真实隔离 PostgreSQL + 原 gateway.Handler：预算不足不联系后端且不创建预留；有效 JSON 费用结算；缺少证据时返回失败，状态 uncertain，保留 288 微元占额，不计作成功费用。
- 真实数据库下 SSE：完整 usage + DONE 才结算；缺 usage 或缺 DONE 保留占额。测试金额均为合成规则。

实际 new-api checkout 本轮已 `go build -mod=readonly` 成功并通过 `--help` 启动检查。使用当前云环境 Go 1.27.3 编译（源码声明最低 1.26.10），不是该项目固定发布工具链/完整测试矩阵的验证。没有启动对外 HTTP 管理端、没有配置真实凭证、没有进行真实模型调用；未运行 new-api 的 SQLite/MySQL/PostgreSQL 完整测试矩阵。

## 下一阶段顺序与完成条件

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
