# 调用执行与 attempt 元数据

本阶段在既有调用账本上增加执行事实，不将费用结算与协议结果混为一谈。Web、CLI 和 TUI 均通过现有项目作用域 `/requests` API 查询；没有新增匿名或全局 attempt_id 查询入口。

## 生命周期与事实边界

每个成功预留的请求最多一个 attempt，attempt_id 与 request_id 独立。当前仍不重试、不自动切换供应商或区域。开始记录在预留之后、派发标记和供应商网络请求之前持久化；未持久化成功则不派发。

| 执行结果 | 含义 |
| --- | --- |
| unfinished / 未结束 | 已记录执行意图，尚无持久化终态；可能是处理中、进程中断或结束记录失败，不证明上游已被调用 |
| completed / 执行完成 | 上游协议/用量已校验，财务已结算且执行终态已持久化；不证明客户端收到响应 |
| failed / 执行失败 | 本次执行在协议、派发、用量、超时、写入或结算阶段失败，错误码为网关固定分类 |
| canceled / 已取消 | 完成前观察到客户端请求 context 取消；不据此认定供应商未计费 |

开始记录失败时保留 reserved 和原有占额；派发阶段之后失败继续遵循原有 uncertain/保守占额规则。结束记录与账本结算不是跨模块原子事务：结算成功后元数据结束写入失败，账本仍可能 settled，而 attempt 是 unfinished；接口不返回正常 JSON 成功结果或 SSE `[DONE]`。不能为修复元数据失败而退款、重复结算或重放供应商请求。

成功标记之前等待终态落库。若提交已成功但数据库应答丢失，记录可能已 completed，而客户端收到错误；后续失败补写不能覆盖该终态。重复提交相同结束结果幂等且保留原时间，冲突终态拒绝覆盖。查询只展示实际持久化事实。

历史请求不回填模型/结果；`attempt: null` 表示未记录，既可能是历史数据，也可能是开始记录失败。鉴权、请求校验或预算拒绝且没有建立预留的请求不在本账本内。崩溃遗留 unfinished 不自动改成失败/成功，不新增恢复重试或退款任务。

## 字段与隐私

嵌套 attempt 包含独立 ID、关联 request_id、公开模型别名、供应商、配置区域、SSE/JSON、执行结果、固定错误码、上游 HTTP 状态、通过当时格式及上界检查的已观测输入/输出 token、开始/结束时间与 duration_ms。上游状态未取得、token 未取得、结束时间未确定均为 null，不显示成零。

`duration_ms` 是数据库墙钟记录的开始到结束时间，包含派发、上游、记账和元数据写入等待，不含鉴权/预留之前的时间，也不代表供应商纯延迟或客户端收到最后字节的时间。遇到墙钟回拨时按非负值收敛；它不是性能基准或首 token 延迟。

失败记录中的 token 不代表最终账单，不用于自动重结算；以独立账本实际/预留字段和人工核对为准。

错误码示例：upstream_rejected、upstream_protocol_error、upstream_stream_incomplete、upstream_usage_invalid、upstream_timeout、settlement_failed、downstream_write_failed、client_canceled。只保存受控标识，不保存原始上游错误文字、提示词、回复、认证头、供应商密钥、Cookie、物理模型 endpoint ID 或内部合同编号。

配置区域沿用供应商 profile，是配置事实，不是独立地理位置证明。三家供应商和应用/数据库/日志/备份的中国大陆处理与存储要求不变；真实账号、合同及大陆部署仍待验收，见[大陆供应商接入](mainland-providers.md)。本阶段验证仅使用本地合成数据。

## 查询与双端操作

现有列表/详情均返回 `attempt`（对象或 null），字段契约生成 Go/TypeScript 类型。列表追加可选 `outcome=unfinished|completed|failed|canceled`，可与原 `state` 账本筛选组合。没有 attempt 的记录只出现在不限制 outcome 的列表里。分页仍按原创建时间和 request_id，不是快照；翻页保持同一组条件。

```sh
./bin/aigw requests list --outcome failed --output json
./bin/aigw requests list --state uncertain --outcome canceled
./bin/aigw requests get --request-id REQUEST_ID
```

Web 的“调用账本”提供独立的执行结果筛选，URL 保留条件；详情展示 model/provider/region、错误码、上游状态、token 与时间。CLI 表格与 JSON 使用同一契约。TUI 项目概览按 t 打开账本，Enter 看详情，↑/↓ 滚动较长元数据，r 刷新，Esc 返回。历史未记录与正在/遗留未结束明确区分。

项目授权仍先于读取。SQL 从已授权组织/项目的预算行关联 attempt，并显式投影公开字段，不直接把数据库整行序列化。跨项目和跨组织 request_id 均保持 404，项目推理密钥不能访问管理查询。

## 迁移及验证

部署前运行 `cmd/migrate up` 应用 `0007_inference_attempts.sql`。新增受管表与 outcome 索引，FK 关联既有预算预留，request_id 唯一保证单次尝试。未改历史 SQL，没有修改旧余额或伪造历史执行结果。正常启动/readiness 继续检查版本与结构，缺失迁移或索引漂移拒绝就绪。

验证入口：`AIGW_VERIFY_WEB=1 bash scripts/verify.sh`。覆盖真实 PostgreSQL 的 v6→v7 升级/回滚、历史记录不回填、元数据与财务状态分离、并发幂等及冲突终态保护；模拟记录器故障确保开始失败不派发、结束失败无 `[DONE]`；本地 TLS 三家供应商的成功/失败/取消、角色及跨租户隔离、CLI/TUI 读取同一 API；浏览器检查结果筛选、模型/供应商/用量/时间详情和 URL 条件。没有真实供应商流量。

尚未实现：拒绝前全量访问日志、多次 attempt/重试、上游请求追踪 ID、首 token 延迟、自动处理 unfinished、遥测导出与保留清理策略。它们需要独立设计与验收，不能从当前账本状态推导。

阶段验收（2026-11-03）：81 个后端顶层测试、65 个子测试、浏览器 fixture 父测试及 5 个 Chromium 场景通过，无失败或跳过。包含 Go/Web 类型差异、vet/race、冻结安装、TS/ESLint/Prettier、生产构建及桌面详情截图核对。所有供应商场景使用合成数据的本地 fixture，未完成真实大陆账号验收。
