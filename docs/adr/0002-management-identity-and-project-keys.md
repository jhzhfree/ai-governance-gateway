# ADR 0002：管理身份与项目推理密钥分离

状态：本地实现及集成验证阶段，真实企业 IdP 待验收。

## 决策

管理 API 用 OIDC discovery/JWKS 验证 RS256 JWT 的签名、issuer、audience、时效与 aigw:manage scope。业务角色从 PostgreSQL 查询，不接受角色请求头或直接信任外部角色。浏览器会话不在本阶段实现，不把临时 JWT API 接口冒充完整登录系统。

用户身份使用 issuer+subject；组织 Owner/Admin/Viewer 与项目 Admin/Developer/Viewer 明确分层。所有作用域查询携带 organization_id/project_id；不可见资源返回 404。业务写入和审计同事务，权限检查锁住成员记录直到事务结束。

推理密钥为独立高熵服务凭证，持久化 SHA-256 摘要，创建时只展示一次，强制有效期。每次调用读取权威数据库检查撤销和模型白名单，再确定预算作用域；不缓存授权，先保证撤销语义。数据库不可用时失败关闭，不自动放行。

## 取舍

每次推理增加一次数据库授权查询；需要后续测量再设计缓存与撤销传播。成员角色变更不会隐式销毁项目服务凭证，必须显式撤销。已通过授权的在途请求不强制取消。

暂不提供密码登录、Cookie 会话、Device Flow、全局管理员、Owner 转移或成员删除。现有身份接口保留 Authenticator 边界，未来可增加浏览器会话适配器，但不能降低令牌验证或混用项目密钥。

当前服务入口只允许 mock-only、loopback 和 HTTPS issuer。生产开放前需完善版本迁移、IdP 运维、TLS 入口、限流、审计及真实供应商契约。
