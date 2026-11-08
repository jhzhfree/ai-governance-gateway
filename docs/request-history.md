# 调用账本查询

本阶段把现有推理预算账本提供给 Web、CLI 和 TUI。使用推理响应的 `X-Request-ID` 查询一次调用的预留、派发和结算状态；不保存或返回提示词、回复正文、认证头及项目/供应商密钥。

这是**有预算预留的调用记录**，不是完整访问日志。鉴权、请求格式或预算检查失败且尚未建立预留的请求不会出现。新请求可关联[执行元数据](execution-metadata.md)，提供公开模型、配置供应商/区域、上游状态、用量和执行时间；历史空值不从价格版本推测。`settled` 只表示账本结算完成，不代表响应成功送达客户端。

## 契约与权限

- `GET /api/v1/organizations/{org}/projects/{project}/requests`：跨预算周期查询，按 `created_at DESC, request_id DESC` 排序。支持 `limit=1..100`、不透明 `after` 游标（最长 512 字节）、`state` 过滤。
- `GET /api/v1/organizations/{org}/projects/{project}/requests/{request}`：按完整 request_id 查询详情。

两者要求管理身份或已登录浏览器会话，并复用项目 RBAC。组织 owner/admin、组织 viewer，以及有对应项目成员身份的 developer 可读。跨组织、错误项目、无项目权限和未知 request_id 返回 404；推理项目密钥不能访问管理查询。请求 ID 不能代替授权。

列表返回 `{data: [...], next_cursor: "..."}`。每条记录包含 request_id、预算周期、单位、价格版本、账本状态、预留量、结算量、创建及更新时间，另有可空 attempt 对象及独立 outcome 筛选。列表和详情均为只读，不触发恢复、退款或重新计费。`actual: null` 表示未结算，不能显示为零。

| 账本状态 | 展示含义 |
| --- | --- |
| reserved | 已建立预留，尚未确认派发 |
| dispatched | 已标记派发，结果尚未结算 |
| uncertain | 待核对；保留保守预留 |
| settled | 已按 usage 完成账本结算 |
| released | 已确认可释放，结算量为零 |
| estimated_final | 已按保守估算完成结算，非供应商最终账单确认 |

分页不是快照。翻页时保留相同作用域和状态条件；新请求通过刷新获取。并发状态变化会影响筛选结果，不能将页面结果作为审计导出完整性凭据。相同创建时间用 request_id 破除并列，避免静态数据重复或漏项。

## 双端使用

Web 侧栏打开“调用账本”，选择组织和项目。支持账本状态筛选、加载更多、完整 request_id 查询和详情。筛选和详情 ID 写入 URL，可刷新恢复。查询缓存同时绑定登录主体、项目路径和筛选/请求 ID，切换项目不保留旧项目详情。桌面及窄屏均可横向滚动表格。

```sh
./bin/aigw requests list --org ORGANIZATION_ID --project PROJECT_ID
./bin/aigw requests list --state uncertain --output json
./bin/aigw requests list --limit 20 --after CURSOR --output json
./bin/aigw requests get --request-id REQUEST_ID --output json
./bin/aigw tui
```

CLI 可省略已通过 `context use` 保存的组织/项目，输出/退出码沿用[终端契约](terminal-client.md)。TUI 在项目概览按 `t` 打开账本，↑/↓ 选择，Enter 打开详情，Esc 返回，r 刷新，n/p 翻页。网络请求仍可取消，并忽略迟到响应；刷新失败标记同作用域旧数据。

## 单进程供应商完成证据

0032 起，单进程 Chat 详情附带可选 `embedded_completion`。Web 在详情下方显示“供应商完成证据”；CLI/TUI 详情和 JSON 查询也读取同一数据。没有嵌入执行绑定的旧记录不返回该对象，不能由供应商名推测其执行引擎。

| 证据状态 | 页面含义 | 操作含义 |
| --- | --- | --- |
| awaiting_usage | 未取得完成证据 | 数量、回执金额和时间为空，保留未知费用占额 |
| recorded | 完成证据已记录 | 展示已持久化数量及记录时间；尚无回执，结合账本状态等待恢复或核对 |
| receipted | 已生成结算回执 | 展示原价格、数量、回执金额与时间；不会把未结束/取消的请求改成成功 |

证据金额与费率的 API 单位仍为父记录的 `currency`；Web 将 CNY_MICRO 转换为元并保留微元精度，CLI 表格明确显示原始单位。记录对象不返回原始 usage 字节、供应商响应、配置摘要、密钥或内容。列表/详情读取不触发后台结算；查看权限与其他调用元数据相同。

已生成回执的原始用量可用于成本统计和账单差异核对，即使客户端进程在写入 attempt 数量前崩溃；未生成回执的事件不会单独抬高统计覆盖率。原有 attempt 用量语义继续保留，两个来源不会重复相加。回执不证明客户端送达或内容审计封存，仍须分别查看执行结果及审计记录。

## 数据库与验证

`0005_request_history_index.sql` 是最初调用历史索引迁移；单进程完成证据使用后续 0032。本分支正常启动默认自动执行追加迁移，保留事务锁、校验和及结构漂移检查；分离升级角色可先运行 `cmd/migrate up` 再以 `--schema-mode=check` 启动。生产大表建索引需要评估锁及执行窗口，开发隔离库验证不代替生产演练。

`AIGW_VERIFY_WEB=1 bash scripts/verify.sh` 覆盖真实 PostgreSQL、签名测试 IdP、推理网关及模拟供应商。验收包括：推理后查同一请求、响应契约、元数据无正文/凭证、跨租户/项目和角色隔离、未知 ID、同时间分页、状态过滤、待核对保持占额、旧库升级、CLI 与 TUI 读取同一 API，以及浏览器 51 次真实模拟推理后的加载更多/筛选/详情/跨项目/移动端与 axe 检查。没有真实供应商付费调用。

后续已实现模型目录、独立 CNY_MICRO、价格指纹及三家[大陆供应商受控适配](mainland-providers.md)，真实账号/现价/驻留仍待验收；不能把现有 `MOCK` 数字直接解释成真实货币。完整调用结果、耗时、模型和 attempt 元数据将作为单独增量设计，与账本状态分开。

阶段验证（2026-11-03）：最终完整校验通过 61 个后端顶层测试及 39 个子测试，另有浏览器 fixture 父测试与 4 个 Chromium 场景；无失败或跳过。冻结依赖、Go/Web 类型差异、vet/race、TS/ESLint/Prettier、生产构建和 CLI 构建/帮助冒烟均通过。桌面与移动端截图已生成，桌面布局已核对。
