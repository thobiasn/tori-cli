package agent

import "testing"

func TestParseCondition(t *testing.T) {
	tests := []struct {
		input   string
		scope   string
		field   string
		op      string
		numVal  float64
		strVal  string
		isStr   bool
		wantErr bool
	}{
		{"host.cpu_percent > 90", "host", "cpu_percent", ">", 90, "", false, false},
		{"host.memory_percent >= 85.5", "host", "memory_percent", ">=", 85.5, "", false, false},
		{"host.disk_percent > 90", "host", "disk_percent", ">", 90, "", false, false},
		{"container.state == 'exited'", "container", "state", "==", 0, "exited", true, false},
		{"container.cpu_percent > 80", "container", "cpu_percent", ">", 80, "", false, false},
		{"container.state != 'running'", "container", "state", "!=", 0, "running", true, false},
		{"host.load1 > 4", "host", "load1", ">", 4, "", false, false},
		{"host.load5 >= 2.5", "host", "load5", ">=", 2.5, "", false, false},
		{"host.load15 < 1", "host", "load15", "<", 1, "", false, false},
		{"host.swap_percent > 80", "host", "swap_percent", ">", 80, "", false, false},
		{"container.health == 'unhealthy'", "container", "health", "==", 0, "unhealthy", true, false},
		{"container.restart_count > 5", "container", "restart_count", ">", 5, "", false, false},
		{"container.exit_code != 0", "container", "exit_code", "!=", 0, "", false, false},

		// Invalid cases.
		{"", "", "", "", 0, "", false, true},
		{"host.cpu_percent", "", "", "", 0, "", false, true},            // too few tokens
		{"host.cpu_percent > 90 extra", "", "", "", 0, "", false, true}, // too many tokens
		{"cpu_percent > 90", "", "", "", 0, "", false, true},            // no scope.field
		{"host.cpu_percent ~ 90", "", "", "", 0, "", false, true},       // bad operator
		{"host.cpu_percent > abc", "", "", "", 0, "", false, true},      // bad numeric
		{"unknown.cpu_percent > 90", "", "", "", 0, "", false, true},    // bad scope
		{"host.unknown_field > 2", "", "", "", 0, "", false, true},      // unknown field
		{"container.image == 'nginx'", "", "", "", 0, "", false, true},  // unknown field
		{"container.state > 'exited'", "", "", "", 0, "", false, true},  // string field with > op
		{"container.state >= 'a'", "", "", "", 0, "", false, true},      // string field with >= op
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			c, err := parseCondition(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.Scope != tt.scope {
				t.Errorf("scope = %q, want %q", c.Scope, tt.scope)
			}
			if c.Field != tt.field {
				t.Errorf("field = %q, want %q", c.Field, tt.field)
			}
			if c.Op != tt.op {
				t.Errorf("op = %q, want %q", c.Op, tt.op)
			}
			if c.IsStr != tt.isStr {
				t.Errorf("isStr = %v, want %v", c.IsStr, tt.isStr)
			}
			if c.IsStr && c.StrVal != tt.strVal {
				t.Errorf("strVal = %q, want %q", c.StrVal, tt.strVal)
			}
			if !c.IsStr && c.NumVal != tt.numVal {
				t.Errorf("numVal = %f, want %f", c.NumVal, tt.numVal)
			}
		})
	}
}

func TestCompareNum(t *testing.T) {
	tests := []struct {
		actual    float64
		op        string
		threshold float64
		want      bool
	}{
		{91, ">", 90, true},
		{90, ">", 90, false},
		{89, ">", 90, false},
		{89, "<", 90, true},
		{90, "<", 90, false},
		{90, ">=", 90, true},
		{90, "<=", 90, true},
		{90, "==", 90, true},
		{91, "==", 90, false},
		{91, "!=", 90, true},
		{90, "!=", 90, false},
	}

	for _, tt := range tests {
		got := compareNum(tt.actual, tt.op, tt.threshold)
		if got != tt.want {
			t.Errorf("compareNum(%f, %q, %f) = %v, want %v", tt.actual, tt.op, tt.threshold, got, tt.want)
		}
	}
}

func TestCompareStr(t *testing.T) {
	tests := []struct {
		actual, op, expected string
		want                 bool
	}{
		{"exited", "==", "exited", true},
		{"running", "==", "exited", false},
		{"exited", "!=", "running", true},
		{"running", "!=", "running", false},
	}

	for _, tt := range tests {
		got := compareStr(tt.actual, tt.op, tt.expected)
		if got != tt.want {
			t.Errorf("compareStr(%q, %q, %q) = %v, want %v", tt.actual, tt.op, tt.expected, got, tt.want)
		}
	}
}
