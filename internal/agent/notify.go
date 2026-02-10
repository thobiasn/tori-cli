package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Notifier sends alert notifications via email and/or webhook.
type Notifier struct {
	email   *EmailConfig
	webhook *WebhookConfig
}

// NewNotifier creates a Notifier from config. Safe to call with zero-value config;
// Send becomes a no-op if no channels are enabled.
func NewNotifier(cfg *NotifyConfig) *Notifier {
	var n Notifier
	if cfg.Email.Enabled {
		n.email = &cfg.Email
	}
	if cfg.Webhook.Enabled {
		n.webhook = &cfg.Webhook
	}
	return &n
}

// Send dispatches the alert to all enabled channels. Errors are logged, never returned —
// alerting must not block the collect loop.
func (n *Notifier) Send(ctx context.Context, subject, body string) {
	if n.email != nil {
		if err := n.sendEmail(subject, body); err != nil {
			slog.Error("email notification failed", "error", err)
		}
	}
	if n.webhook != nil {
		if err := n.sendWebhook(ctx, subject, body); err != nil {
			slog.Error("webhook notification failed", "error", err)
		}
	}
}

func (n *Notifier) sendEmail(subject, body string) error {
	addr := fmt.Sprintf("%s:%d", n.email.SMTPHost, n.email.SMTPPort)
	to := strings.Join(n.email.To, ", ")

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n\r\n%s",
		n.email.From, to, subject, time.Now().Format(time.RFC1123Z), body)

	return smtp.SendMail(addr, nil, n.email.From, n.email.To, []byte(msg))
}

func (n *Notifier) sendWebhook(ctx context.Context, subject, body string) error {
	payload, err := json.Marshal(map[string]string{
		"text": fmt.Sprintf("*%s*\n%s", subject, body),
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhook.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
