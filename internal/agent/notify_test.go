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
	sendWithRetry(context.Background(), ch, "test", "body")

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
	sendWithRetry(context.Background(), ch, "test", "body")

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
	sendWithRetry(ctx, ch, "test", "body")

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
