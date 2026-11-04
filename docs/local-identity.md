# 正式本地身份与离线管理员（0015）

本阶段提供正式本地账号，不依赖测试 IdP 或外部身份服务：管理员初始化、首次强制改密、账号创建/停用/管理权、密码重置、会话撤销，以及 CLI 的两小时管理令牌。生产默认仍使用 OIDC，只有显式 AIGW_AUTH_MODE=local 才启用本地模式。

本地管理令牌不等于终端设备准入令牌，也不能代替项目推理密钥。JH-SAC/JH-DAS 的设备证明、短令牌绑定与强制下线事件仍需实际接口契约，不能把本地登录测试称为这些系统已对接。

## Ubuntu 首次部署

1. 构建 Web 与正常发布二进制（make build），独立执行 migrate up/check，升级到 0015。数据库账号与权限按 Ubuntu 手册配置，正常服务不需要 DDL。
2. 在启动网关之前，使用运维数据库凭证运行 local-admin。已有账号时 init 拒绝覆盖，无默认管理员或默认密码。
3. 把 AIGW_AUTH_MODE=local 与精确 HTTPS AIGW_BROWSER_ORIGIN 写入 gateway.env，按需绑定供应商平台管理员，再启动。
4. 浏览器使用本地用户名/初始化密码登录，首次必须改密；改密会撤销初始化会话，重新登录后进入控制台。

交互式初始化（在目标 Ubuntu 主机，使用已安装 release）：

```bash
sudo systemd-run --unit=aigw-local-init --wait --pty --collect \
  -p EnvironmentFile=/etc/aigw/migrate.env \
  /opt/aigw/current/bin/local-admin \
  --origin https://gateway.example.com --username admin init
```

密码由 TTY 隐藏读取，不作为参数；至少 12 个字符、最多 256 UTF-8 字节。自动化只能显式用 --password-stdin，从受控秘密输入源传入，不把实际密码写进命令、脚本或 shell 历史。

初始化 stdout 只返回 issuer、subject、username、must_change_password 和 action。若该账号还要配置全局真实供应商，将返回的精确 issuer/subject 分别设置为 AIGW_PROVIDER_ADMIN_ISSUER/AIGW_PROVIDER_ADMIN_SUBJECT；本地账号管理员与供应商管理员是独立权限。

本地模式不要求 OIDC issuer/audience/client ID，也不会在启动时连接 OIDC discovery。仍须有可用的数据库、Web 构建与合法 origin。生产必须 HTTPS；本机 IP 回环 HTTP 需要 local-admin 的 --allow-loopback-http 和网关的 --allow-loopback-browser-http 显式开发开关，不能用于普通局域网域名 HTTP。

issuer 为固定 origin 加 /auth/local，subject 为不可变账号 ID，不是用户名。切换 OIDC/本地模式或修改 origin 不会自动合并原身份的组织授权；会话也不会跨 issuer 复用。首次部署就确定正式域名，并为需要应急访问的本地身份显式授予组织权限。

## 页面使用

“本地账号”对本地登录用户可见。普通账号仅能修改自身密码；本地账号管理员还可创建账号、分配管理权、启用/停用、重置密码和撤销会话，并分页查看账号安全事件。

新建和重置后的账号必须首次改密，改密前不能进入管理 API 或签发 CLI 令牌。创建账号不自动加入任何组织；组织/项目权限仍在成员管理中独立赋予。本地管理员不能停用或取消自己的管理权，避免最后一个可用管理员通过页面自锁。

停用需要确认：同一事务撤销全部浏览器会话、短期令牌及该身份创建的项目密钥，并写账号安全事件和项目密钥撤销审计。失败回滚全部修改。重新启用不恢复已撤销的项目密钥。在途厂商调用可能已发生费用，停用不会抹账或自动退款。

修改密码、重置密码或“撤销会话”会使旧登录/短期令牌失效；密码重置和账号状态更新比较 expected_revision，避免覆盖并发修改。密码变更/撤销与发放会话通过账号行锁及版本检查协调，旧密码验证完成后的迟到登录不能绕过撤销。

Cookie 为 HttpOnly、Secure、SameSite=Lax，路径 /；写操作校验精确 Origin 与 CSRF。账号凭证只通过密码输入框提交，不进入应用查询/变更缓存、localStorage 或 sessionStorage。密码采用独立随机盐的 Argon2id（64 MiB、3 次、并行度 1）保存，无明文找回接口。

登录每用户名每分钟最多 5 次尝试，存在/不存在账号均使用相同外部错误；每实例最多两项密码计算，避免无界占用内存。生产反代还应配置入口速率限制；这不是公网密码认证安全专项的替代验收。

## CLI 本地登录

```bash
./bin/aigw login --local --username admin \
  --server https://gateway.example.com
./bin/aigw organizations list
./bin/aigw tui
./bin/aigw logout
```

默认使用 OS 钥匙串，密码在 TTY 隐藏读取。无头环境可显式 --credential-store=file（沿用目录 0700/文件 0600 校验）及 --password-stdin。首次或重置后必须先在 Web 改密，CLI 不接受 --password 参数，不在 stdout 打印密码或令牌。

令牌为随机不透明值，数据库只保存 SHA-256 摘要，有效期最长两小时，服务端每次检查账号启用与到期状态。客户端绑定 server/issuer/client/audience，禁止重定向发送凭证；登录前确认目标网关确实声明本地模式，登录后实际访问组织 API 验证。

本地模式的 CLI logout 尝试撤销当前服务端令牌并删除本机保存值；服务不可达时明确提示撤销未确认，不能声称远端已登出。OIDC logout 仍不撤销 IdP 会话，AIGW_TOKEN 环境令牌不受本机文件删除影响。管理令牌不授予 /v1 推理权限，应用仍用项目密钥。

## 忘记管理员密码

由受控主机运维人员对已有本地管理员执行 recover，使用同样的隐藏密码输入方式。它不会把普通账号提升为管理员，不覆盖账号 ID/组织权限，恢复后再次强制改密，并撤销原会话/短期令牌，记录离线恢复事件。

```bash
sudo systemd-run --unit=aigw-local-recover --wait --pty --collect \
  -p EnvironmentFile=/etc/aigw/migrate.env \
  /opt/aigw/current/bin/local-admin \
  --origin https://gateway.example.com --username admin recover
```

维护命令不暴露为 HTTP 恢复后门。数据库备份、角色、密码哈希/盐、账号安全事件须按大陆部署/保留策略保护。尚无邮件找回、MFA、企业 SCIM 或 JH-SAC/DAS 原生对接。

## 接口与运行

公共 GET /auth/config 返回 oidc/local；本地模式提供 POST /auth/local/login、/token、/logout、/password，GET/POST /auth/local/users、PUT /auth/local/users/{user}、POST /auth/local/users/{user}/password、/revoke，GET /auth/local/events。原 /auth/session 和 /auth/logout 兼容。账号管理和安全事件只有本地账号管理员可访问；密码和令牌的请求/响应不应进入代理请求体日志。

后台分批清理过期短期令牌与旧限频计数；安全事件当前保留供核查，不声明完整归档/销毁已实现。正常发布包含 gateway、migrate、aigw、content-audit、local-admin 五个二进制，不包含开发 IdP 或模拟供应商。

验收：149 个后端顶层测试与两套浏览器共 12 个场景通过，覆盖无 IdP 登录、首次改密、账号/令牌/项目密钥停用、密码重置 CAS/审计回滚、限频与迟到登录保护、CLI 绑定/撤销及 OIDC 回归。真实企业准入、原生钥匙串、生产安全和恢复验收仍待完成。
