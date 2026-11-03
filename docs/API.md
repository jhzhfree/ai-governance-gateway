# API契约 V0.1

所有控制/调用接口使用`Authorization: Bearer <credential>`，不使用Cookie。管理凭证仅有其所属租户权限，服务凭证绑定主体与项目。HTTP仅用于本地验证，生产需TLS。请求JSON最大64KiB，上游响应最大2MiB。正文默认不落盘。

| 方法 | 路径 | 凭证 | 说明 |
|---|---|---|---|
| GET | /healthz | 无 | 进程存活，不代表上游已健康 |
| POST | /api/v1/budgets | admin | `{project,cap}`，整数微元CNY |
| POST | /api/v1/credentials | admin | `{subject,project,ttlSeconds}`，TTL为60—86400秒 |
| POST | /api/v1/credentials/{uuid}/revoke | admin | 撤销服务凭证，重复返回200或已存在记录状态；无权操作其他租户 |
| GET | /api/v1/overview?limit=50&before=... | admin | limit≤100，before仅用于audit.seq游标；requests为最新N条，预算最多100个项目 |
| POST | /api/v1/requests/{uuid}/reconcile | admin | 待对账请求的`{cost,inputTokens,outputTokens}`，实际金额来自人工核对 |
| GET | /internal/admission/check | service | forward_auth契约，200返回可信身份头 |
| POST | /v1/chat/completions | service | 支持文本非流式及网关模型别名 |

POST `/v1/chat/completions`额外要求`Idempotency-Key`为8—100字符字母、数字、下划线或连字符。支持字段：model、messages、max_tokens、stream=false。messages只支持system/user/assistant角色及字符串content。未知字段返回400，不静默丢弃。

示例：

```json
{"model":"approved-chat","messages":[{"role":"user","content":"你好"}],"max_tokens":32,"stream":false}
```

响应保留choices中的文本，返回网关请求ID、usage与governance.accounting。客户端不得依赖供应商私有字段透传。未获得usage时accounting为PENDING_RECONCILIATION，并暂不返回usage。输出规则阻断也可能已经发生上游费用，应照实结算。

## 状态与错误

| HTTP | 典型code | 客户端处理 |
|---|---|---|
| 400 | unsupported_field / idempotency_key_required / invalid_integer | 修正输入 |
| 401 | invalid_credential / bearer_required | 重新获取有效凭证 |
| 402 | budget_exhausted | 申请预算或等待对账，不盲目重试 |
| 403 | admin_required / model_not_allowed / input_rule_blocked | 检查授权或内容规则 |
| 409 | request_already_recorded / idempotency_conflict | 查询原请求，不换键自动重放 |
| 413/415 | request_too_large / json_required | 调整大小/类型 |
| 422 | stream_not_available_in_alpha | 使用stream=false或升级后续版本 |
| 502 | upstream_failed / upstream_unavailable | 原请求可能已收费，检查请求ID与待核对状态 |
| 503 | service_unavailable | 服务/账本暂不可用，按退避处理 |

错误体统一为`{"error":{"code":"..."}}`。已成功预占的调用在响应头携带`x-agw-request-id`。错误不暴露供应商响应、数据库异常或认证头。

当前查询接口为早期聚合视图，非完整企业分页API。生产版本应拆分预算、请求和审计列表，使用稳定二元游标，并提供受控单请求查询。手工对账尚未实现证据附件与双人审批，不作为正式财务结算系统使用。
