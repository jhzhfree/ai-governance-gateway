package terminal

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
)

type ReadAPI interface {
	BillingRecords(context.Context, string, string, string, string, int) (types.BillingPage, error)
	Runtime(context.Context, string, string) (types.Runtime, error)
	Organizations(context.Context, string, int) (types.OrganizationPage, error)
	Projects(context.Context, string, string, int) (types.ProjectPage, error)
	Project(context.Context, string, string) (types.ProjectDetails, error)
	Usage(context.Context, string, string) (types.Usage, error)
	Keys(context.Context, string, string, string, int) (types.KeyPage, error)
	Requests(context.Context, string, string, string, string, int) (types.RequestPage, error)
	Request(context.Context, string, string, string) (types.RequestRecord, error)
}
type screen int

const (
	organizationsScreen screen = iota
	projectsScreen
	dashboardScreen
	requestsScreen
	requestDetailScreen
	runtimeScreen
	billingScreen
)

type dataMessage struct {
	bills         types.BillingPage
	runtime       types.Runtime
	requests      types.RequestPage
	record        types.RequestRecord
	generation    uint64
	screen        screen
	err           error
	organizations types.OrganizationPage
	projects      types.ProjectPage
	keys          types.KeyPage
	usage         types.Usage
	project       types.ProjectDetails
}
type Model struct {
	bills                []types.BillingRecord
	runtime              *types.Runtime
	records              []types.RequestRecord
	record               *types.RequestRecord
	detailID             string
	api                  ReadAPI
	ctx                  context.Context
	config               Config
	save                 func(string, string) error
	screen               screen
	generation           uint64
	cancel               context.CancelFunc
	initial              context.Context
	stop                 context.CancelFunc
	organizations        []types.Organization
	projects             []types.Project
	keys                 []types.Key
	usage                *types.Usage
	details              *types.ProjectDetails
	position             int
	cursor, next         string
	previous             []string
	width, height        int
	busy, stale, noColor bool
	err                  error
	updated              time.Time
}

func NewModel(ctx context.Context, api ReadAPI, config Config, save func(string, string) error, noColor bool) Model {
	lifetime, stop := context.WithCancel(ctx)
	initial, cancel := context.WithCancel(lifetime)
	m := Model{ctx: lifetime, initial: initial, cancel: cancel, stop: stop, busy: true, api: api, config: config, save: save, width: 80, height: 24, noColor: noColor, generation: 1}
	if config.Organization != "" && config.Project != "" {
		m.screen = dashboardScreen
	}
	return m
}
func (m Model) Init() tea.Cmd {
	captured := m
	captured.ctx = m.initial
	cmd := captured.fetch()
	return func() tea.Msg { defer m.cancel(); return cmd() }
}
func (m Model) fetch() tea.Cmd {
	// Each command captures its scope and generation before leaving the UI loop.
	return func() tea.Msg {
		result := dataMessage{generation: m.generation, screen: m.screen}
		ctx := m.ctx
		switch m.screen {
		case billingScreen:
			result.bills, result.err = m.api.BillingRecords(ctx, m.config.Organization, m.config.Project, m.cursor, "", 20)
		case runtimeScreen:
			result.runtime, result.err = m.api.Runtime(ctx, m.config.Organization, m.config.Project)
		case organizationsScreen:
			result.organizations, result.err = m.api.Organizations(ctx, m.cursor, 20)
		case projectsScreen:
			result.projects, result.err = m.api.Projects(ctx, m.config.Organization, m.cursor, 20)
		case requestsScreen:
			result.requests, result.err = m.api.Requests(ctx, m.config.Organization, m.config.Project, m.cursor, "", 20)
		case requestDetailScreen:
			result.record, result.err = m.api.Request(ctx, m.config.Organization, m.config.Project, m.detailID)
		case dashboardScreen:
			result.project, result.err = m.api.Project(ctx, m.config.Organization, m.config.Project)
			if result.err != nil {
				break
			}
			result.usage, result.err = m.api.Usage(ctx, m.config.Organization, m.config.Project)
			if result.err != nil {
				break
			}
			result.keys, result.err = m.api.Keys(ctx, m.config.Organization, m.config.Project, m.cursor, 20)
		}
		return result
	}
}
func (m Model) load() (Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	// Keep the lifetime parent separate: refresh must not inherit a previously
	// cancelled request context. The captured fetch model alone receives ctx.
	m.cancel = cancel
	m.generation++
	m.busy = true
	m.err = nil
	captured := m
	captured.ctx = ctx
	cmd := captured.fetch()
	return m, func() tea.Msg { defer cancel(); return cmd() }
}
func (m *Model) reset() {
	m.cursor = ""
	m.next = ""
	m.previous = nil
	m.position = 0
	m.organizations = nil
	m.projects = nil
	m.keys = nil
	m.bills = nil
	m.runtime = nil
	m.records = nil
	m.record = nil
	m.usage = nil
	m.details = nil
	m.stale = false
	m.updated = time.Time{}
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, v.Width)
		m.height = max(1, v.Height)
	case dataMessage:
		if v.generation != m.generation || v.screen != m.screen {
			return m, nil
		}
		m.busy = false
		if v.err != nil {
			m.err = v.err
			m.stale = !m.updated.IsZero()
			return m, nil
		}
		m.err = nil
		m.stale = false
		m.updated = time.Now()
		switch m.screen {
		case billingScreen:
			m.bills = v.bills.Data
			m.next = v.bills.NextCursor
		case runtimeScreen:
			m.runtime = &v.runtime
		case organizationsScreen:
			m.organizations = v.organizations.Data
			m.next = v.organizations.NextCursor
		case projectsScreen:
			m.projects = v.projects.Data
			m.next = v.projects.NextCursor
		case requestsScreen:
			m.records = v.requests.Data
			m.next = v.requests.NextCursor
		case requestDetailScreen:
			m.record = &v.record
		case dashboardScreen:
			m.usage = &v.usage
			m.details = &v.project
			m.keys = v.keys.Data
			m.next = v.keys.NextCursor
		}
		m.position = min(m.position, max(0, m.itemCount()-1))
	case tea.KeyPressMsg:
		switch v.String() {
		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			if m.stop != nil {
				m.stop()
			}
			return m, tea.Quit
		case "esc":
			if m.screen == organizationsScreen {
				if m.cancel != nil {
					m.cancel()
				}
				if m.stop != nil {
					m.stop()
				}
				return m, tea.Quit
			}
			if m.screen == requestDetailScreen {
				m.screen = requestsScreen
			} else if m.screen == requestsScreen || m.screen == runtimeScreen || m.screen == billingScreen {
				m.screen = dashboardScreen
			} else if m.screen == dashboardScreen {
				m.screen = projectsScreen
			} else {
				m.screen = organizationsScreen
			}
			m.reset()
			return m.load()
		case "b":
			if m.screen == dashboardScreen {
				if m.usage == nil || m.usage.Currency != "CNY_MICRO" {
					m.err = input("账单核查要求已核验大陆部署的 CNY_MICRO 项目")
					return m, nil
				}
				m.screen = billingScreen
				m.reset()
				return m.load()
			}
		case "s":
			if m.screen == dashboardScreen {
				m.screen = runtimeScreen
				m.reset()
				return m.load()
			}
		case "t":
			if m.screen == dashboardScreen {
				m.screen = requestsScreen
				m.reset()
				return m.load()
			}
		case "r":
			return m.load()
		case "up", "k":
			m.position = max(0, m.position-1)
		case "down", "j":
			m.position = min(max(0, m.itemCount()-1), m.position+1)
		case "n":
			if !m.busy && m.next != "" {
				m.previous = append(m.previous, m.cursor)
				m.cursor = m.next
				m.position = 0
				return m.load()
			}
		case "p":
			if !m.busy && len(m.previous) > 0 {
				m.cursor = m.previous[len(m.previous)-1]
				m.previous = m.previous[:len(m.previous)-1]
				m.position = 0
				return m.load()
			}
		case "enter":
			if m.busy {
				return m, nil
			}
			if m.screen == requestsScreen {
				if len(m.records) == 0 {
					return m, nil
				}
				m.detailID = m.records[m.position].RequestId
				m.screen = requestDetailScreen
				m.reset()
				return m.load()
			}
			org, project := m.config.Organization, m.config.Project
			switch m.screen {
			case organizationsScreen:
				if len(m.organizations) == 0 {
					return m, nil
				}
				org = m.organizations[m.position].Id
				project = ""
			case projectsScreen:
				if len(m.projects) == 0 {
					return m, nil
				}
				project = m.projects[m.position].Id
			default:
				return m, nil
			}
			if m.save != nil {
				if err := m.save(org, project); err != nil {
					m.err = err
					return m, nil
				}
			}
			m.config.Organization = org
			m.config.Project = project
			if m.screen == organizationsScreen {
				m.screen = projectsScreen
			} else {
				m.screen = dashboardScreen
			}
			m.reset()
			return m.load()
		}
	}
	return m, nil
}
func (m Model) itemCount() int {
	if m.screen == billingScreen {
		return len(m.bills)
	}
	if m.screen == runtimeScreen {
		if m.runtime == nil {
			return 0
		}
		return len(runtimeLines(*m.runtime))
	}
	if m.screen == organizationsScreen {
		return len(m.organizations)
	}
	if m.screen == projectsScreen {
		return len(m.projects)
	}
	if m.screen == requestsScreen {
		return len(m.records)
	}
	if m.screen == requestDetailScreen {
		if m.record != nil {
			return 8 + len(attemptLines(*m.record))
		}
		return 0
	}
	return len(m.keys)
}
func (m Model) accent(value string) string {
	if m.noColor {
		return value
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#66a983")).Render(value)
}
func (m Model) View() tea.View {
	prefix := []string{m.accent("序衡 · AI 治理网关"), "只读终端 / " + SafeText(m.config.Server)}
	if m.busy {
		prefix = append(prefix, "正在加载…（Esc 返回，q 退出）")
	}
	if m.err != nil {
		prefix = append(prefix, "请求失败："+SafeText(m.err.Error()))
	}
	if m.stale {
		prefix = append(prefix, "数据可能已过期；按 r 重试")
	}
	if !m.updated.IsZero() {
		prefix = append(prefix, "更新时间 "+m.updated.UTC().Format("15:04:05 UTC"))
	}
	prefix = append(prefix, "")
	var rows []string
	switch m.screen {
	case billingScreen:
		prefix = append(prefix, "项目标准账单（只读，财务审核使用 Web）")
		for _, record := range m.bills {
			rows = append(rows, billingLine(record))
		}
	case runtimeScreen:
		prefix = append(prefix, "运行状态 / "+SafeText(m.config.Project))
		if m.runtime != nil {
			rows = runtimeLines(*m.runtime)
		}
	case organizationsScreen:
		prefix = append(prefix, "选择组织")
		for i, o := range m.organizations {
			marker := "  "
			if i == m.position {
				marker = "› "
			}
			rows = append(rows, marker+SafeText(o.Name)+"  ["+SafeText(string(o.Role))+"]")
		}
	case projectsScreen:
		prefix = append(prefix, "组织："+SafeText(m.config.Organization), "选择项目")
		for i, p := range m.projects {
			marker := "  "
			if i == m.position {
				marker = "› "
			}
			rows = append(rows, marker+SafeText(p.Name)+"  ["+SafeText(string(p.Role))+"]")
		}
	case requestsScreen:
		prefix = append(prefix, "调用账本 / "+SafeText(m.config.Project), "结算状态不代表客户端收到响应")
		for i, r := range m.records {
			marker := "  "
			if i == m.position {
				marker = "› "
			}
			outcome := "未记录"
			if r.Attempt != nil {
				outcome = executionOutcome(string(r.Attempt.Outcome)) + " " + SafeText(r.Attempt.Model)
			}
			rows = append(rows, marker+SafeText(r.RequestId)+"  "+outcome+"  "+requestState(string(r.State))+"  "+actualAmount(r.Actual)+" / "+SafeText(r.Currency))
		}
	case requestDetailScreen:
		prefix = append(prefix, "调用账本详情", "结算状态不代表客户端收到响应")
		if r := m.record; r != nil {
			rows = []string{"请求编号：" + SafeText(r.RequestId), "账本状态：" + requestState(string(r.State)), "周期：" + SafeText(r.Period), "单位：" + SafeText(r.Currency), "价格版本：" + SafeText(r.PriceVersion), fmt.Sprintf("预留：%d / 实际：%s", r.Reserved, actualAmount(r.Actual)), "创建：" + r.CreatedAt.UTC().Format(time.RFC3339), "更新：" + r.UpdatedAt.UTC().Format(time.RFC3339)}
			rows = append(rows, attemptLines(*r)...)
		}
	case dashboardScreen:
		name := m.config.Project
		if m.details != nil {
			name = m.details.Name
		}
		prefix = append(prefix, "项目："+SafeText(name))
		if m.details != nil {
			prefix = append(prefix, "授权模型："+SafeText(strings.Join(m.details.Models, " · ")))
		}
		if m.usage != nil {
			u := m.usage
			prefix = append(prefix, fmt.Sprintf("周期 %s / %s", SafeText(u.Period), SafeText(string(u.Currency))), fmt.Sprintf("预算 %d  已用 %d  预留 %d", u.LimitAmount, u.Spent, u.Held), fmt.Sprintf("可用 %d", u.LimitAmount-u.Spent-u.Held), "", "访问密钥（只显示元数据）")
		}
		for i, key := range m.keys {
			state := "有效"
			if key.RevokedAt != nil {
				state = "已撤销"
			} else if !key.ExpiresAt.After(time.Now()) {
				state = "已过期"
			}
			marker := "  "
			if i == m.position {
				marker = "› "
			}
			rows = append(rows, marker+SafeText(key.Name)+"  "+SafeText(key.Prefix)+"…  "+state)
		}
	}
	if len(rows) == 0 && !m.busy && m.err == nil {
		rows = append(rows, "暂无可显示的数据")
	}
	help := "↑↓选择 Enter打开 Esc返回 r刷新 n/p翻页 q退出"
	if m.screen == dashboardScreen {
		help = "b账单 s运行状态 t调用账本  " + help
	}
	if m.screen == requestDetailScreen || m.screen == runtimeScreen {
		help = "↑↓滚动 Esc返回 r刷新 q退出"
	}
	if m.screen == billingScreen {
		help = "↑↓滚动 Esc返回 r刷新 n/p翻页 q退出"
	}
	if m.width < 65 && m.screen == dashboardScreen {
		help = "b账单 s状态 t账本 Esc返回 r刷新 q退出"
	}
	if m.width < 30 {
		help = "r刷新 q退出"
		if m.screen == dashboardScreen {
			help = "t账本 " + help
		}
	}
	footer := []string{"", help}
	capacity := max(0, m.height-len(footer))
	if len(prefix) > capacity {
		prefix = prefix[:capacity]
	}
	visible := max(0, capacity-len(prefix))
	start := max(0, m.position-visible+1)
	start = min(start, len(rows))
	lines := append(prefix, rows[start:min(start+visible, len(rows))]...)
	lines = append(lines, footer...)
	if len(lines) > m.height {
		lines = lines[len(lines)-m.height:]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "…")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

func RunModel(ctx context.Context, model Model, in io.Reader, out io.Writer) error {
	defer func() {
		if model.cancel != nil {
			model.cancel()
		}
		if model.stop != nil {
			model.stop()
		}
	}()
	_, err := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("终端界面未能正常结束")
	}
	return nil
}
