package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

const maxRoutingObservations = 10000

const fitnessObservationQuery = `SELECT kind,id,base_id,supersedes,task_id,attempt_id,model,provider,domain,profile,body,current_id FROM (
	SELECT 0 AS kind,
	 CASE WHEN length(CAST(e.id AS BLOB)) BETWEEN 1 AND 128 THEN e.id END AS id,
	 CASE WHEN length(CAST(e.id AS BLOB)) BETWEEN 1 AND 128 THEN e.id END AS base_id,
	 '' AS supersedes,
	 CASE WHEN length(CAST(e.task_id AS BLOB)) BETWEEN 1 AND 128 THEN e.task_id END AS task_id,
	 CASE WHEN length(CAST(e.attempt_id AS BLOB)) BETWEEN 1 AND 128 THEN e.attempt_id END AS attempt_id,
	 e.model,e.provider,e.domain,e.profile,
	 CASE WHEN length(CAST(e.body AS BLOB)) BETWEEN 1 AND 262144 AND json_valid(e.body) THEN e.body END AS body,
	 CASE WHEN length(CAST(h.current_id AS BLOB)) BETWEEN 1 AND 128 THEN h.current_id END AS current_id
	 FROM evaluations AS e INDEXED BY evaluations_routing_key LEFT JOIN evaluation_heads h ON h.base_id=e.id
	 WHERE e.model=? AND e.provider=? AND e.domain=? AND e.profile=?
	 UNION ALL
	SELECT 1 AS kind,
	 CASE WHEN length(CAST(r.id AS BLOB)) BETWEEN 1 AND 128 THEN r.id END AS id,
	 CASE WHEN length(CAST(r.base_id AS BLOB)) BETWEEN 1 AND 128 THEN r.base_id END AS base_id,
	 CASE WHEN length(CAST(r.supersedes AS BLOB)) BETWEEN 1 AND 128 THEN r.supersedes END AS supersedes,
	 CASE WHEN length(CAST(e.task_id AS BLOB)) BETWEEN 1 AND 128 THEN e.task_id END AS task_id,
	 CASE WHEN length(CAST(e.attempt_id AS BLOB)) BETWEEN 1 AND 128 THEN e.attempt_id END AS attempt_id,
	 e.model,e.provider,e.domain,e.profile,
	 CASE WHEN length(CAST(r.body AS BLOB)) BETWEEN 1 AND 262144 AND json_valid(r.body) THEN r.body END AS body,
	 CASE WHEN length(CAST(h.current_id AS BLOB)) BETWEEN 1 AND 128 THEN h.current_id END AS current_id
	 FROM evaluations AS e INDEXED BY evaluations_routing_key
	 CROSS JOIN evaluation_revisions r ON r.base_id=e.id
	 LEFT JOIN evaluation_heads h ON h.base_id=e.id
	 WHERE e.model=? AND e.provider=? AND e.domain=? AND e.profile=?)
	ORDER BY base_id,kind,id LIMIT ?`

// ObservationSet returns immutable, event-time evidence for one routing key.
// Existing evaluation and audit records already persist their stable identities
// and UTC source timestamps, so no insertion-time projection is needed. The
// routing package resolves correction heads and applies decay using its injected
// clock. This keeps replay, late arrival, and backfill independent of row order.
// By default advisory evidence is included. Supplying exactly one false value
// skips the audit table entirely for configurations where judging is disabled.
func (s *Store) ObservationSet(ctx context.Context, key routing.Key, includeAdvisory ...bool) (routing.ObservationSet, error) {
	if ctx == nil || !validObservationKey(key) || len(includeAdvisory) > 1 {
		return routing.ObservationSet{}, routing.ErrInvalid
	}
	withAdvisory := len(includeAdvisory) == 0 || includeAdvisory[0]
	return s.observationSet(ctx, key, withAdvisory, false)
}

// DirectObservationSet supplies transferable priors without judge-only verdicts
// or advisory reviews. Each correction family is validated before filtering;
// its current verdict determines eligibility so a user correction of an earlier
// judge verdict can contribute exactly once while retaining its original time.
func (s *Store) DirectObservationSet(ctx context.Context, key routing.Key) (routing.ObservationSet, error) {
	if ctx == nil || !validObservationKey(key) {
		return routing.ObservationSet{}, routing.ErrInvalid
	}
	return s.observationSet(ctx, key, false, true)
}

func (s *Store) observationSet(ctx context.Context, key routing.Key, withAdvisory, directOnly bool) (routing.ObservationSet, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return routing.ObservationSet{}, err
	}
	defer tx.Rollback()

	out := routing.ObservationSet{Fitness: []routing.FitnessObservation{}, Advisory: []routing.AdvisoryObservation{}, Validity: []routing.ValidityObservation{}}
	if err = appendFitnessObservations(ctx, tx, key, &out, directOnly); err != nil {
		return routing.ObservationSet{}, err
	}
	if withAdvisory {
		if err = appendAdvisoryObservations(ctx, tx, key, &out); err != nil {
			return routing.ObservationSet{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return routing.ObservationSet{}, err
	}
	return out, nil
}

func validObservationKey(key routing.Key) bool {
	for i, value := range []string{key.Model, key.Provider, key.Domain, key.Profile} {
		limit := 128
		if i == 0 {
			limit = 512
		}
		if value == "" || len(value) > limit || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return false
		}
	}
	return true
}

func appendFitnessObservations(ctx context.Context, tx *sql.Tx, key routing.Key, out *routing.ObservationSet, directOnly bool) error {
	// One bounded union reads every base and revision for this key. Resolving
	// heads in memory avoids an evaluationHistory query per attempt.
	rows, err := tx.QueryContext(ctx, fitnessObservationQuery, key.Model, key.Provider, key.Domain, key.Profile, key.Model, key.Provider, key.Domain, key.Profile, maxRoutingObservations+1)
	if err != nil {
		return err
	}
	type storedVersion struct {
		base, supersedes, head string
		record                 evaluation.Record
		isBase                 bool
	}
	families := make(map[string][]storedVersion)
	count := 0
	for rows.Next() {
		var kind int
		var id, base, supersedes, task, attempt, model, provider, domain, profile, head sql.NullString
		var body []byte
		if err = rows.Scan(&kind, &id, &base, &supersedes, &task, &attempt, &model, &provider, &domain, &profile, &body, &head); err != nil {
			rows.Close()
			return err
		}
		count++
		if count > maxRoutingObservations || (kind != 0 && kind != 1) || !id.Valid || !base.Valid || !supersedes.Valid || !task.Valid || !attempt.Valid || !model.Valid || !provider.Valid || !domain.Valid || !profile.Valid || !head.Valid || len(body) == 0 || !sessions.ValidEventPageID(id.String) || !sessions.ValidEventPageID(base.String) || !sessions.ValidEventPageID(task.String) || !sessions.ValidEventPageID(attempt.String) || !sessions.ValidEventPageID(head.String) || model.String != key.Model || provider.String != key.Provider || domain.String != key.Domain || profile.String != key.Profile {
			rows.Close()
			return evaluation.ErrEvidence
		}
		if kind == 0 && (id.String != base.String || supersedes.String != "") || kind == 1 && (id.String == base.String || !sessions.ValidEventPageID(supersedes.String)) {
			rows.Close()
			return evaluation.ErrEvidence
		}
		var record evaluation.Record
		if json.Unmarshal(body, &record) != nil || record.Validate() != nil || record.ID != id.String || record.TaskID != task.String || record.AttemptID != attempt.String || record.Key != key {
			rows.Close()
			return evaluation.ErrEvidence
		}
		families[base.String] = append(families[base.String], storedVersion{base: base.String, supersedes: supersedes.String, head: head.String, record: record, isBase: kind == 0})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	baseIDs := make([]string, 0, len(families))
	for baseID := range families {
		baseIDs = append(baseIDs, baseID)
	}
	sort.Strings(baseIDs)
	for _, baseID := range baseIDs {
		versions := families[baseID]
		// Existing evaluation history permits one base plus at most 100
		// append-only corrections. Observation reads enforce the same boundary.
		if len(versions) > 101 {
			return evaluation.ErrEvidence
		}
		byID := make(map[string]storedVersion, len(versions))
		children := make(map[string]string, len(versions)-1)
		var base storedVersion
		for _, version := range versions {
			if _, duplicate := byID[version.record.ID]; duplicate || version.base != baseID {
				return evaluation.ErrEvidence
			}
			byID[version.record.ID] = version
			if version.isBase {
				if base.record.ID != "" {
					return evaluation.ErrEvidence
				}
				base = version
			} else {
				if _, fork := children[version.supersedes]; fork {
					return evaluation.ErrEvidence
				}
				children[version.supersedes] = version.record.ID
			}
		}
		if base.record.ID != baseID || base.head == "" {
			return evaluation.ErrEvidence
		}
		current := base
		visited := 0
		familyStart := len(out.Fitness)
		for {
			record := current.record
			outcome, resolveErr := evaluation.Resolve(record.Checks, record.AllowJudge)
			if resolveErr != nil || record.Key != key || !record.Time.Equal(base.record.Time) {
				return evaluation.ErrEvidence
			}
			quality := 0.0
			if outcome.Accepted {
				quality = 1
			}
			observation := routing.FitnessObservation{
				ID: record.ID, BaseID: baseID, Supersedes: current.supersedes, Key: key,
				Quality: quality, Compliance: cloneObservationBool(record.SchemaPassed),
				Reliability: record.ExecutionSucceeded, Latency: record.Latency,
				Cost: record.Cost, Time: record.Time.UTC(),
			}
			out.Fitness = append(out.Fitness, observation)
			visited++
			nextID, ok := children[record.ID]
			if !ok {
				if record.ID != base.head || visited != len(versions) {
					return evaluation.ErrEvidence
				}
				if outcome.Source == evaluation.Withdrawn || (directOnly && outcome.Source == evaluation.LLMJudge) {
					out.Fitness = out.Fitness[:familyStart]
				}
				break
			}
			next, ok := byID[nextID]
			if !ok || evaluation.ValidateStoredRevision(record, next.record) != nil || next.head != base.head {
				return evaluation.ErrEvidence
			}
			current = next
		}
	}
	return nil
}

func cloneObservationBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func appendAdvisoryObservations(ctx context.Context, tx *sql.Tx, key routing.Key, out *routing.ObservationSet) error {
	// Select candidate-bound records in SQL, then decode and order by the exact
	// RFC3339Nano source time in Go. SQLite rowid and textual timestamp ordering
	// are deliberately not evidence-order authorities.
	rows, err := tx.QueryContext(ctx, `SELECT
		CASE WHEN length(CAST(a.id AS BLOB)) BETWEEN 1 AND 128 THEN a.id END,
		CASE WHEN length(CAST(a.task_id AS BLOB)) BETWEEN 1 AND 128 THEN a.task_id END,
		CASE WHEN length(CAST(a.body AS BLOB)) BETWEEN 1 AND 262144 AND json_valid(a.body) THEN a.body END,
		(SELECT count(*) FROM evaluations e WHERE e.task_id=a.task_id AND e.attempt_id=json_extract(a.body,'$.AttemptID')),
		(SELECT count(*) FROM evaluations e WHERE e.task_id=a.task_id AND e.attempt_id=json_extract(a.body,'$.AttemptID')
		 AND e.model=? AND e.provider=? AND e.domain=? AND e.profile=?)
		FROM audit_records a JOIN events t ON t.task_id=a.task_id
		 AND json_extract(t.body,'$.kind')='turn.started'
		 AND json_extract(t.body,'$.attempt_id')=json_extract(a.body,'$.AttemptID')
		WHERE json_extract(t.body,'$.data.model_id')=? AND json_extract(t.body,'$.data.provider_id')=?
		 AND json_extract(a.body,'$.Audit.domain')=?
		 AND COALESCE((SELECT json_extract(p.body,'$.data.domain') FROM events p
		  WHERE p.task_id=a.task_id AND p.sequence<=t.sequence AND json_extract(p.body,'$.kind')='route.selected'
		  AND NULLIF(json_extract(p.body,'$.data.domain'),'') IS NOT NULL ORDER BY p.sequence DESC LIMIT 1),
		 (SELECT json_extract(p.body,'$.data.domain') FROM events p WHERE p.task_id=a.task_id
		  AND json_extract(p.body,'$.kind')='task.started' AND NULLIF(json_extract(p.body,'$.data.domain'),'') IS NOT NULL LIMIT 1),'general')=?
		 AND COALESCE((SELECT json_extract(p.body,'$.data.profile') FROM events p
		  WHERE p.task_id=a.task_id AND p.sequence<=t.sequence AND json_extract(p.body,'$.kind')='route.selected'
		  AND NULLIF(json_extract(p.body,'$.data.profile'),'') IS NOT NULL ORDER BY p.sequence DESC LIMIT 1),
		 (SELECT json_extract(p.body,'$.data.profile') FROM events p WHERE p.task_id=a.task_id
		  AND json_extract(p.body,'$.kind')='task.started' AND NULLIF(json_extract(p.body,'$.data.profile'),'') IS NOT NULL LIMIT 1),'default')=?
		ORDER BY a.id LIMIT ?`, key.Model, key.Provider, key.Domain, key.Profile, key.Model, key.Provider, key.Domain, key.Domain, key.Profile, maxRoutingObservations+1)
	if err != nil {
		return err
	}
	type candidate struct {
		record evaluation.AuditRecord
	}
	candidates := make([]candidate, 0)
	seenAudit := make(map[string]bool)
	total := 0
	for rows.Next() {
		total++
		var id, task sql.NullString
		var body []byte
		var direct, matchingDirect int
		if err = rows.Scan(&id, &task, &body, &direct, &matchingDirect); err != nil {
			rows.Close()
			return err
		}
		if total > maxRoutingObservations || !id.Valid || !task.Valid || !sessions.ValidEventPageID(id.String) || !sessions.ValidEventPageID(task.String) || len(body) == 0 || seenAudit[id.String] {
			rows.Close()
			return evaluation.ErrEvidence
		}
		seenAudit[id.String] = true
		record, decodeErr := decodeAuditRecord(body, id.String, task.String)
		if decodeErr != nil {
			rows.Close()
			return decodeErr
		}
		if record.Audit.Domain != key.Domain {
			rows.Close()
			return evaluation.ErrEvidence
		}
		if direct < 0 || direct > 1 || matchingDirect < 0 || matchingDirect > direct || direct == 1 && matchingDirect != 1 {
			rows.Close()
			return evaluation.ErrEvidence
		}
		if direct == 0 {
			candidates = append(candidates, candidate{record: record})
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(candidates) > maxRoutingObservations {
		return evaluation.ErrEvidence
	}

	// An authenticated direct evaluation outranks all audits for the same
	// candidate attempt. The bounded query above proves that any suppressing
	// evaluation belongs to this candidate key; the fitness scan validates its
	// complete body and revision topology without another per-audit query.
	current := make(map[[2]string]evaluation.AuditRecord)
	for _, item := range candidates {
		r := item.record
		// A model may warn about itself, but its positive or abstaining review
		// never displaces an independent audit. Self-warning influence is capped.
		if r.EvaluatorModel == key.Model && r.EvaluatorProvider == key.Provider && r.Audit.Verdict != "reject" {
			continue
		}
		attemptKey := [2]string{r.TaskID, r.AttemptID}
		prior, found := current[attemptKey]
		if !found || prior.Time.Before(r.Time) || (prior.Time.Equal(r.Time) && prior.ID < r.ID) {
			current[attemptKey] = r
		}
	}

	selected := make([]evaluation.AuditRecord, 0, len(current))
	for _, record := range current {
		selected = append(selected, record)
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Time.Equal(selected[j].Time) {
			return selected[i].ID > selected[j].ID
		}
		return selected[i].Time.After(selected[j].Time)
	})
	if len(selected) > 100 {
		selected = selected[:100]
	}
	for _, record := range selected {
		if record.Audit.Verdict == "abstain" || record.Audit.Confidence == 0 {
			continue
		}
		confidence := record.Audit.Confidence
		if record.EvaluatorModel == key.Model && record.EvaluatorProvider == key.Provider && confidence > .25 {
			confidence = .25
		}
		quality := 0.0
		if record.Audit.Verdict == "accept" {
			quality = 1
		}
		out.Advisory = append(out.Advisory, routing.AdvisoryObservation{ID: record.ID, Key: key, Quality: quality, Confidence: confidence, Time: record.Time.UTC()})
	}
	return nil
}
