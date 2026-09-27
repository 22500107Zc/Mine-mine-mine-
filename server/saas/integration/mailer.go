package integration

import (
	"context"
	"strings"

	"github.com/cortezaproject/corteza/server/pkg/mail"
)

// Mailer sends CulpOS transactional email through the configured SMTP relay
type Mailer struct {
	FromAddress string
	FromName    string
}

func (m Mailer) Send(_ context.Context, to, subject, body string) error {
	msg := mail.New()
	if addr := strings.TrimSpace(m.FromAddress); addr != "" {
		msg.SetAddressHeader("From", addr, m.FromName)
	}

	msg.SetAddressHeader("To", to, "")
	msg.SetHeader("Subject", subject)
	msg.SetBody("text/html", body)
	return mail.Send(msg)
}
