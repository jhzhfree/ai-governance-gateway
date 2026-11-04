package billing

import (
	"strings"
	"testing"
)

const header = "record_id,request_id,period,currency,amount,input_tokens,output_tokens\n"

func TestNormalizedCSVBoundsAndExactAmounts(t *testing.T) {
	rows, err := Parse(header + "record-1,,2026-11,CNY_MICRO,9007199254740991,2,0\n")
	if err != nil || len(rows) != 1 || rows[0].Amount != 9007199254740991 {
		t.Fatal("exact amount lost")
	}
	for _, row := range []string{"=formula,r,2026-11,CNY_MICRO,1,1,1", "a,r,2026-13,CNY_MICRO,1,1,1", "a,r,2026-11,MOCK,1,1,1", "a,r,2026-11,CNY_MICRO,1.5,1,1", "a,r,2026-11,CNY_MICRO,-1,1,1", "a,r,2026-11,CNY_MICRO,01,1,1", "a,r,2026-11,CNY_MICRO,9007199254740992,1,1", "a,r,2026-11,CNY_MICRO,0,2000001,1"} {
		if _, err := Parse(header + row + "\n"); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
	if _, err := Parse(header + strings.Repeat("same,r,2026-11,CNY_MICRO,1,1,1\n", 2)); err == nil {
		t.Fatal("duplicate row accepted")
	}
	if _, err := Parse(header); err == nil {
		t.Fatal("empty statement accepted")
	}
	if _, err := Parse(strings.Repeat("x", MaxCSV+1)); err == nil {
		t.Fatal("oversized statement accepted")
	}
}
