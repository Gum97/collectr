// Package notify delivers the one message the platform cannot do without: the
// link a data subject uses to reach their own data.
//
// Marketing email is out of scope and deliberately so. This exists because the
// portal is unreachable without it.
package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Message is one outbound notification.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Notifier delivers messages.
type Notifier interface {
	Send(ctx context.Context, m Message) error
}

// LogNotifier writes messages to the log instead of sending them.
//
// It is the default so a fresh `docker compose up` has a working portal without
// SMTP credentials: the operator reads the link out of the logs. That is fine for
// evaluation and unacceptable in production, which is why it says so on every
// message it handles.
type LogNotifier struct {
	log *slog.Logger
}

// NewLogNotifier returns a LogNotifier.
func NewLogNotifier(log *slog.Logger) *LogNotifier { return &LogNotifier{log: log} }

// Send records the message.
func (n *LogNotifier) Send(_ context.Context, m Message) error {
	// The recipient is masked even here. Logs get shipped, indexed and read by
	// people who have no business seeing whose data is in the system.
	n.log.Warn("notification not sent: no SMTP configured, message written to the log instead",
		"to", maskRecipient(m.To),
		"subject", m.Subject,
		"body", m.Body,
	)
	return nil
}

// SMTPConfig configures the SMTP notifier.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// StartTLS upgrades the connection after greeting. Off only for a local relay
	// that is already on a trusted network.
	StartTLS bool
}

// SMTPNotifier sends mail over SMTP.
type SMTPNotifier struct {
	cfg SMTPConfig
}

// NewSMTPNotifier returns an SMTPNotifier.
func NewSMTPNotifier(cfg SMTPConfig) (*SMTPNotifier, error) {
	if cfg.Host == "" || cfg.From == "" {
		return nil, fmt.Errorf("smtp host and from address are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	return &SMTPNotifier{cfg: cfg}, nil
}

// Send delivers one message.
func (n *SMTPNotifier) Send(ctx context.Context, m Message) error {
	addr := net.JoinHostPort(n.cfg.Host, fmt.Sprint(n.cfg.Port))

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connecting to smtp server: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, n.cfg.Host)
	if err != nil {
		return fmt.Errorf("starting smtp session: %w", err)
	}
	defer func() {
		if err := client.Quit(); err != nil {
			_ = client.Close()
		}
	}()

	if n.cfg.StartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: n.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starting tls: %w", err)
		}
	}
	if n.cfg.Username != "" {
		auth := smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authenticating: %w", err)
		}
	}

	if err := client.Mail(n.cfg.From); err != nil {
		return fmt.Errorf("setting sender: %w", err)
	}
	if err := client.Rcpt(m.To); err != nil {
		return fmt.Errorf("setting recipient: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("opening message body: %w", err)
	}
	headers := strings.NewReplacer("\r", "", "\n", "")
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		headers.Replace(n.cfg.From), headers.Replace(m.To), headers.Replace(m.Subject), m.Body)
	if _, err := wc.Write([]byte(body)); err != nil {
		_ = wc.Close()
		return fmt.Errorf("writing message: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("closing message: %w", err)
	}
	return nil
}

// maskRecipient keeps enough of an address to recognise it without recording it.
func maskRecipient(to string) string {
	at := strings.LastIndex(to, "@")
	if at <= 0 {
		if len(to) <= 4 {
			return "***"
		}
		return to[:2] + "***" + to[len(to)-2:]
	}
	local, domain := to[:at], to[at+1:]
	if len(local) <= 2 {
		return "***@" + domain
	}
	return local[:2] + "***@" + domain
}
