# new-api 双进程 V1 候选部署与验收

## 交付范围

2026-11-07 确认的 V1 范围是现有四类受控 API：Chat Completions（含审核客户端函数／内联图片）、文本 Embeddings、无状态文本 Responses、纯文本 Messages。后两者支持的结构化文本、实时流及明确排除项见 [交付跟踪](v1-delivery.md#当前确认的-v1-公共协议范围) 和包内 `api/openapi/management.json`。不表示完整 OpenAI／Anthropic／Claude Code 兼容。

本 Linux 包只包含企业控制面、管理 Web、CLI、迁移和主机代理工具。默认执行模式仍为 native；显式 newapi 模式需要另外部署经过审核的真实 new-api 服务。包不含 new-api 可执行文件、Web、数据库、供应商账号或测试夹具，也不会安装服务或执行迁移。Bifrost 独立入口不在这个包内。

`manifest.json` 保留格式 1 的 `engine:native` 默认值，新增 `execution_engines:["native","newapi"]` 与 `newapi` 声明。数据面源仓库、已验证的完整提交号及 `bundled:false` 同时保存在 `deploy/newapi-compatibility.json`；两份值必须一致。当前固定 fb0c2251b5489568d1a3e766ce303c7a94e2816c，不把分支名或“最新版”当成已验证版本。验证器兼容旧版 native 包，但旧包不因此获得新协议能力。

## 部署前置条件

| 对象 | 要求 |
| --- | --- |
| 控制面 | 独立非 root 账号；专用 PostgreSQL；正式 HTTPS origin、身份模式和加密密钥；可信构建来源及整包摘要 |
| 数据面 | 真实 new-api 服务及独立持久数据库；上述已验证提交；保留原项目署名／许可证并完成实际分发授权审查 |
| 内部网络 | 只允许控制面访问治理入口，使用实际端到端 TLS；不能用转发头冒充 TLS；客户端不得直接取得内部服务令牌或绕过控制面 |
| 服务身份 | new-api 中专用服务用户及原生 sk- Token；服务用户不与管理员个人或其他控制面集群混用；监控其停用、有效期和原生额度 |
| 渠道目录 | 运维审核实际端点、凭证、模型映射、覆盖参数、驻留证据、价格／计数边界；将审核后的渠道指纹写入只读目录 |
| 外部验收 | 目标 Linux／NetworkManager、32 GiB 主机和 CPU 规格、IdP／终端、供应商真实计量与账单、代理流式传输及跨主机故障 |

不要使用 `scripts/testdata/governance_server` 作为产品数据面，它只有合成供应商与测试身份。测试二进制通过不等于真实产品安装完成。数据面的正常构建、管理初始化、备份和授权材料须按其实际项目交付流程提供；本包不会替代这些步骤。

## 配置与启用顺序

1. 保存两端数据库及密钥备份，在隔离环境验证可恢复。分别记录控制面包源 commit／SHA-256 和数据面实际 commit，不只记录分支。
2. 先准备数据面。设置 `GOVERNANCE_SERVICE_USER_ID` 为专用服务用户 ID；配置成对的 `SERVER_TLS_CERT_FILE`／`SERVER_TLS_KEY_FILE`。未配置服务用户时治理路由不注册；证书配置错误应阻止启动。原生厂商凭证只放在数据面受控存储。
3. 通过独立管理通道核对渠道配置。服务身份访问 `GET /internal/governance/v1/channels/{id}/revision` 获取指纹；它只提供指纹与状态，不替代审批。审核完成后更新模型目录的 `newapi_channel_id`／`newapi_channel_revision`、价格、输入／输出上限和驻留证据。禁止自动把最新指纹当成审批。
4. 控制面通过服务管理器／秘密系统注入 `AIGW_NEWAPI_ORIGIN` 和 `AIGW_NEWAPI_SERVICE_TOKEN`，同时注入数据库、身份和审计密钥。网关不会自动加载 `.env`。目录不含供应商密钥。
5. 显式启动控制面，以下路径由部署方替换为实际受控路径：

```sh
/opt/aigw/current/bin/aigw-gateway \
  --execution-engine=newapi \
  --providers=/etc/aigw/reviewed-models.json \
  --web-dir=/opt/aigw/current/web \
  --listen=127.0.0.1:8080
```

6. 默认启动会事务加锁自动升级控制面到 schema v30，校验和、漂移、新版本拒绝与失败回滚继续生效。分离迁移账号可先 `aigw-migrate up`／`check`，业务进程再用 `--schema-mode=check`。自动升级不等于自动备份、混合版本无停机升级或自动降级。
7. 检查 readiness。它包含恢复能力协商与审核指纹，失败时不开放新推理；不能通过绕过 readiness 掩盖错误。确认恢复扫描、预算和审计指标正常后再逐项执行验收。

`GOVERNANCE_ALLOW_LOOPBACK_HTTP` 和 `AIGW_NEWAPI_ALLOW_LOOPBACK_HTTP` 仅为明确启用的本机测试选项；正式部署使用可信证书和实际 TLS，不据此部署跨主机明文链路。主机网络写操作另按 [系统管理](system-management.md) 验收，不由本部署流程自动启用。

## 四类接口验收记录

每项记录源提交、模型／渠道审批依据、测试身份、脱敏请求 ID、实际物理调用数、预算前后值、原始用量、回执及内容审计状态。真实调用先确定费用预算，使用合成文本，不将秘密或真实业务正文写入验收报告。

| 场景 | 验收要点 |
| --- | --- |
| Chat | JSON／SSE；审核函数参数和调用关联；审核内联图片；未审核内容拒绝；完整工具结果须等待完成证明 |
| Embeddings | 文本批量／float 维度边界；input-only 费用；向量不冒充可文本审计的输出；未支持编码拒绝 |
| Responses | 字符串与文本消息列表；store=false；JSON／实时 SSE；跨块关键词；缺失完成证明不发 completed |
| Messages | 字符串／text 块和 system；审核 Anthropic 实时流；起始计数为原始临时证据；最终 message_stop 需结算与审计封存 |
| 公共治理 | 跨租户／撤销 Key／设备证明拒绝；预算不足不派发；输入拦截和输出策略；正文查看授权、加密、保留与封存 |
| 细分计费 | 普通输入、缓存读、缓存写、可选 5 分钟／1 小时费率；每项原始计数与真实账单对应；缺失细项不猜零 |
| 故障 | 客户端取消、断流、缺失 trailer、审计拒绝、控制面／数据面退出；未知费用保留占额；原始完成事件幂等恢复，不自动重试 |
| 容量 | 20 路实际供应商并发，逐笔账本／回执／审计守恒；目标 32 GiB、长流／持续负载／内存及跨主机网络独立验收 |

已有本机 4 vCPU／16 GiB、合成供应商、真实隔离数据库、SDK、浏览器、双控制面 SIGKILL 和 20 并发开发证据见 [集成记录](new-api-integration.md) 与 [性能基线](new-api-performance.md)。不能据此勾选真实账号、目标硬件或生产 SLA。

## 升级与发布边界

先核对数据面能力和完整版本，再审核渠道指纹，最后升级控制面；启用细分时长计费前必须退出不识别新回执的旧消费者。保持同一专用服务用户进行 Token 轮换。已在途请求使用绑定的历史配置／价格，事后改价不能改变它的结算。

普通回滚只能在兼容范围内更换版本，不能让旧二进制直接连接较新结构。数据库恢复不会撤销供应商已产生的费用，也不会恢复外部网卡状态；必须补齐备份后的撤销与账务事件。正式切流前完成真实供应商／账单、IdP／终端、目标硬件／NetworkManager、跨主机故障、密钥恢复和实际分发授权审查。此包为 development_candidate，不生成 V1.0 标签或自动切换生产流量。
