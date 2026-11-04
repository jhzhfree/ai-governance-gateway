package terminal

import (
	"context"
	"fmt"
	"time"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
)

func (c *Client) Runtime(ctx context.Context, org, project string) (v types.Runtime, err error) {
	path, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	err = c.get(ctx, path+"/runtime", nil, &v)
	return
}
func circuitState(state string) string {
	names := map[string]string{"unobserved": "尚无记录", "not_applicable": "模拟供应商", "closed": "正常放行", "open": "暂停派发", "half_open": "单次探测中"}
	if name, ok := names[state]; ok {
		return name
	}
	return SafeText(state)
}
func runtimeLines(v types.Runtime) []string {
	rows := []string{"共享熔断状态（不代表数据驻留验收）"}
	for _, model := range v.Models {
		rows = append(rows, fmt.Sprintf("%s [%s] %s；等待 %d 秒", SafeText(model.Model), SafeText(string(model.Provider)), circuitState(string(model.State)), model.RetryAfter))
	}
	rows = append(rows, fmt.Sprintf("全周期开放占额 %d 笔 / %s 最小账本单位", v.OpenHolds, SafeText(v.HeldAmount)), fmt.Sprintf("执行未结束 %d 笔", v.UnfinishedExecutions))
	if v.Instance == nil {
		return append(rows, "当前实例 HTTP 监控不可用")
	}
	rows = append(rows, "实例 "+SafeText(v.Instance.InstanceId), "采集起点 "+v.Instance.StartedAt.UTC().Format(time.RFC3339))
	if !v.Instance.Tracked {
		return append(rows, "当前实例暂无本项目观测，或已达采集上限")
	}
	c := v.Instance.Counters
	rows = append(rows, fmt.Sprintf("HTTP 请求 %d / 429或503 %d / 5xx %d", c.Requests, c.Rejected, c.ServerErrors), fmt.Sprintf("HTTP 总耗时 %d ms；含清理，不等于供应商延迟", c.DurationMs), "计数仅当前实例，重启清零；SSE HTTP 200 不保证执行成功")
	for i, bound := range []int{100, 500, 1000, 5000, 30000, 120000} {
		if i < len(c.Buckets) {
			rows = append(rows, fmt.Sprintf("耗时 <= %d ms：%d", bound, c.Buckets[i]))
		}
	}
	return rows
}
func (a *app) addRuntime() {
	var org, project string
	group := &cobra.Command{Use: "runtime", Short: "查询供应商熔断、未结占额与当前实例 HTTP 观测", Args: noArgs}
	show := &cobra.Command{Use: "show", Short: "只读运行状态（不触发探测）", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, org, project)
		v, err := c.Runtime(cmd.Context(), o, p)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, line := range runtimeLines(v) {
			rows = append(rows, []string{line})
		}
		return a.emit(v, []string{"运行状态"}, rows, "")
	}}
	show.Flags().StringVar(&org, "org", "", "组织 ID")
	show.Flags().StringVar(&project, "project", "", "项目 ID")
	group.AddCommand(show)
	a.root.AddCommand(group)
}
