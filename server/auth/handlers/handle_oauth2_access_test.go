package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cortezaproject/corteza/server/auth/request"
	"github.com/cortezaproject/corteza/server/auth/settings"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func Test_denyTokenForBlockedAccess(t *testing.T) {
	var (
		rq   = require.New(t)
		user = makeMockUser()
		to   = ""
		h    = &AuthHandlers{
			Log: zap.NewNop(),
			AccessGuard: func(context.Context, uint64) string {
				return to
			},
		}
	)

	newReq := func() *request.AuthReq {
		req := prepareClientAuthReq(h, httptest.NewRequest("POST", "/auth/oauth2/default-client", nil), nil)
		req.AuthUser = request.NewAuthUser(&settings.Settings{}, user, true)
		return req
	}

	// allowed users get their token as usual
	rq.False(h.denyToken(newReq()))

	// blocked users receive an OAuth2 error the web application can act on,
	// never a redirect that a background request cannot follow
	to = "/account/disabled"
	req := newReq()
	rq.True(h.denyToken(req))

	rec := req.Response.(*httptest.ResponseRecorder)
	rq.Equal(http.StatusForbidden, rec.Code)
	rq.Equal(to, rec.Header().Get("X-Access-Location"))
	rq.Empty(rec.Header().Get("Location"))

	var body map[string]string
	rq.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	rq.Equal("access_denied", body["error"])
	rq.Equal(to, body["location"])
}
