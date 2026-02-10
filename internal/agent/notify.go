package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// webhookClient is a dedicated HTTP client for webhook notifications.
// Separate from http.DefaultClient to avoid shared state and configure timeouts.
var webhookClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

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
	addr := net.JoinHostPort(n.email.SMTPHost, fmt.Sprintf("%d", n.email.SMTPPort))

	// Sanitize header values to prevent SMTP header injection.
	from := sanitizeHeader(n.email.From)
	to := make([]string, len(n.email.To))
	for i, t := range n.email.To {
		to[i] = sanitizeHeader(t)
	}
	subject = sanitizeHeader(subject)

	toHeader := strings.Join(to, ", ")
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n\r\n%s",
		from, toHeader, subject, time.Now().Format(time.RFC1123Z), body)

	// Use a dialer with timeout so SMTP doesn't block the collect loop indefinitely.
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	c, err := smtp.NewClient(conn, n.email.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if err := c.Mail(from); err != nil {
		return err
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// sanitizeHeader strips CR and LF characters to prevent SMTP header injection.
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
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

	resp, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
