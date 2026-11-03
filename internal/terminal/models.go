package terminal

import (
	"context"
	"fmt"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/spf13/cobra"
)

func (c *Client) Models(ctx context.Context) (v types.ModelPage, err error) {
	err = c.get(ctx, "/api/v1/models", nil, &v)
	return
}
func (a *app) addModels() {
	group := &cobra.Command{Use: "models", Short: "查看已启用的模型、区域及计价配置", Args: noArgs}
	group.AddCommand(&cobra.Command{Use: "list", Short: "列出不含供应商凭证的模型目录", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, client, err := a.client()
		if err != nil {
			return err
		}
		page, err := client.Models(cmd.Context())
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, m := range page.Data {
			rows = append(rows, []string{m.Id, string(m.Provider), string(m.Region), string(m.Currency), fmt.Sprint(m.MaxInputTokens), fmt.Sprint(m.MaxOutputTokens)})
		}
		return a.emit(page, []string{"模型", "供应商", "配置区域", "单位", "输入上界", "输出上界"}, rows, "")
	}})
	a.root.AddCommand(group)
}
