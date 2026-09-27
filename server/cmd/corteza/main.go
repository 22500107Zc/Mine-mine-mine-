package main

import (
	// Embed the IANA timezone database into the binary so timezone-aware
	// features (e.g. record export) work in minimal deploy images that
	// don't ship /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/cortezaproject/corteza/server/app"
	"github.com/cortezaproject/corteza/server/pkg/cli"
	"github.com/cortezaproject/corteza/server/pkg/logger"
	"github.com/cortezaproject/corteza/server/saas"
)

func main() {
	// Initialize logger before any other action
	logger.Init()

	// Map CulpOS deployment variables (DATABASE_URL, SMTP_USERNAME, MAIL_FROM, APP_URL...)
	saas.ApplyEnvAliases()

	cli.HandleError(app.New().Execute())
}
