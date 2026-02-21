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
	"sync"
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
	Send(ctx context.Context, n notification) error
}

// notification is a queued alert message.
type notification struct {
	subject  string
	body     string
	severity string // "warning", "critical", or "" for non-alert notifications
	status   string // "firing", "resolved", or "" for non-alert notifications
}

// Notifier sends alert notifications via configured channels.
// Notifications are queued and sent asynchronously to avoid blocking the
// collect loop when channels are slow or unreachable.
type Notifier struct {
	channels []Channel
	queue    chan notification
	wg       sync.WaitGroup // tracks run goroutine
	pending  sync.WaitGroup // tracks queued-but-unprocessed items
	stopOnce sync.Once
}

// NewNotifier creates a Notifier from config. Safe to call with zero-value config;
// Send becomes a no-op if no channels are enabled. If channels are configured,
// a background goroutine is started to process the queue — call Stop to shut it down.
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
	n := &Notifier{
		channels: channels,
		queue:    make(chan notification, 64),
	}
	if len(channels) > 0 {
		n.wg.Add(1)
		go n.run()
	}
	return n
}

// HasChannels returns whether any notification channels are configured.
func (n *Notifier) HasChannels() bool {
	return len(n.channels) > 0
}

func (n *Notifier) run() {
	defer n.wg.Done()
	for msg := range n.queue {
		for _, ch := range n.channels {
			sendWithRetry(context.Background(), ch, msg)
		}
		n.pending.Done()
	}
}

// Send queues a notification for async delivery. If the queue is full, the
// notification is dropped with a warning. This never blocks the caller.
func (n *Notifier) Send(subject, body string) {
	n.send(notification{subject: subject, body: body})
}

// SendAlert queues an alert notification with severity and status metadata.
func (n *Notifier) SendAlert(subject, body, severity, status string) {
	n.send(notification{subject: subject, body: body, severity: severity, status: status})
}

func (n *Notifier) send(msg notification) {
	if len(n.channels) == 0 {
		return
	}
	n.pending.Add(1)
	select {
	case n.queue <- msg:
	default:
		n.pending.Done()
		slog.Warn("notification queue full, dropping", "subject", msg.subject)
	}
}

// Flush waits for all queued notifications to be processed.
func (n *Notifier) Flush() {
	n.pending.Wait()
}

// Stop closes the notification queue and waits for remaining items to drain.
// Safe to call multiple times.
func (n *Notifier) Stop() {
	if len(n.channels) == 0 {
		return
	}
	n.stopOnce.Do(func() { close(n.queue) })
	n.wg.Wait()
}

// sendWithRetry attempts to send a notification up to 3 times with backoff (1s, 3s).
// Retries abort early if ctx is cancelled.
func sendWithRetry(ctx context.Context, ch Channel, msg notification) {
	backoffs := []time.Duration{1 * time.Second, 3 * time.Second}
	var err error
	for attempt := range 3 {
		err = ch.Send(ctx, msg)
		if err == nil {
			return
		}
		if attempt < len(backoffs) {
			slog.Warn("notification failed, retrying", "error", err, "attempt", attempt+1)
			select {
			case <-ctx.Done():
				slog.Error("notification retry aborted", "error", ctx.Err())
				return
			case <-time.After(backoffs[attempt]):
			}
		}
	}
	slog.Error("notification failed after 3 attempts", "error", err)
}

// emailChannel sends notifications via SMTP.
type emailChannel struct {
	cfg EmailConfig
}

func (e *emailChannel) Send(ctx context.Context, n notification) error {
	addr := net.JoinHostPort(e.cfg.SMTPHost, fmt.Sprintf("%d", e.cfg.SMTPPort))

	// Sanitize header values to prevent SMTP header injection.
	from := sanitizeHeader(e.cfg.From)
	to := make([]string, len(e.cfg.To))
	for i, t := range e.cfg.To {
		to[i] = sanitizeHeader(t)
	}
	subject := sanitizeHeader(n.subject)
	body := n.body

	toHeader := strings.Join(to, ", ")
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n\r\n%s",
		from, toHeader, subject, time.Now().Format(time.RFC1123Z), body)

	// Use a context-aware dialer so SMTP respects cancellation.
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
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
	Subject  string
	Body     string
	Severity string
	Status   string
}

func newWebhookChannel(cfg WebhookConfig) *webhookChannel {
	wc := &webhookChannel{cfg: cfg}
	if cfg.Template != "" {
		// Template was already validated at config load time.
		wc.tmpl = template.Must(template.New("webhook").Parse(cfg.Template))
	}
	return wc
}

func (w *webhookChannel) Send(ctx context.Context, n notification) error {
	var payload []byte
	if w.tmpl != nil {
		var buf bytes.Buffer
		data := webhookData{
			Subject:  n.subject,
			Body:     n.body,
			Severity: n.severity,
			Status:   n.status,
		}
		if err := w.tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("template execute: %w", err)
		}
		payload = buf.Bytes()
	} else {
		var err error
		payload, err = json.Marshal(map[string]string{
			"text": fmt.Sprintf("*%s*\n%s", n.subject, n.body),
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
