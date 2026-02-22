package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSendNoChannels(t *testing.T) {
	n := NewNotifier(&NotifyConfig{})
	// Should not panic with no channels enabled.
	n.Send("test", "body")
	n.Stop()
}

func TestWebhookPayload(t *testing.T) {
	var got map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{{Enabled: true, URL: srv.URL}},
	})

	n.Send("Alert: high_cpu", "CPU is at 95%")
	n.Stop()

	if got["text"] == "" {
		t.Fatal("webhook payload text is empty")
	}
	// Verify subject appears in text.
	if got["text"] != "*Alert: high_cpu*\nCPU is at 95%" {
		t.Errorf("webhook text = %q", got["text"])
	}
}

func TestWebhookErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{{Enabled: true, URL: srv.URL}},
	})

	// Should not panic — errors are logged, not returned.
	n.Send("test", "body")
	n.Stop()
}

func TestWebhookCustomHeaders(t *testing.T) {
	var gotAuth string
	var gotCustom string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{{
			Enabled: true,
			URL:     srv.URL,
			Headers: map[string]string{
				"Authorization": "Bearer token123",
				"X-Custom":      "myvalue",
			},
		}},
	})

	n.Send("test", "body")
	n.Stop()

	if gotAuth != "Bearer token123" {
		t.Errorf("Authorization = %q, want Bearer token123", gotAuth)
	}
	if gotCustom != "myvalue" {
		t.Errorf("X-Custom = %q, want myvalue", gotCustom)
	}
}

func TestWebhookCustomTemplate(t *testing.T) {
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{{
			Enabled:  true,
			URL:      srv.URL,
			Template: `{"summary":"{{.Subject}}","detail":"{{.Body}}"}`,
		}},
	})

	n.Send("CPU alert", "CPU is high")
	n.Stop()

	want := `{"summary":"CPU alert","detail":"CPU is high"}`
	if gotBody != want {
		t.Errorf("body = %q, want %q", gotBody, want)
	}
}

func TestWebhookTemplateJSONEscape(t *testing.T) {
	tmpl := `{"title":"{{.Subject}}","text":"{{.Body}}","sev":"{{.Severity}}","status":"{{.Status}}"}`

	tests := []struct {
		name     string
		subject  string
		body     string
		severity string
		status   string
	}{
		{
			name:     "log alert with quoted match pattern",
			subject:  "Alert: mem_killed",
			body:     `[critical] mem_killed: log matches for "OOM|out of memory"`,
			severity: "critical",
			status:   "firing",
		},
		{
			name:     "host alert plain text",
			subject:  "Alert: high_cpu",
			body:     "[warning] high_cpu: host.cpu_percent",
			severity: "warning",
			status:   "firing",
		},
		{
			name:     "resolved alert",
			subject:  "Resolved: high_cpu",
			body:     "[warning] high_cpu: host.cpu_percent",
			severity: "warning",
			status:   "resolved",
		},
		{
			name:     "test notification",
			subject:  "Test: high_cpu",
			body:     "Test notification for rule 'high_cpu'.",
			severity: "critical",
			status:   "test",
		},
		{
			name:     "container label with special chars",
			subject:  "Alert: exited",
			body:     `[critical] exited: container.state (my-app/web "v2")`,
			severity: "critical",
			status:   "firing",
		},
		{
			name:     "backslashes in pattern",
			subject:  "Alert: path_error",
			body:     `[warning] path_error: log matches for "C:\Users\test"`,
			severity: "warning",
			status:   "firing",
		},
		{
			name:     "newlines and tabs in body",
			subject:  "Alert: multiline",
			body:     "[critical] multiline: log matches for \"line1\nline2\ttab\"",
			severity: "critical",
			status:   "firing",
		},
		{
			name:     "unicode and emoji",
			subject:  "Alert: disk_full",
			body:     "[warning] disk_full: host.disk_percent (mount: /données)",
			severity: "warning",
			status:   "firing",
		},
		{
			name:     "empty severity and status",
			subject:  "Manual notification",
			body:     "Something happened",
			severity: "",
			status:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			n := NewNotifier(&NotifyConfig{
				Webhooks: []WebhookConfig{{
					Enabled:  true,
					URL:      srv.URL,
					Template: tmpl,
				}},
			})

			n.SendAlert(tt.subject, tt.body, tt.severity, tt.status)
			n.Stop()

			// Every payload must be valid JSON.
			var parsed map[string]any
			if err := json.Unmarshal([]byte(gotBody), &parsed); err != nil {
				t.Fatalf("invalid JSON: %v\npayload: %s", err, gotBody)
			}

			// Verify the decoded values match the original inputs.
			if parsed["title"] != tt.subject {
				t.Errorf("title = %q, want %q", parsed["title"], tt.subject)
			}
			if parsed["text"] != tt.body {
				t.Errorf("text = %q, want %q", parsed["text"], tt.body)
			}
			if parsed["sev"] != tt.severity {
				t.Errorf("sev = %q, want %q", parsed["sev"], tt.severity)
			}
			if parsed["status"] != tt.status {
				t.Errorf("status = %q, want %q", parsed["status"], tt.status)
			}
		})
	}
}

func TestWebhookSlackAttachmentTemplate(t *testing.T) {
	// Real-world Slack attachment template — the format that triggered the original bug.
	tmpl := `{"attachments":[{"color":"{{if eq .Status "resolved"}}#2ecc71{{else if eq .Severity "critical"}}#e74c3c{{else}}#f39c12{{end}}","title":"{{.Subject}}","text":"{{.Body}} | {{.Severity}} · {{.Status}}"}]}`

	tests := []struct {
		name     string
		subject  string
		body     string
		severity string
		status   string
	}{
		{
			name:     "log alert firing",
			subject:  "Alert: mem_killed",
			body:     `[critical] mem_killed: log matches for "OOM|out of memory" (my-app/worker)`,
			severity: "critical",
			status:   "firing",
		},
		{
			name:     "resolved",
			subject:  "Resolved: high_cpu",
			body:     "[warning] high_cpu: host.cpu_percent",
			severity: "warning",
			status:   "resolved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			n := NewNotifier(&NotifyConfig{
				Webhooks: []WebhookConfig{{
					Enabled:  true,
					URL:      srv.URL,
					Template: tmpl,
				}},
			})

			n.SendAlert(tt.subject, tt.body, tt.severity, tt.status)
			n.Stop()

			var parsed map[string]any
			if err := json.Unmarshal([]byte(gotBody), &parsed); err != nil {
				t.Fatalf("invalid JSON: %v\npayload: %s", err, gotBody)
			}
		})
	}
}

func TestMultipleWebhooks(t *testing.T) {
	var mu sync.Mutex
	var called []string

	handler := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			called = append(called, name)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}

	srv1 := httptest.NewServer(handler("hook1"))
	defer srv1.Close()
	srv2 := httptest.NewServer(handler("hook2"))
	defer srv2.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{
			{Enabled: true, URL: srv1.URL},
			{Enabled: true, URL: srv2.URL},
		},
	})

	n.Send("test", "body")
	n.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(called) != 2 {
		t.Fatalf("expected 2 webhooks called, got %d", len(called))
	}
}

func TestWebhookDisabledSkipped(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{
			{Enabled: false, URL: srv.URL},
		},
	})

	n.Send("test", "body")
	n.Stop()

	if called {
		t.Error("disabled webhook should not be called")
	}
}

func TestWebhookRetrySuccess(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := newWebhookChannel(WebhookConfig{Enabled: true, URL: srv.URL})
	sendWithRetry(context.Background(), ch, notification{subject: "test", body: "body"})

	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestWebhookRetryExhausted(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ch := newWebhookChannel(WebhookConfig{Enabled: true, URL: srv.URL})
	// Should not panic — errors are logged after exhausting retries.
	sendWithRetry(context.Background(), ch, notification{subject: "test", body: "body"})

	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestWebhookRetryContextCancelled(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the retry backoff select picks up ctx.Done().
	cancel()

	ch := newWebhookChannel(WebhookConfig{Enabled: true, URL: srv.URL})
	sendWithRetry(ctx, ch, notification{subject: "test", body: "body"})

	mu.Lock()
	defer mu.Unlock()
	// First attempt runs, then retry aborts due to cancelled context.
	if attempts > 2 {
		t.Fatalf("expected at most 2 attempts with cancelled context, got %d", attempts)
	}
}

func TestWebhookHeaderSanitization(t *testing.T) {
	var gotVal string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVal = r.Header.Get("X-Injected")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{{
			Enabled: true,
			URL:     srv.URL,
			Headers: map[string]string{
				"X-Injected": "safe\r\nEvil-Header: injected",
			},
		}},
	})

	n.Send("test", "body")
	n.Stop()

	if strings.Contains(gotVal, "\r") || strings.Contains(gotVal, "\n") {
		t.Errorf("header value should be sanitized, got %q", gotVal)
	}
}

func TestWebhookTemplateSeverityStatus(t *testing.T) {
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{{
			Enabled:  true,
			URL:      srv.URL,
			Template: `{"sev":"{{.Severity}}","status":"{{.Status}}","msg":"{{.Subject}}"}`,
		}},
	})

	n.SendAlert("CPU alert", "CPU is high", "critical", "firing")
	n.Stop()

	want := `{"sev":"critical","status":"firing","msg":"CPU alert"}`
	if gotBody != want {
		t.Errorf("body = %q, want %q", gotBody, want)
	}
}

func TestWebhookTemplateSeverityEmpty(t *testing.T) {
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&NotifyConfig{
		Webhooks: []WebhookConfig{{
			Enabled:  true,
			URL:      srv.URL,
			Template: `{"sev":"{{.Severity}}","status":"{{.Status}}","msg":"{{.Subject}}"}`,
		}},
	})

	// Plain Send (no severity/status) — fields render as empty strings.
	n.Send("test", "body")
	n.Stop()

	want := `{"sev":"","status":"","msg":"test"}`
	if gotBody != want {
		t.Errorf("body = %q, want %q", gotBody, want)
	}
}
