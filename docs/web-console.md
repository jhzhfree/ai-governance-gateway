# Web 控制台与浏览器会话

本阶段将组织、项目、预算、密钥与审计管理接入真实后端。界面暂用「序衡」作为产品名，技术名称仍是 ai-governance-gateway。采用 React、TypeScript、Vite、TDesign、React Router 与 TanStack Query；不是静态演示页面。模型调用仍使用模拟供应商，真实企业 IdP 尚待外部环境验收。

## 功能与界面

- OIDC 登录、当前会话查询、退出与服务端会话撤销。
- 组织/项目切换，选择状态写入 URL；列表支持继续加载。
- 创建组织与项目，读取服务端有效角色和模型授权。
- 预算总额、已结算、预留和剩余额度；明确显示模拟单位及配置周期。
- 创建密钥、一次性展示和复制、查看元数据、按权限撤销。
- 组织审计记录，加载、空状态、失败重试、权限不足和会话过期反馈。
- 桌面侧栏、移动布局、键盘跳转、弹窗焦点处理与对比度检查。

本阶段没有成员编辑界面、模型/策略编辑器、调用调试器或真实计费。后端已有相应部分接口不代表这些界面已交付。`can_revoke` 是服务端返回的操作提示，执行 DELETE 时仍重新授权。

## 登录与安全边界

`GET /auth/login` 创建五分钟登录流程并跳转到可信 IdP。使用授权码、PKCE S256、nonce、随机 state 和绑定浏览器的 HttpOnly Cookie；回调原子消费 state，拒绝错误绑定、nonce 不符与重放。

服务器交换 code，分别验证面向浏览器 client ID 的 ID Token、面向管理 audience 且具备 aigw:manage scope 的 API JWT；两者必须指向同一 issuer/subject。浏览器不接触 OAuth token，服务器也不持久化 access/refresh token。

登录成功生成随机会话凭证，数据库只存摘要及身份。会话最多八小时，且不超过 ID Token、API JWT 和 token response 的已知有效期；没有静默刷新。再次登录旋转旧会话，退出删除服务端会话。IdP 登出和 back-channel logout 未实现。

HTTPS 使用 `__Host-aigw_session`、Secure、HttpOnly、SameSite=Lax、Path=/、无 Domain。仅显式允许的 loopback HTTP 开发模式使用独立 Cookie 名。应用按配置的 canonical origin 检查 Host，不信任转发头；反向代理必须保留原始 Host。

Cookie 管理写操作和退出必须同时提供精确匹配的 Origin 与 X-CSRF-Token。CSRF token 从会话凭证做单向域分离派生，通过同源会话接口读取，仅保留于内存。API Bearer 认证仍可供 CLI 使用，不依赖浏览器 Cookie。

所有查询缓存按身份与资源作用域隔离；退出或收到管理接口 401 时移除身份数据并关闭工作空间。密钥明文只在弹窗组件内存中存在，不进入查询/变更缓存、localStorage 或 sessionStorage。关闭弹窗立即丢弃。

登录流程暂存短期 PKCE verifier，回调时删除；过期记录在后续登录时清理。独立定时清理任务尚未实现。错误回调返回不含上游敏感信息的通用错误；完整的登录取消/失败引导仍可继续改进。

## 本地运行

使用 `.go-version` 与 `.node-version` 指定的工具链：

```bash
npm --prefix web ci
npm --prefix web run api:check
npm --prefix web run build
```

在原有数据库、OIDC issuer/API audience 和 mock upstream 配置基础上，添加：

| 变量 | 用途 |
| --- | --- |
| AIGW_BROWSER_ORIGIN | 浏览器访问的 canonical origin，不含尾部斜线 |
| AIGW_OIDC_CLIENT_ID | 浏览器 OIDC 客户端 ID |
| AIGW_OIDC_CLIENT_SECRET | 机密客户端按 IdP 要求安全注入；公共客户端可留空 |

IdP 必须注册 `${AIGW_BROWSER_ORIGIN}/auth/callback`，支持授权码和 PKCE，并为该客户端发行符合管理 API audience 与 aigw:manage scope 的 JWT。不要使用 ID Token 冒充 API access token。

```bash
go run ./cmd/mock-provider
```

在另一终端加载环境配置后启动：

```bash
go run ./cmd/migrate up
go run ./cmd/gateway --mock-only --web-dir web/dist
```

本机 HTTP 开发可将 origin 设为 `http://127.0.0.1:8080` 并显式增加 `--allow-loopback-browser-http`。真实 IdP issuer 仍要求 HTTPS；不存在跳过证书/签名验证的选项。Vite 开发代理可用，但浏览器 origin 和回调必须对应实际前端端口；生产风格复验使用 Go 同源服务构建产物。

构建目录未提交到 Git，发布前必须构建。服务仅开放构建入口与 assets，不提供源文件目录浏览；配置 CSP、no-sniff 和 Referrer-Policy。当前 readiness 已接入[迁移版本与结构核对](database-migrations.md)，仍不代表真实 IdP 健康。

## 验证

```bash
AIGW_VERIFY_WEB=1 bash scripts/verify.sh
```

需要本地 Docker，或显式提供专用 TEST_DATABASE_URL。可运行 `cd web && npx playwright install --with-deps chromium` 安装浏览器，或通过 PLAYWRIGHT_CHROMIUM_EXECUTABLE 指向现有 Chromium。CI 使用相同脚本。

验证执行：后端 race 与 PostgreSQL 集成测试、登录/PKCE/nonce/CSRF/退出测试、npm ci、生成类型差异、TypeScript、ESLint/React Hooks、Prettier、生产构建，以及真实 Chromium 全流程。Go 校验限定 api/cmd/internal，避免遍历 node_modules 中附带的 Go 源码。

本阶段实际验证（2026-11-03）：26 个后端/会话顶层测试、16 个 Go 子测试，以及 3 个真实 Chromium 端到端场景通过，0 失败、0 跳过；浏览器测试由额外的 Go fixture 测试承载。上述格式、契约、构建和当前页面 axe 检查均通过。已检查桌面和移动端截图；这不代表真实企业 IdP 或 GitHub 托管 CI 已验收。

浏览器测试连接真实 PostgreSQL 和本地 RSA/JWKS 测试 IdP，覆盖创建组织/项目、密钥只展示一次、撤销、退出、移动端无横向溢出、键盘跳转、接口失败重试、审计及过期会话清理。页面运行 axe 的 WCAG 2 A/AA 和 2.1 AA 标签检查；自动检查通过不等于完整无障碍认证。

## 后续边界

版本化数据库迁移已补充；仍需真实 IdP 验收、真实供应商、TUI/CLI、成员与策略编辑界面、限流和生产部署。迁移执行器尚未验收生产大库和非事务 DDL，不代表部署已经就绪。

阶段完成后按用户要求先提交并推送 GitHub，再进入后续开发。浏览器测试数据、截图、会话与依赖缓存均不提交。

新增“调用账本”资源页、账本状态 URL 筛选和 request_id 详情，复用项目权限与主体/项目缓存隔离。语义及验证边界见[调用账本查询](request-history.md)。

现已增加预算单位选择、真实币种项目的显式模型授权、密钥多模型选择及手动刷新。人民币按微人民币整数输入，默认预算 1,000,000 单位对应 1 元。模型目录和区域信息来自管理 API，不能替代合同驻留审查，见[三家大陆供应商接入](mainland-providers.md)。

流量与核查页增加供应商运行状态：按授权模型显示共享熔断、等待提示和全周期未结占额；当前实例 HTTP 计数/延迟分布单独展示，并注明重启清零及 SSE 状态限制。详见[运行手册](supplier-resilience.md)。

CNY_MICRO 项目的流量与核查页新增账单工作台：标准 CSV、差异/重复候选、导入人不可自批、审核决定及原因；未结与终态调整边界见[对账手册](billing-reconciliation.md)。成员管理仍通过 API 设置，不将该工作台视为完整财务系统。

新增“成员管理”：组织 owner/admin 可读，owner 编辑组织成员；组织 owner/admin 管理项目显式授权，页面展示组织继承与实际生效角色，移除需确认。不创建企业 IdP 账号，不自动撤销项目密钥。见[操作手册](model-discovery-and-members.md)。
