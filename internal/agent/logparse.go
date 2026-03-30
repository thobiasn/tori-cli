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
	// Try plain text with positional level detection.
	// Skip timestamp-like tokens, then check for a level keyword.
	if level, displayMsg = parsePlainLevel(message); level != "" {
		return level, displayMsg
	}
	return "", message
}

// parsePlainLevel detects a log level from plain text lines where the level
// appears as the first non-timestamp token or as the very first token.
// Handles formats like:
//
//	"2026/02/19 09:45:54 INFO message..."
//	"2026-02-19T09:45:54Z [error] something failed"
//	"INFO message..."
//	"[WARN] message..."
//	"(ERROR) something failed"
//	"Error: something failed"
func parsePlainLevel(msg string) (string, string) {
	i := 0
	sawTimestamp := false
	for i < len(msg) {
		// Skip whitespace.
		for i < len(msg) && msg[i] == ' ' {
			i++
		}
		if i >= len(msg) {
			break
		}

		// Read the next token.
		start := i
		for i < len(msg) && msg[i] != ' ' {
			i++
		}
		token := msg[start:i]

		// Wrapped token like [INFO], [error], (WARN), (error).
		if inner, ok := unwrapLevel(token); ok {
			if lvl := normalizeLevel(inner); lvl != "" {
				rest := strings.TrimSpace(msg[i:])
				if rest == "" {
					rest = msg
				}
				return lvl, rest
			}
		}

		// Token with digits is timestamp-like — skip it.
		if containsDigit(token) {
			sawTimestamp = true
			continue
		}

		// After a timestamp, any recognized level keyword matches.
		// Without a timestamp, require ALL CAPS or trailing colon
		// to avoid false positives like "information about...".
		if lvl := matchLevel(token, sawTimestamp); lvl != "" {
			rest := strings.TrimSpace(msg[i:])
			if rest == "" {
				rest = msg
			}
			return lvl, rest
		}

		// Unknown token — stop looking.
		break
	}
	return "", msg
}

// matchLevel checks if a token is a level keyword. In relaxed mode (after a
// timestamp), any case matches. In strict mode (no preceding timestamp), the
// token must be ALL CAPS or have a trailing colon — this avoids false positives
// on prose like "information about..." or "Fatal attraction".
func matchLevel(token string, relaxed bool) string {
	if relaxed {
		return normalizeLevel(token)
	}
	// Trailing colon: "Error:", "Warning:" — strip and normalize.
	if len(token) > 1 && token[len(token)-1] == ':' {
		return normalizeLevel(token[:len(token)-1])
	}
	// ALL CAPS: "ERROR", "INFO", "WARN".
	if isAllUpper(token) {
		return normalizeLevel(token)
	}
	return ""
}

func isAllUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' {
			return false
		}
	}
	return true
}

// unwrapLevel checks if a token is wrapped in brackets or parentheses
// and returns the inner string. e.g. "[INFO]" → "INFO", "(error)" → "error".
func unwrapLevel(token string) (string, bool) {
	if len(token) >= 3 {
		if (token[0] == '[' && token[len(token)-1] == ']') ||
			(token[0] == '(' && token[len(token)-1] == ')') {
			return token[1 : len(token)-1], true
		}
	}
	return "", false
}

func containsDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
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
