package httpapi

import "testing"

func TestParsePercent(t *testing.T) {
	cases := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{"0.7%", 0.7, true},
		{"12.34%", 12.34, true},
		{"100.00%", 100, true},
		{" 3.5% ", 3.5, true},
		{"42", 42, true},
		{"0%", 0, true},
		{"-", 0, false},
		{"--", 0, false},
		{"", 0, false},
		{"n/a%", 0, false},
		{"-5%", 0, false},
		{"NaN%", 0, false},
		{"Inf%", 0, false},
		{"+Inf%", 0, false},
	}
	for _, tc := range cases {
		got, ok := parsePercent(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parsePercent(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestToContainerJSONNumbers(t *testing.T) {
	c := toContainerJSON(ContainerSnapshot{CPUPerc: "7.5%", MemPerc: "-"})
	if c.CPUPct == nil || *c.CPUPct != 7.5 {
		t.Fatalf("CPUPct = %v; want 7.5", c.CPUPct)
	}
	if c.MemPct != nil {
		t.Fatalf("MemPct = %v; want nil for unparseable input", *c.MemPct)
	}
	if c.CPUPerc != "7.5%" {
		t.Fatalf("raw string lost: %q", c.CPUPerc)
	}
}
