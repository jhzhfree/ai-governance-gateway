package terminal

import (
	"context"
	"fmt"
	"net/url"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
)

func (c *Client) BudgetAlerts(ctx context.Context, org, project, after string) (v types.BudgetAlertPage, err error) {
	if org == "" {
		return v, input("请先选择组织")
	}
	if len(after) > 32 {
		return v, input("告警游标过长")
	}
	path := "/api/v1/organizations/" + url.PathEscape(org)
	if project != "" {
		path, err = projectPath(org, project)
		if err != nil {
			return
		}
	}
	err = c.get(ctx, path+"/budget/alerts", url.Values{"after": {after}}, &v)
	return
}
func alertLine(v types.BudgetAlert) string {
	state := "待确认"
	if v.AcknowledgedAt != nil {
		state = "已确认"
	}
	return fmt.Sprintf("%d %s [%s] %d%% 预算%s 已用%s 占额%s %s", v.Id, SafeText(v.Period), SafeText(v.Currency), v.Threshold, SafeText(v.Limit), SafeText(v.Spent), SafeText(v.Held), state)
}
func (a *app) addBudgetAlerts() {
	var org, project, after string
	var organization bool
	cmd := &cobra.Command{Use: "alerts", Short: "只读预算阈值告警，确认处理使用 Web", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
		v, err := c.BudgetAlerts(cmd.Context(), o, p, after)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, v := range v.Data {
			rows = append(rows, []string{alertLine(v)})
		}
		return a.emit(v, []string{"预算告警（触发时快照，确认不释放预算）"}, rows, v.NextCursor)
	}}
	cmd.Flags().StringVar(&org, "org", "", "组织 ID")
	cmd.Flags().StringVar(&project, "project", "", "项目 ID")
	cmd.Flags().BoolVar(&organization, "organization", false, "管理员查询组织预算告警")
	cmd.Flags().StringVar(&after, "after", "", "上一页 next_cursor")
	a.root.AddCommand(cmd)
}
