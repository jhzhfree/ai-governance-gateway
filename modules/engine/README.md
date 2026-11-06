# V1 执行引擎验证模块

Bifrost Core v1.14.1，独立 Go 模块。来源为本工作区已验证的 M0 代码，现纳入正式仓库版本管理。尚未替换主网关的供应商执行链路，不提供独立对外服务。

该隔离边界避免在完成协议、计量、审计、生命周期验收前，将 SDK 的依赖升级传播到治理主程序。`adapters/bifrost` 是唯一 SDK 依赖边界；`internal/execution` 定义模块内部执行契约。后续接入主程序时须明确公开接口，不能跨 Go internal 边界引用。

运行 `GO_BIN=/path/to/go bash scripts/verify.sh`，需要 Go 1.27.3。默认测试仅访问本地合成供应商，不需要模型密钥。根目录的验证脚本同时验证本模块；单独运行根模块 `go test ./...` 不会覆盖嵌套模块。

首批契约涵盖取消、截断、单次派发、用量缺失和调用关联。全零用量视为未知，不能作为零费用结算依据。详细证据见 [delivery.md](docs/delivery.md)，V1 总体门禁见 [v1-delivery.md](../../docs/v1-delivery.md)。
