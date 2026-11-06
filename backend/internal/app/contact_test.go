package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

type keptMail struct {
	sent []port.Message
	err  error
}

func (k *keptMail) Send(_ context.Context, m port.Message) error {
	if k.err != nil {
		return k.err
	}
	k.sent = append(k.sent, m)
	return nil
}

func TestContactDeliversToTheInboxWithReplyToTheSender(t *testing.T) {
	mail := &keptMail{}
	svc := app.NewContactService(mail, "info@zoikotax.com")
	err := svc.Submit(context.Background(), app.ContactInput{
		Name: " Grace Hopper ", Email: "grace@example.com", Company: "Example Telecom",
		Message: "We would like a demonstration.", ClientIP: "203.0.113.7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("%d messages sent", len(mail.sent))
	}
	m := mail.sent[0]
	if m.To != "info@zoikotax.com" || m.ReplyTo != "grace@example.com" {
		t.Errorf("to %q, reply-to %q", m.To, m.ReplyTo)
	}
	if m.Subject != "Website enquiry from Grace Hopper (Example Telecom)" {
		t.Errorf("subject %q", m.Subject)
	}
	for _, want := range []string{"We would like a demonstration.", "203.0.113.7", "grace@example.com"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("body lacks %q:\n%s", want, m.Text)
		}
	}
}

func TestContactRefusesWhatItCannotSend(t *testing.T) {
	ok := app.ContactInput{Name: "Grace", Email: "grace@example.com", Message: "Hello"}
	for name, tc := range map[string]struct {
		edit   func(*app.ContactInput)
		reason errs.ReasonCode
	}{
		"no name":            {func(in *app.ContactInput) { in.Name = "  " }, errs.ReasonMissingField},
		"a name over lines":  {func(in *app.ContactInput) { in.Name = "Grace\r\nBcc: everyone@example.com" }, errs.ReasonInvalidValue},
		"not an address":     {func(in *app.ContactInput) { in.Email = "grace" }, errs.ReasonInvalidValue},
		"no message":         {func(in *app.ContactInput) { in.Message = "" }, errs.ReasonMissingField},
		"a message too long": {func(in *app.ContactInput) { in.Message = strings.Repeat("x", app.ContactMessageMax+1) }, errs.ReasonInvalidValue},
	} {
		t.Run(name, func(t *testing.T) {
			in := ok
			tc.edit(&in)
			mail := &keptMail{}
			if err := app.NewContactService(mail, "info@zoikotax.com").Submit(context.Background(), in); errs.ReasonOf(err) != tc.reason {
				t.Fatalf("%v; want %s", err, tc.reason)
			}
			if len(mail.sent) != 0 {
				t.Fatal("sent a message it refused")
			}
		})
	}
}

func TestContactReportsAFailedSendAsRetryable(t *testing.T) {
	svc := app.NewContactService(&keptMail{err: errors.New("connection refused")}, "info@zoikotax.com")
	err := svc.Submit(context.Background(), app.ContactInput{Name: "Grace", Email: "grace@example.com", Message: "Hello"})
	if errs.ReasonOf(err) != errs.ReasonUnavailable {
		t.Fatalf("%v; want TEMPORARILY_UNAVAILABLE", err)
	}
}
