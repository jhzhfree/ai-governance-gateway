# 项目限流与故障核查

升级先执行 `migrate up`，再 `migrate check`；本阶段数据库版本为 0008。旧程序不接受更高版本数据库，发布回退须遵循迁移运行手册，不能删除迁移记录冒充回滚。

## 配置

Web 选择项目 → 流量与核查。组织所有者/管理员可修改每滚动 60 秒请求上限、并发租约上限；其他项目成员只读。保存遇到 409 时刷新，重新核对版本后填写。配置和审计原子提交，不终止既有请求。具体协议见 [ADR 0008](adr/0008-project-admission-and-inspection.md)。

管理路径（前缀 `/api/v1/organizations/{org}/projects/{project}`）：

- `GET /limits`：策略、版本、有效租约数、最近窗口接纳数及快照时间。
- `PUT /limits`：`requests_per_minute`、`max_concurrent`、`expected_revision` 三字段均必填；Cookie 认证须带 CSRF。
- `GET /inspection?older_than_seconds=300&limit=50&after=...`：只读账户重算与异常候选；阈值 1 秒至 30 天，分页 1–100。

不读取客户端伪造组织/项目头；推理请求由项目密钥确定作用域。三家大陆供应商与 mock 路径均遵循共享接纳控制。429 携带 Retry-After，503 表示接纳控制不可用；两者均不创建预算预留或派发。

## 终端操作

沿用现有 OIDC 登录和项目上下文：

```sh
aigw limits show --org ORG --project PROJECT
aigw recovery inspect --org ORG --project PROJECT --older-than 5m
aigw recovery inspect --org ORG --project PROJECT --older-than 1h --output json
aigw requests get --org ORG --project PROJECT --request-id REQUEST
```

`--after` 继续核查分页，`--limit` 控制每页请求数。JSON 包含账户差异、截断提示、原因与 lease_active，不向客户端分发数据库凭证。命令不会执行财务调整。

## 排障顺序

1. 检查 readiness、数据库可达性和迁移状态。数据库恢复后先刷新核查，不通过关闭限流绕过故障。
2. 查看流量策略和有效租约；正常请求应完成后释放，进程终止后的租约最长 180 秒自然失效。不要把失效视为上游已停止。
3. 用默认 5 分钟阈值核查 `open_hold`（预算仍占额）、`execution_unfinished`（执行未结束）、`metadata_unrecorded`（执行元数据缺失），按 request_id 查执行详情和供应商侧记录。
4. 对比账户存储值/重算值；组织账户覆盖组织所有项目，因此与单项目账户不同是正常情况。`consistent=false` 表示同范围账本差异；金额最小单位和币种必须一起核对。账户列表截断时不宣称全量核查完成。
5. 获取供应商最终用量/账单及幂等性证据后，走单独审核的财务处置流程。本版本不提供自动退款、强制结束或重放工具；保留证据和审计，不能直接修改数据库掩盖差异。

本阶段用本地 PostgreSQL 和合成供应商验证，不构成真实供应商、大陆部署或生产灾备验收。
