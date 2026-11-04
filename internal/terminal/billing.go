package terminal

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
)

func (c *Client) BillingRecords(ctx context.Context, org, project, after, decision string, limit int) (v types.BillingPage, err error) {
	path, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	if len(after) > 512 || limit < 1 || limit > 100 || (decision != "" && decision != "pending" && decision != "approved" && decision != "rejected") {
		return v, input("账单分页或审核状态无效")
	}
	q := url.Values{"after": {after}, "limit": {fmt.Sprint(limit)}}
	if decision != "" {
		q.Set("decision", decision)
	}
	err = c.get(ctx, path+"/billing/records", q, &v)
	return
}
func billingIssueText(issues []types.BillingRecordIssues) string {
	values := make([]string, len(issues))
	for i, issue := range issues {
		values[i] = SafeText(string(issue))
	}
	return strings.Join(values, ",")
}
func billingLine(record types.BillingRecord) string {
	return fmt.Sprintf("%s / %s：%s %s [%s] %s", SafeText(string(record.Provider)), SafeText(record.RecordId), SafeText(record.Amount), SafeText(string(record.Currency)), SafeText(string(record.Decision)), billingIssueText(record.Issues))
}
func (a *app) addBilling() {
	var org, project, after, decision string
	var limit int
	group := &cobra.Command{Use: "billing", Short: "只读项目标准账单与人工核对结果", Args: noArgs}
	list := &cobra.Command{Use: "list", Short: "查询账单差异；财务审核使用 Web", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, org, project)
		v, err := c.BillingRecords(cmd.Context(), o, p, after, decision, limit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, record := range v.Data {
			rows = append(rows, []string{record.Id, record.RecordId, record.RequestId, record.Amount, string(record.Currency), string(record.Decision), billingIssueText(record.Issues)})
		}
		return a.emit(v, []string{"ID", "供应商记录", "建议请求", "金额", "单位", "审核状态", "核对事项"}, rows, v.NextCursor)
	}}
	list.Flags().StringVar(&org, "org", "", "组织 ID")
	list.Flags().StringVar(&project, "project", "", "项目 ID")
	list.Flags().StringVar(&after, "after", "", "分页游标")
	list.Flags().StringVar(&decision, "decision", "", "审核状态 pending/approved/rejected")
	list.Flags().IntVar(&limit, "limit", 50, "每页条数（1–100）")
	group.AddCommand(list)
	a.root.AddCommand(group)
}
