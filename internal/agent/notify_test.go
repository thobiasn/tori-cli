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
	n.Send(context.Background(), "test", "body")
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

	n.Send(context.Background(), "Alert: high_cpu", "CPU is at 95%")

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
	n.Send(context.Background(), "test", "body")
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

	n.Send(context.Background(), "test", "body")

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

	n.Send(context.Background(), "CPU alert", "CPU is high")

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

	n.Send(context.Background(), "test", "body")

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

	n.Send(context.Background(), "test", "body")

	if called {
		t.Error("disabled webhook should not be called")
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

	n.Send(context.Background(), "test", "body")

	if strings.Contains(gotVal, "\r") || strings.Contains(gotVal, "\n") {
		t.Errorf("header value should be sanitized, got %q", gotVal)
	}
}
