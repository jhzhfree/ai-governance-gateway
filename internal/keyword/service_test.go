package keyword

import "testing"

func TestRejectsNullableAndUnsupportedRules(t *testing.T) {
	for _, pattern := range []string{"", `a*`, `\b`, `(?=a)`, `(a)\1`, `(`} {
		if _, err := Compile([]Rule{{ID: "x", Kind: "regex", Pattern: pattern, Direction: "input", Action: "block"}}); err == nil {
			t.Fatalf("invalid rule accepted: %q", pattern)
		}
	}
	if _, err := Compile([]Rule{{ID: "x", Kind: "regex", Pattern: `(?i)secret`, Direction: "input", Action: "block"}}); err != nil {
		t.Fatal(err)
	}
}
