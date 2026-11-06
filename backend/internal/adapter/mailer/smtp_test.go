package mailer

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/mail"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

func TestComposeWritesAPlainTextMessage(t *testing.T) {
	s, err := NewSMTP("smtp.example.com:465", "info@zoikotax.com", "secret", "ZoikoTax <info@zoikotax.com>")
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.compose(port.Message{
		To: "grace@example.com", ReplyTo: "ada@example.com", Subject: "Réinitialiser",
		Text: "Hello\nThe link: https://app.example/#set-password=abc\n",
	}, mustAddress(t, "grace@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	msg := string(body)
	for _, want := range []string{
		"From: \"ZoikoTax\" <info@zoikotax.com>\r\n",
		"To: <grace@example.com>\r\n",
		"Reply-To: <ada@example.com>\r\n",
		"Subject: =?utf-8?q?R=C3=A9initialiser?=\r\n",
		"Content-Transfer-Encoding: quoted-printable\r\n",
		"Message-ID: <",
		"@zoikotax.com>\r\n",
		"\r\n\r\nHello\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestComposeRefusesHeaderInjection(t *testing.T) {
	s, err := NewSMTP("smtp.example.com:465", "u", "p", "info@zoikotax.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.compose(port.Message{To: "grace@example.com", Subject: "Hi\r\nBcc: everyone@example.com"},
		mustAddress(t, "grace@example.com"))
	if !errors.Is(err, ErrHeaderInjection) {
		t.Fatalf("%v; want ErrHeaderInjection", err)
	}
}

// A server on a plain port that will not upgrade is refused before anything —
// least of all a password — is written to it.
func TestSendRefusesAServerWithoutSTARTTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 plain.example ESMTP\r\n"))
		var seen []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				got <- strings.Join(seen, "")
				return
			}
			seen = append(seen, line)
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = conn.Write([]byte("250-plain.example\r\n250 AUTH PLAIN\r\n"))
			default:
				_, _ = conn.Write([]byte("221 bye\r\n"))
			}
		}
	}()

	s, err := NewSMTP(ln.Addr().String(), "info@zoikotax.com", "secret", "info@zoikotax.com")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(context.Background(), port.Message{To: "grace@example.com", Subject: "Hi", Text: "Hi"})
	if !errors.Is(err, ErrNoStartTLS) {
		t.Fatalf("%v; want ErrNoStartTLS", err)
	}
	if transcript := <-got; strings.Contains(transcript, "AUTH") {
		t.Fatalf("credentials offered in clear:\n%s", transcript)
	}
}

func mustAddress(t *testing.T, s string) mail.Address {
	t.Helper()
	a, err := mail.ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return *a
}
