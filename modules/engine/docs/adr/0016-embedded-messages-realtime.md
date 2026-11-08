# ADR 0016：原生 Messages 文本实时流

状态：本地检查完成，首次恢复夹具失败及修复复验见根交付跟踪；远端整条门禁另行核验，不作为整体 V1 验收完成声明。

接续 ADR 0015，在明确审核的 DeepSeek `api:messages` 模型上以 `allow_messages_stream:true` 开放公共 Messages、Chat/Responses 文本 SSE。审核位加入不可变路由指纹；旧 JSON 指纹保持，关闭能力或改变供应商/API 后不能沿用旧审核。0036 追加放行原生绑定的 stream 位，不修改 0035 或旧原始计数契约。

复用固定 Bifrost v1.14.1 的公开 Anthropic 请求、流式事件转换与结束原因映射；共用原有 SDK 安全网络客户端、生命周期和限长接收。仅抽出通用 SSE 字节分帧，Chat 与 Messages 分别验证自己的协议。单帧 64 KiB、总量 16 MiB、响应头 10 秒、单次读取空闲 3 秒、整次网络 120 秒限制保持；无隐式重试或完整响应后再回放。评论和 ping 也计入上限。

原生事件必须依次为唯一 message_start、顺序编号的纯 text 内容块、唯一 message_delta、message_stop。起始消息必须模型匹配、assistant、空 content、空结束原因，并给出四个原始计数；内容块最多 64 个，起始 text 必须空（固定 SDK 只转发 delta 文本，非空起始拒绝而非丢弃）。额外工具、thinking、重复字段、错误 event/type、异常索引、没有结束事件或其他未知语义拒绝。允许评论与纯 ping。终止前 EOF 和原生 [DONE] 均不能证明完成。

`Event.InitialUsage` 仅第一条原生流事件可携带。桥接在同步释放首帧前按原始计数和模型上限复核，生成内部起始用量头；不采信客户端或供应商伪造的同名头。公共 writer 沿用既有 Messages 起始计数语义。早期计数只用于展示，不写完成事件或结算；最终完整 usage 必须与起始输入/命中/写入一致且输出不倒退，不将两次用量相加。只有收到真实 message_stop 才把原始最终字节同步交给完成日志，最终金额与审计封存仍由原治理 Handler 处理。

官方语义核对：[DeepSeek Anthropic 兼容](https://api-docs.deepseek.com/guides/anthropic_api/)（2026-11-08）。当前仅其受控纯文本子集，不代表完整 Anthropic 供应商、Claude Code、工具或全部 Messages 协议兼容。未新增或更新锁定依赖项；当前调用 SDK 转换不等于集成 Core worker/全平台。真实供应商、目标机器与商业分发仍单列验收。

验证包括：原始四计数与结束边界、27 项治理场景、实际 SSE SIGKILL/冻结价格恢复、三公共入口 HTTP 实时屏障、审计检查点/封存故障、伪造起始头拒绝、固定 Anthropic/OpenAI SDK、页面审核发布与证据、升级失败回滚/漂移、备份恢复。准确结果与远端提交见根交付跟踪。
