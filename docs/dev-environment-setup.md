# 开发环境安装记录（Ubuntu 24.04 / 2026-11-04）

本文记录本机从零安装到**完整门禁通过**的全过程，可作为其他开发机/构建机的复现手册。所有命令均实际执行，输出摘录见各步"验证"。

**结论先行：安装全程不需要代理。** 本机可直连国内镜像（阿里云、清华、goproxy.cn、npmmirror），仅国际站点（go.dev、nodejs.org、registry.npmjs.org、apt.postgresql.org）不可达；把所有下载源换成国内镜像后，apt、Go、Node、PostgreSQL、npm、Go modules 均可直连完成。代理仅作为附录中的备用方案。

| 项 | 值 |
| --- | --- |
| 主机 | Ubuntu 24.04.1 LTS，x86_64，systemd，48 GiB 可用磁盘 |
| Go | 1.27.3（`.go-version`），`/opt/aigw-toolchains/go-1.27.3` |
| Node / npm | 24.19.0（`.node-version`）/ 11.17.0，`/opt/aigw-toolchains/node-v24.19.0-linux-x64` |
| PostgreSQL | 17.11（PGDG 源，走阿里云镜像），本机 5432 |
| Docker | 29.1.3（仅备用，见 3.6） |
| 其他 | gcc 13.3、GNU Make 4.3、Delve 1.27.2、jq |
| 下载源 | apt/Go 归档/PGDG：`mirrors.aliyun.com`；Go modules：`goproxy.cn`；npm：`registry.npmmirror.com` |
| 验收 | `go build`/`go vet` 通过；`scripts/verify.sh` 全量 PASS；迁移至 0011；`/health/live`、`/health/ready` 均 204 |

---

## 1. 网络现状（实测）

| 目标 | 直连结果 | 结论 |
| --- | --- | --- |
| `mirrors.aliyun.com`、`mirrors.tuna.tsinghua.edu.cn` | 443 可连 | ✅ 作为 apt / PGDG / Go 归档源 |
| `goproxy.cn`、`registry.npmmirror.com` | 443 可连 | ✅ 作为 Go modules / npm 源 |
| `go.dev`、`nodejs.org` | DNS 可解析，443 超时 | ❌ 不用，改用镜像 |
| `registry.npmjs.org`、`apt.postgresql.org` | 443 超时 | ❌ 不用，改用镜像 |
| `github.com` | 443 超时（SSH 需经 443 代理入口，见附录 A） | 仅影响 `git push`，不影响安装 |

> 判断方法（可复现）：`getent hosts <host>` 看 DNS，`timeout 6 bash -c "</dev/tcp/<host>/443"` 看 TCP。

## 2. 系统基础包（直连阿里云）

Ubuntu 24.04 使用 deb822 源，直接替换 `URIs`：

```bash
cp /etc/apt/sources.list.d/ubuntu.sources /root/ubuntu.sources.bak
sed -i 's|http://archive.ubuntu.com/ubuntu/|https://mirrors.aliyun.com/ubuntu/|g; \
        s|http://security.ubuntu.com/ubuntu/|https://mirrors.aliyun.com/ubuntu/|g' \
  /etc/apt/sources.list.d/ubuntu.sources

env -u LD_LIBRARY_PATH apt-get update
env -u LD_LIBRARY_PATH DEBIAN_FRONTEND=noninteractive apt-get install -y \
  build-essential make ca-certificates curl gnupg pkg-config python3 jq \
  xz-utils unzip openssl
```

**坑 1：** 本机 shell 的 `LD_LIBRARY_PATH` 指向一个内置 libstdc++，会让 `apt-get` 报 `GLIBCXX_3.4.32 not found`。所有系统命令都要 `env -u LD_LIBRARY_PATH`；长期解法是把它从 profile 中移除。

验证：`apt-get update` 全部命中 `mirrors.aliyun.com`；`apt-get install tree` 直连安装成功（`tree v2.1.1`）；`gcc --version` → 13.3.0；`make --version` → 4.3。

## 3. 各组件安装

### 3.1 PostgreSQL 17（PGDG 源 + 阿里云镜像）

Ubuntu 24.04 自带 16，项目验证基线是 17：

```bash
curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc -o /tmp/pgdg.asc
gpg --dearmor < /tmp/pgdg.asc > /usr/share/keyrings/pgdg.gpg
printf 'deb [signed-by=/usr/share/keyrings/pgdg.gpg] https://mirrors.aliyun.com/postgresql/repos/apt noble-pgdg main\n' \
  > /etc/apt/sources.list.d/pgdg.list

env -u LD_LIBRARY_PATH apt-get update
env -u LD_LIBRARY_PATH DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql-17 postgresql-client-17
```

> PGDG 签名密钥需从 `postgresql.org` 获取；若该域名也不可达，可用镜像站的 `postgresql/keys/` 路径，或从已安装的同版本机器导入同一把公钥（指纹应一致）。

验证：

```text
psql (PostgreSQL) 17.11 (Ubuntu 17.11-1.pgdg24.04+2)
17  main  5432  online  postgres  /var/lib/postgresql/17/main
```

创建开发/测试库（`make db-init` 等价）：

```bash
su postgres -c "psql -c \"CREATE ROLE aigw LOGIN PASSWORD 'aigw_dev' CREATEDB;\""
su postgres -c "psql -c \"CREATE DATABASE aigw_dev OWNER aigw;\""
su postgres -c "psql -c \"CREATE DATABASE aigw_test OWNER aigw;\""
```

> 口令 `aigw_dev` 仅限本机开发；测试库会被测试用例建删 schema，**绝不可指向任何真实环境**。

### 3.2 Go 1.27.3（阿里云归档 + SHA-256 校验）

```bash
mkdir -p /opt/aigw-toolchains /tmp/aigw-dl && cd /tmp/aigw-dl
curl -fsSL -O https://mirrors.aliyun.com/golang/go1.27.3.linux-amd64.tar.gz

# 校验值与官方 JSON 一致（本机实测两个来源得到同一哈希）
echo "65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40  go1.27.3.linux-amd64.tar.gz" | sha256sum -c -

mkdir -p /opt/aigw-toolchains/go-1.27.3
tar -C /opt/aigw-toolchains/go-1.27.3 --strip-components=1 -xzf go1.27.3.linux-amd64.tar.gz
ln -sf /opt/aigw-toolchains/go-1.27.3/bin/go /usr/local/bin/go
ln -sf /opt/aigw-toolchains/go-1.27.3/bin/gofmt /usr/local/bin/gofmt
```

校验值取自 `https://go.dev/dl/?mode=json`（安装时记录）；阿里云归档实测 SHA-256 与官方完全相同，说明镜像未改包。

### 3.3 Node 24.19.0（npmmirror 镜像）

```bash
cd /tmp/aigw-dl
curl -fsSL -O https://npmmirror.com/mirrors/node/v24.19.0/node-v24.19.0-linux-x64.tar.xz
curl -fsSL -O https://npmmirror.com/mirrors/node/v24.19.0/SHASUMS256.txt
grep "node-v24.19.0-linux-x64.tar.xz" SHASUMS256.txt | sha256sum -c -

tar -C /opt/aigw-toolchains -xf node-v24.19.0-linux-x64.tar.xz
for b in node npm npx; do ln -sf /opt/aigw-toolchains/node-v24.19.0-linux-x64/bin/$b /usr/local/bin/$b; done
```

验证：`node --version` → `v24.19.0`；`npm --version` → `11.17.0`。

### 3.4 PATH

```bash
cat >/etc/profile.d/aigw-toolchain.sh <<'EOF'
export PATH=/opt/aigw-toolchains/go-1.27.3/bin:/opt/aigw-toolchains/node-v24.19.0-linux-x64/bin:$PATH
EOF
```

`go/gofmt/node/npm/npx` 已软链到 `/usr/local/bin`，非交互 shell 也能找到。

### 3.5 包源固化与项目依赖

```bash
go env -w GOPROXY='https://goproxy.cn,direct' GOSUMDB='sum.golang.google.cn'
npm config set registry https://registry.npmmirror.com --location=global

cd /opt/dev/ai-governance-gateway
go mod download                       # 152 MiB 到 /root/go/pkg/mod
npm --prefix web ci --no-audit --no-fund   # 229 包，约 403 MiB
```

验证（均为直连、无代理）：

```text
npm install react@19.3.1  → added 1 package in 684ms（临时目录实测）
GOMODCACHE=/tmp/gomodcache2 go mod download github.com/charmbracelet/x/ansi@v0.11.9 → ok
```

### 3.6 Docker（可选）

```bash
env -u LD_LIBRARY_PATH apt-get install -y docker.io
systemctl enable --now docker
docker info --format '{{.ServerVersion}} {{.Driver}}'   # 29.1.3 overlayfs
```

用途仅限 `scripts/verify.sh` 默认的"一次性测试库"。本机**未配置镜像加速器**，`docker pull postgres:17.6` 不可用；因此统一用本地 PostgreSQL 的 `TEST_DATABASE_URL`，与容器方案等价。若需容器，先在 `/etc/docker/daemon.json` 配置可用的 registry-mirror。

### 3.7 Delve（可选，用于断点调试）

```bash
GOBIN=/usr/local/bin go install github.com/go-delve/delve/cmd/dlv@latest
dlv version   # 1.27.2
```

## 4. 验收记录（实际执行）

| 步骤 | 命令 | 结果 |
| --- | --- | --- |
| 依赖完整性 | `go mod verify` | 通过 |
| 编译 | `go build -mod=readonly ./...` | 退出码 0 |
| 静态检查 | `go vet ./api/... ./cmd/... ./internal/...` | 退出码 0 |
| 前端 | `npm --prefix web run build` | 3935 模块，dist 生成 |
| 后端门禁 | `TEST_DATABASE_URL=... bash scripts/verify.sh` | 全量 PASS（含 race、CLI/TUI、审计） |
| 迁移 | `bin/migrate up` / `status` | `current_version=11`、`pending=[]`、`ready=true` |
| 运行 | `bin/gateway --mock-only` | `authenticated gateway listening on 127.0.0.1:8080` |
| 探针 | `GET /health/live`、`/health/ready` | 204 / 204 |
| 打包 | `make package` | amd64 30 MiB、arm64 28 MiB，arm64 静态链接 |

## 5. 三个容易再次踩到的问题

1. **网关必须在 `AIGW_BROWSER_ORIGIN` 设置后才会托管 Web 控制台**；否则 `/` 返回 404。
2. **`AIGW_OIDC_ISSUER` 必须是 HTTPS**（启动即做 discovery）。开发时可用任意可达的 HTTPS issuer 让进程起来；真实登录仍需企业 IdP。
3. **`--mock-only` 只接受 loopback 的 `/v1/chat/completions` 地址**，必须同时运行 `bin/mock-provider`。

## 附录 A：只有代理才能上网时

若换到另一台只能经代理出网的机器，可用以下方式（本机不再需要）：

```bash
# apt：/etc/apt/apt.conf.d/99proxy
Acquire::http::Proxy "socks5h://<proxy>:<port>";
Acquire::https::Proxy "socks5h://<proxy>:<port>";

# curl
curl --socks5-hostname <proxy>:<port> ...

# go（原生支持 socks5）
HTTPS_PROXY=socks5://<proxy>:<port> go mod download

# npm（不支持 socks5 配置，需包装）
proxychains4 -q npm ci --no-audit --no-fund
```

代理按协议过滤时，SSH 会被丢弃；git 可改走 443 入口（`ssh.github.com:443`）。安装 Go/Node 与依赖都不需要 git 走 SSH。

## 附录 B：回滚

```bash
rm -rf /opt/aigw-toolchains /usr/local/bin/{go,gofmt,node,npm,npx,dlv} /etc/profile.d/aigw-toolchain.sh
apt-get remove --purge -y postgresql-17 postgresql-client-17 docker.io
rm -f /etc/apt/sources.list.d/pgdg.list
cp /root/ubuntu.sources.bak /etc/apt/sources.list.d/ubuntu.sources
go env -u GOPROXY GOSUMDB
npm config delete registry --location=global
```

数据库目录 `/var/lib/postgresql/17/main` 需单独备份后再删除。
