package saas

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// healthHandler reports deployment health without exposing configuration
// values, hosts, credentials or error details
func (svc *Service) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var (
		db     = "ok"
		status = http.StatusOK
	)

	if err := svc.repo.Ping(ctx); err != nil {
		db = "unavailable"
		status = http.StatusServiceUnavailable
	}

	configured := func(ok bool) string {
		if ok {
			return "configured"
		}
		return "not_configured"
	}

	body := map[string]any{
		"status":   map[bool]string{true: "ok", false: "unavailable"}[status == http.StatusOK],
		"product":  svc.cfg.Brand.ProductName,
		"version":  Version,
		"database": db,
		"billing":  configured(svc.cfg.StripeConfigured()),
		"email":    configured(MailConfigured()),
		"time":     svc.now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
