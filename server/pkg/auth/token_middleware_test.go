package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenMiddlewareRejectsInvalidToken(t *testing.T) {
	var (
		req = require.New(t)
		ok  = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	)

	verify, err := TokenVerifierMiddlewareWithSecretSigner("test-secret-test-secret-test-secret")
	req.NoError(err)

	h := verify(HttpTokenValidator("api")(ok))

	for _, accept := range []string{"", "application/json"} {
		r := httptest.NewRequest("GET", "/api/system/users/", nil)
		r.Header.Set("Authorization", "Bearer not-a-token")
		if accept != "" {
			r.Header.Set("Accept", accept)
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		req.Equal(http.StatusUnauthorized, rec.Code)
		req.Contains(rec.Header().Get("Content-Type"), "application/json")
		req.False(strings.Contains(rec.Body.String(), "development mode"), "debug output must not reach clients")
	}

	// requests without a token continue to the handler (anonymous access
	// is decided by the RBAC layer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/system/users/", nil))
	req.Equal(http.StatusNoContent, rec.Code)
}
