# 单进程产品入口与受控启用

## 构建与启动

原生入口保持原命令与默认行为，不引入 Bifrost SDK：

```sh
go build -mod=readonly -o /tmp/aigw-native ./cmd/gateway
```

使用仓库固定 Go 工具链构建独立引擎入口：

```sh
cd modules/engine
go build -mod=readonly -o /tmp/aigw-engine ./cmd/gateway-engine
cd ../..
/tmp/aigw-engine --execution-engine=bifrost \
  --engine-max-bindings=64 --engine-max-concurrent=32 \
  --listen=127.0.0.1:8080 --web-dir=web/dist
```

运行前配置既有数据库、身份、管理端、内容审计和凭证加密环境变量，并构建 Web 资源；沿用 [.env.example](../.env.example)、[本地身份初始化](local-identity.md)、[供应商管理](provider-management.md)和[数据库升级](database-migrations.md)。上述命令是操作说明，本轮没有启动常驻服务或操作生产库。

本分支的产品入口 `gateway-engine` 默认并且仅允许 `bifrost`，省略 `--execution-engine` 即启用进程内受管池。显式选择 native/newapi/未知值会在访问数据库前拒绝，避免安装配置误选数据面。历史 `cmd/gateway` 仍默认 native，用于迁移对照，不能将其误作新产品入口；它不包含 Bifrost 工厂。新引擎只支持数据库托管配置，不与 --mock-only 或 --providers 混用。不会在新引擎故障时自动回退原生执行或其他供应商。

## 启用条件

- 有效的部署及供应商驻留审查、密钥、模型映射和经核验的预算费率。
- 当前网关仅开放文本 JSON/SSE；新执行契约要求 max_tokens 至少为 16。工具、多模态、思考输出未通过网关验收。
- 四家固定配置包括 DeepSeek；缓存优惠/分时价格精确计费仍不包含在当前内部单价结算中。
- max-bindings 包含现用、构建与排空实例，应预留轮换容量；默认 64 为最多 32 个模型的一轮完整替换预留空间，不代表经过容量压测。

## 探针、升级与退出

数据库按默认 auto 模式完成升级后才监听，版本/校验和/结构漂移检查保持；可用 --schema-mode=check 分离权限。单进程完成证据阶段追加迁移 0032，实际最新版本以迁移命令为准。

未配置供应商时管理服务可启动，`/health/live` 为 204、`/health/ready` 为 503；完成受保护的配置发布后刷新就绪状态。ready 同时依赖执行器、数据库、审计和设备条件；不能用 live 代替 ready。

SIGTERM/SIGINT 后先拒绝新请求，允许已有请求最多 10 秒完成；超过后取消连接与执行，并最多等待 15 秒处理器清理，再回收资源。超时会返回明确错误，未确认用量仍保留 uncertain。外部服务管理器需留足上述窗口和自身清理余量。

仍只监听回环地址；TLS、反向代理、地域部署及备份要求沿用部署文档。本轮是可构建、固定选择执行引擎的开发集成入口，尚不是已完成生产验收或 V1.0 发布。下一阶段应重点验证长流/双节点故障、安装升级/恢复及真实外部系统。


## 控制台首阶段

沿用 React、TDesign 与现有页面，不引入第二套管理前端。导航按工作台、模型与访问、费用治理、安全与审计、组织与系统分组；桌面菜单独立滚动，移动端可折叠并支持键盘返回主内容。页面提供对应任务说明。概览中的人民币金额转换为元且最多保留六位小数；微元原始账本值可在金额提示中核查，表单/API 的计价单位未改变。模型目录数量不表示供应商连通性或数据驻留已验证。


## 安装包与配置状态

[scripts/package-linux.sh](../scripts/package-linux.sh) 在本分支生成的 `bin/aigw-gateway` 就是上述进程内产品入口。包内格式 2 清单和 `gateway-build.json` 对照实际链接的 Core 版本；旧根入口仅保留开发对照。使用 [Linux 候选包说明](linux-package.md) 构建和验证，不把历史 native 包误作单进程交付。

供应商 API 在此模式返回 `execution_engine=bifrost`；控制台显示“单进程执行”。`ready` 要求配置有效且执行器可接纳，配置修订号仍保留便于定位失效原因。发布和凭证写入权限完全复用原平台管理主体，组织管理员不会因此获得平台权限。

## 完成证据与费用恢复

文本 Chat 的产品执行链在派发前持久绑定价格/路由，在有效 JSON 完成或 SSE 实际结束后先保存原始数字用量，再提交费用与幂等回执。后台每分钟检查超过 3 分钟的记录，每轮最多处理 100 个过期派发和 100 个事件。没有有效完成证据的派发保留 unknown/uncertain 占额，不自动退款或重放；已知费用恢复不代表客户端收到成功响应或审计完整。恢复失败记录固定日志 `embedded completion recovery pending`，应结合费用核查处理，不能仅依据就绪探针认定历史账务全部清理。

细节、开发故障证据和剩余限制见 [ADR 0010](../modules/engine/docs/adr/0010-durable-chat-completion.md)。本轮仍未开放其余公共协议或缓存差异价格。

## 单进程 Responses 文本入口

`/v1/responses` 复用现有身份、项目 Key、设备、预算和内容审计，必须显式 `store:false`，支持无状态有界文本 JSON/实时 SSE。配置仍使用审核的文本 Chat 模型，物理供应商请求及持久计量格式仍为 Chat；请求不得包含服务端状态、工具或媒体。缺失用量/结束证据不能产生成功终止。详情及 SDK/故障验证边界见 [ADR 0011](../modules/engine/docs/adr/0011-embedded-responses.md)。

```json
{"model":"deepseek/approved-model","input":"你好","max_output_tokens":128,"store":false,"stream":true}
```

模型名称仅为配置示例，使用管理目录中已审核并已授权的实际名称；厂商密钥由管理员托管，调用方仍使用企业项目 Key 或受控设备会话。
