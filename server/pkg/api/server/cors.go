package server

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/cors"
)

// allowedOrigins returns the origins allowed to call the API with credentials.
//
// When the public application URL is configured (APP_URL / PUBLIC_APP_URL) only
// that origin and any origins listed in CORS_ALLOWED_ORIGINS are allowed.
// Without configuration (local development) any origin is allowed.
func allowedOrigins() []string {
	var out []string
	for _, key := range []string{"APP_URL", "PUBLIC_APP_URL"} {
		if u, err := url.Parse(strings.TrimSpace(os.Getenv(key))); err == nil && u.Scheme != "" && u.Host != "" {
			out = append(out, u.Scheme+"://"+u.Host)
		}
	}

	for _, o := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			out = append(out, o)
		}
	}

	if len(out) == 0 {
		return []string{"http://*", "https://*"}
	}

	return out
}

// Sets up default CORS rules to use as a middleware
func handleCORS(next http.Handler) http.Handler {
	return cors.New(cors.Options{
		AllowedOrigins: allowedOrigins(),
		AllowedMethods: []string{
			http.MethodHead,
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
		},
		AllowedHeaders: []string{
			"Accept",
			"Authorization",
			"Content-Type",
			"X-CSRF-ID",
		},
		AllowCredentials: true,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	}).Handler(next)
}

// securityHeaders sets baseline security headers on every response
func securityHeaders(sslTerminated bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
			if sslTerminated || r.TLS != nil {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
