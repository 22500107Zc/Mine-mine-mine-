package integration

import (
	"context"
	"encoding/json"
	"strings"

	composeTypes "github.com/cortezaproject/corteza/server/compose/types"
	"github.com/cortezaproject/corteza/server/pkg/auth"
	"github.com/cortezaproject/corteza/server/store"
	systemTypes "github.com/cortezaproject/corteza/server/system/types"
	"go.uber.org/zap"
)

// Colors used by the email templates before the product rename, mapped to
// the current brand palette
var legacyEmailColors = strings.NewReplacer(
	"#0E1A2B", "#0B0F13",
	"#D4952B", "#00E0C0",
	"#1F4E8C", "#00866F",
)

// MigrateBrand rewrites the previous product name (and description) in content that was
// provisioned into the database before the rename: email templates, company
// workspace pages and namespace subtitles, the built-in sign-in client and the
// system users. Only the old name (and the old email colors) is replaced, so
// anything an administrator wrote around it is preserved. It is idempotent.
func (p *Platform) MigrateBrand(ctx context.Context, oldName, newName, oldDescription, newDescription string) {
	if oldName == "" || oldName == newName {
		return
	}

	rename := strings.NewReplacer(oldName, newName, oldDescription, newDescription)
	changed := 0

	// Email templates
	if tt, _, err := store.SearchTemplates(ctx, p.Store, systemTypes.TemplateFilter{}); err == nil {
		for _, t := range tt {
			if !strings.Contains(t.Template, oldName) && !strings.Contains(t.Meta.Short, oldName) && !strings.Contains(t.Meta.Description, oldName) {
				continue
			}

			t.Template = legacyEmailColors.Replace(rename.Replace(t.Template))
			t.Meta.Short = rename.Replace(t.Meta.Short)
			t.Meta.Description = rename.Replace(t.Meta.Description)
			if err = store.UpdateTemplate(ctx, p.Store, t); err != nil {
				p.Log.Warn("brand migration: template", zap.String("handle", t.Handle), zap.Error(err))
				continue
			}
			changed++
		}
	}

	// Workspace namespaces (company workspaces and the template)
	if nn, _, err := store.SearchComposeNamespaces(ctx, p.Store, composeTypes.NamespaceFilter{}); err == nil {
		for _, ns := range nn {
			if !strings.Contains(ns.Name, oldName) && !strings.Contains(ns.Meta.Subtitle, oldName) {
				continue
			}

			ns.Name = rename.Replace(ns.Name)
			ns.Meta.Subtitle = rename.Replace(ns.Meta.Subtitle)
			if err = store.UpdateComposeNamespace(ctx, p.Store, ns); err != nil {
				p.Log.Warn("brand migration: namespace", zap.String("slug", ns.Slug), zap.Error(err))
				continue
			}
			changed++
		}
	}

	// Workspace pages (welcome and billing content blocks)
	if pp, _, err := store.SearchComposePages(ctx, p.Store, composeTypes.PageFilter{}); err == nil {
		for _, pg := range pp {
			raw, err := json.Marshal(pg.Blocks)
			if err != nil || (!strings.Contains(string(raw), oldName) && !strings.Contains(pg.Title, oldName) && !strings.Contains(pg.Description, oldName)) {
				continue
			}

			var blocks composeTypes.PageBlocks
			if err = json.Unmarshal([]byte(rename.Replace(string(raw))), &blocks); err != nil {
				continue
			}

			pg.Blocks = blocks
			pg.Title = rename.Replace(pg.Title)
			pg.Description = rename.Replace(pg.Description)
			if err = store.UpdateComposePage(ctx, p.Store, pg); err != nil {
				p.Log.Warn("brand migration: page", zap.Uint64("pageID", pg.ID), zap.Error(err))
				continue
			}
			changed++
		}
	}

	// Built-in sign-in client
	if cc, _, err := store.SearchAuthClients(ctx, p.Store, systemTypes.AuthClientFilter{}); err == nil {
		for _, c := range cc {
			if c.Meta == nil || !strings.Contains(c.Meta.Name, oldName) {
				continue
			}

			c.Meta.Name = rename.Replace(c.Meta.Name)
			if err = store.UpdateAuthClient(ctx, p.Store, c); err == nil {
				changed++
			}
		}
	}

	// System users (shown as the author of provisioned content)
	for _, h := range []string{auth.ProvisionUserHandle, auth.ServiceUserHandle, auth.FederationUserHandle} {
		if u, err := store.LookupUserByHandle(ctx, p.Store, h); err == nil && strings.Contains(u.Name, oldName) {
			u.Name = rename.Replace(u.Name)
			if err = store.UpdateUser(ctx, p.Store, u); err == nil {
				changed++
			}
		}
	}

	if changed > 0 {
		p.Log.Info("brand migration applied", zap.String("from", oldName), zap.String("to", newName), zap.Int("objects", changed))
	}
}
