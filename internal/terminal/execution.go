package terminal

import (
	"fmt"
	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"time"
)

func validExecutionOutcome(value string) bool {
	switch value {
	case "", "unfinished", "completed", "failed", "canceled":
		return true
	}
	return false
}
func executionOutcome(value string) string {
	switch value {
	case "unfinished":
		return "未结束"
	case "completed":
		return "执行完成"
	case "failed":
		return "执行失败"
	case "canceled":
		return "已取消"
	}
	return SafeText(value)
}
func durationText(value *int64) string {
	if value == nil {
		return "未结束"
	}
	return fmt.Sprintf("%d ms", *value)
}
func tokensText(value *int64) string {
	if value == nil {
		return "未采集"
	}
	return fmt.Sprint(*value)
}
func attemptRows(record types.RequestRecord) [][]string {
	return append(executionRows(record), completionRows(record)...)
}
func executionRows(record types.RequestRecord) [][]string {
	a := record.Attempt
	if a == nil {
		return [][]string{{"执行元数据", "未记录；不推测历史结果"}}
	}
	status := "未取得"
	if a.UpstreamStatus != nil {
		status = fmt.Sprint(*a.UpstreamStatus)
	}
	code := a.ErrorCode
	if code == "" {
		code = "无"
	}
	finished := "未结束"
	if a.FinishedAt != nil {
		finished = a.FinishedAt.Format(time.RFC3339)
	}
	mode := "JSON"
	if a.Stream {
		mode = "SSE"
	}
	return [][]string{{"Attempt ID", a.AttemptId}, {"模型", a.Model}, {"供应商 / 区域", string(a.Provider) + " / " + string(a.Region)}, {"执行结果", executionOutcome(string(a.Outcome))}, {"错误码", code}, {"上游 HTTP", status}, {"输入 / 输出 token", tokensText(a.InputTokens) + " / " + tokensText(a.OutputTokens)}, {"执行耗时", durationText(a.DurationMs)}, {"响应方式", mode}, {"执行开始", a.StartedAt.Format(time.RFC3339)}, {"执行结束", finished}}
}

func completionRows(record types.RequestRecord) [][]string {
	e := record.EmbeddedCompletion
	if e == nil {
		return nil
	}
	state := map[types.EmbeddedCompletionState]string{types.AwaitingUsage: "未取得完成证据", types.Recorded: "完成证据已记录", types.Receipted: "已生成结算回执"}[e.State]
	if state == "" {
		state = "未知证据状态"
	}
	amount := "尚无结算回执"
	if e.Actual != nil {
		amount = fmt.Sprint(*e.Actual) + " " + record.Currency
	}
	return [][]string{{"供应商完成证据", state}, {"记录输入 / 输出 Token", tokensText(e.InputTokens) + " / " + tokensText(e.OutputTokens)}, {"冻结输入 / 输出费率（原始单位/百万 Token）", fmt.Sprintf("%d / %d", e.InputPerMillion, e.OutputPerMillion)}, {"回执金额", amount}, {"证据说明", "结算回执不代表客户端成功，也不改变原执行或审计结果"}}
}
func attemptLines(record types.RequestRecord) []string {
	lines := []string{}
	for _, row := range attemptRows(record) {
		lines = append(lines, SafeText(row[0])+"："+SafeText(row[1]))
	}
	return lines
}
