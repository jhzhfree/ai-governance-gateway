# Ubuntu 24.04 环境搭建、开发与部署手册

适用代码：内容审计同步 MVP（数据库 0011、ADR 0012），Ubuntu 24.04 LTS amd64/arm64。命令默认 Bash，从仓库根目录执行；`sudo` 表示系统管理操作。本文提供可审阅的手工安装方案，不会自动修改你的机器。

当前产品为开发集成版本。以下 systemd/Nginx 配置是部署模板，尚未在你的 Ubuntu 机器上验收；已有合成服务测试不等于企业 IdP、真实付费供应商、性能与生产容灾验收。配置中的 example 域名、路径、证据编号必须替换为自己的有效值。

## 1. 选定运行方式与资源

| 用途 | 推荐方式 | 外部条件 |
| --- | --- | --- |
| 修改代码、运行回归 | 普通 Linux 用户 + Go/Node + 本地 Docker 临时 PostgreSQL | 不需要真实供应商和企业 IdP；测试自动生成合成服务 |
| 浏览器手工开发 | 本地 PostgreSQL + mock-provider + 网关托管 Web | 仍需可用 HTTPS OIDC，程序没有开发管理员后门 |
| 单机受控部署 | 本机 PostgreSQL 17 + systemd 网关 + Nginx HTTPS | 域名/证书、OIDC、已审阅大陆供应商配置和凭证 |

开发建议从 4 核/8 GiB 内存/40 GiB 空闲 SSD 起步；部署资源需实测，不能据此承诺并发。内容审计优先 NVMe/SSD，不以 eMMC 作为生产正文主存储。先测每请求正文大小、日请求量与保留期，再预留数据库索引、WAL、备份、膨胀空间。

所有真实输入输出、PostgreSQL/复制/WAL/备份、审计密钥与未来外部存证必须在中国大陆处理和存储。三家接口为百炼北京、火山方舟北京和智谱国内入口；域名或配置声明本身不是驻留证明。软件依赖下载与真实业务数据出境是不同问题，不把业务正文交给开发镜像或测试服务。

端口规划：22 SSH（按运维策略）、443 HTTPS；80 仅跳转/证书验证；8080 网关、8091 模拟供应商、5432 PostgreSQL 均限回环。不得直接开放数据库、Vite 或模拟供应商端口。

## 2. 系统准备

```bash
sudo apt update
sudo apt install -y git curl ca-certificates gnupg build-essential pkg-config \
  python3 jq xz-utils unzip openssl nginx
sudo timedatectl set-ntp true
uname -m
lsb_release -ds
timedatectl status
```

`build-essential` 用于 CGO/race；时间错误会影响 JWT、驻留证据到期与内容保留。防火墙启用前先确认现有 SSH 端口/来源规则，避免锁死远程登录。已有服务的服务器不要直接覆盖 Nginx 默认站点。

### 2.1 固定 Go 与 Node

仓库目前 `.go-version` 为 **1.27.3**，`.node-version` 为 **24.19.0**；以拉取后的文件为准。Ubuntu 默认 apt 的 golang/nodejs 可能不满足要求，不能用更低版本凑过检查。

Go：从 Go 官方下载对应架构归档及官方 SHA-256，校验后安装。以下假设已下载到 `/tmp`；amd64 使用 linux-amd64，arm64 使用 linux-arm64。不要填写虚构的校验值；如果该版本在你可访问的发布源不存在，先排查镜像同步/工具链可用性，不修改 go.mod 降级。

```bash
# 示例：下载并核对官方 SHA-256 后，首次安装到独立版本目录。
# 将架构及版本与 .go-version 对齐。
sudo install -d /opt/aigw-toolchains/go-1.27.3
sudo tar -C /opt/aigw-toolchains/go-1.27.3 --strip-components=1 \
  -xzf /tmp/go1.27.3.linux-amd64.tar.gz
export PATH=/opt/aigw-toolchains/go-1.27.3/bin:$PATH
go version
```

Node：使用你已审阅安装的 nvm 或组织内部版本管理器。若尚未安装 nvm，先按其官方发布说明下载、审阅并安装固定版本，不运行不明镜像的 `curl | bash`。

```bash
# 在仓库目录、已加载 nvm 的普通用户 shell 中：
nvm install "$(cat .node-version)"
nvm use "$(cat .node-version)"
node --version
npm --version
```

将 Go PATH 加到个人 `~/.bashrc`，新终端重新加载；systemd 不读取这些交互式配置。只在构建机需要 Go/Node，运行机器只需要发布二进制、静态资源及运行依赖。依赖代理使用已批准镜像，不关闭 TLS 校验、不关闭 Go checksum 验证，不把认证信息写入仓库。

### 2.2 Docker（只为自动测试，可选）

在没有既有 Docker 安装的新开发机，可以使用 Ubuntu 软件包；已有 Docker CE 时保留原安装方式，不混装：

```bash
sudo apt install -y docker.io
sudo systemctl enable --now docker
sudo docker info
```

测试脚本使用普通用户访问本机 `/var/run/docker.sock`。确需加入 docker 组时执行 `sudo usermod -aG docker "$USER"`，退出登录再登录，然后 `docker info`。docker 组相当于主机管理权限，不给正式网关服务账号该权限；不要用 `sudo npm` 或 `sudo` 跑整个测试脚本。脚本不支持以远端 Docker/context 替代本机 socket。

不能使用 Docker 时，配置单独可丢弃的 PostgreSQL 测试库，通过 `TEST_DATABASE_URL` 提供。测试会建/删 schema，**绝不指向开发持久库或部署数据库**。

## 3. 获取代码与自动验收

```bash
mkdir -p ~/src
cd ~/src
git clone https://github.com/jhzhfree/ai-governance-gateway.git
cd ai-governance-gateway
git switch main
git pull --ff-only
cat .go-version .node-version
go mod download
npm --prefix web ci --no-audit --no-fund
(cd web && npx playwright install --with-deps chromium)
bash scripts/verify.sh
AIGW_VERIFY_WEB=1 bash scripts/verify.sh
```

首次浏览器安装可能请求 sudo 安装系统库。若已有匹配 Chromium，可设 `PLAYWRIGHT_CHROMIUM_EXECUTABLE=/绝对路径/chromium`。不使用 `npm install` 更新锁文件来解决环境问题。

verify 默认拉取固定 digest 的 PostgreSQL 17 临时容器、随机回环端口，结束删除；后端执行格式、Go 模块校验、Go 契约生成一致性、vet、race 与真实数据库测试。Web 模式追加冻结安装、TS 契约、类型/lint/格式、生产构建，以及真实网关托管浏览器测试。完整 Web 命令已包含后端；日常可直接执行最后一条。当前内容审计阶段基线为 125 个后端顶层测试、9 个浏览器场景，不以跳过为通过。

资源受限开发机可用 `GOMAXPROCS=2 GOFLAGS=-p=1 AIGW_VERIFY_WEB=1 bash scripts/verify.sh`。测试 IdP、TLS 供应商均为合成 fixture；不能冒充真实模型或真实 OIDC 接入证明。

## 4. 安装持久 PostgreSQL 17

Ubuntu 24.04 自带 PostgreSQL 主版本通常为 16；本项目验证基线为 17，建议使用 PGDG 的 PostgreSQL 17。以下增加官方 PGDG 签名源；内网可替换为组织审阅的等效源：

```bash
sudo install -d /usr/share/postgresql-common/pgdg
curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
  -o /tmp/postgresql-pgdg.asc
sudo install -m 0644 /tmp/postgresql-pgdg.asc \
  /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc
printf '%s\n' 'deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt noble-pgdg main' \
  | sudo tee /etc/apt/sources.list.d/pgdg.list >/dev/null
sudo apt update
sudo apt install -y postgresql-17 postgresql-client-17
sudo systemctl enable --now postgresql
pg_lsclusters
```

已有 16/其他集群时，17 可能分配 5433。先查看 `pg_lsclusters`，本文后续默认全新 17/main 在 5432；不要删除现有集群或误迁移已有库。

编辑 `/etc/postgresql/17/main/postgresql.conf`，确认 `listen_addresses = 'localhost'`；pg_hba.conf 仅允许必要回环连接并用 scram-sha-256。远程数据库须使用完整证书验证 `sslmode=verify-full`、可信 CA 与专用网络；本文单机回环示例才使用 sslmode=disable。

首次建库和角色（进入 psql 后交互设置强密码，不放 shell 历史）：

```bash
sudo -u postgres psql -p 5432
```

```sql
CREATE ROLE aigw_migrator LOGIN;
\password aigw_migrator
CREATE ROLE aigw_app LOGIN;
\password aigw_app
CREATE DATABASE aigw OWNER aigw_migrator;
\connect aigw
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO aigw_migrator;
GRANT CONNECT ON DATABASE aigw TO aigw_app;
GRANT USAGE ON SCHEMA public TO aigw_app;
ALTER DEFAULT PRIVILEGES FOR ROLE aigw_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO aigw_app;
ALTER DEFAULT PRIVILEGES FOR ROLE aigw_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO aigw_app;
\quit
```

正式迁移始终用 aigw_migrator，应用用 aigw_app，不授应用 DDL/超级用户权限。迁移后还需第 7 节已有对象授权。开发环境可同法另建 aigw_dev 与独立开发角色；不要把 aigw_test 与持久数据库混用。

## 5. OIDC 身份系统

当前没有本地用户名/密码、离线管理员或自动安装 IdP。可接已有企业 OIDC，也可自行部署受支持版本的 Keycloak；IdP 的安装、数据备份和域名证书需独立维护。必须先满足以下接入契约，不能把一个未配置 audience/scope 的默认 Keycloak 客户端当作可用：

| 项目 | 要求 |
| --- | --- |
| issuer | 例如 `https://idp.example.com/realms/team`，HTTPS，discovery 中 issuer 完全一致；网关与用户浏览器均可访问 |
| 管理 access token | RS256 签名 JWT；aud 包含 `aigw-management`，scope 包含 `aigw:manage`；不是 opaque token |
| Web 客户端 | 授权码 + PKCE S256，允许 openid/profile/aigw:manage；按客户端类型配置 secret |
| Web 回调 | 正式 `https://gateway.example.com/auth/callback`；本地独立允许 `http://127.0.0.1:8080/auth/callback` |
| CLI 客户端 | 独立公共客户端，支持 RFC 8628 Device Flow，无 client secret；同样配置管理 audience/scope |
| 业务权限 | 登录不自动获得组织权限；可信身份可创建组织成为 owner；成员按 issuer+subject 授权 |

Keycloak 中一般需要创建 `aigw:manage` client scope，并通过 audience mapper 让 access token 的 aud 含管理 API audience；具体字段随版本变化。浏览器 ID token 的 audience 应匹配 Web client ID，不用管理 access token 代替。issuer 不接受 HTTP，即便网关本身在本机 HTTP 开发。企业内部 CA 应安装到系统信任：`sudo install -m 0644 internal-ca.crt /usr/local/share/ca-certificates/`、`sudo update-ca-certificates`；不要使用跳过证书校验选项。

```bash
curl --fail --silent --show-error \
  https://idp.example.com/realms/team/.well-known/openid-configuration \
  | jq '{issuer,authorization_endpoint,token_endpoint,jwks_uri}'
```

不要将 JWT 粘贴到公共解码网站；令牌、Cookie 与 IdP client secret 都是秘密。

## 6. 本地开发

### 6.1 环境文件与构建

```bash
cp .env.example .env
chmod 600 .env
# 用本地编辑器填写 aigw_dev 连接、OIDC、浏览器客户端，不提交 .env。
# AIGW_BROWSER_ORIGIN=http://127.0.0.1:8080
# AIGW_UPSTREAM_URL=http://127.0.0.1:8091/v1/chat/completions
set -a
source ./.env
set +a
go run ./cmd/migrate up
npm --prefix web run build
mkdir -p bin
go build -mod=readonly -o bin/gateway ./cmd/gateway
go build -mod=readonly -o bin/mock-provider ./cmd/mock-provider
```

程序不会自动加载 .env；上面的 source 只适用于可信的 shell 格式开发文件，值含特殊字符时正确引号；DSN 密码特殊字符需 URL 编码。不要 source 不可信文件。systemd EnvironmentFile 的语法另见部署章节。

终端 A 运行 `./bin/mock-provider`；终端 B 在仓库根目录运行：

```bash
./bin/gateway --mock-only --listen 127.0.0.1:8080 \
  --allow-loopback-browser-http --web-dir ./web/dist
```

访问 `http://127.0.0.1:8080` 登录，创建 MOCK 组织、项目与项目密钥，使用 mock-text。不能把 MOCK 组织当作真实 CNY 项目；币种创建后不可更改。模拟模式不具备启用真实内容审计所需的大陆 CNY 目录，审计开发验收用现有自动测试 fixture，或配置经过审阅的真实模式。

健康检查：

```bash
curl -i http://127.0.0.1:8080/health/live
curl -i http://127.0.0.1:8080/health/ready
```

两者正常返回 204。live 只证明进程响应；ready 还检查数据库结构、供应商目录有效性与审计密钥版本。

### 6.2 Web 与 API 开发流程

推荐以构建后网关同源托管作为手工验收入口。修改 Web 后 `npm --prefix web run build`，刷新浏览器；修改 Go 后重新构建并重启。

`npm --prefix web run dev` 的 Vite 服务只用于快速前端编辑，默认 5173，配置代理 /api 和 /auth；不能自动解决 OIDC 精确回调、来源校验及 Cookie 同源问题。不要认为改 origin 为 5173 就与网关 8080 自动兼容。最终登录/CSRF/审计验收使用同源托管和浏览器集成测试。

API 修改先更新 `api/openapi/management.json`，按现有生成脚本更新 Go/TS，运行契约校验；数据库只追加编号迁移，不改 0001–0011。运行 `bash scripts/check-go-api.sh`、`npm --prefix web run api:check` 检查产物一致性；完整门禁用第 3 节命令。

### 6.3 CLI/TUI

```bash
go build -mod=readonly -o bin/aigw ./cmd/aigw
./bin/aigw login --server https://gateway.example.com \
  --issuer https://idp.example.com/realms/team --client-id aigw-terminal
./bin/aigw organizations list
./bin/aigw tui
```

Ubuntu 桌面默认凭证依赖 Secret Service/用户会话总线；无头 SSH 通常没有。需要时显式使用 `--credential-store=file`，配置目录 0700、文件 0600，不静默降级；到期重新登录。loopback HTTP 开发须显式加 `--allow-loopback-http`。更多命令见[终端手册](../terminal-client.md)。

## 7. 构建并安装部署产物

在完成全量验证的源码目录构建，不在正式服务目录运行开发服务器：

```bash
mkdir -p dist/bin
for component in gateway migrate aigw content-audit; do
  go build -mod=readonly -trimpath -o "dist/bin/$component" "./cmd/$component"
done
npm --prefix web ci --no-audit --no-fund
npm --prefix web run build
git rev-parse HEAD > dist/REVISION
sha256sum dist/bin/* > dist/SHA256SUMS
```

原生构建与目标架构一致。交叉编译/ARM、glibc 依赖须另验收；不要假设 amd64 可直接运行 arm64 产物。正式服务不安装/运行 mock-provider。保留提交号、构建日志与依赖锁文件。

首次安装（以下固定目录是示例，后续发布用新目录，禁止覆盖正在运行的 release）：

```bash
sudo useradd --system --home-dir /var/lib/aigw --create-home \
  --shell /usr/sbin/nologin aigw
sudo install -d -m 0755 /opt/aigw/releases/initial/bin /opt/aigw/releases/initial/web
sudo install -d -m 0750 -o root -g aigw /etc/aigw
sudo install -m 0755 dist/bin/* /opt/aigw/releases/initial/bin/
sudo cp -a web/dist/. /opt/aigw/releases/initial/web/
sudo install -m 0644 dist/REVISION /opt/aigw/releases/initial/REVISION
sudo ln -s /opt/aigw/releases/initial /opt/aigw/current
sudo install -m 0640 -o root -g aigw config/providers.mainland.example.json \
  /etc/aigw/providers.json
sudo install -m 0600 /dev/null /etc/aigw/gateway.env
sudo install -m 0600 /dev/null /etc/aigw/migrate.env
```

useradd/ln 仅首次执行；已存在时检查既有状态，不删除用户或强制覆盖真实 release。产物归 root 所有，服务只读。编辑 `/etc/aigw/providers.json`：填写大陆部署及每模型的有效证据引用/到期时间、账号实际模型或方舟端点、当前价格版本、CNY_MICRO 单价和保守 token 上界。模板故意过期且不是现价，不允许只把过期日期改远就声称核验完成。

用 `sudoedit /etc/aigw/gateway.env` 填写，以下仅格式示例（替换所有占位）：

```ini
AIGW_DATABASE_URL='postgres://aigw_app:URL_ENCODED_PASSWORD@127.0.0.1:5432/aigw?sslmode=disable'
AIGW_OIDC_ISSUER=https://idp.example.com/realms/team
AIGW_OIDC_AUDIENCE=aigw-management
AIGW_BROWSER_ORIGIN=https://gateway.example.com
AIGW_OIDC_CLIENT_ID=aigw-web
AIGW_OIDC_CLIENT_SECRET=REPLACE_WITH_CLIENT_SECRET_IF_REQUIRED
AIGW_ALIYUN_API_KEY=REPLACE_WITH_ALIYUN_SECRET
AIGW_VOLCENGINE_API_KEY=REPLACE_WITH_VOLCENGINE_SECRET
AIGW_ZHIPU_API_KEY=REPLACE_WITH_ZHIPU_SECRET
AIGW_CONTENT_AUDIT_KEYS=
AIGW_CONTENT_AUDIT_ACTIVE_KEY=
```

EnvironmentFile 中不写 `export`，不使用 `$变量`/`$(命令)`；systemd 不执行它们。内容审计 JSON 可用外层单引号保留双引号。秘密文件 root:root 0600，systemd 管理器读取后传入进程；环境变量不是硬件密钥保险箱，限制 root/调试权限。不要以 ps 参数、journal 或工单输出秘密。

`/etc/aigw/migrate.env` 仅设置 migrator 的 AIGW_DATABASE_URL。用受控 transient unit 执行，避免 source 生产 EnvironmentFile：

```bash
sudo systemd-run --unit=aigw-migrate-initial --wait --pipe --collect \
  -p EnvironmentFile=/etc/aigw/migrate.env \
  /opt/aigw/current/bin/migrate up
```

迁移后以管理员连接 aigw 库，补充已有表/序列授权（后续新表由前述 default privileges 覆盖）：

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO aigw_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO aigw_app;
```

上面是当前模块共用连接的实用权限，不是每表最小权限隔离。迁移账号不交给服务进程；应用不应拥有表。首次及每次升级后用运行账号核对只读结构检查：

```bash
sudo systemd-run --unit=aigw-schema-check --wait --pipe --collect \
  -p User=aigw -p EnvironmentFile=/etc/aigw/gateway.env \
  /opt/aigw/current/bin/migrate check
```

## 8. systemd 常驻服务

创建 `/etc/systemd/system/aigw.service`：

```ini
[Unit]
Description=AI Governance Gateway
Wants=network-online.target
After=network-online.target postgresql.service

[Service]
Type=simple
User=aigw
Group=aigw
WorkingDirectory=/opt/aigw/current
EnvironmentFile=/etc/aigw/gateway.env
ExecStart=/opt/aigw/current/bin/gateway --listen 127.0.0.1:8080 --providers /etc/aigw/providers.json --web-dir /opt/aigw/current/web
Restart=on-failure
RestartSec=5s
TimeoutStopSec=150s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
StateDirectory=aigw
ReadWritePaths=/var/lib/aigw
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

不在 ExecStart 加 --migrate、--mock-only 或允许 HTTP Cookie 的开发开关。供应商模式与 mock-only 互斥。当前网关内部 shutdown 上限仍是 10 秒，systemd 的 150 秒不能改变代码；发布前必须先摘流并等在途结束（请求可达 120 秒），否则会中断流式响应。

```bash
sudo systemd-analyze verify /etc/systemd/system/aigw.service
sudo systemctl daemon-reload
sudo systemctl enable --now aigw
sudo systemctl status aigw --no-pager
sudo journalctl -u aigw -n 80 --no-pager
curl --fail --silent --show-error -o /dev/null http://127.0.0.1:8080/health/ready
```

服务失败优先检查日志，不循环修改数据库或关闭权限校验。日志不应包含正文和凭证；仍应限制运维日志读取权限。

## 9. Nginx HTTPS 与 SSE

先准备由客户端信任的证书、完整链与私钥。域名 DNS 指向服务器；内网可用企业 CA。以下假设 `/etc/nginx/tls/gateway.fullchain.pem` 与 `gateway.key` 已存在，私钥 root 0600。证书签发/续期按企业或 ACME 流程维护，不提供临时自签绕过生产验证。

创建 `/etc/nginx/sites-available/aigw`，替换域名与证书路径：

```nginx
server {
    listen 80;
    server_name gateway.example.com;
    return 301 https://$host$request_uri;
}
server {
    listen 443 ssl;
    server_name gateway.example.com;
    ssl_certificate /etc/nginx/tls/gateway.fullchain.pem;
    ssl_certificate_key /etc/nginx/tls/gateway.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    client_max_body_size 1m;

    # 默认日志会记录 URL 查询参数，认证回调 code/state 不落 access log。
    location /auth/ {
        access_log off;
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 150s;
    }
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 150s;
        proxy_send_timeout 150s;
        gzip off;
    }
}
```

此模板直接终止 TLS，保留 Host 供浏览器来源校验，禁用响应缓冲保证 SSE 逐段输出，不缓存管理 API/正文。后续增加 CDN/LB 时也要禁用 SSE 缓冲/缓存，并复核可信代理与域名。不要添加记录 Authorization、Cookie、request_body 或敏感查询参数的日志格式。站点必须在 `/`，不能改为 `/console/` 子路径。

```bash
sudo ln -s /etc/nginx/sites-available/aigw /etc/nginx/sites-enabled/aigw
sudo nginx -t
sudo systemctl reload nginx
curl -I https://gateway.example.com/
curl -i https://gateway.example.com/health/ready
```

本模板健康路径经公网可访问，仅返回状态码；若需限制，请在反代/网络层仅允许运维来源。开放 443 前检查站点冲突、证书与防火墙，保持 8080/5432 不对外可达。

## 10. 首次业务配置与内容审计

登录 Web → 创建组织（真实模式选择 CNY_MICRO）→ 创建项目与初始预算 → 配置模型白名单 → 发项目密钥 → 用授权别名请求 /v1/models 与 /v1/chat/completions → 核对同一 request_id 的用量、执行与财务状态。创建组织的身份成为 owner；IdP scope 不等于组织业务角色。现阶段没有在线预算调整与周期轮转，建立组织时先确定预算/周期。

推理请求使用项目密钥 Bearer，管理使用 OIDC/浏览器会话，二者不能混用。测试正文仅用无敏感合成内容。实际付费 JSON/SSE/取消、账单关联与大陆驻留应逐家验收，不因 HTTP 200 宣告三家完成。

内容审计默认关。开启前用大陆秘密系统生成随机 32 字节主密钥（标准 base64），设置 keyring JSON 与 active ID；不要把真实值写入本文。生产不在终端 stdout 输出密钥；可在受控文件中生成并直接导入秘密系统。组织 owner/admin 设置项目保留期/截断上限/告知依据；owner 显式授予项目审计员后才能查看，owner 默认无解密权。

审计默认 30 天、每方向 64 KiB；同步封存成功才发 JSON/DONE，封存失败不等于费用未发生，禁止自动退款/重放。崩溃时内存中回复可能丢失；当前无异步持久 spool。密钥轮换、专用审计入口、完整性与密钥遗失恢复详见[内容审计手册](../content-audit.md)。

### 10.1 定期清理

创建 `/etc/systemd/system/aigw-content-purge.service`：

```ini
[Unit]
Description=Purge one bounded batch of expired encrypted content
After=postgresql.service

[Service]
Type=oneshot
User=aigw
Group=aigw
EnvironmentFile=/etc/aigw/gateway.env
ExecStart=/opt/aigw/current/bin/content-audit purge 1000
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
```

创建 `/etc/systemd/system/aigw-content-purge.timer`：

```ini
[Unit]
Description=Schedule bounded content retention cleanup

[Timer]
OnBootSec=5min
OnUnitActiveSec=5min
Persistent=true
Unit=aigw-content-purge.service

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now aigw-content-purge.timer
sudo systemctl list-timers aigw-content-purge.timer
sudo journalctl -u aigw-content-purge.service -n 30 --no-pager
```

每次只清一批，按流量监控积压；不能把每 5 分钟 1000 条当作无限吞吐。正文到期立即拒读，即使任务没跑。清理保留元数据、访问日志、DEK、哈希链；不立即擦除备份/WAL。当前正文仅默认分区，自动月分区尚未实现。操作人员可另执行 `content-audit verify ORGANIZATION_ID`（同样经受控 EnvironmentFile），将非秘密检查点保存到大陆受控归档；没有自动外部不可变锚定。

## 11. 升级、备份与恢复

每次升级：记录当前提交/目录 → 运行全量测试并构建新 release → 备份 → 摘流等待在途结束 → 用迁移角色执行新版本 migrate up → 以运行角色 check → 切换 current → restart → 回环及 HTTPS 健康检查 → 合成登录/JSON/SSE/审计冒烟 → 恢复流量。迁移可能影响旧版本，因此不得假设跨版本兼容。

切换可先创建 `/opt/aigw/current.next` 指向新 release，再 `sudo mv -Tf /opt/aigw/current.next /opt/aigw/current`；事前检查链接目标和权限。不要通过重置代码覆盖运行目录。没有 down 迁移，禁止手工回退迁移台账；失败通常前滚修复。旧二进制遇到更新 schema 可能拒绝启动，回滚要先证明兼容，否则执行受控数据库恢复，并明确恢复点之后的数据损失。

备份至少覆盖数据库、角色/授权、供应商审阅配置、部署配置、证书与秘密版本；秘密备份独立加密保管。示例以数据库管理员本地 peer 认证执行，不把密码写参数：

```bash
sudo install -d -o postgres -g postgres -m 0700 /var/backups/aigw
sudo -u postgres /usr/lib/postgresql/17/bin/pg_dump -p 5432 -Fc \
  -f /var/backups/aigw/aigw.dump aigw
sudo -u postgres /usr/lib/postgresql/17/bin/pg_dumpall -p 5432 --globals-only \
  -f /var/backups/aigw/roles.sql
```

这是单次示例，文件名会覆盖，不是完整备份策略。正式用时间戳、加密、权限、保留、离机副本、完整性检查与容量告警；角色转储也可能包含秘密。需要更小 RPO 时规划物理备份/WAL 归档，不凭每日 pg_dump 声称连续恢复。

恢复演练在隔离的大陆测试环境，先恢复必要角色再 pg_restore 到新库，注入**相同历史审计主密钥**，运行 schema check/链 verify/正文抽样解密/预算核查，确认后再按变更流程切换。不要直接覆盖在线库；未知密钥丢失时 DB 备份无法解密正文。备份成功日志不等于恢复成功，必须记录实际 RPO/RTO。

## 12. 故障定位

| 现象 | 优先检查 |
| --- | --- |
| Go 自动下载失败/版本不符 | .go-version、PATH、官方发布/批准镜像可用性；不改低版本绕过 |
| Vite/Node 报 engine 或语法错误 | nvm use，核对 .node-version；npm ci，不改锁文件 |
| Docker permission denied | 本机 daemon、socket 权限、docker 组登录刷新；不让服务账号加入组 |
| 数据库认证失败 | 集群端口、SCRAM 密码、DSN URL 编码、pg_hba；不设置生产 trust |
| schema not ready | 独立 migrate status/check、SQL 校验/版本/分区漂移；不手改台账 |
| OIDC 启动失败 | HTTPS discovery、DNS/出口/CA、精确 issuer、系统时钟 |
| 登录后 401/403 | access token aud/scope/RS256、回调/origin、业务成员；不能伪造管理员头 |
| 页面 404/静态文件缺失 | web build、--web-dir、current 目标；必须根路径部署 |
| Nginx 502 | aigw 状态、127.0.0.1:8080、日志与权限 |
| SSE 最后一起出现 | 每一层代理 buffering/cache/gzip、客户端流式读取；不是加大超时即可解决 |
| 目录/ready 503 | 模板证据过期、模型/价格配置无效、DB 漂移、旧审计密钥缺失 |
| 审计员看不到正文 | 独立项目授权、理由、到期、当前大陆配置、访问日志落库、正确历史密钥 |
| 请求失败但预算仍 held/spent | 查 request_id/attempt、uncertain、人工账单证据；禁止直接改账退款 |
| 取消/重启后审计 capturing | 尚未封存，不代表完整；保留请求并核查财务，当前无自动回复恢复 |
| CLI 钥匙串失败 | 用户会话总线/Secret Service，明确选择受权限保护 file 模式 |

排错仅提交固定错误码、版本、无秘密配置摘要和 request_id；不要上传真实正文、JWT、Cookie、数据库 URL 或供应商/主密钥。不要对正常保留中的业务请求开启全量 HTTP debug。

## 13. 本机验收清单

- [ ] 工具版本与锁文件一致，完整自动验收无失败/跳过。
- [ ] PostgreSQL 17 正确集群/端口，运行角色无 DDL，schema check 通过。
- [ ] 网关仅回环，Nginx TLS 与域名正确，数据库/模拟端口未暴露。
- [ ] Web 登录/退出、回调、CSRF、组织/项目跨权限拒绝通过。
- [ ] 三家真实账号逐一验证 JSON/SSE/取消及真实账单，证据覆盖大陆处理与存储。
- [ ] 审计默认关闭；显式授权后可采集；未授权拒读、查看留痕、到期拒读、定时清理有效。
- [ ] 历史密钥轮换/丢失恢复、数据库备份恢复、篡改核查与发布摘流已演练。
- [ ] 容量、监控告警、备份/WAL 保留、RPO/RTO、安全与隐私审批有实际证据。

更多专题：[工程规范](../engineering.md)、[迁移](../database-migrations.md)、[大陆供应商](../mainland-providers.md)、[限流与核查](../traffic-and-recovery.md)、[内容审计](../content-audit.md)、[整体功能覆盖](../functional-coverage.md)。
