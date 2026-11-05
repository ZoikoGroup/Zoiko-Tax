// Package mailer sends email over SMTP submission.
//
// Mail never travels in clear. Port 465 speaks TLS from the first byte; any
// other port must offer STARTTLS, and a server that does not is refused rather
// than written to in plain text — the messages this sends carry set-password
// links, which are credentials.
package mailer

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

	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Timeout bounds one send end to end: dial, TLS, authentication and data. A
// server that takes longer has failed the send, and the caller says so.
const Timeout = 30 * time.Second

// ErrNoStartTLS is a server on a non-TLS port that will not upgrade.
var ErrNoStartTLS = errors.New("mailer: the server does not offer STARTTLS")

// ErrHeaderInjection is a header value carrying a line break, which would let
// whoever chose the value write headers of their own.
var ErrHeaderInjection = errors.New("mailer: header value contains a line break")

// SMTP sends through one submission server as one account.
type SMTP struct {
	addr     string
	host     string
	port     string
	username string
	password string
	from     mail.Address
}

var _ port.Mailer = (*SMTP)(nil)

// NewSMTP builds a mailer for addr (host:port), authenticating as username,
// with from as the sender of every message.
func NewSMTP(addr, username, password, from string) (*SMTP, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("mailer: address %q: %w", addr, err)
	}
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("mailer: from %q: %w", from, err)
	}
	return &SMTP{addr: addr, host: host, port: p, username: username, password: password, from: *sender}, nil
}

// From is the sender every message goes out under.
func (s *SMTP) From() string { return s.from.Address }

// Send delivers m.
func (s *SMTP) Send(ctx context.Context, m port.Message) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("mailer: recipient: %w", err)
	}
	body, err := s.compose(m, *to)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	c, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if err := c.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
		return fmt.Errorf("mailer: authenticate: %w", err)
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mailer: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("mailer: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("mailer: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: end of data: %w", err)
	}
	return c.Quit()
}

// dial connects and secures the connection: TLS at once on 465, STARTTLS on
// anything else.
func (s *SMTP) dial(ctx context.Context) (*smtp.Client, error) {
	tlsConfig := &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("mailer: dial: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if s.port == "465" {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("mailer: TLS: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mailer: greeting: %w", err)
	}
	if _, isTLS := conn.(*tls.Conn); !isTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			_ = c.Close()
			return nil, ErrNoStartTLS
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("mailer: STARTTLS: %w", err)
		}
	}
	return c, nil
}

// compose renders m as an RFC 5322 message: UTF-8 text, quoted-printable.
func (s *SMTP) compose(m port.Message, to mail.Address) ([]byte, error) {
	for _, v := range []string{m.Subject, m.ReplyTo} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, ErrHeaderInjection
		}
	}
	msgID, err := messageID(s.from.Address)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", s.from.String())
	header("To", to.String())
	if m.ReplyTo != "" {
		replyTo, err := mail.ParseAddress(m.ReplyTo)
		if err != nil {
			return nil, fmt.Errorf("mailer: reply-to: %w", err)
		}
		header("Reply-To", replyTo.String())
	}
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", msgID)
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="utf-8"`)
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(m.Text, "\n", "\r\n"))); err != nil {
		return nil, fmt.Errorf("mailer: encode body: %w", err)
	}
	if err := qp.Close(); err != nil {
		return nil, fmt.Errorf("mailer: encode body: %w", err)
	}
	return b.Bytes(), nil
}

// messageID is a fresh Message-ID in the sender's domain.
func messageID(from string) (string, error) {
	domain := "localhost"
	if at := strings.LastIndexByte(from, '@'); at >= 0 {
		domain = from[at+1:]
	}
	r := make([]byte, 16)
	if _, err := rand.Read(r); err != nil {
		return "", fmt.Errorf("mailer: message id: %w", err)
	}
	return "<" + hex.EncodeToString(r) + "@" + domain + ">", nil
}
