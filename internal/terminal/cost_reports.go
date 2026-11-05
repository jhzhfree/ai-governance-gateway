package terminal

import (
	"context"
	"fmt"
	"net/url"
	"time"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
)

func costDates(from, to string) error {
	start, e1 := time.Parse("2006-01-02", from)
	end, e2 := time.Parse("2006-01-02", to)
	if e1 != nil || e2 != nil || start.Year() < 1 || end.Year() < 1 || !end.After(start) || end.Sub(start) > 90*24*time.Hour {
		return input("日期须为 YYYY-MM-DD，结束日期不含，范围为 1–90 天")
	}
	return nil
}
func (c *Client) CostReport(ctx context.Context, org, project, from, to string) (v types.CostReport, err error) {
	return c.CostReportForDepartment(ctx, org, project, from, to, "", "")
}
func (c *Client) CostReportForDepartment(ctx context.Context, org, project, from, to, department, state string) (v types.CostReport, err error) {
	if len(department) > 80 || (state != "" && state != "assigned" && state != "unassigned" && state != "historical") || (department != "" && state != "" && state != "assigned") {
		return v, input("部门筛选无效；部门 ID 只能与已分配状态组合")
	}
	if err = costDates(from, to); err != nil {
		return
	}
	if org == "" {
		return v, input("组织 ID 无效")
	}
	path := "/api/v1/organizations/" + url.PathEscape(org)
	if project != "" {
		path, err = projectPath(org, project)
		if err != nil {
			return
		}
	}
	query := url.Values{"from": {from}, "to": {to}}
	if department != "" {
		query.Set("department_id", department)
	}
	if state != "" {
		query.Set("department_state", state)
	}
	err = c.get(ctx, path+"/cost-report", query, &v)
	return
}
func reportLines(v types.CostReport) []string {
	rows := []string{"UTC 入账日期 " + SafeText(v.From) + " 至 " + SafeText(v.To) + "（不含）；观察 " + v.ObservedAt.UTC().Format(time.RFC3339), "最小账本单位；已知 token 仅完整 usage；晚结算会更新历史，非官方账单"}
	for _, r := range v.Rows {
		model, provider := "未知归属", "未知归属"
		if r.Model != nil {
			model = SafeText(*r.Model)
		}
		if r.Provider != nil {
			provider = SafeText(*r.Provider)
		}
		department := "历史未记录"
		if r.DepartmentState == "unassigned" {
			department = "入账时未分配"
		}
		if r.DepartmentState == "assigned" {
			department = ""
			for _, d := range r.DepartmentPath {
				if department != "" {
					department += " / "
				}
				department += SafeText(d.Name) + "(" + SafeText(d.Id) + ")"
			}
		}
		rows = append(rows, "部门 "+department)
		rows = append(rows, fmt.Sprintf("%s %s %s/%s [%s] 请求%d 已结算%s 估算%s 占额%s 已释放%d 用量覆盖%d/%d 输入%s 输出%s", SafeText(r.Day), SafeText(r.ProjectId), model, provider, SafeText(r.Currency), r.Admitted, SafeText(r.SettledAmount), SafeText(r.EstimatedAmount), SafeText(r.OpenReserved), r.Released, r.UsageKnown, r.Admitted, SafeText(r.InputTokens), SafeText(r.OutputTokens)))
	}
	if len(v.Rows) == 0 {
		rows = append(rows, "所选时间范围没有账本记录")
	}
	return rows
}
func (a *app) addCostReports() {
	var org, project, from, to, department, state string
	var organization bool
	cmd := &cobra.Command{Use: "cost-report", Short: "只读 UTC 成本报表（已结算、估算、占额分列）", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := costDates(from, to); err != nil {
			return err
		}
		if organization && cmd.Flags().Changed("project") {
			return input("--organization 不能与 --project 同用")
		}
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, org, project)
		if organization {
			p = ""
		}
		v, err := c.CostReportForDepartment(cmd.Context(), o, p, from, to, department, state)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, line := range reportLines(v) {
			rows = append(rows, []string{line})
		}
		return a.emit(v, []string{"成本报表"}, rows, "")
	}}
	cmd.Flags().StringVar(&org, "org", "", "组织 ID")
	cmd.Flags().StringVar(&project, "project", "", "项目 ID")
	cmd.Flags().BoolVar(&organization, "organization", false, "管理员查询整个组织，忽略保存的项目")
	cmd.Flags().StringVar(&from, "from", "", "起始 UTC 日期，含")
	cmd.Flags().StringVar(&to, "to", "", "结束 UTC 日期，不含")
	cmd.Flags().StringVar(&department, "department-id", "", "按入账时部门或祖先 ID 筛选")
	cmd.Flags().StringVar(&state, "department-state", "", "assigned / unassigned / historical")
	a.root.AddCommand(cmd)
}
