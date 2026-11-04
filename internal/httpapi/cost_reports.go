package httpapi

import (
	"bytes"
	"encoding/csv"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) costReport(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if len(query) != 2 || len(query["from"]) != 1 || len(query["to"]) != 1 {
		failure(w, r, control.ErrInvalid)
		return
	}
	exporting := r.Method == http.MethodPost
	v, err := h.Store.CostReport(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), query.Get("from"), query.Get("to"), exporting)
	if err != nil {
		failure(w, r, err)
		return
	}
	if !exporting {
		send(w, 200, v)
		return
	}
	var data bytes.Buffer
	writer := csv.NewWriter(&data)
	_ = writer.Write([]string{"day_utc", "project_id", "model", "provider", "currency", "admitted", "settled_amount", "estimated_amount", "open_reserved", "released", "usage_known", "input_tokens_known_only", "output_tokens_known_only", "observed_at", "from_utc", "to_utc_exclusive", "organization_id"})
	for _, item := range v.Rows {
		model, provider := "", ""
		if item.Model != nil {
			model = *item.Model
		}
		if item.Provider != nil {
			provider = *item.Provider
		}
		_ = writer.Write([]string{item.Day, csvText(item.ProjectID), csvText(model), csvText(provider), csvText(item.Currency), strconv.FormatInt(item.Admitted, 10), item.SettledAmount, item.EstimatedAmount, item.OpenReserved, strconv.FormatInt(item.Released, 10), strconv.FormatInt(item.UsageKnown, 10), item.InputTokens, item.OutputTokens, v.ObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"), v.From, v.To, csvText(r.PathValue("org"))})
	}
	writer.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="aigw-cost-report.csv"`)
	w.Header().Set("X-AIGW-Observed-At", v.ObservedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	_, _ = w.Write(data.Bytes())
}

// Prevent spreadsheet formula evaluation after CSV parsing. Whitespace prefixes
// are checked because spreadsheet importers may trim them before interpretation.
func csvText(s string) string {
	value := strings.TrimLeft(s, " \t\r\n")
	if value != "" && strings.ContainsRune("=+-@", rune(value[0])) {
		return "'" + s
	}
	if strings.ContainsAny(s, "\t\r\n") {
		return "'" + s
	}
	return s
}
