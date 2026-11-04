package terminal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type Options struct {
	In         io.Reader
	Out, Err   io.Writer
	Getenv     func(string) string
	Keyring    Keyring
	HTTPClient *http.Client
	IsTerminal func() bool
	RunUI      func(context.Context, Model) error
}
type inputError struct{ message string }

func (e *inputError) Error() string { return e.message }
func input(message string) error    { return &inputError{message} }

type app struct {
	options              Options
	state                State
	path, server, output string
	allowHTTP            bool
	root                 *cobra.Command
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return input("该命令不接受额外参数，请使用 --help")
	}
	return nil
}
func NewCommand(options Options) *cobra.Command {
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.Err == nil {
		options.Err = os.Stderr
	}
	if options.Getenv == nil {
		options.Getenv = os.Getenv
	}
	path, err := DefaultConfigPath()
	if err != nil {
		path = ""
	}
	a := &app{options: options, path: path}
	root := &cobra.Command{Use: "aigw", Short: "AI 治理网关终端客户端", SilenceUsage: true, SilenceErrors: true, Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	a.root = root
	root.SetIn(options.In)
	root.SetOut(options.Out)
	root.SetErr(options.Err)
	root.PersistentFlags().StringVar(&a.path, "config", path, "私有配置文件位置")
	root.PersistentFlags().StringVar(&a.server, "server", "", "API 服务 origin；也可设置 AIGW_SERVER")
	root.PersistentFlags().StringVar(&a.output, "output", "table", "输出格式：table 或 json")
	root.PersistentFlags().BoolVar(&a.allowHTTP, "allow-loopback-http", false, "显式允许本机 IP 的 HTTP 开发连接")
	root.SetFlagErrorFunc(func(*cobra.Command, error) error { return input("命令参数无效，请使用 --help") })
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		if a.output != "table" && a.output != "json" {
			return input("--output 必须是 table 或 json")
		}
		if a.path == "" {
			return input("请使用 --config 指定私有配置文件")
		}
		absolute, err := filepath.Abs(a.path)
		if err != nil {
			return input("配置路径无效")
		}
		a.state = State{Path: absolute, Keyring: options.Keyring}
		return nil
	}
	a.addAuthentication()
	a.addQueries()
	a.addRequests()
	a.addModels()
	a.addOperations()
	a.addRuntime()
	a.addCostReports()
	a.addBilling()
	a.addContentAudit()
	a.addContext()
	a.addTUI()
	return root
}
func (a *app) config() (Config, error) {
	cfg, _, err := a.state.Load()
	if err != nil {
		return cfg, input(err.Error())
	}
	override := a.server
	if override == "" {
		override = a.options.Getenv("AIGW_SERVER")
	}
	allow := cfg.AllowLoopbackHTTP
	if a.root.PersistentFlags().Changed("allow-loopback-http") {
		allow = a.allowHTTP
	}
	if override != "" {
		normalized, err := canonicalURL(override, allow)
		if err != nil {
			return cfg, input(err.Error())
		}
		if cfg.Server != "" && cfg.Server != normalized && a.options.Getenv("AIGW_TOKEN") == "" {
			return cfg, input("已保存的凭证属于其他服务；请重新登录，不能用 --server 转发已有凭证")
		}
		cfg.Server = normalized
	}
	if cfg.Server == "" {
		return cfg, input("请先登录，或通过 --server / AIGW_SERVER 指定服务")
	}
	cfg.AllowLoopbackHTTP = allow
	return cfg, nil
}
func (a *app) client() (Config, *Client, error) {
	cfg, err := a.config()
	if err != nil {
		return cfg, nil, err
	}
	token := a.options.Getenv("AIGW_TOKEN")
	if token == "" {
		credential, e := a.state.Credential(cfg)
		if e != nil {
			return cfg, nil, fmt.Errorf("%w：%s", ErrNotLoggedIn, e.Error())
		}
		token = credential.AccessToken
	}
	client, err := NewClient(cfg.Server, token, cfg.AllowLoopbackHTTP, a.options.HTTPClient)
	return cfg, client, err
}
func (a *app) emit(value any, headers []string, rows [][]string, cursor string) error {
	if a.output == "json" {
		return writeJSON(a.options.Out, value)
	}
	table := tabwriter.NewWriter(a.options.Out, 0, 4, 3, ' ', 0)
	if _, err := fmt.Fprintln(table, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		for i := range row {
			row[i] = SafeText(row[i])
		}
		if _, err := fmt.Fprintln(table, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if len(rows) == 0 {
		if _, err := fmt.Fprintln(a.options.Out, "暂无数据"); err != nil {
			return err
		}
	}
	if cursor != "" {
		_, err := fmt.Fprintf(a.options.Err, "还有更多结果，可使用 --after %s 继续查询。\n", SafeText(cursor))
		return err
	}
	return nil
}
func (a *app) addAuthentication() {
	var issuer, clientID, audience, store, username string
	var localLogin, passwordStdin bool
	var timeout time.Duration
	login := &cobra.Command{Use: "login", Short: "使用 OIDC Device Flow 登录（公共客户端）", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if a.options.Getenv("AIGW_TOKEN") != "" {
			return input("交互登录前请先取消 AIGW_TOKEN，避免环境凭证覆盖登录状态")
		}
		old, exists, err := a.state.Load()
		if err != nil {
			return input(err.Error())
		}
		cfg := old
		target := a.server
		if target == "" {
			target = a.options.Getenv("AIGW_SERVER")
		}
		if target == "" {
			target = cfg.Server
		}
		allow := cfg.AllowLoopbackHTTP
		if a.root.PersistentFlags().Changed("allow-loopback-http") {
			allow = a.allowHTTP
		}
		cfg.Server, err = canonicalURL(target, allow)
		if err != nil {
			return input(err.Error())
		}
		cfg.AllowLoopbackHTTP = allow
		if issuer != "" {
			cfg.Issuer = issuer
		}
		if clientID != "" {
			cfg.ClientID = clientID
		}
		if audience != "" {
			cfg.Audience = audience
		}
		if cfg.Audience == "" {
			cfg.Audience = "aigw-management"
		}
		if cmd.Flags().Changed("credential-store") || cfg.CredentialStore == "" {
			cfg.CredentialStore = store
		}
		if cfg.CredentialStore != "keyring" && cfg.CredentialStore != "file" {
			return input("credential-store 只能是 keyring 或 file")
		}
		if timeout <= 0 {
			return input("登录超时必须为正数")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		var credential Credential
		if localLogin {
			if issuer != "" || clientID != "" || audience != "" || username == "" {
				return input("本地登录请指定 --username，不与 OIDC 参数混用")
			}
			cfg.Issuer = cfg.Server + "/auth/local"
			cfg.ClientID = "aigw-local"
			cfg.Audience = "aigw-management"
			var secret []byte
			if passwordStdin {
				line, e := bufio.NewReader(io.LimitReader(a.options.In, 258)).ReadString('\n')
				if e != nil && e != io.EOF {
					return e
				}
				secret = []byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
			} else {
				file, ok := a.options.In.(*os.File)
				if !ok || !term.IsTerminal(int(file.Fd())) {
					return input("无交互终端时请显式使用 --password-stdin")
				}
				fmt.Fprint(a.options.Err, "本地账号密码：")
				secret, err = term.ReadPassword(int(file.Fd()))
				fmt.Fprintln(a.options.Err)
				if err != nil {
					return err
				}
			}
			credential, err = LocalLogin(ctx, cfg, username, string(secret), a.options.HTTPClient)
			for i := range secret {
				secret[i] = 0
			}
		} else {
			credential, err = DeviceLogin(ctx, cfg, a.options.HTTPClient, func(c Challenge) error {
				_, err := fmt.Fprintf(a.options.Err, "请在浏览器打开 %s\n输入验证码：%s\n等待授权，按 Ctrl+C 可取消。\n", c.VerificationURI, c.UserCode)
				return err
			})
		}
		if err != nil {
			return err
		}
		if !old.sameBinding(cfg) {
			cfg.Organization = ""
			cfg.Project = ""
		}
		if err := a.state.SaveCredential(cfg, credential); err != nil {
			return err
		}
		if err := a.state.Save(cfg); err != nil {
			return err
		}
		if exists && (old.CredentialStore != cfg.CredentialStore || (old.CredentialStore == "keyring" && !old.sameBinding(cfg))) {
			if err := a.state.DeleteCredential(old); err != nil {
				fmt.Fprintln(a.options.Err, "新登录已保存，但旧凭证清理未完成，请检查原存储。")
			}
		}
		if cfg.CredentialStore == "file" {
			fmt.Fprintln(a.options.Err, "已按显式选择将令牌保存到私有 0600 文件；无静默降级。")
		}
		status := map[string]any{"configured": true, "server": cfg.Server, "user": credential.User, "expires_at": credential.ExpiresAt, "source": cfg.CredentialStore}
		return a.emit(status, []string{"状态", "用户", "服务", "有效期至"}, [][]string{{"已登录", credential.User.Subject, cfg.Server, credential.ExpiresAt.Format(time.RFC3339)}}, "")
	}}
	login.Flags().BoolVar(&localLogin, "local", false, "使用显式本地账号登录，无需外部 IdP")
	login.Flags().BoolVar(&passwordStdin, "password-stdin", false, "从受控标准输入读取密码，不使用密码参数")
	login.Flags().StringVar(&username, "username", "", "本地账号用户名")
	login.Flags().StringVar(&issuer, "issuer", "", "OIDC issuer（允许 realm 路径）")
	login.Flags().StringVar(&clientID, "client-id", "", "公共终端客户端 ID")
	login.Flags().StringVar(&audience, "audience", "", "管理 API audience（默认 aigw-management）")
	login.Flags().StringVar(&store, "credential-store", "keyring", "凭证存储：keyring 或显式 file")
	login.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "设备授权等待上限")
	a.root.AddCommand(login)
	a.root.AddCommand(&cobra.Command{Use: "logout", Short: "删除已保存凭证；本地账号同时尝试撤销服务端令牌", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, exists, err := a.state.Load()
		if err != nil {
			return err
		}
		if exists {
			if cfg.ClientID == "aigw-local" {
				if credential, e := a.state.Credential(cfg); e == nil {
					ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
					err := RevokeLocalCredential(ctx, cfg, credential.AccessToken, a.options.HTTPClient)
					cancel()
					if err != nil {
						fmt.Fprintln(a.options.Err, "服务端令牌撤销未确认；请管理员撤销会话或等待令牌到期。")
					}
				}
			}
			if err := a.state.DeleteCredential(cfg); err != nil {
				return err
			}
			cfg.Organization = ""
			cfg.Project = ""
			if err := a.state.Save(cfg); err != nil {
				return err
			}
		}
		return a.emit(map[string]any{"local_credentials_removed": true}, []string{"状态"}, [][]string{{"本地凭证已清理；环境令牌和 IdP 会话不受影响"}}, "")
	}})
	auth := &cobra.Command{Use: "auth", Short: "本地身份状态", Args: noArgs}
	auth.AddCommand(&cobra.Command{Use: "status", Short: "检查本地配置与有效期（不代表服务端已认证）", Args: noArgs, RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := a.config()
		if err != nil {
			return err
		}
		if a.options.Getenv("AIGW_TOKEN") != "" {
			return a.emit(map[string]any{"configured": true, "source": "environment", "server": cfg.Server}, []string{"来源", "服务"}, [][]string{{"environment", cfg.Server}}, "")
		}
		cred, err := a.state.Credential(cfg)
		if err != nil {
			return fmt.Errorf("%w：%s", ErrNotLoggedIn, err.Error())
		}
		return a.emit(map[string]any{"configured": true, "source": cfg.CredentialStore, "server": cfg.Server, "user": cred.User, "expires_at": cred.ExpiresAt}, []string{"来源", "用户", "有效期至"}, [][]string{{cfg.CredentialStore, cred.User.Subject, cred.ExpiresAt.Format(time.RFC3339)}}, "")
	}})
	a.root.AddCommand(auth)
}
func (a *app) addQueries() {
	var orgAfter string
	var orgLimit int
	organizations := &cobra.Command{Use: "organizations", Aliases: []string{"orgs"}, Short: "组织查询", Args: noArgs}
	orgList := &cobra.Command{Use: "list", Short: "列出可访问组织", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, client, err := a.client()
		if err != nil {
			return err
		}
		v, err := client.Organizations(cmd.Context(), orgAfter, orgLimit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, o := range v.Data {
			rows = append(rows, []string{o.Id, o.Name, string(o.Role), o.Period})
		}
		return a.emit(v, []string{"ID", "名称", "角色", "周期"}, rows, v.NextCursor)
	}}
	orgList.Flags().StringVar(&orgAfter, "after", "", "分页游标")
	orgList.Flags().IntVar(&orgLimit, "limit", 50, "每页数量（1–100）")
	organizations.AddCommand(orgList)
	a.root.AddCommand(organizations)
	var projectOrg, projectAfter string
	var projectLimit int
	projects := &cobra.Command{Use: "projects", Short: "项目查询", Args: noArgs}
	projectList := &cobra.Command{Use: "list", Short: "列出组织内可访问项目", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		org := projectOrg
		if org == "" {
			org = cfg.Organization
		}
		v, err := client.Projects(cmd.Context(), org, projectAfter, projectLimit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, p := range v.Data {
			rows = append(rows, []string{p.Id, p.Name, string(p.Role)})
		}
		return a.emit(v, []string{"ID", "名称", "角色"}, rows, v.NextCursor)
	}}
	projectList.Flags().StringVar(&projectOrg, "org", "", "组织 ID，默认当前上下文")
	projectList.Flags().StringVar(&projectAfter, "after", "", "分页游标")
	projectList.Flags().IntVar(&projectLimit, "limit", 50, "每页数量（1–100）")
	projects.AddCommand(projectList)
	a.root.AddCommand(projects)
	var usageOrg, usageProject string
	usage := &cobra.Command{Use: "usage", Short: "查看当前项目预算与用量", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		org, project := scope(cfg, usageOrg, usageProject)
		v, err := client.Usage(cmd.Context(), org, project)
		if err != nil {
			return err
		}
		return a.emit(v, []string{"周期", "单位", "预算", "已用", "预留", "可用"}, [][]string{{v.Period, string(v.Currency), fmt.Sprint(v.LimitAmount), fmt.Sprint(v.Spent), fmt.Sprint(v.Held), fmt.Sprint(v.LimitAmount - v.Spent - v.Held)}}, "")
	}}
	usage.Flags().StringVar(&usageOrg, "org", "", "组织 ID")
	usage.Flags().StringVar(&usageProject, "project", "", "项目 ID")
	a.root.AddCommand(usage)
	var keyOrg, keyProject, keyAfter string
	var keyLimit int
	keys := &cobra.Command{Use: "keys", Short: "只读密钥元数据", Args: noArgs}
	keyList := &cobra.Command{Use: "list", Short: "列出密钥元数据，不展示完整密钥", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		org, project := scope(cfg, keyOrg, keyProject)
		v, err := client.Keys(cmd.Context(), org, project, keyAfter, keyLimit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, k := range v.Data {
			state := "有效"
			if k.RevokedAt != nil {
				state = "已撤销"
			} else if !k.ExpiresAt.After(time.Now()) {
				state = "已过期"
			}
			rows = append(rows, []string{k.Id, k.Name, k.Prefix, state, k.ExpiresAt.Format(time.RFC3339)})
		}
		return a.emit(v, []string{"ID", "名称", "前缀", "状态", "有效期至"}, rows, v.NextCursor)
	}}
	keyList.Flags().StringVar(&keyOrg, "org", "", "组织 ID")
	keyList.Flags().StringVar(&keyProject, "project", "", "项目 ID")
	keyList.Flags().StringVar(&keyAfter, "after", "", "分页游标")
	keyList.Flags().IntVar(&keyLimit, "limit", 50, "每页数量（1–100）")
	keys.AddCommand(keyList)
	a.root.AddCommand(keys)
	var auditOrg, auditAfter string
	var auditLimit int
	audit := &cobra.Command{Use: "audit", Short: "只读组织审计", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		org := auditOrg
		if org == "" {
			org = cfg.Organization
		}
		v, err := client.Audit(cmd.Context(), org, auditAfter, auditLimit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, entry := range v.Data {
			rows = append(rows, []string{fmt.Sprint(entry.Id), entry.Action, entry.Actor.Subject, entry.ResourceId, entry.CreatedAt.Format(time.RFC3339)})
		}
		return a.emit(v, []string{"ID", "动作", "操作者", "资源", "时间"}, rows, v.NextCursor)
	}}
	audit.Flags().StringVar(&auditOrg, "org", "", "组织 ID")
	audit.Flags().StringVar(&auditAfter, "after", "", "十进制审计游标")
	audit.Flags().IntVar(&auditLimit, "limit", 50, "每页数量（1–100）")
	a.root.AddCommand(audit)
}
func scope(cfg Config, org, project string) (string, string) {
	if org == "" {
		org = cfg.Organization
	}
	if project == "" {
		project = cfg.Project
	}
	return org, project
}
func (a *app) addContext() {
	group := &cobra.Command{Use: "context", Short: "本地组织/项目上下文", Args: noArgs}
	group.AddCommand(&cobra.Command{Use: "show", Short: "显示不含凭证的上下文", Args: noArgs, RunE: func(*cobra.Command, []string) error {
		cfg, err := a.config()
		if err != nil {
			return err
		}
		return a.emit(map[string]string{"server": cfg.Server, "organization": cfg.Organization, "project": cfg.Project}, []string{"服务", "组织", "项目"}, [][]string{{cfg.Server, cfg.Organization, cfg.Project}}, "")
	}})
	var org, project string
	use := &cobra.Command{Use: "use", Short: "验证权限后保存组织/项目上下文", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		if org == "" {
			return input("context use 需要 --org")
		}
		if _, err := client.Projects(cmd.Context(), org, "", 1); err != nil {
			return err
		}
		if project != "" {
			if _, err := client.Project(cmd.Context(), org, project); err != nil {
				return err
			}
		}
		cfg.Organization = org
		cfg.Project = project
		if err := a.state.Save(cfg); err != nil {
			return err
		}
		return a.emit(map[string]string{"server": cfg.Server, "organization": org, "project": project}, []string{"组织", "项目"}, [][]string{{org, project}}, "")
	}}
	use.Flags().StringVar(&org, "org", "", "组织 ID（必填）")
	use.Flags().StringVar(&project, "project", "", "项目 ID；不填则清除项目选择")
	group.AddCommand(use)
	a.root.AddCommand(group)
}
func (a *app) addTUI() {
	a.root.AddCommand(&cobra.Command{Use: "tui", Short: "交互式只读控制台", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if a.output == "json" {
			return input("tui 不支持 JSON；请使用 organizations/projects/usage/keys 命令")
		}
		terminal := a.options.IsTerminal
		if terminal == nil {
			terminal = func() bool {
				in, ok := a.options.In.(*os.File)
				if !ok {
					return false
				}
				out, ok := a.options.Out.(*os.File)
				return ok && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
			}
		}
		if !terminal() {
			return input("tui 需要交互终端；脚本请使用 --output json")
		}
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		model := NewModel(cmd.Context(), client, cfg, func(org, project string) error {
			current, _, err := a.state.Load()
			if err != nil {
				return err
			}
			if current.Server != "" && !current.sameBinding(cfg) {
				return errors.New("登录配置已变化，请重启终端界面")
			}
			cfg.Organization = org
			cfg.Project = project
			return a.state.Save(cfg)
		}, a.options.Getenv("NO_COLOR") != "" || a.options.Getenv("TERM") == "dumb")
		defer model.stop()
		if a.options.RunUI != nil {
			return a.options.RunUI(cmd.Context(), model)
		}
		return RunModel(cmd.Context(), model, a.options.In, a.options.Out)
	}})
}
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var usage *inputError
	if errors.As(err, &usage) {
		return 2
	}
	var api *APIError
	if errors.Is(err, ErrNotLoggedIn) || errors.Is(err, ErrLoginDenied) || (errors.As(err, &api) && (api.Status == 401 || api.Status == 403)) {
		return 3
	}
	return 1
}
func Execute(ctx context.Context, options Options, args []string) int {
	root := NewCommand(options)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	if strings.HasPrefix(err.Error(), "unknown command") {
		err = input("未知命令，请使用 --help")
	}
	out := options.Err
	if out == nil {
		out = os.Stderr
	}
	format, _ := root.PersistentFlags().GetString("output")
	if format == "json" {
		_ = writeJSON(out, map[string]any{"error": map[string]any{"message": SafeText(err.Error()), "exit_code": ExitCode(err)}})
	} else {
		fmt.Fprintln(out, SafeText(err.Error()))
	}
	return ExitCode(err)
}
