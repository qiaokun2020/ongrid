package store

import (
	"context"
	"time"

	"gorm.io/gorm"

	bizreport "github.com/ongridio/ongrid/internal/manager/biz/report"
)

// FactsCollector implements bizreport.FactsCollector with pure SQL over
// the alert / audit / proposal / edge tables. No LLM, no mutation —
// read-only aggregation that produces the deterministic numbers the
// reporter agent narrates. Lives in the data layer because it queries
// tables owned by other domains; it touches them read-only and never
// imports their biz packages.
type FactsCollector struct {
	db *gorm.DB
}

func NewFactsCollector(db *gorm.DB) *FactsCollector { return &FactsCollector{db: db} }

var _ bizreport.FactsCollector = (*FactsCollector)(nil)

// severityRank orders severities for the SeverityMin filter.
var severityRank = map[string]int{"info": 0, "warning": 1, "critical": 2}

// Collect runs the aggregation. Each sub-query degrades independently:
// a failure in one source (e.g. the proposals table empty on installs
// without the SOP feature) returns its zero value rather than aborting
// the whole report — a report with partial facts beats no report.
func (c *FactsCollector) Collect(ctx context.Context, period, prev bizreport.Period, scope bizreport.Scope) (*bizreport.ReportFacts, error) {
	facts := &bizreport.ReportFacts{
		Period:      period,
		PrevPeriod:  prev,
		AlertCounts: map[string]int{},
	}

	incidents, err := c.collectIncidents(ctx, period, scope)
	if err != nil {
		return nil, err
	}
	facts.Incidents = incidents

	facts.Actions = c.collectActions(ctx, period)
	facts.AlertCounts = c.collectAlertCounts(ctx, period, scope)
	facts.Edges = c.collectEdges(ctx)

	facts.Hero = c.buildHero(ctx, period, prev, scope, incidents, facts.Actions)
	return facts, nil
}

// incidentRow is the projection for the incident aggregation.
type incidentRow struct {
	ID           uint64
	RuleName     string
	Severity     string
	Status       string
	DeviceID     *uint64
	FirstFiredAt time.Time
	ResolvedAt   *time.Time
}

func (c *FactsCollector) collectIncidents(ctx context.Context, p bizreport.Period, scope bizreport.Scope) ([]bizreport.IncidentFact, error) {
	q := c.db.WithContext(ctx).
		Table("alert_incidents").
		Select("id, rule_name, severity, status, device_id, first_fired_at, resolved_at").
		Where("deleted_at IS NULL").
		Where("first_fired_at >= ? AND first_fired_at < ?", p.Start, p.End)
	q = applyEdgeScope(q, scope)
	q = applySeverityScope(q, scope)

	var rows []incidentRow
	if err := q.Order("first_fired_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]bizreport.IncidentFact, 0, len(rows))
	for _, r := range rows {
		f := bizreport.IncidentFact{
			ID:          r.ID,
			Title:       r.RuleName,
			Severity:    r.Severity,
			Status:      r.Status,
			DurationMin: durationMinutes(r.FirstFiredAt, r.ResolvedAt, p.End),
		}
		if r.DeviceID != nil {
			f.DeviceID = *r.DeviceID
		}
		out = append(out, f)
	}
	return out, nil
}

// collectActions counts agent actions over the period. Mutating actions
// come from chat_mutating_proposals (decision approve/reject/pending);
// safe-tool count is a coarse proxy from audit_logs success rows scoped
// to agent-driven resource changes. Best-effort — empty on installs
// without the proposal feature.
func (c *FactsCollector) collectActions(ctx context.Context, p bizreport.Period) bizreport.ActionsSummary {
	var sum bizreport.ActionsSummary

	type propRow struct {
		ToolName string
		Decision string
		Cnt      int
	}
	var props []propRow
	_ = c.db.WithContext(ctx).
		Table("chat_mutating_proposals").
		Select("tool_name, decision, COUNT(*) as cnt").
		Where("created_at >= ? AND created_at < ?", p.Start, p.End).
		Group("tool_name, decision").
		Find(&props).Error

	byTool := map[string]int{}
	for _, pr := range props {
		sum.MutatingTotal += pr.Cnt
		if pr.Decision == "approve" {
			sum.MutatingApproved += pr.Cnt
		}
		byTool[pr.ToolName] += pr.Cnt
	}
	for tool, cnt := range byTool {
		sum.ByTool = append(sum.ByTool, bizreport.ToolCount{Tool: tool, Count: cnt})
	}
	sortToolCounts(sum.ByTool)

	// Safe-tool proxy: audit success rows in the window. Coarse but
	// avoids a dependency on per-tool-call telemetry not yet persisted.
	var safe int64
	_ = c.db.WithContext(ctx).
		Table("audit_logs").
		Where("occurred_at >= ? AND occurred_at < ?", p.Start, p.End).
		Where("status = ?", "success").
		Count(&safe).Error
	sum.SafeTotal = int(safe)
	return sum
}

func (c *FactsCollector) collectAlertCounts(ctx context.Context, p bizreport.Period, scope bizreport.Scope) map[string]int {
	type sevRow struct {
		Severity string
		Cnt      int
	}
	q := c.db.WithContext(ctx).
		Table("alert_incidents").
		Select("severity, COUNT(*) as cnt").
		Where("deleted_at IS NULL").
		Where("first_fired_at >= ? AND first_fired_at < ?", p.Start, p.End)
	q = applyEdgeScope(q, scope)
	var rows []sevRow
	_ = q.Group("severity").Find(&rows).Error
	out := map[string]int{}
	for _, r := range rows {
		out[r.Severity] = r.Cnt
	}
	return out
}

func (c *FactsCollector) collectEdges(ctx context.Context) bizreport.EdgeFact {
	var f bizreport.EdgeFact
	var total, online int64
	_ = c.db.WithContext(ctx).Table("edges").Where("deleted_at IS NULL").Count(&total).Error
	_ = c.db.WithContext(ctx).Table("edges").Where("deleted_at IS NULL AND status = ?", "online").Count(&online).Error
	f.Total = int(total)
	f.Online = int(online)
	return f
}

// buildHero computes the big-number cards with period-over-period deltas
// and daily-bucket sparklines. All numbers SQL-true.
func (c *FactsCollector) buildHero(ctx context.Context, p, prev bizreport.Period, scope bizreport.Scope, incidents []bizreport.IncidentFact, actions bizreport.ActionsSummary) []bizreport.HeroStat {
	// Resolved count + MTTR over this period.
	resolved, mttr := resolvedAndMTTR(incidents)
	prevResolved := c.countIncidents(ctx, prev, scope)

	hero := []bizreport.HeroStat{
		{
			Key:       "incidents",
			Label:     "Incidents",
			Value:     float64(len(incidents)),
			DeltaPct:  deltaPct(float64(len(incidents)), float64(prevResolved)),
			Sparkline: c.dailyIncidentBuckets(ctx, p, scope),
		},
		{
			Key:   "mttr_minutes",
			Label: "MTTR",
			Value: float64(mttr),
			Unit:  "min",
		},
		{
			Key:   "actions",
			Label: "Actions",
			Value: float64(actions.MutatingTotal + actions.SafeTotal),
		},
		{
			Key:   "resolved",
			Label: "Resolved",
			Value: float64(resolved),
		},
	}
	return hero
}

func (c *FactsCollector) countIncidents(ctx context.Context, p bizreport.Period, scope bizreport.Scope) int {
	q := c.db.WithContext(ctx).Table("alert_incidents").
		Where("deleted_at IS NULL").
		Where("first_fired_at >= ? AND first_fired_at < ?", p.Start, p.End)
	q = applyEdgeScope(q, scope)
	var n int64
	_ = q.Count(&n).Error
	return int(n)
}

// dailyIncidentBuckets returns a per-day incident count series across
// the period (for the sparkline). Capped at 31 buckets so a custom
// long-range schedule doesn't produce an unwieldy series.
func (c *FactsCollector) dailyIncidentBuckets(ctx context.Context, p bizreport.Period, scope bizreport.Scope) []int {
	const maxBuckets = 31
	days := int(p.End.Sub(p.Start).Hours()/24 + 0.5)
	if days < 1 {
		days = 1
	}
	if days > maxBuckets {
		days = maxBuckets
	}
	buckets := make([]int, days)
	span := p.End.Sub(p.Start)
	if span <= 0 {
		return buckets
	}
	// One COUNT per bucket keeps it portable across MySQL/SQLite without
	// date-function dialect differences. days is small (≤31).
	for i := 0; i < days; i++ {
		bStart := p.Start.Add(time.Duration(i) * span / time.Duration(days))
		bEnd := p.Start.Add(time.Duration(i+1) * span / time.Duration(days))
		q := c.db.WithContext(ctx).Table("alert_incidents").
			Where("deleted_at IS NULL").
			Where("first_fired_at >= ? AND first_fired_at < ?", bStart, bEnd)
		q = applyEdgeScope(q, scope)
		var n int64
		_ = q.Count(&n).Error
		buckets[i] = int(n)
	}
	return buckets
}

// --- helpers ---

func applyEdgeScope(q *gorm.DB, scope bizreport.Scope) *gorm.DB {
	if len(scope.EdgeIDs) > 0 {
		return q.Where("device_id IN ?", scope.EdgeIDs)
	}
	return q
}

func applySeverityScope(q *gorm.DB, scope bizreport.Scope) *gorm.DB {
	if scope.SeverityMin == "" {
		return q
	}
	min, ok := severityRank[scope.SeverityMin]
	if !ok {
		return q
	}
	allowed := make([]string, 0, 3)
	for sev, rank := range severityRank {
		if rank >= min {
			allowed = append(allowed, sev)
		}
	}
	return q.Where("severity IN ?", allowed)
}

func durationMinutes(start time.Time, resolved *time.Time, periodEnd time.Time) int {
	end := periodEnd
	if resolved != nil {
		end = *resolved
	}
	d := end.Sub(start)
	if d < 0 {
		return 0
	}
	return int(d.Minutes())
}

func resolvedAndMTTR(incidents []bizreport.IncidentFact) (resolved, mttrMin int) {
	var total int
	for _, in := range incidents {
		if in.Status == "resolved" {
			resolved++
			total += in.DurationMin
		}
	}
	if resolved > 0 {
		mttrMin = total / resolved
	}
	return resolved, mttrMin
}

// deltaPct returns the period-over-period percentage change, or nil when
// there's no prior value to compare against (renders as "new").
func deltaPct(cur, prev float64) *float64 {
	if prev == 0 {
		return nil
	}
	d := (cur - prev) / prev * 100
	return &d
}

func sortToolCounts(tc []bizreport.ToolCount) {
	// Simple insertion sort by count desc — slices are tiny (#tools).
	for i := 1; i < len(tc); i++ {
		for j := i; j > 0 && tc[j].Count > tc[j-1].Count; j-- {
			tc[j], tc[j-1] = tc[j-1], tc[j]
		}
	}
}
