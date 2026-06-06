package report

import (
	"context"
	"encoding/json"
)

// ReportFacts is the deterministic, SQL-computed input handed to the
// reporter agent (HLD-014 §数据收集). The agent narrates these facts —
// it never computes the numbers. Every count, duration, and sparkline
// here comes from a query against incidents / audit / proposals / edges
// over the report's period; the agent's only freedom is prose
// (narrative + advice) and incident ordering commentary.
type ReportFacts struct {
	Period     Period `json:"-"`
	PrevPeriod Period `json:"-"`

	// Hero is the pre-computed big-number set with period-over-period
	// deltas and per-bucket sparklines. Copied verbatim into
	// Content.Hero by the generator — the agent cannot alter it.
	Hero []HeroStat `json:"hero"`

	// Incidents are the period's incidents, newest-impact first. The
	// agent picks the top-N to feature and may attach a root-cause
	// snippet from the linked RCA report.
	Incidents []IncidentFact `json:"incidents"`

	// Actions is the agent-action summary (mutating proposals + safe
	// tool calls) over the period.
	Actions ActionsSummary `json:"actions"`

	// AlertCounts is the period's alert event volume by severity.
	AlertCounts map[string]int `json:"alert_counts"`

	// Edges is the fleet snapshot at period end.
	Edges EdgeFact `json:"edges"`
}

// IncidentFact is one incident's SQL-true facts. DurationMin is
// resolved_at - first_fired_at (or now - first_fired_at if still open).
type IncidentFact struct {
	ID          uint64 `json:"id"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	DeviceID    uint64 `json:"device_id,omitempty"`
	DurationMin int    `json:"duration_min"`
}

// EdgeFact is the fleet snapshot. Online is the count online at period
// end; Total is registered (non-deleted) edges.
type EdgeFact struct {
	Online int `json:"online"`
	Total  int `json:"total"`
}

// Scope is the parsed ReportSchedule.ScopeJSON. v1 honours EdgeIDs and
// SeverityMin; FleetTags is parsed but a no-op until G.2.6 edge tags
// land. An empty Scope means full coverage.
type Scope struct {
	FleetTags   []string `json:"fleet_tags,omitempty"`
	EdgeIDs     []uint64 `json:"edge_ids,omitempty"`
	SeverityMin string   `json:"severity_min,omitempty"`
}

// ParseScope reads a ScopeJSON blob. Empty / "{}" / invalid → zero
// Scope (full coverage) rather than an error — a malformed scope should
// degrade to "show everything", not block report generation.
func ParseScope(raw string) Scope {
	var s Scope
	if raw == "" {
		return s
	}
	_ = json.Unmarshal([]byte(raw), &s) // best-effort; zero value on failure
	return s
}

// FactsCollector runs the pure-SQL collection. Implemented by
// data/report/store.FactsCollector. Period and prev are pre-computed by
// the Usecase (PeriodFor); scope filters the queries.
type FactsCollector interface {
	Collect(ctx context.Context, period, prev Period, scope Scope) (*ReportFacts, error)
}
