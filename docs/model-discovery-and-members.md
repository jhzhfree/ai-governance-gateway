# 模型发现与 Web 成员管理

本轮无新迁移，数据库仍为 0010。部署新二进制和 Web 构建后即可使用。

## 项目密钥模型目录

将 OpenAI 兼容客户端的 base URL 指向网关 /v1，并使用项目密钥调用 models.list；不要使用管理 JWT。GET /v1/models 返回该密钥、项目白名单和当前可用配置的交集。密钥/项目撤销后后续查询立即按数据库状态生效；真实供应商驻留证据无效的模型不会进入结果。

```json
{"object":"list","data":[{"id":"mock-text","object":"model","created":0,"owned_by":"mock"}]}
```

created=0 表示网关不发布模型创建时间；owned_by 是供应商标识。空交集返回空数组；401 为密钥无效，503 为授权服务不可用；只支持 GET。目录不执行推理、不预留预算、不自动探测供应商；不代表供应商健康或真实大陆驻留已验收。管理目录 /api/v1/models 仍要求管理身份，CLI 的 models list 仍使用该管理目录。

## 成员与项目授权

组织 owner/admin 可进入 Web“成员管理”。组织范围展示成员及角色，只有 owner 可新增/调整；新增需要该企业账号的准确 issuer 与 subject。账号本身需已由 IdP 管理，不能将姓名或邮箱自动当作 subject。

选择“当前项目授权”查看组织继承和项目显式角色。组织 developer 可分配 admin/developer/viewer；无显式角色时无项目管理访问。组织 owner/admin/viewer 的继承权限不被项目角色覆盖。移除显式授权须确认，对新的管理请求生效；已经发出的项目密钥仍须到“访问密钥”单独撤销。

Web 保存时提交 expected_role，遇到 409 先刷新核对。组织 owner 被保护，不提供所有权转移或组织成员删除；账号停用由企业 IdP 管理。项目授权移除不是组织成员移除，也不是项目密钥撤销。

## 管理接口

- GET /api/v1/organizations/{org}/members
- GET /api/v1/organizations/{org}/projects/{project}/members
- 既有 PUT 同路径，增加可选 expected_role；旧客户端未传时保持原赋权行为。
- DELETE 项目 members：JSON 为 issuer、subject、expected_role；只撤销显式角色。

列表支持 limit=1..100 与 after 游标，返回 data/next_cursor；成员包括 role（组织）、project_role（可空）与 effective_role。组织范围的 effective_role 等于组织角色。写操作仍要求 CSRF 或管理 JWT，返回 204；角色冲突 409，权限不足 403，跨租户/不存在资源 404。

实现决策见 [ADR 0011](adr/0011-model-discovery-and-membership-ui.md)。真实企业 IdP、三家账号和大陆处理/存储、生产部署与灾备验收仍保持待完成。

## 阶段验证（2026-11-04）

113 个 Go 顶层测试通过，包含真实 PostgreSQL、vet/race、模型交集/撤销/到期/空目录/无推理副作用、成员权限即时变化、分页、并发角色比较及审计回滚；OpenAPI Go/TS 差异、冻结依赖、TS/ESLint/Prettier 与生产构建通过。混合浏览器 fixture 补齐 GET 转发后，在独立 PostgreSQL/OIDC/Go 网关下验证全部 8 个 Chromium 场景，成员确认控件收尾后再次构建并复验通过。包括移动端、键盘/无障碍及成员赋权/移除刷新流程，无跳过数据库测试。

这些仍是本地签名身份与合成供应商证据，不代表企业 IdP、真实供应商或生产大陆部署验收。
