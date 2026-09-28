// culpos-fixture writes generated operational history into one company of a
// TEST or DEMO CulpOS database, for screenshots and local development.
//
// It is not part of the CulpOS server and must never be run against a
// production database: it refuses to run unless CULPOS_FIXTURE_CONFIRM is
// set to "test-environment", and every event it writes is marked with
// source = 'fixture' so it can always be identified and removed:
//
//	DELETE FROM saas_activity_events WHERE source = 'fixture';
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/cortezaproject/corteza/server/saas/fixture"
)

func main() {
	var (
		dsn     = flag.String("db", "", "PostgreSQL connection string of a TEST database")
		company = flag.Uint64("company", 0, "company ID")
		days    = flag.Int("days", 400, "days of history")
		volume  = flag.Float64("volume", 1, "work volume (1 ≈ 24 new items per weekday)")
		seed    = flag.Int64("seed", 42, "random seed")
		people  = flag.String("people", "", "comma-separated user IDs of company members")
		depts   = flag.String("departments", "", "id:Name,… (workspace Department records)")
		teams   = flag.String("teams", "", "id:Name:departmentID,… (workspace Team records, in order: intake, review, approvals, support)")
	)
	flag.Parse()

	if os.Getenv("CULPOS_FIXTURE_CONFIRM") != "test-environment" {
		fail("refusing to write synthetic history: set CULPOS_FIXTURE_CONFIRM=test-environment (test and demo databases only)")
	}
	if *dsn == "" || *company == 0 {
		fail("-db and -company are required")
	}

	var pp []uint64
	for _, s := range split(*people) {
		id, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			fail("bad -people")
		}
		pp = append(pp, id)
	}
	org := fixture.DefaultOrg(pp)
	if *depts != "" && *teams != "" {
		org.Departments, org.Teams, org.TeamDept = map[uint64]string{}, map[uint64]string{}, map[uint64]uint64{}
		for _, d := range split(*depts) {
			p := strings.SplitN(d, ":", 2)
			id, _ := strconv.ParseUint(p[0], 10, 64)
			org.Departments[id] = p[1]
		}
		var ids []uint64
		for _, t := range split(*teams) {
			p := strings.SplitN(t, ":", 3)
			id, _ := strconv.ParseUint(p[0], 10, 64)
			dep, _ := strconv.ParseUint(p[2], 10, 64)
			org.Teams[id], org.TeamDept[id] = p[1], dep
			ids = append(ids, id)
		}
		if len(ids) != 4 {
			fail("-teams needs exactly four teams: intake, review, approvals, support")
		}
		// the generator addresses teams by role; map them onto the real IDs
		members := map[uint64][]uint64{}
		for i, p := range pp {
			members[ids[i%4]] = append(members[ids[i%4]], p)
		}
		org.Members = members
		fixture.TeamIDs = [4]uint64{ids[0], ids[1], ids[2], ids[3]}
	}

	db, err := sql.Open("postgres", *dsn)
	if err != nil {
		fail(err.Error())
	}
	defer db.Close()

	var name string
	if err = db.QueryRow(`SELECT name FROM saas_companies WHERE id = $1`, *company).Scan(&name); err != nil {
		fail("company not found: " + err.Error())
	}

	ee := fixture.Generate(fixture.Options{Now: time.Now().UTC(), Days: *days, Volume: *volume, Seed: *seed, Org: org, FirstID: 900_000_000_000})
	tx, err := db.Begin()
	if err != nil {
		fail(err.Error())
	}
	stmt, err := tx.Prepare(`INSERT INTO saas_activity_events (company_id, occurred_at, module, record_id, title, kind, from_status, to_status,
		actor_id, assignee_id, department_id, due_at, source, team_id, category, priority, customer_id, case_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'fixture',$13,$14,$15,$16,$17)`)
	if err != nil {
		fail(err.Error())
	}
	for _, e := range ee {
		if _, err = stmt.Exec(*company, e.OccurredAt, e.Module, e.RecordID, e.Title, e.Kind, e.FromStatus, e.ToStatus, e.ActorID, e.AssigneeID,
			e.DepartmentID, e.DueAt, e.TeamID, e.Category, e.Priority, e.CustomerID, e.CaseID); err != nil {
			_ = tx.Rollback()
			fail(err.Error())
		}
	}
	if _, err = tx.Exec(`UPDATE saas_companies SET activity_backfilled_at = COALESCE(activity_backfilled_at, NOW()) WHERE id = $1`, *company); err != nil {
		_ = tx.Rollback()
		fail(err.Error())
	}
	if err = tx.Commit(); err != nil {
		fail(err.Error())
	}
	fmt.Printf("wrote %d synthetic events (source='fixture') for %s\n", len(ee), name)
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "culpos-fixture:", msg)
	os.Exit(1)
}
