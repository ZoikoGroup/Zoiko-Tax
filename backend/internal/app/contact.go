package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Contact form limits. They bound what one anonymous request can make the
// cell send, not what a person might want to say.
const (
	ContactNameMax    = 200
	ContactCompanyMax = 200
	ContactMessageMax = 5000
)

// ContactService delivers the public contact form to the cell's inbox.
//
// Nothing is stored: the message goes to ContactTo with Reply-To set to the
// sender, so answering it is answering them, and the cell keeps no copy of an
// enquiry it has no other use for.
type ContactService struct {
	mailer port.Mailer
	to     string
}

// NewContactService wires the service. A cell with no mailer or no inbox has
// no ContactService, and the form answers 503.
func NewContactService(mailer port.Mailer, to string) *ContactService {
	return &ContactService{mailer: mailer, to: to}
}

// ContactInput is one enquiry.
type ContactInput struct {
	Name    string
	Email   string
	Company string
	Message string
	// ClientIP is recorded in the message so that abuse can be traced and
	// blocked; it is not stored anywhere else.
	ClientIP string
}

// Submit validates an enquiry and sends it.
func (s *ContactService) Submit(ctx context.Context, in ContactInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.TrimSpace(in.Email)
	in.Company = strings.TrimSpace(in.Company)
	in.Message = strings.TrimSpace(in.Message)

	switch {
	case in.Name == "":
		return errs.Invalid("name", errs.ReasonMissingField, "A name is required.")
	case utf8.RuneCountInString(in.Name) > ContactNameMax || strings.ContainsAny(in.Name, "\r\n"):
		return errs.Invalid("name", errs.ReasonInvalidValue, "The name is too long or spans lines.")
	case identity.ValidateEmail(in.Email) != nil:
		return errs.Invalid("email", errs.ReasonInvalidValue, "That is not a valid email address.")
	case utf8.RuneCountInString(in.Company) > ContactCompanyMax || strings.ContainsAny(in.Company, "\r\n"):
		return errs.Invalid("company", errs.ReasonInvalidValue, "The company name is too long or spans lines.")
	case in.Message == "":
		return errs.Invalid("message", errs.ReasonMissingField, "A message is required.")
	case utf8.RuneCountInString(in.Message) > ContactMessageMax:
		return errs.Invalid("message", errs.ReasonInvalidValue,
			fmt.Sprintf("The message is longer than %d characters.", ContactMessageMax))
	}

	company := in.Company
	if company == "" {
		company = "(not given)"
	}
	subject := "Website enquiry from " + in.Name
	if in.Company != "" {
		subject += " (" + in.Company + ")"
	}
	err := s.mailer.Send(ctx, port.Message{
		To:      s.to,
		ReplyTo: in.Email,
		Subject: subject,
		Text: fmt.Sprintf(`A new enquiry arrived through the ZoikoTax contact form.

Name:    %s
Email:   %s
Company: %s
From IP: %s

%s
`, in.Name, in.Email, company, in.ClientIP, in.Message),
	})
	if err != nil {
		return errs.Wrap(err, errs.CategoryUnavailable, errs.ReasonUnavailable,
			"The message could not be sent. Retry in a moment, or email us directly.")
	}
	return nil
}
