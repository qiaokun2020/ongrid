package store

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	bizreport "github.com/ongridio/ongrid/internal/manager/biz/report"
)

// The facts collector queries tables owned by other domains by name.
// For the test we create minimal tables with just the columns the
// collector reads — this keeps the test decoupled from the full
// alert/audit/edge models while exercising the real SQL.
func newFactsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE alert_incidents (
			id INTEGER PRIMARY KEY, rule_name TEXT, severity TEXT, status TEXT,
			device_id INTEGER, first_fired_at DATETIME, resolved_at DATETIME,
			deleted_at DATETIME)`,
		`CREATE TABLE chat_mutating_proposals (
			id TEXT PRIMARY KEY, tool_name TEXT, decision TEXT, created_at DATETIME)`,
		`CREATE TABLE audit_logs (
			id INTEGER PRIMARY KEY, occurred_at DATETIME, status TEXT)`,
		`CREATE TABLE edges (id INTEGER PRIMARY KEY, status TEXT, deleted_at DATETIME)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return db
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestFactsCollector_Collect(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()

	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	prev := bizreport.Period{
		Start: mustParse(t, "2026-05-25T00:00:00Z"),
		End:   mustParse(t, "2026-06-01T00:00:00Z"),
	}

	// 3 incidents in period, 2 resolved (durations 30 + 90 → MTTR 60),
	// 1 still open. 1 incident in prev period (for delta).
	db.Exec(`INSERT INTO alert_incidents VALUES
		(1,'CPU High','warning','resolved',7,'2026-06-02T10:00:00Z','2026-06-02T10:30:00Z',NULL),
		(2,'Disk Full','critical','resolved',9,'2026-06-04T08:00:00Z','2026-06-04T09:30:00Z',NULL),
		(3,'OOM','warning','open',7,'2026-06-06T12:00:00Z',NULL,NULL),
		(4,'Old','warning','resolved',7,'2026-05-28T00:00:00Z','2026-05-28T00:10:00Z',NULL)`)

	// proposals: 2 approved restart_service + 1 rejected disk_cleanup in period.
	db.Exec(`INSERT INTO chat_mutating_proposals VALUES
		('p1','restart_service','approve','2026-06-03T00:00:00Z'),
		('p2','restart_service','approve','2026-06-03T01:00:00Z'),
		('p3','disk_cleanup','reject','2026-06-05T00:00:00Z')`)

	db.Exec(`INSERT INTO audit_logs VALUES
		(1,'2026-06-02T00:00:00Z','success'),
		(2,'2026-06-03T00:00:00Z','success'),
		(3,'2026-06-03T00:00:00Z','failure')`)

	db.Exec(`INSERT INTO edges VALUES (1,'online',NULL),(2,'online',NULL),(3,'offline',NULL)`)

	fc := NewFactsCollector(db)
	facts, err := fc.Collect(ctx, period, prev, bizreport.Scope{})
	if err != nil {
		t.Fatal(err)
	}

	if len(facts.Incidents) != 3 {
		t.Errorf("incidents = %d, want 3", len(facts.Incidents))
	}
	// MTTR over the 2 resolved = (30+90)/2 = 60.
	var mttr float64
	var incidentsHero, resolvedHero, actionsHero *bizreport.HeroStat
	for i := range facts.Hero {
		switch facts.Hero[i].Key {
		case "mttr_minutes":
			mttr = facts.Hero[i].Value
		case "incidents":
			incidentsHero = &facts.Hero[i]
		case "resolved":
			resolvedHero = &facts.Hero[i]
		case "actions":
			actionsHero = &facts.Hero[i]
		}
	}
	if mttr != 60 {
		t.Errorf("MTTR = %v, want 60", mttr)
	}
	if incidentsHero == nil || incidentsHero.Value != 3 {
		t.Errorf("incidents hero = %+v, want 3", incidentsHero)
	}
	// delta vs prev (1 incident): (3-1)/1*100 = 200%.
	if incidentsHero.DeltaPct == nil || *incidentsHero.DeltaPct != 200 {
		t.Errorf("incidents delta = %+v, want 200", incidentsHero.DeltaPct)
	}
	if resolvedHero == nil || resolvedHero.Value != 2 {
		t.Errorf("resolved hero = %+v, want 2", resolvedHero)
	}
	// actions = mutating(3) + safe(2 success audit) = 5.
	if actionsHero == nil || actionsHero.Value != 5 {
		t.Errorf("actions hero = %+v, want 5", actionsHero)
	}

	if facts.Actions.MutatingTotal != 3 || facts.Actions.MutatingApproved != 2 {
		t.Errorf("actions = %+v, want total 3 approved 2", facts.Actions)
	}
	if facts.Edges.Online != 2 || facts.Edges.Total != 3 {
		t.Errorf("edges = %+v, want online 2 total 3", facts.Edges)
	}
	if facts.AlertCounts["warning"] != 2 || facts.AlertCounts["critical"] != 1 {
		t.Errorf("alert counts = %+v", facts.AlertCounts)
	}
	// sparkline has 7 daily buckets (7-day period).
	if len(incidentsHero.Sparkline) != 7 {
		t.Errorf("sparkline len = %d, want 7", len(incidentsHero.Sparkline))
	}
}

func TestFactsCollector_EdgeScopeFilter(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()
	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	db.Exec(`INSERT INTO alert_incidents VALUES
		(1,'A','warning','resolved',7,'2026-06-02T10:00:00Z','2026-06-02T10:30:00Z',NULL),
		(2,'B','warning','resolved',9,'2026-06-03T10:00:00Z','2026-06-03T10:30:00Z',NULL)`)

	fc := NewFactsCollector(db)
	// Scope to device 7 only.
	facts, err := fc.Collect(ctx, period, period, bizreport.Scope{EdgeIDs: []uint64{7}})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Incidents) != 1 || facts.Incidents[0].DeviceID != 7 {
		t.Errorf("edge scope not applied: %+v", facts.Incidents)
	}
}

func TestFactsCollector_SeverityScopeFilter(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()
	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	db.Exec(`INSERT INTO alert_incidents VALUES
		(1,'A','info','resolved',7,'2026-06-02T10:00:00Z','2026-06-02T10:30:00Z',NULL),
		(2,'B','warning','resolved',7,'2026-06-03T10:00:00Z','2026-06-03T10:30:00Z',NULL),
		(3,'C','critical','resolved',7,'2026-06-04T10:00:00Z','2026-06-04T10:30:00Z',NULL)`)

	fc := NewFactsCollector(db)
	// severity_min=warning → drop the info incident.
	facts, _ := fc.Collect(ctx, period, period, bizreport.Scope{SeverityMin: "warning"})
	if len(facts.Incidents) != 2 {
		t.Errorf("severity scope: got %d incidents, want 2 (warning+critical)", len(facts.Incidents))
	}
}

func TestFactsCollector_EmptyPeriodNoError(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()
	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	// No data at all — a calm report's facts. Must not error.
	fc := NewFactsCollector(db)
	facts, err := fc.Collect(ctx, period, period, bizreport.Scope{})
	if err != nil {
		t.Fatalf("empty period should not error: %v", err)
	}
	if len(facts.Incidents) != 0 || facts.Actions.MutatingTotal != 0 {
		t.Errorf("expected zero facts, got %+v", facts)
	}
	// Hero still present (all zeros), so the calm report renders cards.
	if len(facts.Hero) != 4 {
		t.Errorf("hero cards = %d, want 4 even when empty", len(facts.Hero))
	}
}
