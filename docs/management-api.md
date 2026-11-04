# 管理 API、权限与项目密钥

管理/推理后端保留 mock-text 闭环，另支持显式配置的三家大陆供应商及 CNY_MICRO。OpenAPI 3.0.3 契约见 [management.json](../api/openapi/management.json)。后续已增加 [Web 与浏览器会话](web-console.md)，另有 [CLI/TUI](terminal-client.md) 与[大陆供应商适配](mainland-providers.md)，真实账号与驻留仍待验收。下文 JWT 描述适用于直接 API 客户端，浏览器另外使用 Cookie + CSRF。

## 身份与权限

管理端接受 OIDC discovery/JWKS 验证的 RS256 JWT，必须匹配配置的 issuer、管理 API audience、有效期和 `aigw:manage` scope。scope 只允许进入管理 API，不直接授予组织权限。可信用户的身份由 `(issuer, subject)` 唯一标识，不信任请求中的用户 ID、角色头或 JWT 自带业务角色。

当前适配的是带上述约束的 JWT API 令牌；不支持 opaque access token/introspection。使用 coreos/go-oidc 进行签名与标准 claims 校验，不是浏览器 ID Token 登录流程。企业 IdP 必须为管理 API 单独配置 audience 和 scope。服务入口要求 HTTPS issuer，不提供跳过签名验证或开发管理员头。JWKS 依赖可能缓存公钥，OIDC 自身撤销语义取决于 IdP 和令牌有效期；本轮未实现 IdP back-channel logout。

| 操作 | 组织 Owner | 组织 Admin | 组织 Viewer | 组织 Developer |
| --- | --- | --- | --- | --- |
| 读取组织、项目和密钥元数据 | 全部 | 全部 | 全部 | 仅已分配项目 |
| 创建项目、修改模型白名单 | 可以 | 可以 | 不可以 | 不可以 |
| 修改组织成员角色 | 可以，仅非 Owner | 不可以 | 不可以 | 不可以 |
| 分配项目角色 | 可以 | 可以 | 不可以 | 不可以 |
| 创建密钥 | 可以 | 可以 | 不可以 | 由项目角色决定 |
| 撤销密钥 | 任意项目密钥 | 任意项目密钥 | 不可以 | 由项目角色决定 |
| 查看组织审计 | 可以 | 可以 | 可以 | 不可以 |

组织 Developer 需要显式分配项目角色：项目 Admin 可创建/撤销该项目任意密钥；项目 Developer 可创建密钥、撤销自己创建的密钥；项目 Viewer 只读。组织 Viewer 即使存在旧的项目高权限记录也保持只读。组织角色变化直接影响后续管理请求，不需要刷新 JWT。

已验证的管理身份可以创建新组织，自动成为唯一 Owner；没有匿名注册、密码系统或全局管理员。暂不支持 Owner 转移、成员删除、项目删除和预算修改。组织成员与项目角色的写入使用事务锁避免并发授权变更穿透。

## API 范围

| 方法与路径 | 功能 |
| --- | --- |
| GET /api/v1/models | 当前启用的模型/供应商/区域/价格元数据，无密钥与合同编号 |
| GET /api/v1/organizations | 当前身份所属组织 |
| POST /api/v1/organizations | 创建组织、Owner、组织预算与审计 |
| PUT /api/v1/organizations/{org}/members | 添加或更改非 Owner 成员 |
| GET /api/v1/organizations/{org}/projects | 按权限过滤项目列表 |
| POST /api/v1/organizations/{org}/projects | 创建项目与项目预算；MOCK 默认授权 mock-text，真实币种默认空白名单 |
| GET /api/v1/organizations/{org}/projects/{project} | 项目详情、有效角色与模型列表 |
| PUT /api/v1/organizations/{org}/projects/{project}/members | 设置项目角色 |
| PUT /api/v1/organizations/{org}/projects/{project}/models | 设置模型白名单，空数组禁用 |
| POST /api/v1/organizations/{org}/projects/{project}/keys | 创建密钥，仅本次返回 secret |
| GET /api/v1/organizations/{org}/projects/{project}/keys | 查看密钥元数据，包含已撤销记录 |
| DELETE /api/v1/organizations/{org}/projects/{project}/keys/{key} | 幂等撤销 |
| GET /api/v1/organizations/{org}/projects/{project}/usage | 当前配置周期内的已花费和预留 |
| GET /api/v1/organizations/{org}/audit | 组织审计元数据 |

列表采用 `limit`（默认 50，最大 100）和 `after` 游标，响应为 `{data, next_cursor}`，空 next_cursor 表示结束。按 ID 排序，不保证按创建时间排序，也不是并发写入下的快照分页。审计 after 为非负十进制事件 ID。

资源不可见时返回 404，已可见资源的操作权限不足返回 403。无效凭证为 401，数据库不可用为 503，均不回显底层异常。管理响应统一 `Cache-Control: no-store`，错误包含 request_id；未知路径或未实现方法返回结构化 404。

金额单位由组织 currency 明确区分 MOCK 测试整数与 CNY_MICRO（1 元 = 1,000,000 单位），不进行币种兑换。管理 API 金额上限为 JavaScript 安全整数，避免未来前端舍入；底层账本仍使用 int64。组织创建时固定预算周期；本阶段不自动跨月轮转，不应把配置周期的预算当作当前自然月预算。

## 密钥生命周期

创建密钥时生成高熵随机值，格式前缀为 `aigw_pk_`。数据库只保存 SHA-256 摘要、展示前缀和元数据。随机密钥不使用用户密码式口令，因此摘要不需要密码哈希的慢算法。密钥有效期限定 60 秒至 90 天。

创建响应包含 secret；没有二次查询或恢复接口。若响应丢失，应创建新密钥并撤销旧记录，不自动重试非幂等创建。日志、审计、列表均不包含完整密钥或摘要。只有撤销操作明确支持幂等重试。

推理调用使用 `Authorization: Bearer <project-key>`。服务端从摘要记录确定组织、项目、预算周期和币种，忽略客户端的组织/项目头。每次新请求查询数据库中的有效期、撤销状态、密钥授权模型和项目实时白名单，不存在凭证缓存失效窗口。授权查询已开始的在途请求可能继续完成，不提供强制中断在途请求的承诺。

项目密钥属于项目服务身份。创建者角色降级不会自动撤销其已有项目密钥；禁用访问必须显式撤销密钥或模型授权。这与管理 API 的成员角色校验分开处理。

管理 JWT 不能调用推理接口，项目密钥不能调用管理接口。推理密钥不会透传给供应商。所有配置变更与审计写入同事务提交；当前审计记录操作者、动作、资源 ID 和时间，完整字段变更 diff 尚未实现。

## 本地启动

准备专用 PostgreSQL，以及可信的 OIDC 测试/企业 IdP。按 [.env.example](../.env.example) 设置进程环境；不要在聊天、日志或版本库中放入实际凭证。环境文件不会被程序自动加载。

先启动模拟供应商：

```bash
go run ./cmd/mock-provider
```

再启动网关：

```bash
go run ./cmd/migrate up
go run ./cmd/gateway --mock-only
```

默认监听 `127.0.0.1:8080`，只允许 loopback listener 与固定路径的 loopback HTTP 模拟上游，拒绝重定向。先执行[版本化迁移](database-migrations.md)；原 `--init-schema` 是 `--migrate` 的弃用别名，也会严格核对旧结构。不要清空既有原型数据。

`/health/live` 检查进程存活，`/health/ready` 只读核对数据库版本、迁移文件校验和和受管表结构。它不代表 IdP 可达性或上游健康。没有 OIDC 配置时拒绝启动，不提供无鉴权降级模式。

获得符合 audience/scope 的管理 API JWT 后，可以按契约依次创建组织、项目、密钥，再用项目密钥完成 SSE 调用、查看用量并撤销。浏览器 Cookie 会话/CSRF 已提供；CLI Device Flow 与令牌安全存储仍待实现。

## 测试与剩余范围

使用 `bash scripts/verify.sh`。集成测试启动真实 PostgreSQL、带 RSA 签名与 JWKS 的本地测试 IdP，以及模拟供应商；管理请求和响应按 OpenAPI 校验。测试凭证仅在内存中使用，不写入文件或日志。

本轮验证（2026-11-03）：23 个顶层测试、16 个子测试通过，0 失败、0 跳过。格式、模块完整性、go vet、OpenAPI 有效性及实际管理 HTTP 契约、Go race 检查通过。验证在当前环境执行，未声称 GitHub 托管 CI 已运行。独立服务命令仍需用户配置真实可信 IdP；本轮完整 HTTP 闭环运行于测试服务器。

验证覆盖：跨组织/跨项目访问、成员角色和权限升级拒绝、开发者撤销归属、密钥摘要与一次性展示、有效期、撤销、实时模型授权、数据库失败关闭、审计故障回滚、分页和输入边界；同时保留前一轮预算/SSE/race 测试。

尚未验证真实企业 IdP；目前只支持 RS256 JWT。版本化迁移和 Web 登录客户端已补充，生产仍需速率限制、完整审计 diff、密钥轮转运维、真实供应商与计价、可观测性及部署验收，不能标记整个产品生产就绪。

项目调用账本列表/详情已通过同一管理身份与项目授权接入，含状态筛选及复合游标，见[查询契约](request-history.md)。账本结算状态不能替代协议成功或客户端送达状态。

组织创建可显式选择 currency=MOCK 或 CNY_MICRO，部署目录须支持该单位，组织创建后不可通过 API 换币种。模型和项目密钥支持最多 32 个不重复的目录别名；模型授权要求与组织币种一致。真实模式、定价/usage 及区域限制见[大陆接入说明](mainland-providers.md)。

调用记录可空 attempt 对象与独立 outcome 筛选已接入，保持原组织/项目授权；不增加全局 attempt 查询。见[执行结果与故障语义](execution-metadata.md)。

项目级 `GET /runtime` 返回已授权模型的共享熔断、全周期未结占额和可空的当前实例 HTTP 观测；查询不触发供应商探测。详见[运行手册](supplier-resilience.md)。

标准账单导入、项目差异查询和独立管理员审核位于项目 `/billing/imports`、`/billing/records`、`/billing/records/{record}/review`；权限、原子财务与幂等边界见[对账手册](billing-reconciliation.md)。

组织/项目 members 支持受权 GET 分页；PUT 增加可选 expected_role，DELETE 项目 members 撤销显式项目授权。另提供推理密钥 GET /v1/models。权限、向后兼容及项目密钥独立生命周期见[操作手册](model-discovery-and-members.md)。
