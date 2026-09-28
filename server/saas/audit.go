package saas

import (
	"context"
	"strconv"
	"time"

	"go.uber.org/zap"
)

const (
	ActorFounder = "founder"
	ActorUser    = "user"
	ActorSystem  = "system"
	ActorStripe  = "stripe"
	ActorPublic  = "public"

	ResultSuccess = "success"
	ResultFailure = "failure"
	ResultDenied  = "denied"
)

// forbidden metadata keys: never persist secrets even if a caller passes them
var sensitiveKeys = map[string]bool{
	"password": true, "token": true, "secret": true, "session": true,
	"card": true, "cvc": true, "authorization": true, "cookie": true,
}

// audit records a platform audit event. Failures to write the audit log are
// logged but never break the calling operation.
func (svc *Service) audit(ctx context.Context, e AuditEntry) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = svc.now()
	}

	for k := range e.Metadata {
		if sensitiveKeys[k] {
			delete(e.Metadata, k)
		}
	}

	if err := svc.repo.InsertAudit(ctx, &e); err != nil {
		svc.log.Error("failed to write audit log", zap.String("action", e.Action), zap.Error(err))
	}

	svc.log.Info("audit",
		zap.String("action", e.Action),
		zap.String("actorType", e.ActorType),
		zap.String("actor", e.ActorLabel),
		zap.Uint64("companyID", e.CompanyID),
		zap.String("target", e.Target),
		zap.String("result", e.Result),
	)
}

func founderActor(f *Founder, ip string) AuditEntry {
	if f == nil {
		return AuditEntry{ActorType: ActorFounder, Role: "Founder", IP: ip}
	}

	return AuditEntry{
		ActorType: ActorFounder,
		ActorID:   strconv.FormatUint(f.ID, 10),
		Role:      "Founder",
		IP:        ip,
	}
}

func userActor(userID uint64, role CompanyRole, companyID uint64, ip string) AuditEntry {
	return AuditEntry{
		ActorType: ActorUser,
		ActorID:   strconv.FormatUint(userID, 10),
		Role:      role.Label(),
		CompanyID: companyID,
		IP:        ip,
	}
}

func (e AuditEntry) with(action, target, result string, meta map[string]string) AuditEntry {
	e.Action = action
	e.Target = target
	e.Result = result
	e.Metadata = meta
	e.OccurredAt = time.Time{}
	return e
}

// labelAudit resolves user and company names for display so the Founder
// sees who did what instead of internal identifiers
func (svc *Service) labelAudit(ctx context.Context, ee []*AuditEntry) []*AuditEntry {
	var (
		userIDs   []uint64
		companies = map[uint64]string{}
	)

	for _, e := range ee {
		if e.ActorType == ActorUser && e.ActorLabel == "" {
			if id, err := strconv.ParseUint(e.ActorID, 10, 64); err == nil {
				userIDs = append(userIDs, id)
			}
		}

		if e.CompanyID > 0 {
			companies[e.CompanyID] = ""
		}
	}

	users := map[uint64]UserInfo{}
	if len(userIDs) > 0 {
		if uu, err := svc.platform.Users(ctx, userIDs...); err == nil {
			users = uu
		}
	}

	for id := range companies {
		if c, err := svc.repo.CompanyByID(ctx, id); err == nil && c != nil {
			companies[id] = c.Name
		}
	}

	for _, e := range ee {
		if e.ActorType == ActorUser && e.ActorLabel == "" {
			if id, err := strconv.ParseUint(e.ActorID, 10, 64); err == nil {
				if u, ok := users[id]; ok {
					e.ActorLabel = u.Email
				}
			}
		}

		e.CompanyName = companies[e.CompanyID]
	}

	return ee
}
