package saas

import (
	"bytes"
	"context"

	"go.uber.org/zap"
)

// sendMail renders a branded transactional email and sends it.
// Mail failures are logged and never break billing/provisioning flows.
func (svc *Service) sendMail(ctx context.Context, to, subject, name string, data map[string]any) {
	if svc.mailer == nil || to == "" {
		return
	}

	body, err := svc.renderEmail(name, data)
	if err != nil {
		svc.log.Error("failed to render email", zap.String("template", name), zap.Error(err))
		return
	}

	if err = svc.mailer.Send(ctx, to, subject, body); err != nil {
		svc.log.Warn("failed to send email", zap.String("template", name), zap.Error(err))
	}
}

func (svc *Service) renderEmail(name string, data map[string]any) (string, error) {
	if data == nil {
		data = map[string]any{}
	}

	data["Brand"] = svc.cfg.Brand
	data["LogoURL"] = svc.cfg.Brand.URL("/stcloud/static/stcloud-email-logo.png")
	data["Year"] = svc.now().Year()

	buf := &bytes.Buffer{}
	if err := svc.tpl.ExecuteTemplate(buf, "email-"+name, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}
