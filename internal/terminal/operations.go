package terminal

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
)

func (c *Client) Limits(ctx context.Context, org, project string) (v types.ProjectLimits, err error) {
	path, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	err = c.get(ctx, path+"/limits", nil, &v)
	return
}
func (c *Client) Inspect(ctx context.Context, org, project, after string, older time.Duration, limit int) (v types.Inspection, err error) {
	path, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	if len(after) > 512 || older < time.Second || older > 30*24*time.Hour || older%time.Second != 0 || limit < 1 || limit > 100 {
		return v, input("核查参数无效：时间须为 1 秒至 30 天的整数秒，limit 为 1–100")
	}
	err = c.get(ctx, path+"/inspection", url.Values{"after": {after}, "older_than_seconds": {fmt.Sprint(int64(older / time.Second))}, "limit": {fmt.Sprint(limit)}}, &v)
	return
}
func reasonText(reasons []types.InspectionItemReasons) string {
	values := make([]string, len(reasons))
	for i, r := range reasons {
		values[i] = string(r)
	}
	return strings.Join(values, ",")
}
func (a *app) addOperations() {
	var org, project string
	limits := &cobra.Command{Use: "limits", Short: "项目流量策略与租约查询", Args: noArgs}
	show := &cobra.Command{Use: "show", Short: "查看限流策略及有效租约", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, org, project)
		v, err := c.Limits(cmd.Context(), o, p)
		if err != nil {
			return err
		}
		return a.emit(v, []string{"滚动60秒上限", "并发上限", "策略版本", "有效租约", "窗口请求数"}, [][]string{{fmt.Sprint(v.RequestsPerMinute), fmt.Sprint(v.MaxConcurrent), fmt.Sprint(v.Revision), fmt.Sprint(v.ActiveRequests), fmt.Sprint(v.RecentRequests)}}, "")
	}}
	show.Flags().StringVar(&org, "org", "", "组织 ID")
	show.Flags().StringVar(&project, "project", "", "项目 ID")
	limits.AddCommand(show)
	a.root.AddCommand(limits)
	var inspectOrg, inspectProject, after string
	var older time.Duration
	var limit int
	recovery := &cobra.Command{Use: "recovery", Short: "只读故障核查，不退款、不重放", Args: noArgs}
	inspect := &cobra.Command{Use: "inspect", Short: "核查未结束执行、预算占额与账本差异", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, inspectOrg, inspectProject)
		v, err := c.Inspect(cmd.Context(), o, p, after, older, limit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, account := range v.Accounts {
			rows = append(rows, []string{"账户", string(account.Scope) + "/" + account.Period + "/" + account.Currency, fmt.Sprintf("占额 %s/%s；已用 %s/%s；一致=%t", account.StoredHeld, account.ComputedHeld, account.StoredSpent, account.ComputedSpent, account.Consistent)})
		}
		for _, item := range v.Data {
			rows = append(rows, []string{"请求", item.Request.RequestId, fmt.Sprintf("%s；有效租约=%t", reasonText(item.Reasons), item.LeaseActive)})
		}
		if a.output != "json" {
			fmt.Fprintln(a.options.Err, "核查为只读快照；租约到期不代表供应商停止或费用为零。")
			if v.AccountsTruncated {
				fmt.Fprintln(a.options.Err, "账户超过 100 个，当前账户结果已截断。")
			}
		}
		return a.emit(v, []string{"类型", "对象", "核查结果（存储值/重算值）"}, rows, v.NextCursor)
	}}
	inspect.Flags().StringVar(&inspectOrg, "org", "", "组织 ID")
	inspect.Flags().StringVar(&inspectProject, "project", "", "项目 ID")
	inspect.Flags().StringVar(&after, "after", "", "分页游标")
	inspect.Flags().DurationVar(&older, "older-than", 5*time.Minute, "异常记录最小未更新时间")
	inspect.Flags().IntVar(&limit, "limit", 50, "每页请求数（1–100）")
	recovery.AddCommand(inspect)
	a.root.AddCommand(recovery)
}
