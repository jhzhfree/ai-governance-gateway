# V1 执行引擎验证模块

Bifrost Core v1.14.1，独立 Go 模块。来源为本工作区已验证的 M0 代码，现纳入正式仓库版本管理。原生主入口保持默认执行链；现提供共享治理应用的独立 gateway-engine 入口，可显式启用受管执行器。生产部署与外部系统验收尚未完成。

该隔离边界避免在完成协议、计量、审计、生命周期验收前，将 SDK 的依赖升级传播到治理主程序。`adapters/bifrost` 是唯一 SDK 依赖边界；`execution` 提供不含 SDK 类型的公开执行契约。`gatewaybridge` 通过本地模块引用组合主治理 Handler，提供受治理集成验证；原生入口不含 SDK；独立引擎入口已支持显式启用，参见根 docs/engine-entrypoint.md。

运行 `GO_BIN=/path/to/go bash scripts/verify.sh`，需要 Go 1.27.3 和专用 `TEST_DATABASE_URL`；建议从仓库根运行 `scripts/verify.sh` 自动准备隔离测试库。默认测试仅访问本地合成供应商，不需要模型密钥。根目录的验证脚本同时验证本模块；单独运行根模块 `go test ./...` 不会覆盖嵌套模块。

首批契约涵盖取消、截断、单次派发、用量缺失和调用关联。全零用量视为未知，不能作为零费用结算依据。详细证据见 [delivery.md](docs/delivery.md)，V1 总体门禁见 [v1-delivery.md](../../docs/v1-delivery.md)。

第二阶段增加显式请求白名单和计量一致性：必须提供 `max_tokens`（16 到服务端上限，默认 4096），当前仅支持文本 Chat 和 function 工具请求／历史。未知字段、内建工具和多模态输入拒绝派发。适配器现有 17 个顶层契约测试；范围和迁移限制见 [ADR 0002](docs/adr/0002-bounded-chat-contract.md)。

当前增量增加实例并发上限与取消后关闭，共 20 个顶层契约测试。满载不排队派发，关闭停止接纳并回收在途调用；详见 [ADR 0003](docs/adr/0003-execution-lifecycle.md)。尚未完成真实负载容量和长期稳定性验收。

第五阶段增加受治理执行桥接与原始用量证据检查，见 [ADR 0004](docs/adr/0004-governed-execution-bridge.md)。必须保留 SDK 原始帧捕获缺口的生产门禁，不能将本地合成联调等同三家供应商兼容或生产切换完成。

第六阶段：流式 Chat 由适配层直接接收原始 SSE，非流式继续 Bifrost SDK；两种路径互斥且不回退。当前受支持流子集不再依赖 SDK 原始捕获，用量证据到有效 DONE 才输出。契约、代价与剩余门禁见 [ADR 0005](docs/adr/0005-strict-stream-reception.md)。

第七阶段增加三家大陆供应商固定路径/参数和 SupplierBinding 工厂。凭证快照变化使旧绑定拒绝接纳新快照，但尚无自动轮换和共享池；详见 [ADR 0006](docs/adr/0006-mainland-supplier-bindings.md)。

有界实例池：`gatewaybridge.NewPool` 接受明确实例/并发上限；完整快照通过 `Publish` 原子切换，旧租约排空后回收。调用方仍负责有序发布与退出 Close。该阶段历史记录不包括后续数据库联动与入口接线；见 [ADR 0007](docs/adr/0007-bounded-execution-pool.md)。

受管发布：`NewManaged` 从一致数据库快照刷新路由和实例池，必须作为同一个网关的 Routing 与 Executor 成对安装。同步刷新失败时停止新接纳，已有请求正常排空；事务内模型查询不发布实例。该组件现可由独立入口显式启用，读取边界详见 [ADR 0008](docs/adr/0008-managed-snapshot-publication.md)。

当前入口与生命周期见 [ADR 0009](docs/adr/0009-explicit-engine-entrypoint.md)。单进程分支的独立命令默认且仅允许 `bifrost`，不再接受 native/newapi；历史根命令保留 native 默认值用于迁移对照。
