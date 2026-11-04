# 境内监控与指标抓取

网关支持 Prometheus 文本格式 0.0.4。`/metrics` 默认返回 404；部署时注入独立的 `AIGW_METRICS_TOKEN`（32～256 个可见 ASCII 字符，建议随机 32 字节的十六进制表示），重启后启用。密钥不能复用供应商凭据、项目密钥或用户令牌。轮换需更新网关环境和抓取端令牌文件后重启网关。令牌不进入指标与日志。

Prometheus、Grafana、持久盘和备份必须部署在中国大陆。程序提供被动抓取接口，不向公共监控服务发送数据。HTTPS 终止层只允许境内监控网络访问 `/metrics`，不记录 Authorization；如现有代理只转发 `/v1/`、`/api/`、`/auth/`，需要单独增加此路径。不要将指标端点直接暴露公网。TLS 校验保持开启。

在境内 Prometheus 配置（域名与证书按实际部署替换）：

```yaml
scrape_configs:
  - job_name: aigw
    scrape_interval: 15s
    scrape_timeout: 5s
    scheme: https
    metrics_path: /metrics
    static_configs:
      - targets: [gateway.example.cn]
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/secrets/aigw-token
    tls_config:
      ca_file: /etc/prometheus/certs/gateway-ca.pem
```

令牌文件限监控服务账号读取，不写入 Git。HTTPS 入口 `/metrics` 应原样转发至 `127.0.0.1:8080/metrics`，关闭缓存。即使凭据有效，也只支持 GET；无效或重复 Authorization 返回 401。此令牌拥有整个部署的聚合指标读取权，不绑定组织角色。

| 指标 | 含义 |
| --- | --- |
| `aigw_gateway_requests_total` | `/v1/` 网关响应计数，包含认证前拒绝 |
| `aigw_gateway_request_duration_seconds` | 从进入网关至处理结束的直方图；流式包含整个流生命周期 |
| `aigw_process_start_time_seconds` | 进程启动 Unix 时间，可识别重启 |

请求指标标签仅有固定的 `route=chat/models/other` 和 `status_class=2xx/3xx/4xx/5xx/other`。不加入动态路径、query、请求编号、组织、用户、模型、原文、令牌或 IP。管理 API、静态资源、健康检查和抓取自身不计入请求指标。进程启动前/首次请求前没有请求序列，不能把缺失当成零流量。

统计 HTTP 状态，不把已经发出 HTTP 200 后发生的 SSE 错误伪装成 HTTP 5xx；SSE 内部失败和供应商结算情况仍需调用账本/核查。耗时不代表首 token 延迟。指标不能替代预算账本、财务账单或审计证据。

常用 PromQL：

```promql
# 每秒网关请求
sum(rate(aigw_gateway_requests_total[5m]))

# 按接口分类的 P95（流式为完整响应时间）
histogram_quantile(0.95, sum by (route, le) (rate(aigw_gateway_request_duration_seconds_bucket[5m])))

# HTTP 服务端失败比例；低流量应另设最小请求阈值
sum(rate(aigw_gateway_requests_total{status_class="5xx"}[5m]))
/
sum(rate(aigw_gateway_requests_total[5m]))
```

持久历史由境内 Prometheus TSDB 保存，按容量配置保留周期（例如 30 天）和备份。计数为进程内累积值，重启归零；用 `rate`/`increase` 处理重启，不能直接跨实例相加累计值来生成财务报表。网关本地持久采样、成本报表、OTel 追踪与通知仍在交付计划中，当前实现未宣称这些能力已完成。
