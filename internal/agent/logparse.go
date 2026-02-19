package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// InferLevel extracts and normalizes a log level from a raw log message.
// Tries JSON (level/lvl key), then logfmt (level/lvl key).
// Returns "ERR", "WARN", "INFO", "DBUG", or "".
func InferLevel(message string) string {
	if len(message) > 0 && message[0] == '{' {
		if lvl := parseJSONLevel(message); lvl != "" {
			return lvl
		}
	}
	if strings.ContainsRune(message, '=') {
		fields := parseLogfmtFields(message, "level", "lvl")
		for _, k := range []string{"level", "lvl"} {
			if v, ok := fields[k]; ok {
				return normalizeLevel(v)
			}
		}
	}
	return ""
}

// ExtractDisplayMsg extracts a clean display message from a raw log message.
// Tries JSON (msg/message/error key), then logfmt (msg/message key).
// Returns the original message if no structured format is detected.
func ExtractDisplayMsg(message string) string {
	if len(message) > 0 && message[0] == '{' {
		if msg := parseJSONMsg(message); msg != "" {
			return msg
		}
	}
	if strings.ContainsRune(message, '=') {
		fields := parseLogfmtFields(message, "msg", "message")
		for _, k := range []string{"msg", "message"} {
			if v, ok := fields[k]; ok {
				return v
			}
		}
	}
	return message
}

// parseJSONLevel extracts a normalized level from a JSON log line.
func parseJSONLevel(raw string) string {
	var m map[string]interface{}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	for _, k := range []string{"level", "lvl"} {
		if v, ok := m[k]; ok {
			return normalizeLevel(fmt.Sprint(v))
		}
	}
	return ""
}

// parseJSONMsg extracts the message field from a JSON log line.
func parseJSONMsg(raw string) string {
	var m map[string]interface{}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	for _, k := range []string{"msg", "message", "error"} {
		if v, ok := m[k]; ok {
			return fmt.Sprint(v)
		}
	}
	return ""
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
