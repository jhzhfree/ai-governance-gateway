package httpapi

import "testing"

func TestCostCSVFormulaProtection(t *testing.T) {
	for _, s := range []string{"=HYPERLINK(\"https://example.invalid\")", " +1", "\t@SUM(1)", "-1", "text\n=1"} {
		if csvText(s) != "'"+s {
			t.Fatal("spreadsheet formula not escaped")
		}
	}
	if csvText("aliyun/model") != "aliyun/model" || csvText("") != "" {
		t.Fatal("ordinary metadata changed")
	}
}
