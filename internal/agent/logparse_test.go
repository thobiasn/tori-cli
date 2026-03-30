package agent

import "testing"

func TestInferLevel(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"json level info", `{"level":"info","msg":"started"}`, "INFO"},
		{"json level warn", `{"level":"warning","msg":"slow"}`, "WARN"},
		{"json level error", `{"level":"error","msg":"failed"}`, "ERR"},
		{"json level debug", `{"level":"debug","msg":"trace"}`, "DBUG"},
		{"json lvl key", `{"lvl":"INFO","message":"ok"}`, "INFO"},
		{"json fatal", `{"level":"fatal","msg":"crash"}`, "ERR"},
		{"json panic", `{"level":"panic","msg":"crash"}`, "ERR"},
		{"json trace", `{"level":"trace","msg":"detail"}`, "DBUG"},
		{"json mixed case", `{"level":"Warning","msg":"hey"}`, "WARN"},
		{"logfmt level info", `level=info msg="started ok"`, "INFO"},
		{"logfmt level error", `ts=123 level=error msg="bad"`, "ERR"},
		{"logfmt lvl key", `lvl=warn msg=hello`, "WARN"},
		{"plain text", "just a plain log message", ""},
		{"empty string", "", ""},
		{"invalid json", `{"level":"info"`, ""},
		{"json unknown level", `{"level":"notice","msg":"x"}`, ""},

		// Plain text with positional level detection.
		{"slog info", "2026/02/19 09:45:54 INFO client disconnected remote=@", "INFO"},
		{"slog warn", "2026/02/19 09:45:54 WARN slow query duration=5s", "WARN"},
		{"slog error", "2026/02/19 09:45:54 ERROR connection failed host=db", "ERR"},
		{"slog debug", "2026/02/19 09:45:54 DEBUG request details method=GET", "DBUG"},
		{"iso timestamp info", "2026-02-19 09:45:54 INFO started", "INFO"},
		{"bracketed error", "2026/02/19 09:45:54 [error] something failed", "ERR"},
		{"bracketed warn no timestamp", "[WARN] something happened", "WARN"},
		{"parens error no timestamp", "(ERROR) something failed", "ERR"},
		{"parens info with timestamp", "2026/02/19 09:45:54 (info) started", "INFO"},
		{"bare level no timestamp", "INFO starting up", "INFO"},
		{"bare error no timestamp", "ERROR connection refused", "ERR"},
		{"error with colon", "Error: failed to connect", "ERR"},
		{"warn with colon", "Warning: disk almost full", "WARN"},
		{"timestamp then whitespace only", "2026/02/19 09:45:54   ", ""},
		{"timestamp no level", "2026/02/19 09:45:54 starting server", ""},
		{"no false positive in body", "the server reported an error", ""},
		{"nginx style", "2026/02/19 09:45:54 [error] 123#0: *456 something", "ERR"},

		// Without a timestamp, lowercase bare words must not match (false positive risk).
		{"no false positive lowercase", "info starting up", ""},
		{"no false positive information", "information about the system", ""},
		{"no false positive fatal", "Fatal attraction is a movie", ""},
		{"no false positive debug", "Debug mode enabled by admin", ""},

		// Without a timestamp, colon suffix matches any case.
		{"lowercase error with colon", "error: connection refused", "ERR"},
		{"lowercase warn with colon", "warn: something slow", "WARN"},

		// Without a timestamp, ALL CAPS bare words match.
		{"bare WARN no timestamp", "WARN something happened", "WARN"},
		{"bare DEBUG no timestamp", "DEBUG checking state", "DBUG"},
		{"bare FATAL no timestamp", "FATAL crash", "ERR"},
		{"bare PANIC no timestamp", "PANIC unrecoverable", "ERR"},

		// With a timestamp, any case matches (relaxed mode).
		{"timestamp lowercase error", "2026/02/19 09:45:54 error connection refused", "ERR"},
		{"timestamp lowercase warn", "2026/02/19 09:45:54 warning slow query", "WARN"},

		// Mismatched delimiters should not match.
		{"mismatched bracket paren", "[error) something", ""},
		{"mismatched paren bracket", "(error] something", ""},

		// Level keyword as entire message (rest == "" fallback).
		{"bare level entire message", "ERROR", "ERR"},
		{"colon level entire message", "Error:", "ERR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferLevel(tt.message)
			if got != tt.want {
				t.Errorf("InferLevel(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}

func TestExtractDisplayMsg(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"json msg key", `{"level":"info","msg":"started server"}`, "started server"},
		{"json message key", `{"level":"info","message":"started server"}`, "started server"},
		{"json error key", `{"level":"error","error":"connection refused"}`, "connection refused"},
		{"json msg priority over message", `{"msg":"primary","message":"secondary"}`, "primary"},
		{"logfmt msg", `level=info msg="started ok"`, "started ok"},
		{"logfmt message", `level=info message=hello`, "hello"},
		{"plain text passthrough", "just a plain log message", "just a plain log message"},
		{"empty string", "", ""},
		{"invalid json passthrough", `{"msg":"x"`, `{"msg":"x"`},
		{"no message key in json", `{"level":"info","status":"ok"}`, `{"level":"info","status":"ok"}`},
		{"json level only no msg field", `{"level":"error"}`, `{"level":"error"}`},
		{"logfmt escaped quote", `level=info msg="hello \"world\""`, `hello \"world\"`},
		{"logfmt key without equals", `badkey level=info msg=hi`, "hi"},
		{"logfmt level no msg key", `level=error status=500`, `level=error status=500`},
		{"plain level is entire msg", "2026/01/01 [INFO]", "2026/01/01 [INFO]"},
		{"plain unbracket level only", "2026/01/01 INFO", "2026/01/01 INFO"},

		// Plain text with positional level — displayMsg is content after the level.
		{"slog displaymsg", "2026/02/19 09:45:54 INFO client disconnected remote=@", "client disconnected remote=@"},
		{"bracketed displaymsg", "2026/02/19 09:45:54 [error] something failed", "something failed"},
		{"bare level no timestamp displaymsg", "INFO starting up", "starting up"},
		{"error colon displaymsg", "Error: failed to connect", "failed to connect"},
		{"bracketed no timestamp displaymsg", "[WARN] something happened", "something happened"},
		{"parens no timestamp displaymsg", "(ERROR) something failed", "something failed"},
		{"lowercase no timestamp passthrough", "info starting up", "info starting up"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractDisplayMsg(tt.message)
			if got != tt.want {
				t.Errorf("ExtractDisplayMsg(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}

func TestNormalizeLevel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"info", "INFO"},
		{"INFO", "INFO"},
		{"information", "INFO"},
		{"warn", "WARN"},
		{"WARNING", "WARN"},
		{"error", "ERR"},
		{"ERR", "ERR"},
		{"debug", "DBUG"},
		{"DBG", "DBUG"},
		{"trace", "DBUG"},
		{"fatal", "ERR"},
		{"panic", "ERR"},
		{"FATAL", "ERR"},
		{"unknown", ""},
		{"", ""},
		{"  info  ", "INFO"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeLevel(tt.input)
			if got != tt.want {
				t.Errorf("normalizeLevel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
