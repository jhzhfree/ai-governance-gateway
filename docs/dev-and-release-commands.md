# 开发调试与构建发布命令手册

> 当前真实产品入口已更新：make run 使用数据库真实供应商；模拟链路仅限显式测试。请先阅读[真实供应商配置](provider-management.md)。下面涉及旧模拟启动的流程仅供测试参考。


配套：`docs/dev-environment-setup.md`（环境安装）、`Makefile`（命令入口）、`docs/deployment/ubuntu-24.04.md`（生产部署细节）。
所有命令默认在仓库根目录执行；`make help` 随时查看目标说明。

| 项 | 约定 |
| --- | --- |
| 开发库 | `postgres://aigw:aigw_dev@127.0.0.1:5432/aigw_dev` |
| 测试库 | `postgres://aigw:aigw_dev@127.0.0.1:5432/aigw_test`（会被建删 schema，勿指向真实库） |
| 构建产物 | `bin/`（本机二进制）、`web/dist/`（前端）、`dist/`（发布包，已 gitignore） |
| 运行模式 | 开发用 `--mock-only` + `bin/mock-provider`；生产用 `--providers` + 真实供应商配置 |

---

## 1. 五分钟跑起来

```bash
make doctor            # 检查 go/node/psql/docker 与开发库连通
make db-init           # 幂等创建角色与 aigw_dev / aigw_test
make migrate-up        # 迁移到最新（当前 0011）
make build             # 前端 dist + 5 个二进制到 bin/

make dev-oidc          # 终端 A：本地开发 IdP https://127.0.0.1:9443（仅回环）
make run-mock          # 终端 B：模拟供应商 127.0.0.1:8091
make run               # 终端 C：网关 127.0.0.1:8080（mock-only）
make smoke             # 终端 D：探针应 204/204，未授权推理应 4xx
```

**为什么要 `make dev-oidc`：** 网关启动即对 `AIGW_OIDC_ISSUER` 做 discovery 并校验 HTTPS，离线机器上没有可达的 IdP 就会报 `OIDC discovery failed`。`cmd/dev-oidc` 是本机回环 IdP（自签 CA、RS256、PKCE、设备码，自动批准），只用于开发，不进入发布包。首次运行会在 `.aigw/dev-oidc/` 生成 `ca.pem`/`server.pem`/`server.key`（0600，已 gitignore）；`make run` 检测到该 CA 会自动注入 `SSL_CERT_FILE`，无需改系统信任库。

`make run` 的等价原生命令（便于改参数）：

```bash
SSL_CERT_FILE="$PWD/.aigw/dev-oidc/ca.pem" \
AIGW_DATABASE_URL='postgres://aigw:aigw_dev@127.0.0.1:5432/aigw_dev?sslmode=disable' \
AIGW_OIDC_ISSUER='https://127.0.0.1:9443' \
AIGW_OIDC_AUDIENCE='aigw-dev' \
AIGW_UPSTREAM_URL='http://127.0.0.1:8091/v1/chat/completions' \
./bin/gateway --mock-only --listen 127.0.0.1:8080 --web-dir web/dist
```

浏览器开发（Vite 热更新，`/api` 与 `/auth` 代理到 8080，且保留 `Host` 头——网关会用 `AIGW_BROWSER_ORIGIN` 校验 Host）：

```bash
DEV_BROWSER_ORIGIN=http://127.0.0.1:5173 make run   # 自动附带 --allow-loopback-browser-http
make web-dev                                        # http://127.0.0.1:5173
```

取一个开发 token（供 curl / CLI 调用管理 API）：

```bash
make dev-token                 # 输出 RS256 Bearer token（aud=aigw-dev, scope=aigw:manage）
curl -H "Authorization: Bearer $(make -s dev-token)" http://127.0.0.1:8080/api/v1/organizations
```

## 2. 命令总表

| 场景 | 命令 | 说明 |
| --- | --- | --- |
| 环境自检 | `make doctor` | 版本、包源（GOPROXY / npm registry）、数据库连通性 |
| 安装依赖 | `make deps` | `go mod download` + `npm ci` |
| 建库 | `make db-init` | 需要 `su postgres` 权限 |
| 开发 IdP | `make dev-oidc` | 本地 OIDC（9443，回环，自动批准，勿用于生产） |
| 开发 token | `make dev-token` | 打印 aud=`aigw-dev` 的 Bearer token |
| 迁移 | `make migrate-up` / `migrate-status` / `migrate-check` | check 用于"二进制与库结构是否一致" |
| 全量构建 | `make build` | 前端 + Go |
| 仅 Go | `make build-go` | 输出 `bin/{gateway,migrate,aigw,content-audit,mock-provider}` |
| 仅前端 | `make build-web` | 输出 `web/dist` |
| 后端测试 | `make test` | race，使用 `TEST_DATABASE_URL` |
| 完整门禁 | `make verify` | gofmt + API 契约 + vet + race（CI 同款） |
| 前端检查 | `make lint` | tsc + eslint + prettier |
| 断点调试 | `make debug` | Delve headless `127.0.0.1:2345` |
| 打包 | `make package` | linux/amd64 + linux/arm64 tar.gz + SHA256SUMS |
| 清理 | `make clean` | 删除 `bin/ dist/ web/dist`，不动数据库 |

## 3. 日常开发

### 3.1 改后端

```bash
make build-go && make run        # 或 go run ./cmd/gateway --mock-only ...
```

日志里每条请求都有 `request_id=... code=...`，失败码与 HTTP 状态映射见 `docs/product-handbook.md` 附录。

### 3.2 打一次真实调用（mock）

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer <项目密钥>" -H 'Content-Type: application/json' \
  -d '{"model":"<项目已授权模型>","messages":[{"role":"user","content":"hi"}],"max_tokens":16}'
```

`--mock-only` 对请求体有额外校验（缺失字段返回 `unsupported_mock_request`），排查时先看 `code` 再怀疑鉴权。

### 3.3 前端

```bash
make web-dev                      # 5173，改动即时生效
npm --prefix web run api:generate # 后端 OpenAPI 变更后重新生成类型
npm --prefix web run api:check    # 契约一致性检查
```

### 3.4 数据库

```bash
psql "postgres://aigw:aigw_dev@127.0.0.1:5432/aigw_dev?sslmode=disable"
\dt                                   # 业务表
select version, name from schema_migrations order by version;
select status, count(*) from budget_reservations group by 1;   -- 预算状态机
```

测试库隔离：`psql "$TEST_DATABASE_URL"`，**不要**在 `aigw_dev` 上跑测试。

### 3.5 审计与运维 CLI

```bash
./bin/aigw --server https://gateway.example auth status            # 也可用 AIGW_SERVER / AIGW_TOKEN
./bin/aigw runtime show
./bin/aigw content-audit list --org <org>                          # 只读元数据
AIGW_CONTENT_AUDIT_KEYS=... ./bin/content-audit verify --org <org> # 校验哈希链（服务端密钥）
```

完整子命令见 `docs/terminal-client.md`、`docs/content-audit.md`。

## 4. 断点调试

```bash
make debug
# 客户端二选一
dlv connect 127.0.0.1:2345
# 或 VS Code launch.json：
# {"type":"go","request":"attach","mode":"remote","host":"127.0.0.1","port":2345,"substitutePath":[{"from":"${workspaceFolder}","to":"/opt/dev/ai-governance-gateway"}]}
```

调试单个测试：

```bash
TEST_DATABASE_URL='postgres://aigw:aigw_dev@127.0.0.1:5432/aigw_test?sslmode=disable' \
  go test -run TestName -count=1 -v ./internal/<pkg>
```

## 5. 测试与门禁

```bash
make test      # 等同 go test -race -count=1 ./api/... ./cmd/... ./internal/...
make verify    # CI 同款：gofmt 检查、API 契约、vet、race 测试
AIGW_VERIFY_WEB=1 make verify   # 追加 npm ci + api:check + vite build + 浏览器 e2e
```

- `scripts/verify.sh` 在未设置 `TEST_DATABASE_URL` 时会 `docker run postgres:17.6`；本机镜像不可拉取，**必须**用本地库。
- e2e 需要 `npx playwright install chromium`（未预装，按需下载）。
- 任何提交前至少跑 `make verify`；前端改动再加 `make lint`。

## 6. 构建

```bash
make build          # 本机架构，带调试信息
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/gw ./cmd/gateway   # 手动交叉编译
```

- 发布包内二进制统一 `CGO_ENABLED=0`（静态链接，可直接放进最小 rootfs 或 ARM 盒子）。
- 版本信息写入包内 `BUILD` 文件（`version/commit/built/arch`）；当前二进制未注入 `-ldflags` 版本变量，若需要 `gateway --version` 能力需另行加变量。

## 7. 打包发布

```bash
make package
# dist/aigw-<version>-linux-amd64.tar.gz
# dist/aigw-<version>-linux-arm64.tar.gz
# dist/SHA256SUMS
```

产物结构：

```text
aigw-<version>-linux-<arch>/
├── bin/{gateway,migrate,aigw,content-audit}
├── web/dist/                 # 控制台静态资源
├── BUILD                     # 版本/提交/构建时间/架构
├── README.md
└── docs/deployment/ubuntu-24.04.md
```

发布前检查清单：

```bash
sha256sum -c dist/SHA256SUMS
tar tzf dist/aigw-*-linux-arm64.tar.gz | head
ssh target 'sha256sum aigw-*.tar.gz'    # 传输后两端一致
```

版本号取自 `git describe --tags --always --dirty`；正式发布建议打 tag：

```bash
git tag -a v0.2.0 -m "v0.2.0" && make package
```

## 8. 部署（单机）

顺序：**装包 → 迁移（独立身份）→ 起服务 → 反代 → 探针**。迁移必须由有 DDL 权限的账号单独执行，应用账号只读结构。

```bash
tar xzf aigw-<ver>-linux-amd64.tar.gz -C /opt && ln -sfn /opt/aigw-<ver>-linux-amd64 /opt/aigw
install -d -m 0750 /etc/aigw && $EDITOR /etc/aigw/aigw.env   # 权限 0640，内容勿入仓库
/opt/aigw/bin/migrate status && /opt/aigw/bin/migrate up && /opt/aigw/bin/migrate check
```

systemd 单元（最小版，环境变量来自 `EnvironmentFile`）：

```ini
[Unit]
Description=AI governance gateway
After=network-online.target postgresql.service
[Service]
User=aigw
EnvironmentFile=/etc/aigw/aigw.env
ExecStart=/opt/aigw/bin/gateway --providers /etc/aigw/providers.json --listen 127.0.0.1:8080 --web-dir /opt/aigw/web/dist
Restart=on-failure
TimeoutStopSec=30
[Install]
WantedBy=multi-user.target
```

- 网关**只能监听回环**，前置 Nginx/Caddy 终结 TLS 并限制 `/api/` 来源。
- 滚动升级前先摘流（关闭优雅期 10s，流式请求可达 120s）；升级后跑 `GET /health/ready`。
- 回滚 = 切回上一个 `/opt/aigw-<old>` 软链并重启；数据库迁移**只前进不回滚**，回滚二进制前先 `migrate check`。

## 9. 常见故障

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `GLIBCXX_3.4.32 not found` | 内置 `LD_LIBRARY_PATH` 覆盖系统库 | 命令前加 `env -u LD_LIBRARY_PATH` |
| `./bin/gateway: No such file or directory`（文件存在） | 同上，动态链接器被劫持 | 同上 |
| 启动报 `OIDC issuer must be a credential-free HTTPS URL` | issuer 用了 http 或带查询串 | 用 HTTPS、无 userinfo/query |
| 启动报 `database schema is not ready` | 未迁移或迁移版本落后 | `make migrate-up` 或部署迁移 Job |
| `/` 返回 404 | 未设置 `AIGW_BROWSER_ORIGIN` | 设置后重启（控制台才挂载） |
| 推理返回 400 `unsupported_mock_request` | mock 模式请求体不符合 mock 契约 | 用完整 messages/max_tokens 字段 |
| 启动报 `OIDC discovery failed` | issuer 不可达（离线机器常见） | 先 `make dev-oidc`；或 `DEV_OIDC_ISSUER=<可达 HTTPS IdP>` |
| `/auth/login` 返回 403 `origin_mismatch` | 请求 Host 与 `AIGW_BROWSER_ORIGIN` 不一致 | 走 5173（Vite 保留 Host），或直接以同端口访问 |
| `verify.sh` 卡在 docker run | 本机未配置镜像加速器，`postgres:17.6` 拉不下来 | 设置 `TEST_DATABASE_URL` 用本地库 |
| `go mod download` 超时 | 直连 `proxy.golang.org` 不可达 | `go env -w GOPROXY=https://goproxy.cn,direct` |
| `npm ci` 超时或 404 | 直连 `registry.npmjs.org` 不可达 | `npm config set registry https://registry.npmmirror.com` |
| `apt-get install` 卡住 | 源指向 `archive.ubuntu.com` | 源换成 `mirrors.aliyun.com`，见环境安装文档第 2 节 |
