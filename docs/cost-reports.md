# 成本报表：Web、CLI 与终端 UI

控制台“成本报表”读取 PostgreSQL 预算账本与执行记录，按 UTC 入账日期、项目 ID、模型、供应商和币种分组。没有执行记录时模型/供应商为未知；不从当前路由或价格推断历史归属。不包含输入输出正文。项目角色可看已授权项目，整个组织汇总仅 owner/admin 可读。

查询范围为起始日期含、结束日期不含，1～90 天；最多 1000 个分组，超过会拒绝整份报表，请缩短日期或选择项目，不会截断后返回不完整总额。数据库读取设 10 秒上限。`observed_at` 表示一致性账本快照时间，晚到结算会改变原入账日期的历史报表。

| 列 | 含义 |
| --- | --- |
| 入账请求 | 已建立预算占额的请求笔数，未占额的鉴权/预算拒绝不包含 |
| 已结算 | `settled` 的 actual 合计，使用历史账本金额 |
| 估算终结 | `estimated_final` 的 actual 合计，独立展示，不冒充真实账单 |
| 开放占额 | `reserved/dispatched/uncertain` 的 reserved 合计，不是已消费金额 |
| 已释放笔数 | `released` 笔数，金额为零 |
| 已知用量覆盖 | 输入、输出 token 同时存在的请求笔数 / 入账请求笔数 |
| 已知 token 合计 | 仅完整 usage 的 token 汇总；覆盖数为零时页面显示“未知” |

金额和 token 汇总用十进制字符串传输，保持大整数精度；金额是最小账本单位，`CNY_MICRO` 的 1,000,000 单位为人民币 1 元。不同币种分组，不直接相加。结果不能替代供应商官方账单，也不推断客户端是否收到响应。当前执行记录不具备可靠的最终用户归属，不能据此冒充部门/个人成本报表；组织层级与账单适配仍有后续工作。

## 使用

Web 选择项目、设置 UTC 日期并查询；管理员可选择“汇总整个组织”。“导出成本 CSV”按已提交的查询条件重新读取账本快照，审计事件 `cost_report.exported` 成功提交后才发送文件，审计失败返回 503，不返回 CSV。导出可能与页面观察时间不同。CSV 包含观察时间、范围和组织 ID，模型等文本做公式转义；大整数列导入电子表格时选择“文本”类型，避免软件转换丢失精度。导出文件及副本必须留在中国大陆、按组织数据策略保管。

CLI 复用现有登录与上下文：

```bash
aigw cost-report --from 2026-11-01 --to 2026-12-01 --output json
aigw cost-report --org ORGANIZATION_ID --project PROJECT_ID --from 2026-11-01 --to 2026-11-08
aigw cost-report --org ORGANIZATION_ID --organization --from 2026-11-01 --to 2026-11-08
```

终端 UI 项目页按 `c` 查看最近 7 个 UTC 日期，按 `r` 刷新、Esc 返回。终端为只读报表，CSV 导出使用 Web，避免引入绕过导出审计的客户端写路径。

API：

- GET `/api/v1/organizations/{org}/cost-report?from=YYYY-MM-DD&to=YYYY-MM-DD`
- GET `/api/v1/organizations/{org}/projects/{project}/cost-report?from=YYYY-MM-DD&to=YYYY-MM-DD`
- 对相应路径的 `/export` 发 POST，日期仍作为 query 参数。Cookie 会话必须提供正确 Origin 和 CSRF。

这些 API 不变更预算、结算状态或供应商配置，也不会调用模型供应商。
