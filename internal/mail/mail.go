// Package mail sends the few plain-text messages the app needs: somebody's
// personal or management link, to an address they typed in themselves.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// ErrInvalidAddress means the recipient is not a single plain address.
var ErrInvalidAddress = errors.New("invalid e-mail address")

// Message is one plain-text mail.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender delivers a message. The web layer depends on this interface so that
// tests can capture mail instead of sending it.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends through a mail server over TLS: either implicit TLS from the
// first byte (port 465, "SMTPS") or STARTTLS on the submission port 587.
type SMTP struct {
	Host string
	Port string
	User string
	Pass string
	From string
	// Timeout bounds the whole conversation, so a hanging server cannot
	// hold a request forever.
	Timeout time.Duration
	// ImplicitTLS opens the connection with TLS right away, as port 465
	// expects, instead of upgrading it with STARTTLS.
	ImplicitTLS bool
	// Insecure allows sending unencrypted to a server without STARTTLS, for
	// a local relay or a test server. Off, nothing goes out unencrypted:
	// the mail carries a secret link. It never relaxes certificate checks
	// and does not matter with implicit TLS.
	Insecure bool
	// TLSConfig overrides the TLS settings, for tests.
	TLSConfig *tls.Config
}

// NewSMTP builds a sender from the app's SMTP settings.
func NewSMTP(host, port, user, pass, from string, implicitTLS, insecure bool) SMTP {
	return SMTP{Host: host, Port: port, User: user, Pass: pass, From: from,
		ImplicitTLS: implicitTLS, Insecure: insecure}
}

// Address checks that s is exactly one address without a display name or
// anything that could smuggle in extra headers, and returns it cleaned up.
func Address(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 || strings.ContainsAny(s, "\r\n<>,;\"") {
		return "", ErrInvalidAddress
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || !strings.Contains(a.Address, "@") {
		return "", ErrInvalidAddress
	}
	return a.Address, nil
}

// Send delivers m. With implicit TLS the whole conversation is encrypted
// from the start. Otherwise it refuses servers without STARTTLS unless
// Insecure is set, and never sends a login in clear text even then.
func (s SMTP) Send(ctx context.Context, m Message) error {
	to, err := Address(m.To)
	if err != nil {
		return err
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("sender address: %w", err)
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.TLSConfig != nil {
		tlsConfig = s.TLSConfig.Clone()
	}
	if tlsConfig.ServerName == "" {
		// Both TLS modes verify the certificate against the configured host.
		tlsConfig.ServerName = s.Host
	}
	addr := net.JoinHostPort(s.Host, s.Port)
	var conn net.Conn
	if s.ImplicitTLS {
		// net/smtp recognises the *tls.Conn and treats the session as
		// encrypted, so it allows the login and skips STARTTLS.
		d := tls.Dialer{Config: tlsConfig}
		conn, err = d.DialContext(ctx, "tcp", addr)
	} else {
		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to mail server: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("greet mail server: %w", err)
	}
	defer c.Close()

	if !s.ImplicitTLS {
		switch ok, _ := c.Extension("STARTTLS"); {
		case ok:
			if err := c.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		case s.User != "":
			return errors.New("mail server offers no STARTTLS; refusing to send the password in clear text")
		case !s.Insecure:
			return errors.New("mail server offers no STARTTLS; refusing to send the link unencrypted")
		}
	}
	if s.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			return fmt.Errorf("log in to mail server: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(compose(from, to, m)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finish message: %w", err)
	}
	// The server has accepted the message; a failing QUIT must not turn
	// that into an error and invite a duplicate.
	_ = c.Quit()
	return nil
}

// compose builds the raw message: UTF-8 subject and body, quoted-printable,
// so umlauts survive any relay.
func compose(from *mail.Address, to string, m Message) []byte {
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to)
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+messageID()+"@"+domainOf(from.Address)+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	// Marks the mail as automated, so out-of-office replies stay quiet.
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(strings.ReplaceAll(m.Body, "\n", "\r\n")))
	qp.Close()
	return b.Bytes()
}

func messageID() string {
	var r [12]byte
	rand.Read(r[:])
	return hex.EncodeToString(r[:])
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return "localhost"
}
