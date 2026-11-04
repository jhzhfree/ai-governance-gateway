# CLI 与终端控制台

本阶段提供只读管理客户端：组织/项目、预算用量、密钥元数据、组织审计，以及经服务端验证后保存的本地上下文。它调用与 Web 相同的 `/api/v1`，不访问数据库，不接收推理项目密钥作为管理身份。现已提供[调用账本列表与 request_id 详情](request-history.md)，只展示账本事实，不覆盖未建立预留的失败或完整协议日志。

## 构建与登录

使用 `.go-version` 固定的 Go 1.27.3：

```sh
go build -mod=readonly -o ./bin/aigw ./cmd/aigw
./bin/aigw --help
./bin/aigw login --server https://gateway.example \
  --issuer https://idp.example/realms/team --client-id aigw-terminal
```

身份管理员需创建支持 RFC 8628 Device Authorization Grant 的**公共客户端**，无需 client secret，允许 `openid aigw:manage` scope。签发的 access token 必须为管理 API 支持的 RS256 JWT，issuer 与服务端配置一致，audience 默认为 `aigw-management`（可用 `--audience` 指定）。audience 是本地验证目标，不会作为供应商私有 OAuth 参数发送；具体 IdP 的 audience mapper/resource 配置须单独验证。浏览器 Web 登录客户端与终端公共客户端应分别注册。

命令在 stderr 展示浏览器地址和用户验证码，轮询等待授权。成功前校验 issuer、audience、签名、过期时间、管理 scope，并实际请求目标服务的组织列表。令牌不写入 stdout，也不保存 refresh token；到期重新登录。`--timeout` 默认 5 分钟，Ctrl+C 可取消轮询。

默认使用系统钥匙串。Linux 需要可用的 Secret Service/会话总线；不可用时登录明确失败，不静默落盘。确需无钥匙串登录时显式添加 `--credential-store=file`，凭证写入配置旁的 `config.json.credentials.json`。配置目录必须私有（0700），配置及凭证必须为私有普通文件（0600），拒绝共享目录和符号链接；不要将它们提交版本库。验证基于 Linux POSIX 权限，Windows ACL 与真实桌面钥匙串尚未验收。

默认配置为系统用户配置目录下的 `aigw/config.json`，可用 `--config` 自定义到私有目录。配置只保存服务、身份绑定和当前组织/项目。凭证绑定 server、issuer、client ID、audience；不允许用 `--server` 将已有凭证转发到其他服务。文件分别原子替换，配置与凭证不是跨文件事务；保存中断后应重新登录恢复。

HTTPS 为默认要求。本机开发可显式指定 `--allow-loopback-http`，且 HTTP 主机必须是 loopback IP（如 `127.0.0.1`），不接受普通域名。API 地址只能为 origin，不支持子路径部署。禁用 HTTP 自动重定向，避免认证头被转发。

## 查询与脚本

```sh
./bin/aigw models list --output json
./bin/aigw organizations list
./bin/aigw projects list --org ORGANIZATION_ID
./bin/aigw context use --org ORGANIZATION_ID --project PROJECT_ID
./bin/aigw context show
./bin/aigw usage
./bin/aigw keys list --limit 20
./bin/aigw audit --limit 20
./bin/aigw requests list --state uncertain --output json
./bin/aigw requests get --request-id REQUEST_ID --output json
./bin/aigw usage --output json
./bin/aigw auth status
./bin/aigw logout
```

集合查询支持 `--limit 1..100` 和 `--after` 游标。JSON 直接保留 API 的 `data`/`next_cursor`，每次输出一个 JSON 文档；表格将翻页提示写 stderr。`usage` 展示当前预算周期、计价单位、已用和预留整数，含义与 Web 一致。密钥列表仅包含前缀等元数据，不能恢复完整密钥。审计权限由服务端裁决。

自动化从密钥管理系统注入 `AIGW_TOKEN`（短期管理 JWT）及 `AIGW_SERVER`，然后运行例如 `aigw usage --org ORGANIZATION_ID --project PROJECT_ID --output json`。环境令牌优先，不持久化，不提供 token 命令行参数。不要在 shell 历史、脚本源码或日志中直接写令牌值。存在环境令牌时交互 login 会被拒绝；logout 仅清理本地存储，不撤销环境令牌或 IdP 会话。`auth status` 检查本地配置/有效期，不代表服务端已完成认证。

成功数据写 stdout，错误写 stderr；JSON 模式的错误形如 `{"error":{"message":"…","exit_code":3}}`。退出码：0 成功，1 网络/服务/其他错误，2 参数或非交互终端错误，3 缺失身份、授权拒绝或 HTTP 401/403，130 context/信号取消。超时属于 1。

## TUI

```sh
./bin/aigw tui
NO_COLOR=1 ./bin/aigw tui
```

需要 stdin/stdout 均为 TTY，不支持 `--output json`。界面按组织 → 项目 → 项目概览导航；已保存项目可直接打开概览。概览显示周期用量与密钥元数据，按 t 打开调用账本列表，Enter 查看详情。↑/↓ 或 j/k 选择、Enter 确认、Esc 返回、r 刷新、n/p 翻页、q 或 Ctrl+C 退出。退出恢复终端模式和备用屏幕；`NO_COLOR` 关闭颜色，重定向与脚本应使用普通 JSON 命令。

网络请求可取消；切页取消旧请求并忽略迟到响应。刷新失败保留同作用域最后一次成功数据，顶部显示错误/过期提示；切换作用域不会继续展示上一项目数据。界面按窗口裁剪，列表跟随选择滚动。所有展示文本过滤控制字符及方向控制字符，避免服务端资源名称触发终端转义。

## 验证与边界

`bash scripts/verify.sh` 校验生成 Go 类型、静态检查、race 与真实 PostgreSQL 集成；`AIGW_VERIFY_WEB=1` 同时回归浏览器。测试 IdP 使用真实 RSA 签名，覆盖 pending 后授权、拒绝、错误 audience、过期、非法轮询间隔和取消，以及文件权限、跨服务凭证保护、跨组织拒绝、JSON 契约和分页。TUI 验证状态转移、迟到响应、缩放、错误保留，并在 Linux 真正 PTY 中验证退出恢复终端。

这不是企业 IdP 或原生系统钥匙串的上线验收：钥匙串适配器使用内存替身测试，企业接入、Windows/macOS、屏幕阅读器及真实运维终端人工体验仍待验证。本终端阶段未新增写入业务命令、真实模型适配或自动令牌刷新。后续调用账本与索引迁移见独立文档。

阶段验收（2026-11-03）：完整后端校验通过，56 个顶层测试及 39 个子测试，无失败/跳过；Web 冻结安装、类型差异、TS/ESLint/Prettier、生产构建及 3 个 Chromium 场景通过；`cmd/aigw` 二进制构建和 `--help` 冒烟通过。未以 mock 供应商证明真实模型已接通。

模型目录与 CNY_MICRO 计价已作为后续阶段接入，`models list` 不返回供应商凭证或内部驻留证据编号；TUI 项目概览显示授权别名与实际预算单位。区域/真实供应商验收边界见[大陆供应商说明](mainland-providers.md)。

调用账本现支持 `requests list --outcome failed` 与执行详情（模型、供应商/配置区域、固定错误码、可空 token/耗时）。TUI 详情可滚动，历史未记录不显示为零。语义和落库故障限制见[执行元数据](execution-metadata.md)。

项目流量策略查询和预算/执行只读核查见[操作手册](traffic-and-recovery.md)，使用 `limits show`、`recovery inspect`（支持 JSON 和分页）。TUI 暂沿用调用列表与详情排查，独立核查屏幕待后续增加。

供应商共享熔断与当前实例 HTTP 观测见[运行手册](supplier-resilience.md)，使用 `runtime show`；TUI 项目页按 `s`，上下滚动、`r` 刷新、Esc 返回。

标准账单只读查询使用 `billing list --decision pending --output json`，TUI 的 CNY_MICRO 项目页按 `b`；财务审核通过 Web/受权 API。见[对账手册](billing-reconciliation.md)。

## 内容审计增量

新增 aigw content-audit list --org <ID> --project <ID> --output json，仅返回分页元数据。正文不进入终端；数据库侧 cmd/content-audit purge/verify 为独立维护命令。 详见[内容审计手册](content-audit.md)。
