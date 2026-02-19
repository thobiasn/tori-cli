package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseLogFields extracts both level and display message from a raw log line
// with a single parse pass. Returns normalized level ("ERR","WARN","INFO","DBUG","")
// and a clean display message (or the original message if no structured format).
func ParseLogFields(message string) (level, displayMsg string) {
	if len(message) > 0 && message[0] == '{' {
		var m map[string]interface{}
		if json.Unmarshal([]byte(message), &m) == nil {
			for _, k := range []string{"level", "lvl"} {
				if v, ok := m[k]; ok {
					level = normalizeLevel(fmt.Sprint(v))
					break
				}
			}
			for _, k := range []string{"msg", "message", "error"} {
				if v, ok := m[k]; ok {
					displayMsg = fmt.Sprint(v)
					break
				}
			}
			if level != "" || displayMsg != "" {
				if displayMsg == "" {
					displayMsg = message
				}
				return level, displayMsg
			}
		}
	}
	if strings.ContainsRune(message, '=') {
		fields := parseLogfmtFields(message, "level", "lvl", "msg", "message")
		for _, k := range []string{"level", "lvl"} {
			if v, ok := fields[k]; ok {
				level = normalizeLevel(v)
				break
			}
		}
		for _, k := range []string{"msg", "message"} {
			if v, ok := fields[k]; ok {
				displayMsg = v
				break
			}
		}
		if level != "" || displayMsg != "" {
			if displayMsg == "" {
				displayMsg = message
			}
			return level, displayMsg
		}
	}
	return "", message
}

// InferLevel extracts and normalizes a log level from a raw log message.
func InferLevel(message string) string {
	level, _ := ParseLogFields(message)
	return level
}

// ExtractDisplayMsg extracts a clean display message from a raw log message.
func ExtractDisplayMsg(message string) string {
	_, dm := ParseLogFields(message)
	return dm
}

// parseLogfmtFields extracts the values for the specified keys from a logfmt line.
func parseLogfmtFields(raw string, keys ...string) map[string]string {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	result := make(map[string]string)
	i := 0
	for i < len(raw) {
		for i < len(raw) && raw[i] == ' ' {
			i++
		}
		if i >= len(raw) {
			break
		}
		keyStart := i
		for i < len(raw) && raw[i] != '=' && raw[i] != ' ' {
			i++
		}
		if i >= len(raw) || raw[i] != '=' {
			for i < len(raw) && raw[i] != ' ' {
				i++
			}
			continue
		}
		key := raw[keyStart:i]
		i++ // skip '='

		var val string
		if i < len(raw) && raw[i] == '"' {
			i++ // skip opening quote
			valStart := i
			for i < len(raw) && raw[i] != '"' {
				if raw[i] == '\\' && i+1 < len(raw) {
					i++
				}
				i++
			}
			val = raw[valStart:i]
			if i < len(raw) {
				i++ // skip closing quote
			}
		} else {
			valStart := i
			for i < len(raw) && raw[i] != ' ' {
				i++
			}
			val = raw[valStart:i]
		}

		if want[strings.ToLower(key)] {
			result[strings.ToLower(key)] = val
		}
	}
	return result
}

// normalizeLevel normalizes a log level string to a standard form.
func normalizeLevel(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "INFO", "INFORMATION":
		return "INFO"
	case "WARN", "WARNING":
		return "WARN"
	case "ERR", "ERROR":
		return "ERR"
	case "DEBUG", "DBG", "TRACE":
		return "DBUG"
	case "FATAL", "PANIC":
		return "ERR"
	default:
		return ""
	}
}
