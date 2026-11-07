# 系统管理：Linux / NetworkManager

本阶段新增系统管理页面、受保护 API、只读监控、本机特权代理和 IPv4/DNS 变更闭环。默认不开启系统权限和网络写操作；不是通用远程 Shell，也不向网关授予 root。未发布 V1.0，真实虚拟机网络切换尚未验收。

## 范围与权限

- 采样异常：必要内存/负载字段缺失、非有限数或计数溢出时返回不可用，不伪造为零；失败样本不更新 CPU 差分基线。
- CPU：相邻 `/proc/stat` 样本的整体使用率（不重复计算 guest），首次返回未知；可见逻辑 CPU、1 分钟负载。
- 内存：操作系统总量、可用量；另列 cgroup v2 挂载根的内存使用/限制及 CPU 配额；不代表任意 systemd 子组的服务专属配额。cgroup 不可读、v1 或无限制不伪造为零。CPU 百分比不是容器配额归一化百分比。
- 网卡：当前网络命名空间的接口、启用标志、MTU、地址和累计收发字节。`up` 是接口管理标志，不保证物理链路/互联网畅通。
- DNS：读取当前 `/etc/resolv.conf` 的 nameserver；可能只是本机缓存解析器。网卡配置页面另显示 NetworkManager 的 IPv4 DNS 配置。
- 配置：允许列表中的已连接、持久化普通以太网 profile；IPv4 DHCP/静态地址（最多 8 个）、网关、IPv4 DNS（最多 4 个）、是否接收自动 DNS。不支持停用/删除接口、MTU 修改、IPv6 修改、Wi-Fi/802.1X、桥接、Bond、VLAN、临时或外部 profile。

系统管理员通过精确 issuer+subject 配置，独立于组织 owner/admin、供应商管理员和本地账号管理员。未配置或其他身份返回 403，不能读取主机地址和系统操作历史。本地身份仍检查停用/强制改密；浏览器复用现有会话、Origin 与 CSRF 保护。界面入口本身不是授权依据。

每个请求绑定 `node`，防止均衡器把 A 节点的操作默默执行到 B。首版按实例访问，不是集中主机清单；部署需使用固定节点管理地址或可靠粘性路由。节点名必须唯一、稳定，网关和本机代理一致。

## 只读启用

在现有网关配置中设置以下变量；不填代理 socket 时只读取网关所在命名空间。

```text
AIGW_SYSTEM_ADMIN_ISSUER=<精确身份 issuer>
AIGW_SYSTEM_ADMIN_SUBJECT=<精确身份 subject>
AIGW_SYSTEM_NODE=<唯一节点名>
AIGW_HOST_AGENT_SOCKET=
```

OIDC 使用实际 issuer/subject。本地账号 issuer 为 `<浏览器 origin>/auth/local`，subject 为账号的稳定用户 ID，不能用显示名。容器内读取的网卡/DNS不是宿主机；此版本部署主机管理代理时应直接运行在 Linux 宿主机。

## 网络写操作的部署边界

构建 `go build -o aigw-host-agent ./cmd/host-agent`，由运维安装到 `/usr/local/libexec/aigw-host-agent`，root 拥有且普通用户不可修改。网关使用专用非 root 账号 `aigw`。示例单元见 `deploy/systemd/aigw-host-agent.service`；需先审查适配目标系统，不在开发验证中自动安装或启动。

root 管理 `/etc/aigw/host-agent.env`，示例字段（UID 和接口名应来自真实机器）：

```text
AIGW_GATEWAY_UID=<网关服务的非 root 数字 UID>
AIGW_NODE=<与网关相同的唯一节点名>
AIGW_INTERFACES=<逗号分隔的允许接口，例如 ens192>
```

代理只监听 `/run/aigw-host/agent.sock`，目录 root:aigw、0750，socket 0660；校验路径各级目录 root 所有、不可被组/其他用户写入且无符号链接，并通过 Linux `SO_PEERCRED` 仅接纳指定网关 UID。没有 TCP 监听，没有命令字符串或 shell 调用。网关设置 `AIGW_HOST_AGENT_SOCKET=/run/aigw-host/agent.sock`。代理须显式启用 `--enable-network-changes` 才有写能力，接口允许列表不可由浏览器扩展。

使用 NetworkManager 的系统 D-Bus API，依赖 Checkpoint、Reapply、Update2（目标至少支持 1.12 对应 API，发行版/后端仍须实机验收）。systemd 单元限制文件系统修改，网络配置由 NetworkManager 持久化；不直接写 `/etc/resolv.conf`、不接管 NetworkManager 未管理的接口。没有支持时保持只读/返回不可用，不降级到 shell 修改。

## 临时应用、确认与回退

1. 前端展示原配置和拟应用配置，用户输入原因并确认临时应用。
2. 网关在 PostgreSQL **提交 requested 操作记录后**才访问代理，记录 actor、node、原因、修订摘要和非机密配置。数据库失败不得下发。
3. 代理串行接纳一个变更，重新读取运行/持久配置，校验修订与接口范围，创建单接口 90 秒检查点，再以实际运行版本号调用 Reapply。此时不写持久 profile。
4. 用户验证稳定管理 origin 下的连通性后确认；最后 15 秒拒绝开始确认。重新核验运行版本和 profile，延长检查点到当前时刻后 60 秒，Update2 持久化，再销毁检查点。只有两个调用均成功才返回 confirmed。
5. 手动回退检查每个设备的结果，并复读已激活状态与原配置；不能把异步回退请求已接受当作完成。若检查点已在到期后消失，须观察原持久 profile 和原 IPv4 运行配置后才认可 rolled_back。无法证明恢复时保持 expired_unverified/rollback_failed/commit_unknown，不自动当作成功。

检查点超时由 NetworkManager 进程执行，网关/代理退出不会取消其计时；**NetworkManager 重启、主机重启/断电不在该保证内**。代理当前 pending 状态在内存中，重启后不能续接确认；已有 NM 检查点仍会阻止重叠操作并等待到期，需结合系统操作记录和实际配置核查。确认保存与检查点销毁不是跨 D-Bus 原子事务，中途失联可能是未知结果；检查点丢失不能用作成功证据。

本模块能拒绝已观察到的修订变化及并发 Reapply；不能锁住 nmcli、桌面网络设置、云初始化或配置管理工具。变更窗口须确保这些工具不并行修改同一 profile；当前还没有 profile 持久化与外部写入之间的完整 CAS 保证，列为生产验收边界。不得把普通以太网能力推广到其他接口类型。

修改管理网卡可能切断当前会话。应保留控制台/带外通道与稳定浏览器 origin；不要假定改成新 IP 后原站点 Cookie、证书和 Origin 仍然有效。确认只是管理员的连通性判断，不是自动证明 DNS、OIDC、数据库、供应商全部可达。

## 审计与失败处理

迁移 `0025_system_management.sql` 追加系统级操作表，沿用启动自动升级、事务锁、校验和/漂移校验和只检查模式。未修改历史迁移，未增加自动降级。

API：`GET /api/v1/system`、`GET /api/v1/system/events`（最近 100 条）、`POST /api/v1/system/network`、`POST /api/v1/system/network/{id}/confirm|rollback`。契约和 Go/TypeScript 类型同步。历史读取限系统管理员，不能借组织权限查看；当前没有分页、保留清理与外部锚定。

每次写操作记录 requested 以及 succeeded/rejected/unknown。apply 的 succeeded 只表示临时应用成功，不表示已确认持久化；具体结果在 payload.result.state。请求后无结果记录也必须视为未知，禁止自动重放。本地代理响应限制为 16 KiB 响应头和 1 MiB 正文，不接受截断、多段 JSON 或尾随垃圾；写结果必须匹配操作 ID、网卡/状态及临时应用截止时间。校验失败按不可用/未知处理，不重放写请求。

代理/数据库之间没有分布式事务；结果记录失败会返回不可用，原 intent 保留，用户必须刷新实际状态核查。自动超时的观测可能在后续回退核验中记录，不宣称所有主机事件实时完整回传或日志防 root 篡改。

## 本轮验收与下一步

- 单元：CPU 差分/cgroup 分离；IPv4 注入/范围拒绝；检查点、临时不落盘、确认、回退、异步回退的复读核验、过期、外部变化和未知结果。
- 真实本地通信：Unix socket、peer UID；私有 `dbus-daemon` 上的合成 NetworkManager 服务，验证实际 D-Bus 类型、IPv4 DNS 编解码与确认调用。
- PostgreSQL/API：身份与角色隔离、节点错误阻止派发、先落审计、未知结果、迁移升级/回滚/结构漂移。
- 浏览器：真实 OIDC/数据库和 Unix 代理 fixture 的监控、预览、临时应用、确认、操作历史及移动布局/可访问性。

本环境未安装 NetworkManager，所有网络写测试均使用合成服务；没有修改开发机 DNS/网卡，也没有生产部署。正式启用前必须在专用 Linux VM 验证：目标 NM 版本、真实 DHCP/静态切换、DNS 生效、90 秒失联回退、代理 SIGKILL、NetworkManager 重启、系统重启、权限部署、外部配置冲突及带外恢复，再开放生产写操作。

实现依据：[NetworkManager 检查点 API](https://www.networkmanager.dev/docs/api/latest/gdbus-org.freedesktop.NetworkManager.html)、[设备 Reapply](https://www.networkmanager.dev/docs/api/latest/gdbus-org.freedesktop.NetworkManager.Device.html)、[连接 Update2](https://www.networkmanager.dev/docs/api/latest/gdbus-org.freedesktop.NetworkManager.Settings.Connection.html)。这些文档说明协议，不替代实机验收。
