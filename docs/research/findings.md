# 开源源码研究与验证结论

研究日期：2026-11-03。范围为 Bifrost、LiteLLM、Coze Studio、Dify、TDesign React、Bubble Tea、Cobra、Casdoor。以下记录初次研究快照；当时未引入产品依赖或复制上游业务实现。后续实际采用见 [ADR 0004](../adr/0004-terminal-client-and-device-login.md) 和[当前依赖记录](../open-source-references.md#5-终端阶段实际采用)。

## 1. 验证结果与边界

这次完成固定提交的源码检查、根许可证检查和部分运行测试，不是八个平台完整部署、安全审计或性能比较。源码存在某种能力，不代表部署配置正确，也不代表已验证生产可用。

| 项目 | 实际验证 | 结果与局限 |
| --- | --- | --- |
| Bifrost | 阅读 Provider、插件生命周期、消息交接和取消回归测试；尝试在 core 模块运行 3 个测试 | 依赖下载被网络策略拒绝，测试未进入执行；不能宣称通过或发现代码缺陷 |
| LiteLLM | 阅读预算预留、结算和中间件；直接加载未修改的中间件执行本项目研究探针 | 6 个测试通过；覆盖 HTTP/WebSocket、异常、取消、无预留和释放失败传播，不验证完整代理或预算并发一致性 |
| Bubble Tea | 原有事件循环、退出、context 和死锁回归测试 | 7 个顶层测试及 2 个子测试通过，0 失败、0 跳过；未验证真实交互终端、钥匙串和 SSH 环境 |
| Cobra | 原有 `go test ./... -count=1 -timeout=120s` | 根包和 doc 包通过；没有据此推断本项目 CLI 已实现 |
| Coze Studio | 应用入口与依赖、发布服务及 repository 边界 | 静态检查；未安装或启动整个平台 |
| Dify | 发布可见性过滤、Web 依赖及许可证 | 静态检查；未启动、未测性能 |
| TDesign React | ConfigProvider 与远程分页示例 | 静态检查；未做浏览器渲染、构建及可访问性验证 |
| Casdoor | token 控制器、Device Flow 分支与授权状态存储 | 静态检查；未验证完整 OIDC 登录、令牌吊销及多副本部署 |

临时使用官方 Go 1.27.3 工具链，下载归档 SHA256 与 Go 官方发布元数据核对一致；GOPATH/GOCACHE 均位于临时研究目录。Bubble Tea 依赖使用 Go 原有模块校验机制。LiteLLM 探针使用环境已有 Starlette 1.6.0，不代表已匹配 LiteLLM 全部锁定依赖。

Bifrost 阻塞：`github.com/klauspost/compress@v1.20.0` 的模块下载跳转到 `storage.googleapis.com`，收到 Forbidden。没有关闭 TLS、修改校验和、替换依赖或绕过网络策略。解除该域名访问限制后应重跑目标测试，再决定是否直接采用其核心。

固定 commit、许可证初查和永久源码链接见 [源码索引](sources.md)。根许可证并不覆盖所有子目录、资产或商标的最终采用审查。

## 2. Bifrost：借鉴生命周期，收窄接口

源码依据：`core/schemas/provider.go`、`core/schemas/plugin.go`、`core/bifrost.go`、`core/abandonedstream_test.go`。

- Provider 覆盖聊天、Responses、音频、图像等大量能力；对成熟平台合理，但超出本项目首期文本对话范围。
- 插件说明区分请求级与 attempt 级阶段，Pre/Post LLM hook 可在 fallback 中再次执行，并允许修改结果或短路。
- 消息交接通过 claim/abandon 状态竞争决定结果归属；取消测试专门覆盖放弃接收后生产者阻塞、终结钩子执行和结果归属。

设计决定：首期只定义文本 `Complete`/`Stream` 能力，后续按能力增加独立接口。每个逻辑请求拥有 request_id，每次上游尝试拥有 attempt_id。业务预算预留放请求级，尝试成本单独记账；不能将计费简单放到可能多次触发的 Post hook。

不引入通用动态插件、WASM、Agent/MCP 引擎。先用编译期显式流水线和依赖注入，保证鉴权 → 策略 → 预算的顺序可读、可测。供应商协议转换封装在适配器内部，Web 和 TUI 不依赖供应商 SDK。

取消不仅是调用 `cancel()`：还需确保生产者能退出、缓冲有界、上游连接被关闭，以及终结逻辑有唯一归属。本工程使用尽可能直接的同步流转发；若使用 goroutine/channel，必须明确谁关闭 channel、谁释放响应体、谁执行最终结算。

## 3. LiteLLM：预留方向正确，严格度必须成为明确契约

源码依据：`budget_reservation.py`、`cost_finalization.py`、`budget_reservation_release_middleware.py`。

关键观察：

1. 请求先估算最大成本再预留；成本未知时函数可返回 None，退化到读时检查。
2. `fail_closed_budget_enforcement` 参数默认 False；部分不足额情况允许将预留缩减到剩余额度。因此不能仅凭“存在预留”推断所有配置都保证严格上限。
3. 取消路径专门处理 CancelledError/GeneratorExit，并用 shield 保护清理；该函数按输入估算成本结算并释放输出预留。这是实现策略，不是所有供应商都只收输入费用的证明。
4. 中间件在 finally 调用释放函数，实际是否应释放由 callback ownership 和 finalized 状态判定。研究探针确认 finally 在被测试的异常与取消路径执行，也确认释放函数抛错会向外传播。

本工程采用的账本契约：

```text
reserved → dispatched → settled
    │           └──→ uncertain → settled / estimated_final
    └──→ released（确认尚未发送上游）
```

- `reserved`：按价格版本与输出上限原子预留；同一 request_id 重试预留返回已有结果。
- `dispatched`：发送前持久化保守的派发标记；标记后崩溃也可能尚未真正发送，因此恢复时优先避免错误退款。
- `uncertain`：断连、超时或崩溃后无法确认最终 usage；保留预留，进入核对，不将取消视为免费。
- `settled`：有可信 usage 的最终结算；唯一约束和事务保证幂等。
- `estimated_final`：无法对账时按明确策略保守结算，保留估算标记与审计；后续可信账单可追加修正事件，不能修改历史而无痕。

组织和项目两级预算在同一事务内按固定顺序锁定。金额与价格用定点数/整数表示，价格版本、币种、预算周期 ID 与预留绑定；跨周期结束的请求仍结算回原周期。费率变化不追溯更改在途预留。

取消上游用请求 context；结算使用脱离客户端取消信号的独立有界 context，并保留数据库恢复任务。进程退出仍可能打断清理，所以不能只依赖 defer、后台 goroutine 或内存 finalized 标记。

硬限额只对能保守计算成本上界的模型开放；成本上界未知、计价维度不支持或无法预留时拒绝硬限额模式的新请求。若未来提供软预算，必须单独命名并说明可能超额，不做隐式退化。

## 4. Coze Studio 与 Dify：采用版本发布思想，控制工程规模

Coze Studio 的 app service 将发布校验、版本创建、资源打包和连接器状态更新组织为显式流程，并通过 repository 访问数据。前端入口采用 RouterProvider/Suspense，应用包有大量 workspace 依赖，并使用 Rsbuild。它适合研究模块边界，不适合把整个大型 monorepo 模板搬入初创网关。

Dify 的 `public_runtime.py` 区分 draft 与 published 引用，检查发布产物属于同一应用/工作空间；Web 包依赖 Next。说明成熟产品会严格区分编辑态与运行态，但不说明本工程必须引入 Next 或整套工作流。

本工程策略配置采用 `draft → validated → active → superseded`：发布事务创建不可变版本、切换 active 指针并写审计。请求开始时固定策略版本，避免同一请求前后读到不同规则；发布使用期望版本校验防止并发覆盖。回滚是激活已有版本的新增审计操作，不删除历史。密钥撤销等紧急安全检查保持独立快速失效，不被长期策略快照掩盖。

首期 Web 继续 React + Vite；按业务 feature 拆目录，暂不采用微前端、大型多包构建或 Next 服务端运行时。参考其工作空间与调试流程，保留本产品的治理导航。

## 5. TDesign：适合作为组件底座，示例不等于业务架构

ConfigProvider 提供语言/全局配置上下文，并存在供插件使用的模块级全局配置。远程分页示例以组件 state 和 fetch 展示 API，用静态 total 和示例数据；这是演示组件，不是完整数据层。

本工程在应用根部统一 ConfigProvider。项目权限、筛选和组织信息不放入组件库全局配置；请求缓存键必须包含组织、项目和筛选条件，退出/切换身份时清理授权相关缓存。表格由 URL 驱动筛选分页，TanStack Query 负责请求、取消与缓存，后端返回真实分页数据。

只封装高频业务组合 `ResourceTable`、`FilterBar`、`StatusTag`、`DetailDrawer`；避免重新封装所有基础组件。初期使用分页，不提前引入复杂虚拟滚动。键盘操作、焦点恢复和错误提示仍需浏览器验证。

## 6. Bubble Tea 与 Cobra：双模式入口，版本明确

当前 Bubble Tea 快照模块路径为 `charm.land/bubbletea/v2`，go.mod 要求 Go 1.26.0，Model.View 返回 `tea.View`，不是旧版 string。不能将当前主分支与 v1 的示例、Bubbles 或 Lip Gloss 版本任意拼接。

Cobra 提供 ExecuteContext/Context，适合将退出信号传给命令及 API 调用。Cobra 负责参数、子命令和退出行为；Bubble Tea 负责交互状态。

本工程采用两个入口模式：`aigw projects list --output json` 与 `aigw tui`。JSON 模式 stdout 只输出数据，错误走 stderr；非 TTY 不自动进入 TUI。TUI 网络任务运行于 Cmd 中，返回携带资源上下文和请求序号的消息，丢弃切换项目后的过期响应。关闭页面或退出程序时取消相应请求。

后续固定一个已发布稳定版本及其兼容组件，不直接把本次研究 HEAD 当作依赖版本。当前测试证明所选源码路径能运行，不等于已验证完整 TUI 产品体验。

## 7. Casdoor：确认 Device Flow 代码路径，部署配置仍是验收重点

token 控制器与 OAuth 实现存在 `urn:ietf:params:oauth:grant-type:device_code` 分支，并处理等待授权、过期和重复使用等情况。device_auth_store 默认内存存储，配置 Redis 后可切换共享存储；初始化失败会警告并退回内存。Redis 路径的消费操作使用 GetDel。

因此可以将 Casdoor 保留为身份服务候选，但多副本验收必须验证共享状态实际启用、Redis 支持 GETDEL，并测试用户授权和轮询落到不同实例时的行为，不能仅根据配置项判定可用。

业务项目权限留在本工程；IdP 只提供可信身份。Web 使用会话 Cookie 和 CSRF 防护，TUI 使用面向管理 API 的短期令牌；API 验证 issuer、audience、有效期和 scope，绝不接受项目推理密钥访问管理接口。优先使用已有企业 IdP，不将新增 Casdoor 作为首期强制依赖。

## 8. 最终设计建议与实施顺序

保留 Go 模块化单体 + PostgreSQL + Redis + React/TDesign + Cobra/Bubble Tea。首期自主实现小范围治理核心和单一 OpenAI 兼容适配器；Bifrost/LiteLLM 继续作为源码与测试场景参考。暂不 fork 全平台，也不同时运行两套权威预算系统。若后续供应商数量显著增加，再以已通过契约测试的边界评估嵌入或独立接入成熟网关。

实际落地顺序：

1. 定义 request/attempt/预算状态和管理 OpenAPI，固定错误码与权限矩阵。
2. 先做 PostgreSQL 原子预留、幂等结算和崩溃恢复验证，确定硬限额支持边界。
3. 用模拟供应商实现最小适配器，验证 SSE 分片、错误、中断、背压和取消后的结算。
4. 用同一 API 做 Web 项目/记录页面与 CLI 查询，再增加 TUI；完成身份接入。
5. 验证策略发布版本、多实例失效和日志脱敏后，接入首个真实供应商。

下一阶段退出条件：干净环境可重复运行；并发预算与取消/恢复测试通过；Web/TUI 无跨组织缓存串数据；真实身份流程可用。现阶段尚未满足这些产品实现条件。

## 9. 复现命令

先检出 [源码索引](sources.md) 对应 commit，使用满足模块要求的 Go，保持默认 TLS 与模块校验。网络必须允许依赖下载。

```bash
# Cobra 仓库根目录
go test ./... -count=1 -timeout=120s

# Bubble Tea 仓库根目录
go test -p 2 -json ./... -run '^(TestTeaModel|TestTeaQuit|TestTeaContext|TestTeaContextImplodeDeadlock|TestTeaContextBatchDeadlock|TestTeaNoRun|TestTeaNoTerminalQueriesWithoutInput)$' -count=1 -timeout=120s

# Bifrost 仓库的 core/ 目录；本轮因依赖下载受限未执行成功
go test -p 2 -json . -run '^(TestDrainAbandonedStream_UnblocksProducer|TestDrainAbandonedStream_NilIsSafe|TestChannelMessageHandoffIsExclusive)$' -count=1 -timeout=120s

# 本工程根目录；需要 Python 3.10+ 和 Starlette
python3 docs/research/validate_litellm_middleware.py /path/to/litellm
```

LiteLLM 探针不启动代理、不连接数据库，不验证金额计算或完整请求取消。它确认的是所选中间件的控制流，不应扩大为“LiteLLM 预算验证通过”。
