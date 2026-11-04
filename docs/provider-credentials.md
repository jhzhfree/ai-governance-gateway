# 页面录入与更新供应商 API Key

数据库 0013、ADR 0014。在“供应商配置”中，平台配置管理员可以直接录入百炼、方舟、智谱 API Key，提交后以 AES-256-GCM 加密保存到 PostgreSQL。新请求使用页面配置的凭证，不需要为每次更新修改环境文件或重启。页面不读取原值，状态接口仅返回来源、版本和是否可用。

此能力只在数据库供应商管理模式开放：正常启动不传 --providers 或 --mock-only。文件兼容模式仍只读；组织 owner/admin 不自动成为平台配置管理员，授权继续使用 AIGW_PROVIDER_ADMIN_ISSUER/AIGW_PROVIDER_ADMIN_SUBJECT。

## 一次性部署初始化（Ubuntu/systemd）

先独立迁移到 0013，再配置用于供应商凭证的独立加密主密钥。该主密钥不能保存在同一个数据库或通过浏览器设置，也不要复用内容审计主密钥。

以下在目标主机手工执行：生成私有 root 环境文件，不向终端输出秘密；文件已存在时拒绝覆盖。先按 Ubuntu 手册创建 /etc/aigw。

```bash
sudo python3 - <<'PY'
import os, json, base64, secrets
path = '/etc/aigw/provider-encryption.env'
version = 'provider-master-v1'
encoded = base64.b64encode(secrets.token_bytes(32)).decode('ascii')
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as file:
    file.write("AIGW_PROVIDER_ENCRYPTION_KEYS='" + json.dumps({version: encoded}) + "'\n")
    file.write('AIGW_PROVIDER_ENCRYPTION_ACTIVE_KEY=' + version + '\n')
print('Created protected provider encryption environment file; no key printed.')
PY
```

通过 `sudo systemctl edit aigw` 增加下面的 drop-in（原有 gateway.env 仍保留）：

```ini
[Service]
EnvironmentFile=/etc/aigw/provider-encryption.env
```

按原摘流流程完成 daemon-reload/restart。对多实例，全部实例必须具备当前及历史版本主密钥；同一 DB 保存成功不等于每个实例都能解密。恢复流量前逐实例检查 readiness。

非 systemd 环境同样向网关进程注入 AIGW_PROVIDER_ENCRYPTION_KEYS（JSON：版本 ID → 标准 base64 编码随机 32 字节）及 AIGW_PROVIDER_ENCRYPTION_ACTIVE_KEY。环境文件不自动加载。没有正确主密钥时，页面提示尚未启用安全存储，后端拒绝保存，不改为明文落库。

## 页面操作

1. 用已绑定的平台配置管理员登录，打开“供应商配置”。
2. 在“供应商 API Key”中选择相应厂商的密码输入框，填写真实新密钥并保存。不要将项目密钥当作厂商密钥。
3. 输入框在提交时清空；状态刷新后来源为“页面加密保存”，凭证版本递增。状态接口不含原值、尾号或密钥摘要。
4. 再发布真实模型、价格与大陆驻留依据，授权项目模型、签发项目密钥，通过本网关调用实际厂商。

输入内容不进入应用的 TanStack mutation/query 缓存、localStorage 或 sessionStorage；只通过写入 API 提交。请勿为 /api 路由开启请求体日志。TLS 在 Nginx 终止后，回环到网关的本机链路与进程内存仍接触明文，需保持主机访问控制。

保存会验证格式、权限、加密可用性和 expected_revision，并在同一事务写入密文与更改事件。事件只含厂商、版本、操作者及时间，不含密钥或其摘要。旧版本提交返回 409，需要刷新状态后重新输入；错误不回显提交值。保存不会自动发起付费模型调用。

## 生效、轮换与恢复

页面凭证优先于同一厂商的 AIGW_*_API_KEY 环境变量。没有数据库凭证时继续兼容环境注入。数据库已有凭证但缺失主密钥、密文损坏或 AAD 校验失败时关闭放行，不回退旧环境账号。当前提供保存和覆盖更新，不提供读取原值或删除后隐式回退。

AAD 绑定供应商、凭证版本、主密钥版本；随机 nonce 避免重复加密。主密钥 ID 不可在原 ID 下替换字节。轮换时先在全部实例同时保留旧、新版本，再切换 active ID；后续页面保存用新版本加密。旧记录仍按原 key_id 解密；可以重新提交相同厂商密钥来迁移加密版本，不能通过页面找回原值。备份恢复周期内继续保留旧主密钥。

厂商 API Key 更新只影响后续取得的 Route，在途请求保留原凭证，不代表已被厂商撤销的请求还能完成。若切换到不同计费账号，先核对该账号模型、合同价格和大陆驻留，再发布相应价格版本；凭证更新不自动调整预算或对账归属。

恢复必须同时具备数据库备份和相应历史主密钥。主密钥无法找回时，可从厂商秘密系统重新取得/签发 API Key，通过页面覆盖不可解密记录；没有管理员明文查看通道。数据库、WAL、备份和主密钥系统均须在中国大陆处理与存储，逻辑覆盖不立即擦除旧备份中的密文。

接口：PUT /api/v1/providers/{provider}/credential，JSON 为 expected_revision 与 write-only api_key；成功 204。Cookie 请求使用既有 CSRF/Origin 校验。主密钥由部署层维护，尚无原生 KMS 集成或自动批量重加密任务。

## 验证证据

132 个后端顶层测试及 10 个浏览器场景通过：包含真实临时 PostgreSQL、race、迁移回滚、权限/CAS、密文存储与篡改拒绝、事件失败回滚、主密钥轮换/缺失/恢复；浏览器覆盖页面录入、提交后清空、不回显、路由发布及使用该路由推理。真实适配器使用合成 TLS 供应商验证，未使用真实厂商秘密，不代表付费账号或生产部署已验收。
