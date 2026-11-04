package terminal

import (
	"context"
	"fmt"
	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
	"net/url"
	"time"
)

func (c *Client) ContentRecords(ctx context.Context, org, project, after string, limit int) (v types.ContentPage, err error) {
	path, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	if len(after) > 512 || limit < 1 || limit > 100 {
		return v, input("内容审计分页无效")
	}
	err = c.get(ctx, path+"/content-audit/records", url.Values{"after": {after}, "limit": {fmt.Sprint(limit)}}, &v)
	return
}
func (a *app) addContentAudit() {
	var org, project, after string
	var limit int
	group := &cobra.Command{Use: "content-audit", Short: "只读内容审计元数据；正文查看使用独立授权 Web", Args: noArgs}
	list := &cobra.Command{Use: "list", Short: "不解密、不返回正文", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, org, project)
		v, err := c.ContentRecords(cmd.Context(), o, p, after, limit)
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, r := range v.Data {
			rows = append(rows, []string{r.RequestId, r.Model, string(r.State), fmt.Sprint(r.Status), r.ExpiresAt.Format(time.RFC3339)})
		}
		return a.emit(v, []string{"请求编号", "模型", "封存状态", "HTTP", "正文到期"}, rows, v.NextCursor)
	}}
	list.Flags().StringVar(&org, "org", "", "组织 ID")
	list.Flags().StringVar(&project, "project", "", "项目 ID")
	list.Flags().StringVar(&after, "after", "", "分页游标")
	list.Flags().IntVar(&limit, "limit", 50, "每页条数（1–100）")
	group.AddCommand(list)
	a.root.AddCommand(group)
}
