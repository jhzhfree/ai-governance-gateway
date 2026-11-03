# 中国大陆供应商接入与验收

用户要求：至少三家中国境内供应商，并且请求处理、数据存储均限定中国大陆。本阶段实现三家服务的文本 Chat Completions 适配、模型目录/授权、人民币整数计价及预算链路。**代码实现与本地协议验证不等于真实账号、模型、价格或数据驻留已经验收。**

## 三家接入

| 供应商 | 固定服务入口 | 配置区域 | 密钥环境变量 |
| --- | --- | --- | --- |
| 阿里云百炼 | `https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions` | 北京共享网关 | `AIGW_ALIYUN_API_KEY` |
| 火山引擎方舟 | `https://ark.cn-beijing.volces.com/api/v3/chat/completions` | 北京 | `AIGW_VOLCENGINE_API_KEY` |
| 智谱 | `https://open.bigmodel.cn/api/paas/v4/chat/completions` | 国内服务入口；实际处理/存储区域必须另验合同 | `AIGW_ZHIPU_API_KEY` |

阿里与火山的北京入口有官方 SDK 源码佐证；智谱官方 SDK 佐证的是国内服务 URL，**并未据此证明所有账号、模型和内部处理/存储均在大陆**。不支持新加坡、香港、国际站或未知自定义 URL；不自动换区、跨供应商回退或重试。DeepSeek 未纳入本轮已实现名单，避免将厂商国别直接当作驻留证明。

启用前分别取得三家账号及所选模型的大陆处理、存储、日志、保留、子处理方与备份条款证据。网关进程、PostgreSQL、日志、反向代理、备份与运维导出本身也必须在大陆。不能通过跨境代理转发真实提示词或回复；当前开发云环境的所在地未获核验，因此这里只运行合成数据的本地 TLS fixture，没有真实模型流量。

## 启用条件与程序边界

配置要求顶层 `deployment_residency`（包含应用、数据库、日志和备份）及每个模型的 `residency`：

- `region` 必须为 `cn-mainland`。
- `evidence_ref` 必须是已核验的非敏感合同/区域审查记录编号，不能填密钥。
- `expires_at` 必须尚未到期；到期后拒绝该路由的新请求，从目录隐藏，并使整体 readiness 失败。在途网络请求的 deadline 也不超过审查到期时间。

这些字段是受信任部署管理员的验收声明，**程序不能验证编号背后的合同真伪，也不能观测供应商内部的数据复制**。未取得实际证据时保持空值/过期并禁用；不能为了启动填一个占位编号冒充已验收。域名后缀、供应商总部、TLS 证书或服务器 IP 定位都不能单独替代这项验收。

配置解析拒绝未知字段，包括自定义 endpoint、明文 api_key 和国际区覆盖；配置中只允许已实现供应商。URL 与密钥变量名由服务端 profile 固定。密钥只读部署环境，不在模型目录、账本、浏览器和日志中返回。出站请求不复制项目凭证、浏览器 Cookie 或客户端作用域头；TLS 正常校验，HTTP 重定向全部拒绝。

## 模型、价格与货币

配置模板：[providers.mainland.example.json](../config/providers.mainland.example.json)。模板包含三家条目，但证据为空、日期过期，所以不能直接启用。模型名、火山北京 endpoint ID、价格及 token 上限均须按实际账号核实；示例数字不是官方现行报价。每个模型用稳定的公开别名，如 `aliyun/qwen-plus`，映射实际 upstream_model。

`CNY_MICRO` 表示微人民币，1 元 = 1,000,000 最小单位。价格为“每百万 token 的微人民币整数”；例如示例输入价 2,000,000 对应每百万 token 2 元，仅用于说明单位。不得使用浮点数累计，也不得将旧 MOCK 账户改名或兑换为人民币。创建组织时明确选择 currency；币种创建后不可通过 API 更换，项目继承组织币种。

费用为 `ceil((输入token × 输入费率 + 输出token × 输出费率) / 1,000,000)`，合并后一次向上取整到微人民币。按配置费率结算用量，不代表最终供应商账单；缓存优惠、阶梯价格或其他计价差异需采用覆盖所选模型/上下文范围的保守费率并人工对账。不支持额外工具、搜索或媒体收费。

不使用未验证 tokenizer 估算输入。派发前预留**整个配置输入/context 上界**加本次 max_tokens 输出上界的费用，代价是短请求也会暂时占用较多余额。`max_input_tokens` 必须覆盖供应商允许的实际输入上界，不能为了减少占额填小数值；不能据未经核实的模型上限承诺真实账单硬限额。请求/usage 超出已配置上界、缺失用量、重复用量、超时、中断或结算失败时，保留 uncertain/held，禁止无条件退款。

价格标识包含别名、人工价格版本，以及币种、供应商、端点、实际模型、费率与上限的 SHA-256 指纹；同一配置快照得到同一指纹，修改关键字段会改变指纹。账本保留该标识。应将不含密钥的部署价格配置归档以便追溯；多个实例必须使用一致配置。当前未实现数据库发布的价格目录或供应商自动账单对账。

## 管理及调用

先应用新增 `0006` 迁移，再使用二选一的启动模式：

```sh
go run ./cmd/migrate up
go run ./cmd/gateway --mock-only
# 或在已验收的大陆部署环境中：
go run ./cmd/gateway --providers /run/aigw/providers.json
```

两种模式互斥，真实模式不会退回 mock。供应商 JSON 不存密钥；三个环境变量从大陆部署的密钥管理服务注入，不将令牌写入命令行、Git 或日志。入口仍限制 loopback，面向用户的部署需大陆 TLS 反向代理及已验收 OIDC/数据库配置；本阶段不等于生产部署验收。

`GET /api/v1/models` 或 `aigw models list --output json` 查询当前目录，只含别名、供应商、配置区域、价格指纹、费率、单位和上限，无密钥或内部合同编号。Web 创建 CNY_MICRO 组织/项目后，在“配置模型”明确授权三家别名，再选择密钥可用模型。真实币种项目默认不授权任何模型；撤销模型授权立即影响新请求。

`POST /v1/chat/completions` 使用项目密钥，支持普通文本 system/user/assistant 消息、max_tokens，以及非流式 JSON 或 `stream:true` 的 SSE。模型使用本地公开别名，网关映射供应商实际模型并规范化响应；响应 id 固定为 `chatcmpl-{网关request_id}`，流式所有片段一致，model 返回公开别名。默认关闭 thinking；所选模型必须支持对应参数。百炼/方舟发送 include_usage，智谱使用其原生尾部 usage；这些差异仍须用真实账号复核。

请求体最大 64 KiB、最多 64 条消息、每条正文最多 16 KiB；非流式响应最大 1 MiB；流式每事件 64 KiB、总读取 16 MiB、网络总时限 120 秒，写入超时 2 秒。不支持 tools、图像/音频、推理内容、n>1、自定义供应商参数或自动回退。SSE 只有收到合法 finish_reason、最终 usage 和 `[DONE]`，且结算持久化后才输出本地 `[DONE]`。SDK 错误正文不回显。账本状态不证明客户端已收到响应。

CLI/TUI 沿用现有只读上下文与账本功能，显示真实币种；TUI 概览增加已授权模型。现有 Web/CLI 项目密钥管理权限不变。完整模型/attempt/延迟结果日志仍待后续增量，本轮账本价格指纹不能替代全部调用遥测。

## 证据及待验收

官方源码参考（只读研究，未复制 SDK 实现、未新增 SDK 运行依赖）：

- [DashScope README @9d82fbd](https://github.com/dashscope/dashscope-sdk-python/blob/9d82fbd22b03ee1804f588da92cc86dcfc5a61c6/README.md)：Regions and endpoints 指明默认北京及无 workspace 时的 legacy shared gateway。文件 SHA-256 `c0d792b226facfd8b0b7d048f02fef84f5efffcaf06c17455f57db31a64161ab`。
- [Ark constants @5df6198](https://github.com/volcengine/volcengine-python-sdk/blob/5df619842a949fc59975cc22c84b482744280c27/volcenginesdkarkruntime/_constants.py)：BASE_URL 为北京 `/api/v3`；[请求类型](https://github.com/volcengine/volcengine-python-sdk/blob/5df619842a949fc59975cc22c84b482744280c27/volcenginesdkarkruntime/types/chat/completion_create_params.py)包含 max_tokens、thinking 以及流终止标记。constants SHA-256 `1e0cc8b557eb9a7f6b188d79f239574ab3700ee4df21d06146e1b565c986f600`。
- [智谱 README @ccbec4c](https://github.com/MetaGLM/zhipuai-sdk-python-v4/blob/ccbec4c7b53ca23dabac69aa1a0b5599cf29a848/README.md)：国内 base_url。SHA-256 `7062b0e4522bd08a5a5a72f30b4e13652a6da4b0b00ab14cdce5a24fee04fe38`。不将该 URL 当作内部数据驻留证明。

当前环境未配置三家凭证，官方文档站点经代理返回 CONNECT 403；没有绕过策略。参考的是允许访问的固定 GitHub 官方 SDK 快照，不宣称已核验最新套餐、价格或合同。默认测试通过本地受信任 TLS 传输替身覆盖三种固定官方 URL 的请求构造及响应行为，配合真实 PostgreSQL、RSA 签名 IdP 和浏览器；这是适配代码证据，不是实际供应商可用性/驻留证据。

真实上线退出条件：三家所选账号与模型均有大陆处理/存储证据；应用/数据库/日志/备份部署在大陆；现场核实端点、thinking、usage、计价与 token 上界；分别完成最小非流式、流式、取消及错误请求，保存不含凭证/正文的 request_id、价格快照、用量、结果与账单核对记录。上述退出条件当前保持待验收。

本阶段验证（2026-11-03）：完整脚本通过 76 个后端顶层测试、63 个子测试，另有浏览器 fixture 父测试和 5 个 Chromium 场景；无失败或跳过。Go/Web 类型生成差异、vet/race、冻结安装、TS/ESLint/Prettier、生产构建均通过。浏览器覆盖三家合成 TLS 推理、模型撤销、人民币账本、预算单位与模型弹窗键盘焦点/Tab 循环；桌面截图已核对。上述数字均为本地验证，不是三家真实大陆服务验收。
