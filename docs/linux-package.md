# Linux 候选发行包

这是开发候选包，不创建 V1.0 标签、不发布 GitHub Release、不安装 systemd 服务、不启动自动迁移。包含企业控制面（native 默认模式及显式 newapi 模式）；new-api 数据面另行部署，Bifrost 独立入口不在本包范围。需先通过对应提交的测试与部署验收，再决定是否用于部署。

## 构建与校验

构建机必须是 Linux amd64/arm64，使用 `.go-version`、`.node-version` 的精确版本，需要 Git、Go、Node/npm、Python 3.9+、GNU tar 等工具。读取当前仓库 **HEAD 的归档快照**，不会把未提交源码、忽略的 `.env`、本地会话和构建缓存复制进包。脚本输出实际源 commit；执行脚本本身应来自已评审提交。

```bash
bash scripts/package-linux.sh /absolute/path/new-candidate.tar.gz
python3 scripts/verify-package.py /absolute/path/new-candidate.tar.gz --expect-commit "$(git rev-parse HEAD)"
```

输出路径必须尚不存在；同名文件或符号链接不会覆盖，最终以独占硬链接方式发布文件。中途失败清理临时目录，不留下被误认为成功的目标包。冻结安装 Web 依赖，执行 API 契约、类型/Lint、生产构建；Go 使用只读依赖、trimpath、禁用 CGO 的本机架构构建。不会交叉编译或启用未验证的平台。构建前移除继承的 `VITE_*` 覆盖变量。

包内包括：

- `bin/aigw-gateway`、`bin/aigw`、`bin/aigw-migrate`、`bin/aigw-local-admin`、`bin/aigw-content-audit`、`bin/aigw-host-agent`。
- `web/` 控制台、无凭证的 `config.env.example`、已有主机代理单元示例、完整 docs 产品与接入文档，以及 api/openapi/management.json。
- `manifest.json`：源 commit、Linux 架构、Go/Node 版本、默认执行器和开发候选状态；新增 execution_engines 与独立 newapi 数据面版本声明，与 deploy/newapi-compatibility.json 相互核对。
- `SHA256SUMS`：所有载荷文件校验和；Go 模块版本/校验和清单、Go 与 Web 锁文件用于追踪依赖。依赖清单不等于完整 SBOM 或授权审查结果。

验证器逐项检查归档路径、文件类型、重复文件、清单与内容校验和，以及必须组件，不解压或执行归档内容；限制展开数据为 1 GiB、归档成员为 10000；拒绝过宽写权限、同名目录/文件冲突及异常清单类型。`--expect-commit` 将包内源提交与期望提交绑定，不能省略来源核验。它验证内部完整性，不能证明发布者身份。应另从可信交付渠道保存构建输出的整包 SHA-256；攻击者同时替换内容和清单仍可构造内部一致的包。当前没有签名、可重复构建一致性承诺或供应链证明。

## 部署衔接

校验来源和完整性后，由运维解压到独立版本目录。网关以专用非 root 用户运行，显式设置 `--web-dir=<版本目录>/web`；不要依赖开发目录的默认相对路径。按 `.env.example` 和现有入口说明配置身份、数据库、密钥及访问地址。保留自动迁移与 `--schema-mode=check` 的现有行为，升级前执行备份恢复门禁并安排回退窗口。不要直接用旧二进制连接已升级结构来假定支持降级。

主机代理的 root 权限、UID、允许网卡及安装路径应按 [系统管理](system-management.md) 单独部署；包内含二进制不代表已启用网络写权限。安装后仍需真实 Linux/NetworkManager 验收。发行还需要明确项目授权与依赖通知、签名/来源核验、真实升级恢复、目标平台兼容和生产容量门禁；当前没有把候选包标为正式商业发行。

## 持续集成门禁

`bash scripts/verify-release.sh` 运行归档边界测试，构建当前 HEAD 的候选包，校验其源 commit，然后仅对刚构建的网关、主机代理和迁移工具运行 `--help` 冒烟检查。前两者预期退出 0，迁移工具沿用现有用法提示与退出 1；不连接数据库或执行迁移。该入口不接受外部归档参数，不用于执行来历不明的包，退出时清理临时包和解出的三个命令。

GitHub Actions 增加 `package` job，依赖原 `prototype` 后端/浏览器验证成功，再使用固定 Go/Node 运行上述脚本。只验证 Linux amd64，不发布 Release、上传候选包或安装服务。普通成功的旧工作流没有这个门禁；应检查新提交的两个 job 都成功。备份恢复门禁仍须在具备 PostgreSQL 客户端的环境单独启用。


new-api 双进程的服务身份、真实 TLS、审核目录、部署顺序及四类 API 验收见 [候选部署说明](new-api-deployment.md)。数据面版本由 `deploy/newapi-compatibility.json` 固定，CI 夹具和包清单共用它；包不包含该数据面可执行文件或测试夹具。新声明缺少关键产品文档／接口契约、版本不是完整提交或声明不一致时，验证器拒绝；旧格式 native 候选包仍可验证，但不被重新标为支持新能力。
