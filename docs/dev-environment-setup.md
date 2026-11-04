# 开发环境安装记录（Ubuntu 24.04 / 2026-11-04）

本文记录本机从零安装到**完整门禁通过**的全过程，可作为其他开发机/构建机的复现手册。所有命令均实际执行，输出摘录见各步"验证"。

| 项 | 值 |
| --- | --- |
| 主机 | Ubuntu 24.04.1 LTS，x86_64，systemd，48 GiB 可用磁盘 |
| Go | 1.27.3（`.go-version`），`/opt/aigw-toolchains/go-1.27.3` |
| Node / npm | 24.19.0（`.node-version`）/ 11.17.0，`/opt/aigw-toolchains/node-v24.19.0-linux-x64` |
| PostgreSQL | 17.11（PGDG 源），本机 5432 |
| Docker | 29.1.3（仅备用，见 3.6） |
| 其他 | gcc 13.3、GNU Make 4.3、Delve 1.27.2、jq、proxychains4 |
| 验收 | `go build`/`go vet` 通过；`scripts/verify.sh` 全量 PASS；迁移至 0011；`/health/live`、`/health/ready` 均 204 |

---

## 1. 网络前提（重要）

本机**没有直连外网**，唯一出口是内网 SOCKS5 代理 `172.16.2.47:10808`，且**按协议过滤**：只放行 HTTP/HTTPS，不放行裸 SSH（SSH 走 443 入口，另见 `~/.ssh/config`）。

不同工具对代理的支持方式不同，这是本次安装最耗时的地方：

| 工具 | 可用方式 | 不可用方式 |
| --- | --- | --- |
| curl | `--socks5-hostname 172.16.2.47:10808` | — |
| apt | `/etc/apt/apt.conf.d/99aigw-proxy` 中 `socks5h://` | `http://` 代理（该代理不提供 HTTP CONNECT） |
| go | `HTTPS_PROXY=socks5://172.16.2.47:10808`（Go 原生支持 socks5） | — |
| npm | `proxychains4 -q npm ...`（npm 不支持 socks5 配置） | `npm config set proxy`（仅 HTTP） |
| docker pull | 需要 HTTP(S) 代理，未配置，本机不可用 | 用 dockerd 配 `socks5` |

> 代理地址属于内网环境信息，不写入仓库任何文件；`/etc/apt/apt.conf.d/99aigw-proxy`、`/etc/proxychains4.conf` 仅存在于本机。

## 2. 系统基础包

```bash
cat >/etc/apt/apt.conf.d/99aigw-proxy <<'EOF'
Acquire::http::Proxy "socks5h://172.16.2.47:10808";
Acquire::https::Proxy "socks5h://172.16.2.47:10808";
Acquire::Retries "2";
EOF

env -u LD_LIBRARY_PATH apt-get update
env -u LD_LIBRARY_PATH DEBIAN_FRONTEND=noninteractive apt-get install -y \
  build-essential make ca-certificates curl gnupg pkg-config python3 jq \
  xz-utils unzip openssl proxychains4
```

**坑 1：** 当前 shell 的 `LD_LIBRARY_PATH` 指向一个内置 libstdc++，会让 `apt-get` 报 `GLIBCXX_3.4.32 not found`。所有系统命令都要 `env -u LD_LIBRARY_PATH` 运行；长期解法是把它从交互 shell 的 profile 中移除。

验证：`gcc --version` → 13.3.0；`make --version` → GNU Make 4.3。

## 3. 各组件安装

### 3.1 PostgreSQL 17（PGDG 官方源）

Ubuntu 24.04 自带 16，项目验证基线是 17，因此加官方源：

```bash
curl -fsSL --socks5-hostname 172.16.2.47:10808 \
  https://www.postgresql.org/media/keys/ACCC4CF8.asc -o /tmp/pgdg.asc
gpg --dearmor < /tmp/pgdg.asc > /usr/share/keyrings/pgdg.gpg
echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] http://apt.postgresql.org/pub/repos/apt noble-pgdg main" \
  > /etc/apt/sources.list.d/pgdg.list
env -u LD_LIBRARY_PATH apt-get update
env -u LD_LIBRARY_PATH DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql-17 postgresql-client-17
```

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

### 3.2 Go 1.27.3（官方归档 + SHA-256 校验）

```bash
mkdir -p /opt/aigw-toolchains /tmp/aigw-dl && cd /tmp/aigw-dl
curl -fsSL --socks5-hostname 172.16.2.47:10808 -O https://go.dev/dl/go1.27.3.linux-amd64.tar.gz
curl -fsSL --socks5-hostname 172.16.2.47:10808 "https://go.dev/dl/?mode=json&include=all" -o go.json
# 用官方 JSON 中该文件的 sha256 与本地比对
python3 - <<'EOF'
import json, hashlib
d = json.load(open('go.json'))
for r in d:
    if r['version'] == 'go1.27.3':
        for f in r['files']:
            if f['filename'] == 'go1.27.3.linux-amd64.tar.gz':
                h = hashlib.sha256(open(f['filename'], 'rb').read()).hexdigest()
                assert h == f['sha256'], (h, f['sha256'])
                print("sha256 ok", h)
EOF

mkdir -p /opt/aigw-toolchains/go-1.27.3
tar -C /opt/aigw-toolchains/go-1.27.3 --strip-components=1 -xzf go1.27.3.linux-amd64.tar.gz
ln -sf /opt/aigw-toolchains/go-1.27.3/bin/go /usr/local/bin/go
ln -sf /opt/aigw-toolchains/go-1.27.3/bin/gofmt /usr/local/bin/gofmt
```

校验值实测为 `65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40`，与官方 JSON 一致。

### 3.3 Node 24.19.0（官方归档 + SHASUMS256 校验）

```bash
cd /tmp/aigw-dl
curl -fsSL --socks5-hostname 172.16.2.47:10808 -O https://nodejs.org/dist/v24.19.0/node-v24.19.0-linux-x64.tar.xz
curl -fsSL --socks5-hostname 172.16.2.47:10808 -O https://nodejs.org/dist/v24.19.0/SHASUMS256.txt
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

另已把 `go/gofmt/node/npm/npx` 软链到 `/usr/local/bin`，非交互 shell（systemd、CI step）也能找到。

### 3.5 项目依赖

```bash
cd /opt/dev/ai-governance-gateway
HTTPS_PROXY=socks5://172.16.2.47:10808 go mod download          # 152 MiB 到 /root/go/pkg/mod
proxychains4 -q npm --prefix web ci --no-audit --no-fund        # 229 包，约 403 MiB
```

**坑 2：** npm 原生不支持 SOCKS5。`proxychains4` 的 `[ProxyList]` 需配置 `socks5 172.16.2.47 10808` 并启用 `proxy_dns`；若改用 HTTP 代理则直接 `npm config set proxy/https-proxy`。

### 3.6 Docker（可选）

```bash
env -u LD_LIBRARY_PATH apt-get install -y docker.io
systemctl enable --now docker
docker info --format '{{.ServerVersion}} {{.Driver}}'   # 29.1.3 overlayfs
```

用途仅限"一次性测试库"（`scripts/verify.sh` 默认会 `docker run postgres:17.6`）。本机因代理不支持 HTTP CONNECT，**镜像拉取不可用**，因此统一用本地 PostgreSQL 的 `TEST_DATABASE_URL` 跑测试，二者等价。

### 3.7 Delve（可选，用于断点调试）

```bash
GOFLAGS=-mod=mod GOBIN=/usr/local/bin \
HTTPS_PROXY=socks5://172.16.2.47:10808 \
go install github.com/go-delve/delve/cmd/dlv@latest
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
| 运行 | `bin/gateway --mock-only` | 日志 `authenticated gateway listening on 127.0.0.1:8080` |
| 探针 | `GET /health/live`、`/health/ready` | 204 / 204 |
| 打包 | `make package` | amd64 30 MiB、arm64 28 MiB，arm64 二进制静态链接 |

## 5. 三个容易再次踩到的问题

1. **网关必须在 `AIGW_BROWSER_ORIGIN` 设置后才会托管 Web 控制台**；否则 `/` 返回 404、`/api/v1/` 仍可用但无登录入口。
2. **`AIGW_OIDC_ISSUER` 必须是 HTTPS**（启动即做 discovery，无网络会失败）。本机开发用 `https://accounts.google.com` 仅为了让进程起来；真实登录仍需企业 IdP。开发态要访问外网时，网关进程也要带 `HTTPS_PROXY`。
3. **`--mock-only` 只接受 loopback 的 `/v1/chat/completions` 地址**，因此必须同时运行 `bin/mock-provider`（127.0.0.1:8091），否则启动即报配置错误。

## 6. 回滚

```bash
rm -rf /opt/aigw-toolchains /usr/local/bin/{go,gofmt,node,npm,npx,dlv} /etc/profile.d/aigw-toolchain.sh
apt-get remove --purge -y postgresql-17 postgresql-client-17 docker.io proxychains4
rm -f /etc/apt/sources.list.d/pgdg.list /etc/apt/apt.conf.d/99aigw-proxy
```

数据库目录 `/var/lib/postgresql/17/main` 需单独备份后再删除。
