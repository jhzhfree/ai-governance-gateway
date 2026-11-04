SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
BIN := bin
DIST := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BINARIES := gateway migrate aigw content-audit mock-provider

DEV_DATABASE_URL ?= postgres://aigw:aigw_dev@127.0.0.1:5432/aigw_dev?sslmode=disable
TEST_DATABASE_URL ?= postgres://aigw:aigw_dev@127.0.0.1:5432/aigw_test?sslmode=disable
DEV_LISTEN ?= 127.0.0.1:8080
DEV_OIDC_PORT ?= 9443
DEV_OIDC_DIR ?= .aigw/dev-oidc
DEV_OIDC_CA := $(DEV_OIDC_DIR)/ca.pem
DEV_OIDC_ISSUER ?= https://127.0.0.1:$(DEV_OIDC_PORT)
DEV_OIDC_AUDIENCE ?= aigw-dev
DEV_UPSTREAM_URL ?= http://127.0.0.1:8091/v1/chat/completions
DEV_BROWSER_ORIGIN ?=
DEV_OIDC_CLIENT_ID ?= aigw-dev-console

# Managed sandboxes sometimes export a bundled LD_LIBRARY_PATH that shadows
# system libstdc++; drop it when launching long-running binaries or tests.
RUN := env -u LD_LIBRARY_PATH

.PHONY: help doctor deps build build-go build-web run run-mock dev-oidc dev-token \
	web-dev smoke migrate-up migrate-status migrate-check db-init test verify fmt vet \
	lint clean package package-linux-amd64 package-linux-arm64 debug

help: ## 列出可用命令
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

doctor: ## 检查工具链与数据库连通性
	@echo "go:      $$($(GO) version)"
	@echo "node:    $$(node --version 2>/dev/null || echo missing)"
	@echo "npm:     $$(npm --version 2>/dev/null || echo missing)"
	@echo "psql:    $$(psql --version 2>/dev/null || echo missing)"
	@echo "docker:  $$(docker --version 2>/dev/null || echo missing)"
	@echo "goproxy: $$($(GO) env GOPROXY 2>/dev/null)"
	@echo "npmreg:  $$(npm config get registry 2>/dev/null)"
	@echo "version: $(VERSION) ($(COMMIT))"
	@PGPASSWORD=$${PGPASSWORD:-aigw_dev} psql "$(DEV_DATABASE_URL)" -tAc 'select current_database(), version()' || echo "dev database unreachable"

deps: ## 安装 Go 与前端依赖（需要网络）
	$(GO) mod download
	$(NPM) --prefix web ci --no-audit --no-fund

build: build-web build-go ## 构建控制台与全部二进制（本机架构）

build-go: ## 仅构建 Go 二进制到 bin/
	@mkdir -p $(BIN)
	@for c in $(BINARIES); do echo "build $$c"; $(GO) build -mod=readonly -trimpath -o $(BIN)/$$c ./cmd/$$c; done

build-web: ## 仅构建前端到 web/dist
	$(NPM) --prefix web run build

run: ## 前台启动网关（需先 make dev-oidc、make run-mock、make migrate-up）；DEV_BROWSER_ORIGIN 启用控制台
	@ca=""; [ -f "$(DEV_OIDC_CA)" ] && ca="SSL_CERT_FILE=$(CURDIR)/$(DEV_OIDC_CA)"; \
	web=""; flags=""; \
	if [ -n "$(DEV_BROWSER_ORIGIN)" ]; then \
		web="AIGW_BROWSER_ORIGIN=$(DEV_BROWSER_ORIGIN) AIGW_OIDC_CLIENT_ID=$(DEV_OIDC_CLIENT_ID)"; \
		flags="--allow-loopback-browser-http"; \
	fi; \
	env -u LD_LIBRARY_PATH $$ca $$web \
		AIGW_DATABASE_URL='$(DEV_DATABASE_URL)' \
		AIGW_OIDC_ISSUER='$(DEV_OIDC_ISSUER)' \
		AIGW_OIDC_AUDIENCE='$(DEV_OIDC_AUDIENCE)' \
		AIGW_UPSTREAM_URL='$(DEV_UPSTREAM_URL)' \
		$(BIN)/gateway --mock-only --listen $(DEV_LISTEN) --web-dir web/dist $$flags

dev-oidc: ## 前台启动本地开发 IdP（https://127.0.0.1:9443，仅回环、自动批准）
	$(RUN) $(GO) run ./cmd/dev-oidc --listen 127.0.0.1:$(DEV_OIDC_PORT) --cert-dir $(DEV_OIDC_DIR) --audience $(DEV_OIDC_AUDIENCE)

dev-token: ## 打印一个开发用 Bearer token（配合 dev-oidc）
	@$(RUN) $(GO) run ./cmd/dev-oidc --print-token --listen 127.0.0.1:$(DEV_OIDC_PORT) --cert-dir $(DEV_OIDC_DIR) --audience $(DEV_OIDC_AUDIENCE)

run-mock: ## 前台启动模拟供应商（127.0.0.1:8091）
	$(RUN) $(BIN)/mock-provider

web-dev: ## 前台启动 Vite 开发服务器（127.0.0.1:5173）
	$(NPM) --prefix web run dev

smoke: ## 检查探针与推理接口的拒绝行为（mock 模式 4xx）
	@for p in health/live health/ready; do printf '%s -> ' $$p; curl -sS -m 5 -o /dev/null -w '%{http_code}\n' http://$(DEV_LISTEN)/$$p; done
	@printf 'unauthorized chat -> '; curl -sS -m 5 -o /dev/null -w '%{http_code}\n' \
		-X POST http://$(DEV_LISTEN)/v1/chat/completions -H 'Authorization: Bearer invalid' \
		-d '{"model":"mock","messages":[{"role":"user","content":"hi"}],"max_tokens":8}' || true

migrate-up: ## 对开发库执行迁移
	AIGW_DATABASE_URL='$(DEV_DATABASE_URL)' $(BIN)/migrate up

migrate-status: ## 查看开发库迁移状态
	AIGW_DATABASE_URL='$(DEV_DATABASE_URL)' $(BIN)/migrate status

migrate-check: ## 校验开发库结构与当前二进制一致
	AIGW_DATABASE_URL='$(DEV_DATABASE_URL)' $(BIN)/migrate check

db-init: ## 创建本地开发/测试数据库与角色（幂等）
	@su postgres -c "psql -tAc \"select 1 from pg_roles where rolname='aigw'\"" | grep -q 1 || \
		su postgres -c "psql -c \"CREATE ROLE aigw LOGIN PASSWORD 'aigw_dev' CREATEDB;\""
	@su postgres -c "psql -tAc \"select 1 from pg_database where datname='aigw_dev'\"" | grep -q 1 || \
		su postgres -c "psql -c \"CREATE DATABASE aigw_dev OWNER aigw;\""
	@su postgres -c "psql -tAc \"select 1 from pg_database where datname='aigw_test'\"" | grep -q 1 || \
		su postgres -c "psql -c \"CREATE DATABASE aigw_test OWNER aigw;\""

test: ## 运行后端测试（使用 TEST_DATABASE_URL，不建删持久库）
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' $(RUN) $(GO) test -mod=readonly -race -count=1 -timeout=120s ./api/... ./cmd/... ./internal/...

verify: ## 运行仓库完整门禁（gofmt/api 契约/vet/race 测试）
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' bash scripts/verify.sh

fmt: ## 格式化 Go 代码
	$(GO) fmt ./api/... ./cmd/... ./internal/...

vet: ## 静态检查
	$(GO) vet -mod=readonly ./api/... ./cmd/... ./internal/...

lint: ## 前端类型检查、ESLint 与 Prettier
	$(NPM) --prefix web run check

debug: ## 用 Delve 调试网关（需 dlv，参数同 make run）
	@ca=""; [ -f "$(DEV_OIDC_CA)" ] && ca="SSL_CERT_FILE=$(CURDIR)/$(DEV_OIDC_CA)"; \
	env -u LD_LIBRARY_PATH $$ca \
		AIGW_DATABASE_URL='$(DEV_DATABASE_URL)' \
		AIGW_OIDC_ISSUER='$(DEV_OIDC_ISSUER)' \
		AIGW_OIDC_AUDIENCE='$(DEV_OIDC_AUDIENCE)' \
		AIGW_UPSTREAM_URL='$(DEV_UPSTREAM_URL)' \
		dlv debug ./cmd/gateway --headless --listen 127.0.0.1:2345 --api-version=2 \
		-- --mock-only --listen $(DEV_LISTEN) --web-dir web/dist

package: ## 产出 linux/amd64 与 linux/arm64 发布包
	$(MAKE) package-linux-amd64
	$(MAKE) package-linux-arm64
	@cd $(DIST) && sha256sum *.tar.gz > SHA256SUMS && cat SHA256SUMS

package-linux-amd64: build-web ## 打包 linux/amd64
	@$(MAKE) _package GOARCH=amd64

package-linux-arm64: build-web ## 打包 linux/arm64
	@$(MAKE) _package GOARCH=arm64

_package:
	@test -n "$(GOARCH)" || { echo "GOARCH required"; exit 1; }
	@name=aigw-$(VERSION)-linux-$(GOARCH); \
	rm -rf $(DIST)/$$name; mkdir -p $(DIST)/$$name/bin $(DIST)/$$name/web; \
	for c in gateway migrate aigw content-audit; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) $(GO) build -mod=readonly -trimpath -o $(DIST)/$$name/bin/$$c ./cmd/$$c; \
	done; \
	cp -r web/dist $(DIST)/$$name/web/dist; \
	cp README.md $(DIST)/$$name/; \
	mkdir -p $(DIST)/$$name/docs/deployment; cp docs/deployment/ubuntu-24.04.md $(DIST)/$$name/docs/deployment/; \
	printf 'version=%s\ncommit=%s\nbuilt=%s\narch=linux-%s\n' '$(VERSION)' '$(COMMIT)' '$(BUILD_DATE)' '$(GOARCH)' > $(DIST)/$$name/BUILD; \
	tar -C $(DIST) -czf $(DIST)/$$name.tar.gz $$name; \
	rm -rf $(DIST)/$$name; \
	echo "packed $(DIST)/$$name.tar.gz"

clean: ## 清理构建产物（不触碰数据库）
	rm -rf $(BIN) $(DIST) web/dist
