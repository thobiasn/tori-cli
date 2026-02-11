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
	"text/template"
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

// Channel sends alert notifications to a single destination.
type Channel interface {
	Send(ctx context.Context, subject, body string) error
}

// Notifier sends alert notifications via configured channels.
type Notifier struct {
	channels []Channel
}

// NewNotifier creates a Notifier from config. Safe to call with zero-value config;
// Send becomes a no-op if no channels are enabled.
func NewNotifier(cfg *NotifyConfig) *Notifier {
	var channels []Channel
	if cfg.Email.Enabled {
		channels = append(channels, &emailChannel{cfg: cfg.Email})
	}
	for i := range cfg.Webhooks {
		wh := &cfg.Webhooks[i]
		if wh.Enabled {
			channels = append(channels, newWebhookChannel(*wh))
		}
	}
	return &Notifier{channels: channels}
}

// Send dispatches the alert to all enabled channels. Errors are logged, never returned —
// alerting must not block the collect loop.
func (n *Notifier) Send(ctx context.Context, subject, body string) {
	for _, ch := range n.channels {
		if err := ch.Send(ctx, subject, body); err != nil {
			slog.Error("notification failed", "error", err)
		}
	}
}

// emailChannel sends notifications via SMTP.
type emailChannel struct {
	cfg EmailConfig
}

func (e *emailChannel) Send(_ context.Context, subject, body string) error {
	addr := net.JoinHostPort(e.cfg.SMTPHost, fmt.Sprintf("%d", e.cfg.SMTPPort))

	// Sanitize header values to prevent SMTP header injection.
	from := sanitizeHeader(e.cfg.From)
	to := make([]string, len(e.cfg.To))
	for i, t := range e.cfg.To {
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
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return fmt.Errorf("smtp deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, e.cfg.SMTPHost)
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

// webhookChannel sends notifications via HTTP POST.
type webhookChannel struct {
	cfg  WebhookConfig
	tmpl *template.Template // nil = use default JSON payload
}

// webhookData is the data passed to webhook templates.
type webhookData struct {
	Subject string
	Body    string
}

func newWebhookChannel(cfg WebhookConfig) *webhookChannel {
	wc := &webhookChannel{cfg: cfg}
	if cfg.Template != "" {
		// Template was already validated at config load time.
		wc.tmpl = template.Must(template.New("webhook").Parse(cfg.Template))
	}
	return wc
}

func (w *webhookChannel) Send(ctx context.Context, subject, body string) error {
	var payload []byte
	if w.tmpl != nil {
		var buf bytes.Buffer
		if err := w.tmpl.Execute(&buf, webhookData{Subject: subject, Body: body}); err != nil {
			return fmt.Errorf("template execute: %w", err)
		}
		payload = buf.Bytes()
	} else {
		var err error
		payload, err = json.Marshal(map[string]string{
			"text": fmt.Sprintf("*%s*\n%s", subject, body),
		})
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	// Apply custom headers first (sanitize values), then set Content-Type
	// as default only if not overridden by a custom header.
	for k, v := range w.cfg.Headers {
		req.Header.Set(sanitizeHeader(k), sanitizeHeader(v))
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

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
