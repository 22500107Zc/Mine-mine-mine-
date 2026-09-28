package saas

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/cortezaproject/corteza/server/pkg/auth"
	"go.uber.org/zap"
)

type (
	gateDecision int

	// gateRequest carries everything a rule needs to decide
	gateRequest struct {
		method  string
		path    string // path relative to the API base, e.g. /compose/namespace/123/module/
		userID  uint64
		company *Company
		member  *Member
		// target user of a single-user read (checked against company members)
		targetUser uint64
	}
)

const (
	gateDeny gateDecision = iota
	gateAllow
	// allow and filter JSON response set to the company
	gateFilterNamespaces
	gateFilterUsers
	gateFilterRoles
	gateFilterApps
	// allow only when the request body passes validation
	gateValidateRules
)

var (
	reNsScoped       = regexp.MustCompile(`^/compose/namespace/(\d+)(/.*)?$`)
	reComposeRules   = regexp.MustCompile(`^/compose/permissions/(\d+)/rules$`)
	reSystemUser     = regexp.MustCompile(`^/system/users/(\d+)(/[a-z-]+)?/?$`)
	reSystemRole     = regexp.MustCompile(`^/system/roles/(\d+)/?$`)
	reAppFlag        = regexp.MustCompile(`^/system/application/\d+/flag/(\d+)/[^/]+$`)
	reNamespaceInRes = regexp.MustCompile(`^corteza::compose:[a-z-]+/(\d+)(/.*)?$`)
)

// APIGate is the server-side enforcement point for subscription state and
// tenant isolation on the JSON API. It must be mounted after the JWT verifier
// (identity in context) on the API route.
//
// Users that belong to a company are:
//   - blocked entirely when the company is disabled or unpaid,
//   - restricted to their own workspace (namespace), users and roles,
//   - denied platform-level administration endpoints.
//
// Users without company membership are platform staff and are not affected.
func (svc *Service) APIGate(apiBase string) func(http.Handler) http.Handler {
	apiBase = "/" + strings.Trim(apiBase, "/")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ident := auth.GetIdentityFromContext(r.Context())
			if ident == nil || !ident.Valid() || ident.Identity() == 0 {
				next.ServeHTTP(w, r)
				return
			}

			uid := ident.Identity()
			c, m, d, err := svc.AccessForUser(r.Context(), uid)
			if err != nil {
				svc.log.Error("gate access lookup failed", zap.Error(err))
				apiError(w, http.StatusServiceUnavailable, "Service temporarily unavailable.")
				return
			}

			if c == nil {
				// platform staff
				next.ServeHTTP(w, r)
				return
			}

			rel := r.URL.Path
			if apiBase != "/" {
				rel = strings.TrimPrefix(rel, apiBase)
			}

			if !strings.HasPrefix(rel, "/") {
				rel = "/" + rel
			}

			switch d.Level {
			case AccessNone:
				// lets signed-in web applications move the user to the right page
				w.Header().Set(accessHeader, "disabled")
				apiError(w, http.StatusForbidden, "Access to this workspace is currently unavailable.")
				return
			case AccessBillingOnly:
				if !billingOnlyAllowed(r.Method, rel) {
					w.Header().Set(accessHeader, "billing")
					apiError(w, http.StatusPaymentRequired, "An active "+svc.cfg.Brand.ProductName+" subscription is required. Visit Billing to restore access.")
					return
				}
			}

			svc.touchActivity(r.Context(), c.ID)

			gr := &gateRequest{method: r.Method, path: rel, userID: uid, company: c, member: m}
			switch decide(gr) {
			case gateAllow:
				next.ServeHTTP(w, r)

			case gateFilterNamespaces:
				svc.filtered(w, r, next, "namespaceID", map[uint64]bool{c.NamespaceID: true})

			case gateFilterUsers:
				members, err := svc.memberSet(r, c.ID)
				if err != nil {
					apiError(w, http.StatusServiceUnavailable, "Service temporarily unavailable.")
					return
				}

				if gr.targetUser > 0 && !members[gr.targetUser] {
					apiError(w, http.StatusNotFound, "Not found.")
					return
				}
				restrictQueryIDs(r, "userID", members)
				svc.filtered(w, r, next, "userID", members)

			case gateFilterRoles:
				roles := companyRoles(c)
				restrictQueryIDs(r, "roleID", roles)
				svc.filtered(w, r, next, "roleID", roles)

			case gateFilterApps:
				svc.filteredApps(w, r, next)

			case gateValidateRules:
				if !validRulesBody(r, c.NamespaceID) {
					svc.audit(r.Context(), userActor(uid, m.Role, c.ID, clientIP(r)).with("api.permissions", rel, ResultDenied, nil))
					apiError(w, http.StatusForbidden, "Not allowed.")
					return
				}
				next.ServeHTTP(w, r)

			default:
				// Deliberately indistinguishable from a missing resource so
				// that IDs from other companies cannot be probed
				apiError(w, http.StatusNotFound, "Not found.")
			}
		})
	}
}

// decide contains the isolation rules for company users with active access
func decide(gr *gateRequest) gateDecision {
	var (
		p    = strings.TrimRight(gr.path, "/")
		get  = gr.method == http.MethodGet || gr.method == http.MethodHead
		role = gr.member.Role
	)

	if p == "" {
		return gateDeny
	}

	switch {
	// --- workspace (compose) ---------------------------------------------
	case p == "/compose/namespace":
		if get {
			return gateFilterNamespaces
		}
		return gateDeny

	case reNsScoped.MatchString(p):
		sm := reNsScoped.FindStringSubmatch(p)
		nsID, _ := strconv.ParseUint(sm[1], 10, 64)
		if gr.company.NamespaceID == 0 || nsID != gr.company.NamespaceID {
			return gateDeny
		}

		rest := sm[2]
		// the workspace itself cannot be deleted or cloned by company users
		if (rest == "" && gr.method == http.MethodDelete) || strings.HasPrefix(rest, "/clone") {
			return gateDeny
		}

		return gateAllow

	case p == "/compose/permissions" || p == "/compose/permissions/effective":
		return gateAllow

	case reComposeRules.MatchString(p):
		roleID, _ := strconv.ParseUint(reComposeRules.FindStringSubmatch(p)[1], 10, 64)
		if !companyRoles(gr.company)[roleID] || !(role == RoleOwner || role == RoleAdministrator) {
			return gateDeny
		}
		if get {
			return gateAllow
		}
		return gateValidateRules

	case p == "/compose/notification/email":
		return gateAllow

	case strings.HasPrefix(p, "/compose/icon") && get:
		return gateAllow

	case p == "/compose/automation" && get:
		return gateAllow

	// --- identity, preferences, notifications ---------------------------
	case strings.HasPrefix(p, "/system/auth/clients"), p == "/system/auth/impersonate":
		return gateDeny

	case strings.HasPrefix(p, "/system/auth"), strings.HasPrefix(p, "/system/locale"):
		return gateAllow

	case p == "/system/settings/current" && get:
		return gateAllow

	case p == "/system/application" && get:
		return gateFilterApps

	case strings.HasPrefix(p, "/system/application/") && get:
		return gateAllow

	case reAppFlag.MatchString(p):
		owner, _ := strconv.ParseUint(reAppFlag.FindStringSubmatch(p)[1], 10, 64)
		if owner == gr.userID {
			return gateAllow
		}
		return gateDeny

	case strings.HasPrefix(p, "/system/attachment/") && get:
		return gateAllow

	case p == "/system/notification" || strings.HasPrefix(p, "/system/notification/"),
		p == "/system/reminder" || strings.HasPrefix(p, "/system/reminder/"):
		return gateAllow

	case p == "/system/permissions" || p == "/system/permissions/effective":
		return gateAllow

	case p == "/system/expressions/evaluate", p == "/system/automation" && get:
		return gateAllow

	case p == "/system/users":
		if get {
			return gateFilterUsers
		}
		return gateDeny

	case reSystemUser.MatchString(p):
		sm := reSystemUser.FindStringSubmatch(p)
		target, _ := strconv.ParseUint(sm[1], 10, 64)
		sub := sm[2]

		if target == gr.userID {
			switch sub {
			case "", "/avatar", "/avatar-initial", "/password", "/membership", "/credentials", "/sessions":
				return gateAllow
			}
			return gateDeny
		}

		// other users: read-only, and only within the company
		if get && sub == "" {
			gr.targetUser = target
			return gateFilterUsers
		}

		return gateDeny

	case p == "/system/roles":
		if get {
			return gateFilterRoles
		}
		return gateDeny

	case reSystemRole.MatchString(p):
		roleID, _ := strconv.ParseUint(reSystemRole.FindStringSubmatch(p)[1], 10, 64)
		if get && companyRoles(gr.company)[roleID] {
			return gateAllow
		}
		return gateDeny

	// --- automation: only what the UI needs at runtime ---------------------
	case p == "/automation/event-types", p == "/automation/functions", p == "/automation/types",
		p == "/automation/permissions/effective", p == "/automation/permissions",
		p == "/automation/sessions/prompts":
		return gateAllow

	case strings.HasPrefix(p, "/automation/sessions/") && strings.Contains(p, "/state/"):
		return gateAllow

	// --- realtime & integrations -------------------------------------------
	case strings.HasPrefix(p, "/websocket"):
		return gateAllow

	case strings.HasPrefix(p, "/gateway"):
		return gateAllow
	}

	// everything else (platform administration, other tenants' data,
	// federation, discovery, DAL, templates, reports...) is denied
	return gateDeny
}

// billingOnlyAllowed lists API calls permitted while a company can only
// access billing/account recovery
func billingOnlyAllowed(method, rel string) bool {
	p := strings.TrimRight(rel, "/")
	switch {
	case strings.HasPrefix(p, "/system/auth"), strings.HasPrefix(p, "/system/locale"):
		return true
	case p == "/system/settings/current" && method == http.MethodGet:
		return true
	}

	return false
}

func companyRoles(c *Company) map[uint64]bool {
	out := map[uint64]bool{}
	for _, id := range []uint64{c.RoleOwnerID, c.RoleAdminID, c.RoleManagerID, c.RoleEmployeeID} {
		if id > 0 {
			out[id] = true
		}
	}

	return out
}

func (svc *Service) memberSet(r *http.Request, companyID uint64) (map[uint64]bool, error) {
	mm, err := svc.repo.Members(r.Context(), companyID)
	if err != nil {
		return nil, err
	}

	out := make(map[uint64]bool, len(mm))
	for _, m := range mm {
		out[m.UserID] = true
	}

	return out, nil
}

// restrictQueryIDs narrows list queries (userID[] / roleID[]) to allowed IDs
// so that pagination happens on the already-isolated set
func restrictQueryIDs(r *http.Request, key string, allowed map[uint64]bool) {
	q := r.URL.Query()
	requested := append(q[key], q[key+"[]"]...)
	q.Del(key)
	q.Del(key + "[]")

	var ids []string
	if len(requested) > 0 {
		for _, s := range requested {
			for _, part := range strings.Split(s, ",") {
				if id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64); err == nil && allowed[id] {
					ids = append(ids, strconv.FormatUint(id, 10))
				}
			}
		}
	} else {
		for id := range allowed {
			ids = append(ids, strconv.FormatUint(id, 10))
		}
	}

	if len(ids) == 0 {
		// nothing permitted; an ID that can never exist keeps the result empty
		ids = []string{"1"}
	}

	q[key+"[]"] = ids
	r.URL.RawQuery = q.Encode()
}

// validRulesBody ensures RBAC changes submitted by company admins only target
// resources inside their own workspace
func validRulesBody(r *http.Request, nsID uint64) bool {
	if nsID == 0 {
		return false
	}

	if r.Method == http.MethodDelete {
		return true
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return false
	}

	r.Body = io.NopCloser(bytes.NewReader(raw))

	var body struct {
		Rules []struct {
			Resource string `json:"resource"`
		} `json:"rules"`
	}

	if err = json.Unmarshal(raw, &body); err != nil {
		return false
	}

	for _, rule := range body.Rules {
		sm := reNamespaceInRes.FindStringSubmatch(rule.Resource)
		if sm == nil {
			return false
		}

		if id, _ := strconv.ParseUint(sm[1], 10, 64); id != nsID {
			return false
		}
	}

	return true
}

// filtered runs the handler and removes items that do not belong to the
// company from {"response": {"set": [...]}} or a single {"response": {...}}
func (svc *Service) filtered(w http.ResponseWriter, r *http.Request, next http.Handler, key string, allowed map[uint64]bool) {
	rec := &bufferedWriter{header: http.Header{}, status: http.StatusOK}
	next.ServeHTTP(rec, r)

	for k, vv := range rec.header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		w.Header()[k] = vv
	}

	out := rec.buf.Bytes()
	if rec.status == http.StatusOK && strings.Contains(rec.header.Get("Content-Type"), "json") {
		if b, ok := filterPayload(out, key, allowed); ok {
			out = b
		} else {
			apiError(w, http.StatusNotFound, "Not found.")
			return
		}
	}

	w.WriteHeader(rec.status)
	_, _ = w.Write(out)
}

func filterPayload(raw []byte, key string, allowed map[uint64]bool) ([]byte, bool) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw, true
	}

	rsp, has := env["response"]
	if !has {
		return raw, true
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rsp, &obj); err != nil {
		return raw, true
	}

	idOf := func(item map[string]json.RawMessage) (uint64, bool) {
		v, ok := item[key]
		if !ok {
			return 0, false
		}
		s := strings.Trim(string(v), `"`)
		id, err := strconv.ParseUint(s, 10, 64)
		return id, err == nil
	}

	if setRaw, isSet := obj["set"]; isSet {
		var set []map[string]json.RawMessage
		if err := json.Unmarshal(setRaw, &set); err != nil {
			return raw, true
		}

		kept := make([]map[string]json.RawMessage, 0, len(set))
		for _, item := range set {
			if id, ok := idOf(item); ok && allowed[id] {
				kept = append(kept, item)
			}
		}

		obj["set"], _ = json.Marshal(kept)
	} else if id, ok := idOf(obj); ok && !allowed[id] {
		// single resource outside the company
		return nil, false
	}

	env["response"], _ = json.Marshal(obj)
	b, err := json.Marshal(env)
	if err != nil {
		return raw, true
	}

	return b, true
}

type bufferedWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header         { return b.header }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferedWriter) WriteHeader(s int)           { b.status = s }

// accessHeader marks API responses refused because of the company's state
const accessHeader = "X-CulpOS-Access"

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg}})
	_, _ = w.Write(b)
}

// customerApps are the applications company users see in the launcher;
// configuration tooling is reserved for platform staff
var customerApps = map[string]bool{"compose/": true, "/compose/": true, "/company": true, "/billing": true}

func (svc *Service) filteredApps(w http.ResponseWriter, r *http.Request, next http.Handler) {
	rec := &bufferedWriter{header: http.Header{}, status: http.StatusOK}
	next.ServeHTTP(rec, r)

	for k, vv := range rec.header {
		if !strings.EqualFold(k, "Content-Length") {
			w.Header()[k] = vv
		}
	}

	out := rec.buf.Bytes()
	if rec.status == http.StatusOK {
		var env struct {
			Response struct {
				Filter json.RawMessage   `json:"filter"`
				Set    []json.RawMessage `json:"set"`
			} `json:"response"`
		}

		if err := json.Unmarshal(out, &env); err == nil {
			kept := make([]json.RawMessage, 0, len(env.Response.Set))
			for _, item := range env.Response.Set {
				var app struct {
					Unify struct {
						URL string `json:"url"`
					} `json:"unify"`
				}
				if json.Unmarshal(item, &app) == nil && customerApps[app.Unify.URL] {
					kept = append(kept, item)
				}
			}

			env.Response.Set = kept
			if b, err := json.Marshal(env); err == nil {
				out = b
			}
		}
	}

	w.WriteHeader(rec.status)
	_, _ = w.Write(out)
}
