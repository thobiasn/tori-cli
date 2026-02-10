package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		Webhook: WebhookConfig{Enabled: true, URL: srv.URL},
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
		Webhook: WebhookConfig{Enabled: true, URL: srv.URL},
	})

	// Should not panic — errors are logged, not returned.
	n.Send(context.Background(), "test", "body")
}
