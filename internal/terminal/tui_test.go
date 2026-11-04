package terminal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
)

type fakeAPI struct {
	organizations func(context.Context, string, int) (types.OrganizationPage, error)
	projects      func(context.Context, string, string, int) (types.ProjectPage, error)
	usage         func(context.Context, string, string) (types.Usage, error)
}

func (f fakeAPI) Organizations(ctx context.Context, cursor string, limit int) (types.OrganizationPage, error) {
	if f.organizations != nil {
		return f.organizations(ctx, cursor, limit)
	}
	return types.OrganizationPage{Data: []types.Organization{{Id: "org-a", Name: "Alpha"}, {Id: "org-b", Name: "Beta"}}}, nil
}
func (f fakeAPI) Projects(ctx context.Context, org, cursor string, limit int) (types.ProjectPage, error) {
	if f.projects != nil {
		return f.projects(ctx, org, cursor, limit)
	}
	return types.ProjectPage{Data: []types.Project{{Id: "project-a", Name: "App"}}}, nil
}
func (f fakeAPI) Project(context.Context, string, string) (types.ProjectDetails, error) {
	return types.ProjectDetails{Name: "Test project"}, nil
}
func (f fakeAPI) Usage(ctx context.Context, org, project string) (types.Usage, error) {
	if f.usage != nil {
		return f.usage(ctx, org, project)
	}
	return types.Usage{Period: "2026-11", Currency: "MOCK", LimitAmount: 100, Spent: 7, Held: 3}, nil
}
func (f fakeAPI) Keys(context.Context, string, string, string, int) (types.KeyPage, error) {
	return types.KeyPage{Data: []types.Key{{Id: "key-1", Name: "Metadata only", Prefix: "aigw_pk_TEST"}}}, nil
}
func (f fakeAPI) Requests(context.Context, string, string, string, string, int) (types.RequestPage, error) {
	return types.RequestPage{Data: []types.RequestRecord{{RequestId: "request-1", State: types.RequestRecordStateUncertain, Reserved: 10, Currency: "MOCK"}}}, nil
}
func (f fakeAPI) Request(context.Context, string, string, string) (types.RequestRecord, error) {
	return types.RequestRecord{RequestId: "request-1", State: types.RequestRecordStateUncertain, Reserved: 10, Currency: "MOCK", PriceVersion: "mock-v1"}, nil
}
func keyPress(code rune) tea.KeyPressMsg {
	if code < 256 {
		return tea.KeyPressMsg{Code: code, Text: string(code)}
	}
	return tea.KeyPressMsg{Code: code}
}
func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	v, ok := next.(Model)
	if !ok {
		t.Fatal("unexpected model")
	}
	return v, cmd
}
func complete(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected data command")
	}
	next, _ := update(t, m, cmd())
	return next
}

func TestTUIContextNavigationAndPagination(t *testing.T) {
	var saved [][2]string
	api := fakeAPI{projects: func(ctx context.Context, org, cursor string, _ int) (types.ProjectPage, error) {
		if ctx.Err() != nil {
			return types.ProjectPage{}, ctx.Err()
		}
		if org != "org-b" {
			t.Error("wrong organization scope")
		}
		if cursor == "next" {
			return types.ProjectPage{Data: []types.Project{{Id: "project-b", Name: "Second"}}}, nil
		}
		return types.ProjectPage{Data: []types.Project{{Id: "project-a", Name: "First"}}, NextCursor: "next"}, nil
	}}
	m := NewModel(context.Background(), api, testConfig(), func(org, project string) error { saved = append(saved, [2]string{org, project}); return nil }, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = complete(t, m, cmd)
	if m.screen != projectsScreen || m.config.Organization != "org-b" {
		t.Fatal("organization selection failed")
	}
	m, cmd = update(t, m, keyPress('n'))
	m = complete(t, m, cmd)
	if m.projects[0].Id != "project-b" {
		t.Fatal("next page failed")
	}
	m, cmd = update(t, m, keyPress('p'))
	m = complete(t, m, cmd)
	if m.projects[0].Id != "project-a" {
		t.Fatal("previous page failed")
	}
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = complete(t, m, cmd)
	if m.screen != dashboardScreen || m.config.Project != "project-a" || len(saved) != 2 || saved[1] != [2]string{"org-b", "project-a"} {
		t.Fatal("project context did not persist")
	}
	view := m.View()
	if !view.AltScreen || !strings.Contains(view.Content, "已用 7") || !strings.Contains(view.Content, "Metadata only") || strings.Contains(view.Content, "\x1b") {
		t.Fatal("dashboard/NO_COLOR rendering incorrect")
	}
}
func TestTUIIgnoresLateResponsesAndCancelsOldRequest(t *testing.T) {
	api := fakeAPI{organizations: func(ctx context.Context, _ string, _ int) (types.OrganizationPage, error) {
		name := "Current"
		if ctx.Err() != nil {
			name = "Stale"
		}
		return types.OrganizationPage{Data: []types.Organization{{Id: name, Name: name}}}, nil
	}}
	m := NewModel(context.Background(), api, testConfig(), nil, true)
	defer m.stop()
	old := m.Init()
	m, latest := update(t, m, keyPress('r'))
	m = complete(t, m, latest)
	oldResult := old().(dataMessage)
	if oldResult.organizations.Data[0].Name != "Stale" {
		t.Fatal("refresh did not cancel initial request")
	}
	m, _ = update(t, m, oldResult)
	if m.organizations[0].Name != "Current" || m.busy {
		t.Fatal("late response replaced current state")
	}
}
func TestTUIFailureKeepsLastDataAndFitsSmallWindow(t *testing.T) {
	fail := false
	api := fakeAPI{usage: func(context.Context, string, string) (types.Usage, error) {
		if fail {
			return types.Usage{}, errors.New("backend unavailable")
		}
		return types.Usage{LimitAmount: 100, Spent: 7}, nil
	}}
	cfg := testConfig()
	cfg.Organization = "org"
	cfg.Project = "project"
	m := NewModel(context.Background(), api, cfg, nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	fail = true
	m, cmd := update(t, m, keyPress('r'))
	m = complete(t, m, cmd)
	if !m.stale || m.usage == nil || m.usage.Spent != 7 {
		t.Fatal("refresh failure discarded last data")
	}
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 24, Height: 6})
	content := m.View().Content
	if !strings.Contains(content, "过期") || !strings.Contains(content, "q退出") {
		t.Fatal("short-window stale state/help missing")
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 6 {
		t.Fatal("view exceeds terminal height")
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 24 {
			t.Fatal("view exceeds terminal width")
		}
	}
}
func TestTUIListScrollAndUnsafeText(t *testing.T) {
	api := fakeAPI{organizations: func(context.Context, string, int) (types.OrganizationPage, error) {
		page := types.OrganizationPage{}
		for i := 0; i < 20; i++ {
			page.Data = append(page.Data, types.Organization{Id: fmt.Sprint(i), Name: fmt.Sprintf("Row %02d\x1b[2J\u202e", i)})
		}
		return page, nil
	}}
	m := NewModel(context.Background(), api, testConfig(), nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 19; i++ {
		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	content := m.View().Content
	if !strings.Contains(content, "Row 19") || strings.ContainsAny(content, "\x1b\u202e") {
		t.Fatal("focused row hidden or unsafe terminal content")
	}
}
func TestTUISaveFailureDoesNotSwitchScope(t *testing.T) {
	m := NewModel(context.Background(), fakeAPI{}, testConfig(), func(string, string) error { return errors.New("configuration read-only") }, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	m, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || m.screen != organizationsScreen || m.config.Organization != "" || m.err == nil {
		t.Fatal("failed context save silently switched scope")
	}
}

func TestTUIRequestLedgerNavigation(t *testing.T) {
	cfg := testConfig()
	cfg.Organization = "org"
	cfg.Project = "project"
	m := NewModel(context.Background(), fakeAPI{}, cfg, nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	m, cmd := update(t, m, keyPress('t'))
	m = complete(t, m, cmd)
	if m.screen != requestsScreen || !strings.Contains(m.View().Content, "待核对") {
		t.Fatal("request ledger not displayed")
	}
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = complete(t, m, cmd)
	if m.screen != requestDetailScreen || !strings.Contains(m.View().Content, "mock-v1") || !strings.Contains(m.View().Content, "待结算") {
		t.Fatal("request detail invalid")
	}
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = complete(t, m, cmd)
	if m.screen != requestsScreen {
		t.Fatal("detail back did not restore ledger")
	}
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = complete(t, m, cmd)
	if m.screen != dashboardScreen {
		t.Fatal("ledger back did not restore dashboard")
	}
}

func (fakeAPI) Runtime(context.Context, string, string) (types.Runtime, error) {
	return types.Runtime{HeldAmount: "0", Models: []types.RouteStatus{{Model: "aliyun/test", Provider: "aliyun", State: "open", RetryAfter: 30}}}, nil
}

func (fakeAPI) BillingRecords(context.Context, string, string, string, string, int) (types.BillingPage, error) {
	return types.BillingPage{Data: []types.BillingRecord{{RecordId: "bill-test", Provider: "aliyun", Currency: "CNY_MICRO", Amount: "10", Decision: "pending"}}}, nil
}

func (fakeAPI) CostReport(context.Context, string, string, string, string) (types.CostReport, error) {
	return types.CostReport{}, nil
}
