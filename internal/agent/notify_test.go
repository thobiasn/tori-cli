package agent

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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

// selfSignedCert generates a self-signed TLS certificate for testing.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// smtpSession records what a fake SMTP server saw.
type smtpSession struct {
	gotSTARTTLS bool
	gotAuth     string // AUTH argument (e.g. "PLAIN ...")
	mailFrom    string
	rcptTo      []string
	data        string
	err         error
}

// runFakeSMTP speaks enough SMTP protocol to verify TLS, AUTH, and envelope.
// It handles one connection then sends the session record on the result channel.
// Errors are captured in smtpSession.err instead of calling t.Fatal from a goroutine.
func runFakeSMTP(conn net.Conn, tlsCfg *tls.Config) smtpSession {
	defer conn.Close()
	var s smtpSession
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	write := func(line string) {
		fmt.Fprintf(rw, "%s\r\n", line)
		rw.Flush()
	}

	write("220 localhost ESMTP fake")

	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			s.err = fmt.Errorf("read: %w", err)
			return s
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		if len(cmd) > 4 {
			cmd = cmd[:4]
		}

		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"):
			write("250-localhost")
			if tlsCfg != nil && !s.gotSTARTTLS {
				write("250-STARTTLS")
			}
			write("250-AUTH PLAIN")
			write("250 OK")
		case strings.HasPrefix(strings.ToUpper(line), "STARTTLS"):
			write("220 Ready to start TLS")
			tlsConn := tls.Server(conn, tlsCfg)
			if err := tlsConn.Handshake(); err != nil {
				s.err = fmt.Errorf("tls handshake: %w", err)
				return s
			}
			conn = tlsConn
			rw = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
			s.gotSTARTTLS = true
		case strings.HasPrefix(strings.ToUpper(line), "AUTH "):
			s.gotAuth = line[5:]
			write("235 Authentication successful")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
			s.mailFrom = line[10:]
			write("250 OK")
		case strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
			s.rcptTo = append(s.rcptTo, line[8:])
			write("250 OK")
		case cmd == "DATA":
			write("354 Go ahead")
			var dataLines []string
			for {
				dl, err := rw.ReadString('\n')
				if err != nil {
					s.err = fmt.Errorf("read data: %w", err)
					return s
				}
				dl = strings.TrimRight(dl, "\r\n")
				if dl == "." {
					break
				}
				dataLines = append(dataLines, dl)
			}
			s.data = strings.Join(dataLines, "\n")
			write("250 OK")
		case cmd == "QUIT":
			write("221 Bye")
			return s
		default:
			write("500 Unknown command")
		}
	}
}

func testCertPool(t *testing.T, cert tls.Certificate) *x509.CertPool {
	t.Helper()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return pool
}

func TestEmailSendSTARTTLS(t *testing.T) {
	cert := selfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	result := make(chan smtpSession, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		result <- runFakeSMTP(conn, serverTLS)
	}()

	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	ch := &emailChannel{
		cfg: EmailConfig{
			Enabled:  true,
			SMTPHost: "127.0.0.1",
			SMTPPort: portNum,
			From:     "from@test.com",
			To:       []string{"to@test.com"},
			Username: "user",
			Password: "pass",
			TLS:      "starttls",
		},
		tlsConfig: &tls.Config{RootCAs: testCertPool(t, cert), ServerName: "127.0.0.1"},
	}

	if err := ch.Send(context.Background(), notification{subject: "test", body: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case s := <-result:
		if s.err != nil {
			t.Fatalf("fake smtp: %v", s.err)
		}
		if !s.gotSTARTTLS {
			t.Error("expected STARTTLS")
		}
		if s.gotAuth == "" {
			t.Error("expected AUTH")
		}
		if !strings.Contains(s.mailFrom, "from@test.com") {
			t.Errorf("mailFrom = %q", s.mailFrom)
		}
		if len(s.rcptTo) != 1 || !strings.Contains(s.rcptTo[0], "to@test.com") {
			t.Errorf("rcptTo = %v", s.rcptTo)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for fake SMTP session")
	}
}

func TestEmailSendImplicitTLS(t *testing.T) {
	cert := selfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	result := make(chan smtpSession, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		result <- runFakeSMTP(conn, nil)
	}()

	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	ch := &emailChannel{
		cfg: EmailConfig{
			Enabled:  true,
			SMTPHost: "127.0.0.1",
			SMTPPort: portNum,
			From:     "from@test.com",
			To:       []string{"to@test.com"},
			Username: "user",
			Password: "pass",
			TLS:      "tls",
		},
		tlsConfig: &tls.Config{RootCAs: testCertPool(t, cert), ServerName: "127.0.0.1"},
	}

	if err := ch.Send(context.Background(), notification{subject: "test", body: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case s := <-result:
		if s.err != nil {
			t.Fatalf("fake smtp: %v", s.err)
		}
		if s.gotSTARTTLS {
			t.Error("implicit TLS should not use STARTTLS")
		}
		if s.gotAuth == "" {
			t.Error("expected AUTH")
		}
		if !strings.Contains(s.mailFrom, "from@test.com") {
			t.Errorf("mailFrom = %q", s.mailFrom)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for fake SMTP session")
	}
}

func TestEmailSendNoAuth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	result := make(chan smtpSession, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		result <- runFakeSMTP(conn, nil)
	}()

	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	ch := &emailChannel{cfg: EmailConfig{
		Enabled:  true,
		SMTPHost: "127.0.0.1",
		SMTPPort: portNum,
		From:     "from@test.com",
		To:       []string{"to@test.com", "to2@test.com"},
	}}

	if err := ch.Send(context.Background(), notification{subject: "test subj", body: "body text"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case s := <-result:
		if s.err != nil {
			t.Fatalf("fake smtp: %v", s.err)
		}
		if s.gotSTARTTLS {
			t.Error("no TLS configured, should not STARTTLS")
		}
		if s.gotAuth != "" {
			t.Errorf("no auth expected, got %q", s.gotAuth)
		}
		if !strings.Contains(s.mailFrom, "from@test.com") {
			t.Errorf("mailFrom = %q", s.mailFrom)
		}
		if len(s.rcptTo) != 2 {
			t.Errorf("expected 2 recipients, got %d", len(s.rcptTo))
		}
		if !strings.Contains(s.data, "test subj") {
			t.Error("data should contain subject")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for fake SMTP session")
	}
}
