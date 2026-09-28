package integration

import (
	"context"
	"strconv"
	"strings"
	"time"

	composeService "github.com/cortezaproject/corteza/server/compose/service"
	composeTypes "github.com/cortezaproject/corteza/server/compose/types"
	"github.com/cortezaproject/corteza/server/pkg/auth"
	"github.com/cortezaproject/corteza/server/pkg/eventbus"
	"github.com/cortezaproject/corteza/server/pkg/filter"
	"github.com/cortezaproject/corteza/server/saas"
	"github.com/cortezaproject/corteza/server/store"
)

// activity sink implemented by *saas.Service
type activitySink interface {
	RecordActivity(ctx context.Context, namespaceID uint64, ev saas.ActivityEvent)
}

type recordEvent interface {
	Record() *composeTypes.Record
	OldRecord() *composeTypes.Record
	Module() *composeTypes.Module
	Namespace() *composeTypes.Namespace
}

// trackedModules are the workspace modules the Command Deck measures
var trackedModules = map[string]bool{
	"Task": true, "Case": true, "Approval": true, "OperationsRecord": true,
	"Customer": true, "Contact": true, "Document": true,
}

// RegisterActivityHook records every create, update and delete of workspace
// records into the company's activity log
func RegisterActivityHook(eb interface {
	Register(h eventbus.HandlerFn, ops ...eventbus.HandlerRegOp) uintptr
}, sink activitySink) {
	eb.Register(func(ctx context.Context, ev eventbus.Event) error {
		e, ok := ev.(recordEvent)
		if !ok {
			return nil
		}

		var (
			rec, old = e.Record(), e.OldRecord()
			mod, ns  = e.Module(), e.Namespace()
			kind     string
		)

		switch ev.EventType() {
		case "afterCreate":
			kind = saas.ActivityCreated
		case "afterUpdate":
			kind = saas.ActivityUpdated
		case "afterDelete":
			kind = saas.ActivityDeleted
			if old != nil {
				rec = old
			}
		default:
			return nil
		}

		if rec == nil || mod == nil || ns == nil || !trackedModules[mod.Handle] {
			return nil
		}

		out := toActivity(mod.Handle, rec, kind)
		if kind == saas.ActivityUpdated && old != nil {
			if from := value(old, "Status"); from != out.ToStatus {
				out.Kind, out.FromStatus = saas.ActivityStatus, from
			}
		}

		if id := auth.GetIdentityFromContext(ctx); id != nil && id.Valid() {
			out.ActorID = id.Identity()
		}

		sink.RecordActivity(ctx, ns.ID, out)
		return nil
	}, eventbus.For("compose:record"), eventbus.On("afterCreate", "afterUpdate", "afterDelete"))
}

func value(r *composeTypes.Record, name string) string {
	if r == nil {
		return ""
	}
	if v := r.Values.Get(name, 0); v != nil {
		return strings.TrimSpace(v.Value)
	}
	return ""
}

func firstValue(r *composeTypes.Record, names ...string) string {
	for _, n := range names {
		if v := value(r, n); v != "" {
			return v
		}
	}
	return ""
}

func idValue(r *composeTypes.Record, names ...string) uint64 {
	for _, n := range names {
		if id, err := strconv.ParseUint(value(r, n), 10, 64); err == nil && id > 0 {
			return id
		}
	}
	return 0
}

func toActivity(module string, r *composeTypes.Record, kind string) saas.ActivityEvent {
	title := value(r, "Title")
	for _, n := range []string{"Subject", "Name"} {
		if title == "" {
			title = value(r, n)
		}
	}
	if title == "" {
		title = strings.TrimSpace(value(r, "FirstName") + " " + value(r, "LastName"))
	}

	ev := saas.ActivityEvent{
		Module:       module,
		RecordID:     r.ID,
		Title:        title,
		Kind:         kind,
		ToStatus:     value(r, "Status"),
		AssigneeID:   idValue(r, "AssignedTo", "Approver", "Owner", "AccountOwner"),
		DepartmentID: idValue(r, "Department"),
		TeamID:       idValue(r, "Team"),
		Category:     firstValue(r, "Type", "Category"),
		Priority:     value(r, "Priority"),
		CustomerID:   idValue(r, "Customer"),
		CaseID:       idValue(r, "Case"),
	}
	if module == "Customer" {
		ev.CustomerID = r.ID
	}
	if module == "Case" {
		ev.CaseID = r.ID
	}

	if due := value(r, "DueDate"); due != "" {
		for _, layout := range []string{"2006-01-02", time.RFC3339} {
			if t, err := time.Parse(layout, due); err == nil {
				ev.DueAt = &t
				break
			}
		}
	}

	return ev
}

// WorkspaceSnapshot turns the current workspace records into activity: a
// creation event and, when the status has moved on, a status event at the
// last update. Used once for workspaces that predate the Command Deck.
func (p *Platform) WorkspaceSnapshot(ctx context.Context, c *saas.Company) ([]saas.ActivityEvent, error) {
	if c.NamespaceID == 0 {
		return nil, nil
	}

	ctx = sys(ctx)
	mm, _, err := store.SearchComposeModules(ctx, p.Store, composeTypes.ModuleFilter{NamespaceID: c.NamespaceID})
	if err != nil {
		return nil, err
	}

	var out []saas.ActivityEvent
	for _, m := range mm {
		if !trackedModules[m.Handle] {
			continue
		}

		rr, _, err := composeService.DefaultRecord.Find(ctx, composeTypes.RecordFilter{
			NamespaceID: c.NamespaceID, ModuleID: m.ID, Paging: filter.Paging{Limit: 5000},
		})
		if err != nil {
			return nil, err
		}

		for _, r := range rr {
			created := toActivity(m.Handle, r, saas.ActivityCreated)
			created.OccurredAt = r.CreatedAt
			created.ActorID = r.CreatedBy
			status := created.ToStatus

			if r.UpdatedAt != nil && status != "" && r.UpdatedAt.After(r.CreatedAt) {
				created.ToStatus = ""
				moved := created
				moved.Kind, moved.FromStatus, moved.ToStatus = saas.ActivityStatus, "", status
				moved.OccurredAt, moved.ActorID = *r.UpdatedAt, r.UpdatedBy
				out = append(out, created, moved)
				continue
			}

			out = append(out, created)
		}
	}

	return out, nil
}

// WorkspaceLookups resolves department names and each module's record page
func (p *Platform) WorkspaceLookups(ctx context.Context, c *saas.Company) (saas.WorkspaceLookup, error) {
	lk := saas.WorkspaceLookup{Departments: map[uint64]string{}, RecordPages: map[string]uint64{},
		Teams: map[uint64]string{}, TeamDepartment: map[uint64]uint64{}, DepartmentManager: map[uint64]uint64{}}
	if c.NamespaceID == 0 {
		return lk, nil
	}

	ctx = sys(ctx)
	mm, _, err := store.SearchComposeModules(ctx, p.Store, composeTypes.ModuleFilter{NamespaceID: c.NamespaceID})
	if err != nil {
		return lk, err
	}

	pp, _, err := store.SearchComposePages(ctx, p.Store, composeTypes.PageFilter{NamespaceID: c.NamespaceID})
	if err != nil {
		return lk, err
	}

	for _, m := range mm {
		for _, pg := range pp {
			if pg.ModuleID == m.ID {
				lk.RecordPages[m.Handle] = pg.ID
				break
			}
		}

		if m.Handle != "Department" && m.Handle != "Team" {
			continue
		}

		rr, _, err := composeService.DefaultRecord.Find(ctx, composeTypes.RecordFilter{
			NamespaceID: c.NamespaceID, ModuleID: m.ID, Paging: filter.Paging{Limit: 1000},
		})
		if err != nil {
			continue
		}
		for _, r := range rr {
			if m.Handle == "Department" {
				lk.Departments[r.ID] = value(r, "Name")
				if mgr := idValue(r, "Manager"); mgr > 0 {
					lk.DepartmentManager[r.ID] = mgr
				}
				continue
			}
			lk.Teams[r.ID] = value(r, "Name")
			if d := idValue(r, "Department"); d > 0 {
				lk.TeamDepartment[r.ID] = d
			}
		}
	}

	return lk, nil
}
