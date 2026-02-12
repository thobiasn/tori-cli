package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// Known fields per scope, used for validation.
var validFields = map[string]map[string]bool{
	"host": {
		"cpu_percent":    true,
		"memory_percent": true,
		"disk_percent":   true,
		"load1":          true,
		"load5":          true,
		"load15":         true,
		"swap_percent":   true,
	},
	"container": {
		"cpu_percent":    true,
		"memory_percent": true,
		"state":          true,
		"health":         true,
		"restart_count":  true,
		"exit_code":      true,
	},
}

// String-only fields that only support == and != operators.
var stringFields = map[string]bool{
	"state":  true,
	"health": true,
}

// Condition represents a parsed alert condition like "host.cpu_percent > 90".
type Condition struct {
	Scope  string  // "host" or "container"
	Field  string  // "cpu_percent", "memory_percent", "disk_percent", "state"
	Op     string  // ">", "<", ">=", "<=", "==", "!="
	NumVal float64 // numeric threshold (when IsStr is false)
	StrVal string  // string value (when IsStr is true)
	IsStr  bool
}

func parseCondition(s string) (Condition, error) {
	tokens := strings.Fields(s)
	if len(tokens) != 3 {
		return Condition{}, fmt.Errorf("condition must be 3 tokens (got %d): %q", len(tokens), s)
	}

	parts := strings.SplitN(tokens[0], ".", 2)
	if len(parts) != 2 {
		return Condition{}, fmt.Errorf("condition target must be scope.field: %q", tokens[0])
	}

	c := Condition{
		Scope: parts[0],
		Field: parts[1],
		Op:    tokens[1],
	}

	switch c.Scope {
	case "host", "container":
	default:
		return Condition{}, fmt.Errorf("unknown scope %q (must be host or container)", c.Scope)
	}

	fields, ok := validFields[c.Scope]
	if !ok || !fields[c.Field] {
		return Condition{}, fmt.Errorf("unknown field %q for scope %q", c.Field, c.Scope)
	}

	switch c.Op {
	case ">", "<", ">=", "<=", "==", "!=":
	default:
		return Condition{}, fmt.Errorf("unknown operator %q", c.Op)
	}

	val := tokens[2]
	if strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'") && len(val) >= 2 {
		c.IsStr = true
		c.StrVal = val[1 : len(val)-1]
	} else {
		v, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return Condition{}, fmt.Errorf("invalid numeric value %q: %w", val, err)
		}
		c.NumVal = v
	}

	// String fields only support == and !=.
	if stringFields[c.Field] && c.Op != "==" && c.Op != "!=" {
		return Condition{}, fmt.Errorf("field %q only supports == and != operators, got %q", c.Field, c.Op)
	}

	return c, nil
}

func compareNum(actual float64, op string, threshold float64) bool {
	switch op {
	case ">":
		return actual > threshold
	case "<":
		return actual < threshold
	case ">=":
		return actual >= threshold
	case "<=":
		return actual <= threshold
	case "==":
		return actual == threshold
	case "!=":
		return actual != threshold
	}
	return false
}

func compareStr(actual, op, expected string) bool {
	switch op {
	case "==":
		return actual == expected
	case "!=":
		return actual != expected
	}
	return false
}

func conditionValue(c *Condition) string {
	if c.IsStr {
		return "'" + c.StrVal + "'"
	}
	return strconv.FormatFloat(c.NumVal, 'f', -1, 64)
}

func hostFieldValue(m *HostMetrics, field string) float64 {
	switch field {
	case "cpu_percent":
		return m.CPUPercent
	case "memory_percent":
		return m.MemPercent
	case "load1":
		return m.Load1
	case "load5":
		return m.Load5
	case "load15":
		return m.Load15
	case "swap_percent":
		if m.SwapTotal == 0 {
			return 0
		}
		return float64(m.SwapUsed) / float64(m.SwapTotal) * 100
	}
	return 0
}

func containerFieldNum(c *ContainerMetrics, field string) float64 {
	switch field {
	case "cpu_percent":
		return c.CPUPercent
	case "memory_percent":
		return c.MemPercent
	case "restart_count":
		return float64(c.RestartCount)
	case "exit_code":
		return float64(c.ExitCode)
	}
	return 0
}

func containerFieldStr(c *ContainerMetrics, field string) string {
	switch field {
	case "state":
		return c.State
	case "health":
		return c.Health
	}
	return ""
}
