# 标准账单与人工对账手册

部署前执行 `migrate up`、`migrate check`，当前数据库版本为 0010。真实账单只可在已核验的中国大陆网关、数据库、日志、备份与操作终端环境中处理；本地合成验证不证明这些条件成立。未配置有效大陆供应商目录时接口返回不可用；MOCK 项目不展示此工作台。

## 导入标准格式

Web：CNY_MICRO 项目 → 流量与核查 → 账单对账工作台。填写供应商、内部账户引用和最终账单证据引用，选择 CSV 或粘贴内容后导入。引用编号不用密钥、提示词、回复或包含敏感参数的 URL。当前格式不是三家供应商的官方导出格式，必须先根据真实样本转换。

```csv
record_id,request_id,period,currency,amount,input_tokens,output_tokens
synthetic-invoice-001,GATEWAY_REQUEST_ID,2026-11,CNY_MICRO,14000,300,200
synthetic-invoice-002,,2026-11,CNY_MICRO,0,0,0
```

以上仅为合成示例。字段不得猜测：token 缺失时先核实，不以 0 代表未知。record_id 必须能在同一供应商账户下唯一识别记录，例如账单号/行编号；若官方编号周期性重复，应纳入周期。request_id 是网关编号，不应将供应商请求号直接冒充该编号。最多 100 条/24 KiB；币种仅 CNY_MICRO；小数、科学记数法、负数、重复行或超限字段会被拒绝。

同一标准化批次再次导入返回重复提示，不增加记录或审计。其他批次包含已存在的供应商记录则整批冲突，不能部分写入。导入完全不变更预算，也不会调用供应商。

## 核对与审核

列表展示建议/最终请求、金额、用量、当前账本和差异，可按 pending/approved/rejected 筛选、游标翻页。打开详情，核对原始最终账单与项目归属。供应商元数据缺失或关联不可靠时保持待核对，不能猜测匹配。

通过要求另一位组织 owner/admin 登录，勾选最终证据确认并填写原因。成员角色当前通过管理 API 设置，Web 尚无成员管理界面。导入人可以驳回错误证据，但不能通过自己的记录。驳回后记录保留，不覆盖原始数据。

超过预留上界、已结算金额不符、重复请求证据、币种/周期/供应商不符、仍有有效租约或未满 5 分钟的开放占额会被阻止。重复候选先驳回错误记录，再审核准确记录；缺少账单不等同零费用。

审核通过后，开放占额按已核验金额结算；最终零费用结算为 actual=0。已有 settled 同金额仅确认对账，不重复记账。对 reserved、released、estimated_final 或需要超过原上界的金额，当前版本不提供直接改余额。执行 unfinished 不会因财务审核被改成 completed。

## API 与终端

前缀：`/api/v1/organizations/{org}/projects/{project}/billing`。

| 接口 | 用途 |
| --- | --- |
| POST /imports | JSON 含 provider/account_ref/evidence_ref/csv，200 返回批次 id/count/duplicate |
| GET /records | 只读列表，支持 decision、after、limit 1–100；返回实时差异及审核信息 |
| POST /records/{record}/review | action=approve/reject、request_id、expected_revision、confirmed_final、reason；200 返回决定与账本视图 |

Cookie 写操作要求 CSRF。403 表示无权限或尝试自批，409 表示重复证据/版本/账本状态不允许，503 表示配置或数据库不可用。刷新核对后再试；精确重试不会重复记账。

```sh
aigw billing list --org ORG --project PROJECT --decision pending
aigw billing list --org ORG --project PROJECT --output json --limit 20
```

TUI 项目页按 `b` 查看，n/p 翻页、r 刷新、Esc 返回。CLI/TUI 只读，不分发数据库凭证；财务审核使用 Web 或受权管理 API。更完整的取舍见 [ADR 0010](adr/0010-evidence-backed-billing-reconciliation.md)。

## 验证证据

2026-11-04：完整脚本 `GOFLAGS=-p=1 AIGW_VERIFY_WEB=1 PLAYWRIGHT_CHROMIUM_EXECUTABLE=/usr/bin/chromium bash scripts/verify.sh` 通过，107 个 Go 顶层测试、浏览器 fixture 父测试与 7 个 Chromium 场景，无失败/跳过。包含 PostgreSQL/race、幂等导入、两人审核、零费用、并发审核、审计故障回滚、越权/活动租约拦截、CLI/TUI、移动端与无障碍检查。另以隔离 PostgreSQL/race 补充验证重复请求证据和已结算差额不被改写。

所有账单与身份样本都是合成数据。真实账单适配、供应商账户/价格、企业 IdP、原生钥匙串及大陆部署验收仍待完成；不将本阶段视为生产发布验收。
