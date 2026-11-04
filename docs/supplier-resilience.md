# 供应商故障隔离与运行状态

先执行 `migrate up`、`migrate check` 升级至 0009；正常启动仍只检查结构，不隐式建表。该迁移新增 supplier_circuits，不修改预算、执行记录或既有迁移。升级、失败回滚及已有占额保留有 PostgreSQL 测试。

## 操作入口

- Web：项目 → 流量与核查 → 供应商运行状态。共享熔断状态和当前实例 HTTP 观测分别展示，支持手动刷新。
- CLI：`aigw runtime show --org ORG --project PROJECT`，加 `--output json` 输出共享状态、未结占额和实例观测。
- TUI：进入项目后按 `s` 查看运行状态，`r` 刷新、上下键滚动、Esc 返回。无监控器或未观测时明确提示，不补造数据。
- 核查资金：沿用 `aigw recovery inspect` 和 `aigw requests get --request-id REQUEST`，详见[故障核查手册](traffic-and-recovery.md)。

状态含义：

| 状态 | 含义与下一步 |
| --- | --- |
| 尚无记录 unobserved | 当前路由尚无共享熔断行，不代表供应商已验收健康 |
| 正常放行 closed | 熔断允许新请求，仍须满足权限、限流、预算和驻留条件 |
| 暂停派发 open | 等待 Retry-After 提示后再提交新请求，不自动重放旧请求 |
| 单次探测中 half_open | 仅一个实例持有探测资格；成功恢复，失败重新等待 |
| 模拟供应商 not_applicable | mock 路径，不参与真实供应商熔断 |

等待提示是查询时的快照，不预约未来容量。探测进程崩溃后最长等待 180 秒再由新请求替换；旧探测迟到结果不能覆盖新一轮状态。规则、失败分类和作用域详见 [ADR 0009](adr/0009-shared-supplier-circuits.md)。

## 故障处理

先检查数据库 readiness、迁移状态及 `circuit_unavailable` / `circuit_update_failed` 运行日志。供应商熔断不应让网关 readiness 失败，否则可能触发无效重启；数据库或驻留条件异常仍按既有规则拒绝就绪。

HTTP 429/503 占比和 HTTP 5xx 计数可以定位拒绝/故障趋势，但它们有交集，不能相加当作失败总数。单实例计数不能推导集群请求率；观察采集起点、实例 ID、tracked 标记，并结合执行记录排查 SSE 业务失败。跨周期开放占额不代表当前周期余额，不能直接退还该金额。

恢复后核对供应商最终 usage/账单，原 uncertain 占额不会因探测成功被释放。不使用其他供应商或海外入口绕过故障；真实大陆处理与存储证据要求保持有效。

本阶段使用合成协议 fixture 和传输故障注入验证超时、取消、429、5xx、断流、跨连接池探测与迟到结果隔离。没有真实供应商调用，不能据此宣布大陆驻留、真实账号或生产灾备已验收。

## 阶段验证记录（2026-11-04）

`GOFLAGS=-p=1 AIGW_VERIFY_WEB=1 PLAYWRIGHT_CHROMIUM_EXECUTABLE=/usr/bin/chromium bash scripts/verify.sh` 通过：100 个 Go 顶层测试、浏览器 fixture 父测试及 6 个 Chromium 场景，无失败或跳过。包含真实 PostgreSQL、迁移失败回滚、跨连接池竞争、vet/race、OpenAPI Go/TS 生成差异、冻结依赖安装、TS/ESLint/Prettier、生产构建、移动端与无障碍检查。

测试包串行以控制共享环境中数据库初始化资源争用，包内多连接池/多 goroutine 并发测试与竞态检测保持开启。最终纯终端部署注入与 TUI 提示修正后，再次运行相同 `GOFLAGS=-p=1 bash scripts/verify.sh`，100 个 Go 顶层测试全部通过；Web 代码未变更。
