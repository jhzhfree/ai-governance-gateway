# 预算与 SSE 技术原型

本文记录第一轮预算/SSE 原型及其验收边界。后续已补充[管理身份、权限和密钥闭环](management-api.md)，并移除运行时固定项目作用域；下文固定作用域描述只适用于第一轮测试 fixture。Web/TUI、Redis 限流、真实模型和生产部署仍未实现。

## 一次命令执行验证

需要 `.go-version` 指定的 Go、C 编译器（race detector）与本地 Linux Docker。

```bash
bash scripts/verify.sh
```

脚本执行格式检查、依赖校验、go vet、带 race detector 的全部测试。默认启动一次性 PostgreSQL 容器，绑定随机 loopback 端口，测试结束删除容器和数据卷；每个测试另建独立 schema 并清理。固定镜像为 PostgreSQL 17.6，具体 digest 见脚本。

测试容器使用 trust 认证，仅适合无真实数据的本地一次性验证。生产和共享数据库不采用该配置。若已有专用测试 PostgreSQL，可以通过环境变量 `TEST_DATABASE_URL` 提供连接；需允许创建/删除测试 schema，勿指向生产库。脚本不会输出连接字符串。

非标准 Go 路径可通过 `GO_BIN` 指定：

```bash
GO_BIN=/path/to/go bash scripts/verify.sh
```

直接执行 `go test ./...` 且未提供 TEST_DATABASE_URL 时，数据库相关测试会明确跳过，不能作为原型验证通过的依据。正式复验使用上述脚本，CI 也调用同一脚本。

## 已实现的代码边界

| 目录 | 职责 |
| --- | --- |
| internal/budget | 两级预算事务、持久化状态、幂等终结、outbox 和保守恢复 |
| internal/gateway | 固定 mock 模型的 SSE 转发、请求约束、取消与结算 |
| internal/mockprovider | 确定性 OpenAI 风格 SSE fixture，不调用外部模型 |
| internal/testutil | 隔离 PostgreSQL 测试 schema |
| cmd/mock-provider | 仅监听 loopback 的模拟供应商 |

原型网关由集成测试通过 httptest 启动，配置固定测试组织/项目，结束后关闭。没有持久运行的网关 CLI 或对外管理接口。

后续版本增加 `cmd/gateway` 本地服务入口；需要显式 mock-only、OIDC 配置和密钥鉴权，详见[当前启动说明](management-api.md)。不再提供无需鉴权的运行时固定项目模式。

## 单独查看模拟 SSE

```bash
go run ./cmd/mock-provider
```

在另一终端执行：

```bash
curl -N --max-time 10 http://127.0.0.1:8091/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-text","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}'
```

该命令直接调用模拟供应商，不经过预算账本；完整链路通过集成测试验证。mock 定价为输入固定 2 单位、输出每 token 1 单位，每次最多输出 2 token；这是假定的测试协议，不适用于真实提示词计价。

## 验证场景

本轮实际验证（2026-11-03）：完整执行 `scripts/verify.sh`，13 个顶层测试与 10 个子测试通过，0 失败、0 跳过；格式检查、模块校验、go vet 与 Go race 检查通过。测试连接真实 PostgreSQL 17.6，HTTP 链路连接本地模拟供应商，无外部模型请求。CI 工作流已添加，但尚无 GitHub 托管运行结果；本结论来自当前开发环境。

- 多连接并发竞争组织/项目预算，额度不足时两级更新全部回滚。
- 两个兄弟项目共享组织上限，独立项目额度同时生效。
- 并发重复请求只创建一份预留，重复结算不增加成本或重复 outbox 事件。
- request_id 与组织、价格版本、预算周期或金额不一致时拒绝复用。
- 数据库取消和 outbox 故障回滚预算及预留行。
- 新连接池重新读取持久化状态，恢复遗留派发请求并保留占用；这不是实际 SIGKILL 演练。
- 正常 SSE 结算后输出 DONE；客户端读到首个事件后断开会取消上游，并留下 uncertain 状态。
- 断流、缺失 usage、超额 usage、非法/过大事件和上游错误均不输出成功标记，也不回显上游错误正文。
- 结算失败不假装成功；预算不足和不支持字段在派发前拒绝。

## 当前限制

只支持固定 mock-text、stream=true、单条 user 消息和 max_tokens 1–8。不是完整 OpenAI 兼容实现。没有真实 token 计数、模型价格表、身份鉴权、跨组织权限校验、SSRF 出站策略或供应商凭证系统。

恢复扫描尚未分页或加工作租约；outbox 仅验证原子持久化，未实现投递。EstimateFinal 是内部显式操作，没有暴露管理接口，也未实现后续账单修正。真实生产还需处理上游计费超出预留、终结数据库持续不可用、进程被杀和多实例恢复等情况。

架构取舍见 [ADR 0001](adr/0001-budget-and-stream-prototype.md)。
