package terminal

import (
	"context"
	"fmt"
	"net/url"
	"time"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
)

func validRequestState(state string) bool {
	switch state {
	case "", "reserved", "dispatched", "uncertain", "settled", "released", "estimated_final":
		return true
	}
	return false
}
func (c *Client) Requests(ctx context.Context, org, project, after, state string, limit int) (v types.RequestPage, err error) {
	return c.RequestsFiltered(ctx, org, project, after, state, "", limit)
}
func (c *Client) RequestsFiltered(ctx context.Context, org, project, after, state, outcome string, limit int) (v types.RequestPage, err error) {
	path, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	if len(after) > 512 || !validRequestState(state) || !validExecutionOutcome(outcome) || limit < 1 || limit > 100 {
		return v, input("调用记录分页或状态参数无效")
	}
	q := url.Values{"after": {after}, "state": {state}, "outcome": {outcome}, "limit": {fmt.Sprint(limit)}}
	err = c.get(ctx, path+"/requests", q, &v)
	return
}
func (c *Client) Request(ctx context.Context, org, project, id string) (v types.RequestRecord, err error) {
	path, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	if id == "" || len(id) > 256 {
		return v, input("请提供有效的 request_id")
	}
	err = c.get(ctx, path+"/requests/"+url.PathEscape(id), nil, &v)
	return
}
func actualAmount(value *int64) string {
	if value == nil {
		return "待结算"
	}
	return fmt.Sprint(*value)
}
func requestState(state string) string {
	names := map[string]string{"reserved": "已预留", "dispatched": "已派发", "uncertain": "待核对", "settled": "已结算", "released": "已释放", "estimated_final": "估算结算"}
	if name, ok := names[state]; ok {
		return name
	}
	return SafeText(state)
}
func (a *app) addRequests() {
	group := &cobra.Command{Use: "requests", Short: "查询调用账本（结算状态不代表客户端收到响应）", Args: noArgs}
	var org, project, after, state, outcome string
	var limit int
	list := &cobra.Command{Use: "list", Short: "按创建时间倒序列出调用账本", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, org, project)
		v, err := client.RequestsFiltered(cmd.Context(), o, p, after, state, outcome, limit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, r := range v.Data {
			model, provider, outcome, duration := "未记录", "未记录", "未记录", "未记录"
			if r.Attempt != nil {
				model = r.Attempt.Model
				provider = string(r.Attempt.Provider)
				outcome = executionOutcome(string(r.Attempt.Outcome))
				duration = durationText(r.Attempt.DurationMs)
			}
			rows = append(rows, []string{r.RequestId, model, provider, outcome, duration, requestState(string(r.State)), r.Period, r.Currency, fmt.Sprint(r.Reserved), actualAmount(r.Actual), r.CreatedAt.Format(time.RFC3339)})
		}
		return a.emit(v, []string{"请求编号", "模型", "供应商", "执行结果", "耗时", "账本状态", "周期", "单位", "预留", "实际", "创建时间"}, rows, v.NextCursor)
	}}
	list.Flags().StringVar(&org, "org", "", "组织 ID")
	list.Flags().StringVar(&project, "project", "", "项目 ID")
	list.Flags().StringVar(&after, "after", "", "不透明分页游标")
	list.Flags().StringVar(&outcome, "outcome", "", "执行结果：unfinished/completed/failed/canceled")
	list.Flags().StringVar(&state, "state", "", "账本状态筛选")
	list.Flags().IntVar(&limit, "limit", 50, "每页数量（1–100）")
	var detailOrg, detailProject, id string
	get := &cobra.Command{Use: "get", Short: "按 request_id 查询账本详情", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, client, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, detailOrg, detailProject)
		r, err := client.Request(cmd.Context(), o, p, id)
		if err != nil {
			return err
		}
		rows := [][]string{{"请求编号", r.RequestId}, {"账本状态", requestState(string(r.State))}, {"周期", r.Period}, {"单位", r.Currency}, {"价格版本", r.PriceVersion}, {"预留", fmt.Sprint(r.Reserved)}, {"实际", actualAmount(r.Actual)}, {"创建时间", r.CreatedAt.Format(time.RFC3339)}, {"更新时间", r.UpdatedAt.Format(time.RFC3339)}}
		rows = append(rows, attemptRows(r)...)
		return a.emit(r, []string{"字段", "值"}, rows, "")
	}}
	get.Flags().StringVar(&detailOrg, "org", "", "组织 ID")
	get.Flags().StringVar(&detailProject, "project", "", "项目 ID")
	get.Flags().StringVar(&id, "request-id", "", "请求编号（必填）")
	group.AddCommand(list, get)
	a.root.AddCommand(group)
}
