# 终端基础准入：登记、私钥证明与 Linux 本机代理

本阶段实现真实 PostgreSQL 设备登记、管理员确认、项目强制准入、设备绑定的五分钟调用凭证、每次请求的私钥持有证明，以及会话/设备撤销。Web“终端准入”和 CLI 均使用真实接口，不以客户端自报的设备编号或“安全合格”标志授权。

## 能力边界

这是**登记公钥对应私钥的持有证明**，不是硬件身份或终端安全体检。首期客户端支持 Linux POSIX 文件权限：目录 0700，私钥与本机令牌文件 0600。私钥不上传，服务端只保存公钥及调用令牌摘要；调用令牌只在代理内存中使用。

尚未实现 TPM/不可导出密钥、设备证书/mTLS 签发、SAC/DAS、EDR、安全状态心跳、全盘扫描、自动更新安装包、Windows/macOS 保护存储或浏览器管理端的设备强制准入。能够复制私钥的同用户恶意程序或 root 仍可冒用该身份，不能把文件密钥称为硬件绑定。管理账号应由既有身份系统、网络边界和 MFA 保护；持有管理权限的人可以确认设备或修改项目策略。

本阶段选择 **HTTPS + Ed25519 DPoP 请求证明**，兼容现有反向代理部署，暂不引入企业 CA。设备会话签发是网关专用管理 API，不宣称完整 OAuth 授权服务器兼容。设备流登录仍仅表示用户登录，不能代替本模块的设备确认。

## 升级与服务端设置

按 [Ubuntu 部署流程](deployment/ubuntu-24.04.md) 摘流、排空、备份后，使用新二进制 `migrate up` 至 **0023**，再 `migrate check` 后启动。迁移新增设备/状态事件、项目策略/策略事件、会话、防重放 nonce、入账设备关联共 7 张表，并给成员停用历史增加设备回收数。原有项目默认**不强制设备准入**，旧预算、占额、模型费用及部门快照不变。

配置既有 `AIGW_BROWSER_ORIGIN=https://gateway.example`，作为唯一公开地址，同时供浏览器认证与设备证明校验使用；OIDC 模式按原流程配置 Web 客户端。地址必须与 CLI 的 `--server` 一致，不支持路径前缀。开发测试仅允许回环 HTTP，并按既有本机 HTTP 开关启动。

反向代理必须终止可信 HTTPS，保持 `/api/v1/...` 和 `/v1/...` 路径不被改写，转发 `Authorization`、`DPoP` 请求头，不记录其值。设备证明使用配置地址，**不信任 Host 或客户端转发头来决定公开地址**。DPoP 不保护消息正文或 query，HTTPS 仍是必需的完整性和保密边界。

启用强制准入后不得混跑不识别该策略的旧服务；先排空再切换。若重启丢失公开地址配置而数据库仍有强制策略，`/health/ready` 返回 503，不能当作正常部署继续恢复流量。

网关、准入数据、日志、数据库、备份和运维系统必须部署在中国大陆。此功能不能阻止绕过网关直连供应商，需要企业出口规则和供应商凭证集中管理配合。

## Linux 操作步骤

先按 [CLI 登录文档](terminal-client.md) 登录本地身份或企业 IdP，并取得本项目管理员/开发者权限。普通组织只读成员不能签发设备调用会话。

```bash
# 用当前普通桌面用户运行；不要把密钥放到项目仓库或共享目录。
mkdir -p "$HOME/.config/aigw/devices"
chmod 700 "$HOME/.config/aigw/devices"

aigw device keygen \
  --private-key-file "$HOME/.config/aigw/devices/workstation.pem" \
  --local-token-file "$HOME/.config/aigw/devices/local-token"

aigw device register --org ORGANIZATION_ID --name 'Ubuntu 工作站' \
  --private-key-file "$HOME/.config/aigw/devices/workstation.pem"
```

keygen 输出公钥与文件位置，不打印私钥或本机令牌，不覆盖已有文件。登记只发送公钥，初始为“待确认”；也可将 keygen 输出的 `public_key` 粘贴到 Web。由组织 owner/admin 核对所属人员及公钥后确认。普通用户不能自行确认设备；管理员确认代表人工准入，不代表通过安全体检。

登记返回设备 ID。确认后启动代理：

```bash
aigw device proxy --org ORGANIZATION_ID --project PROJECT_ID --device DEVICE_ID \
  --private-key-file "$HOME/.config/aigw/devices/workstation.pem" \
  --local-token-file "$HOME/.config/aigw/devices/local-token" \
  --listen 127.0.0.1:16680
```

代理复用现有登录凭证申请五分钟设备会话，默认绑定项目当前已授权模型，可用 `--models MODEL_ID` 限定。会话快到期时申请新会话；不保存 refresh token。管理登录失效后需要重新登录并重启代理，不能自动回退项目密钥。

SDK 示例：

```python
from pathlib import Path
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:16680/v1",
    api_key=Path.home().joinpath(".config/aigw/devices/local-token").read_text().strip(),
)
# model 使用已授权模型 ID；其余聊天/流式调用保持原 SDK 方式。
```

本机令牌用于保护回环代理，不能当作远端网关凭证。代理只监听明确的回环 IP，不允许 `0.0.0.0`；校验 Host、本机 Bearer 令牌，拒绝带 Origin 的浏览器请求和任意代理路径，仅支持 GET `/v1/models` 与 POST `/v1/chat/completions`。最多 16 个并发请求、64 KiB 请求正文，等待上游响应头最多 30 秒，流式响应不设整体超时。客户端断开会取消上游；代理不自动重试模型 POST，避免重复费用。

## 开启项目强制准入

在 Web“终端准入”选中项目，先验证代理可用，再填写原因并确认“要求已确认设备与请求证明”。开启后，所有长期项目密钥在模型发现和推理入口都被拒绝，携带伪造 `X-Device-ID` 也不能绕过。已有项目密钥可以保留元数据，但不能在该项目直接调用；关闭策略后按原授权恢复使用，这是管理员显式策略选择。

网关每次验证：设备/会话未撤销、五分钟未过期、当前组织成员启用、本地账号有效、当前项目角色可调用，以及会话模型与项目模型的交集。签发时不能跨项目或借用其他成员设备，管理员也不能替他人换取设备调用令牌。

## 证明、防重放与撤销

调用使用 `Authorization: DPoP <opaque-session-token>` 与独立 `DPoP` compact JWT。只支持 Ed25519/EdDSA，必须有受保护的 `typ=dpop+jwt`、公钥、`jti`、`htm`、`htu`、`iat`；资源请求还绑定 `ath` 令牌摘要。仅复制调用令牌而没有私钥不能使用。签名公钥必须匹配登记公钥，不从客户端任意公钥派生信任。

数据库时间为准：证明最多 60 秒旧、最多 30 秒超前，终端应同步时钟。`jti` 在共享 PostgreSQL 一次性消费，跨实例并发也只接受一次。过期 nonce 在后台每分钟分批清理，清理后原证明已超出有效期；签名、数据库、权限检查异常均拒绝新调用，不静默放行。

设备永久撤销会撤销其全部会话，已撤销公钥不能重新登记或恢复，重新接入须生成新密钥。单个会话也可独立撤销。组织成员停用永久回收该组织设备与会话；本地账号停用永久回收其所有组织设备，恢复账号或重新授予权限不会复活。账号令牌撤销/改密等既有全局撤销操作同时撤销设备会话。

普通项目/模型权限变更按当前权限立即约束仍有效会话；如需永久作废凭据，应显式撤销会话或设备。新请求实时读取共享数据库状态，不使用撤销状态缓存。已经授权的在途调用可能继续运行、结算和交付，不能收回已发送字节；本阶段不强制中断既有流，也不退款或释放不确定占额。

## 追溯与 API

预算占额事务同时保存 `request_id → device_id/session_id`。归属写入失败时占额整体回滚，不能继续派发供应商。Web 可查看设备状态历史、会话、已入账调用并跳转账本；“已入账”不等同于已执行成功或已收到响应。没有设备关联的旧请求不回填。

- GET/POST `/api/v1/organizations/{org}/devices`：管理员看本组织，普通成员只看自己的设备；POST `{name, public_key}`。
- PUT `/api/v1/organizations/{org}/devices/{device}`：`{state: active|revoked, expected_revision, reason}`。
- GET 设备路径 `/events`、`/sessions`、`/requests`：固定 50 项分页；列表不返回调用令牌或摘要。
- POST 设备路径 `/sessions/{session}/revoke`：`{reason}`。
- GET/PUT `/api/v1/organizations/{org}/projects/{project}/device-policy`：PUT `{required, expected_revision, reason}`，只允许组织 owner/admin 修改并原子留痕。
- POST `/api/v1/organizations/{org}/projects/{project}/devices/{device}/sessions`：已登录的设备所属人提交 `{models}` 和无 `ath` 的签发 DPoP 证明，响应一次性令牌及到期时间。

管理 Cookie 写入仍受 Origin/CSRF 约束。测试覆盖真实临时 PostgreSQL、签名测试身份、合成 TLS 供应商、Linux 本机代理和浏览器，不代表真实 TPM、企业准入系统或大陆物理部署验收。
