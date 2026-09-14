// Package telemetry owns transactional local persistence.
package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/classification"
	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("event sequence or identity conflict")

const currentStorageSchema = stateschema.Current

type Store struct {
	db *sql.DB

	workboardCursorOnce sync.Once
	workboardCursorKey  [32]byte
	workboardCursorErr  error
}

type storeOpenLock struct {
	token chan struct{}
	users int
}

var storeOpenLocks = struct {
	sync.Mutex
	paths map[string]*storeOpenLock
}{paths: map[string]*storeOpenLock{}}

// Open creates a private on-disk database. Callers must use a dedicated data
// directory; application configuration must never point into shared scratch.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("on-disk database path required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	lockPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		db.Close()
		return nil, err
	}
	release, err := lockStoreOpenPath(ctx, lockPath)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer release()
	if err = s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func lockStoreOpenPath(ctx context.Context, path string) (func(), error) {
	storeOpenLocks.Lock()
	entry := storeOpenLocks.paths[path]
	if entry == nil {
		entry = &storeOpenLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		storeOpenLocks.paths[path] = entry
	}
	entry.users++
	storeOpenLocks.Unlock()
	select {
	case <-ctx.Done():
		storeOpenLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(storeOpenLocks.paths, path)
		}
		storeOpenLocks.Unlock()
		return nil, ctx.Err()
	case <-entry.token:
	}
	return func() {
		entry.token <- struct{}{}
		storeOpenLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(storeOpenLocks.paths, path)
		}
		storeOpenLocks.Unlock()
	}, nil
}

func (s *Store) initialize(ctx context.Context) error {
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON"} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var mode, integrity string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return errors.New("WAL mode unavailable")
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("database integrity check failed")
	}
	// BEGIN IMMEDIATE serializes migration discovery and application across processes.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var version int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > stateschema.Current {
		return errors.New("unsupported database version")
	}
	if version < 46 {
		if err = discardEmptyFutureIntentClassificationAttempts(ctx, conn); err != nil {
			return err
		}
	}
	if version == 46 {
		if err = validateIntentClassificationAttemptSchema(ctx, conn); err != nil {
			return err
		}
	}
	if version < 45 {
		if err = discardEmptyFutureAuxiliaryReviewOutcomes(ctx, conn); err != nil {
			return err
		}
	}
	if version < 44 {
		if err = discardEmptyFutureAuxiliaryReviewSuccessorFences(ctx, conn); err != nil {
			return err
		}
	}
	if version == 42 {
		if err = validateWorkboardExecutionBudgetSchema(ctx, conn); err != nil {
			return err
		}
	}
	if version == 43 {
		if err = validateWorkboardAuxiliaryReviewSchema(ctx, conn); err != nil {
			return err
		}
	}
	if version == 44 {
		if err = validateWorkboardAuxiliaryReviewSuccessorFenceSchema(ctx, conn); err != nil {
			return err
		}
	}
	if version == 45 {
		if err = validateWorkboardAuxiliaryReviewOutcomeSchema(ctx, conn); err != nil {
			return err
		}
	}
	workboardSchemaValidated := false
	if version == 0 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE task_heads (
		 task_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, sequence INTEGER NOT NULL, state TEXT NOT NULL);
		 CREATE TABLE events (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		 sequence INTEGER NOT NULL CHECK(sequence > 0), body BLOB NOT NULL, UNIQUE(task_id, sequence));
		 PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	if version < 2 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE evaluations (
		 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), attempt_id TEXT NOT NULL,
		 model TEXT NOT NULL, provider TEXT NOT NULL, domain TEXT NOT NULL, profile TEXT NOT NULL,
		 body BLOB NOT NULL, UNIQUE(task_id,attempt_id));
		 CREATE TABLE fitness (model TEXT NOT NULL, provider TEXT NOT NULL, domain TEXT NOT NULL, profile TEXT NOT NULL,
		 samples INTEGER NOT NULL, quality REAL NOT NULL, compliance REAL NOT NULL, schema_samples INTEGER NOT NULL,
		 reliability REAL NOT NULL, latency REAL NOT NULL, cost REAL NOT NULL, updated INTEGER NOT NULL,
		 PRIMARY KEY(model,provider,domain,profile)); PRAGMA user_version=2;`)
		if err != nil {
			return err
		}
	}
	if version < 3 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE resource_leases (
		 token TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), owner TEXT NOT NULL,
		 scope TEXT NOT NULL, writer INTEGER NOT NULL CHECK(writer IN (0,1)),
		 expires INTEGER NOT NULL, released INTEGER NOT NULL DEFAULT 0 CHECK(released IN (0,1)));
		 CREATE INDEX resource_leases_scope ON resource_leases(scope,released,expires);
		 PRAGMA user_version=3;`)
		if err != nil {
			return err
		}
	}
	if version < 4 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE memory_facts (
		 scope TEXT NOT NULL, id TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0),
		 privacy TEXT NOT NULL, expires INTEGER NOT NULL, content TEXT NOT NULL, body BLOB NOT NULL,
		 PRIMARY KEY(scope,id));
		 CREATE INDEX memory_expiry ON memory_facts(scope,expires);
		 PRAGMA user_version=4;`)
		if err != nil {
			return err
		}
	}
	if version < 5 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE audit_records (
		 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), body BLOB NOT NULL);
		 CREATE INDEX audit_records_task ON audit_records(task_id,id);
		 PRAGMA user_version=5;`)
		if err != nil {
			return err
		}
	}
	if version < 6 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE evaluation_heads (
		 base_id TEXT PRIMARY KEY REFERENCES evaluations(id), current_id TEXT NOT NULL UNIQUE);
		 INSERT INTO evaluation_heads SELECT id,id FROM evaluations;
		 CREATE TABLE evaluation_revisions (
		 id TEXT PRIMARY KEY, base_id TEXT NOT NULL REFERENCES evaluations(id), supersedes TEXT NOT NULL UNIQUE, body BLOB NOT NULL);
		 CREATE INDEX evaluation_revisions_base ON evaluation_revisions(base_id);
		 PRAGMA user_version=6;`)
		if err != nil {
			return err
		}
	}
	if version < 7 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE review_attempts (
		 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), status TEXT NOT NULL, body BLOB NOT NULL);
		 CREATE INDEX review_attempts_task ON review_attempts(task_id,id);
		 PRAGMA user_version=7;`)
		if err != nil {
			return err
		}
	}
	if version < 8 {
		_, err = conn.ExecContext(ctx, `CREATE INDEX events_model_start ON events(
		 json_extract(body,'$.data.model_id'),json_extract(body,'$.data.provider_id'),task_id,sequence)
		 WHERE json_extract(body,'$.kind')='turn.started';
		 PRAGMA user_version=8;`)
		if err != nil {
			return err
		}
	}
	if version < 9 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE summary_attempts (
		 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), body BLOB NOT NULL);
		 CREATE INDEX summary_attempts_task ON summary_attempts(task_id,id);
		 PRAGMA user_version=9;`)
		if err != nil {
			return err
		}
	}
	if version < 10 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE summary_reviews (
		 id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES summary_attempts(id), body BLOB NOT NULL);
		 CREATE INDEX summary_reviews_attempt ON summary_reviews(attempt_id);
		 CREATE TABLE summary_review_heads (attempt_id TEXT PRIMARY KEY REFERENCES summary_attempts(id),review_id TEXT NOT NULL REFERENCES summary_reviews(id));
		 PRAGMA user_version=10;`)
		if err != nil {
			return err
		}
	}
	if version < 11 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE task_cancellations (task_id TEXT PRIMARY KEY REFERENCES task_heads(task_id),request_id TEXT NOT NULL UNIQUE,requested_at TEXT NOT NULL); PRAGMA user_version=11;`)
		if err != nil {
			return err
		}
	}
	if version < 12 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE submissions (
		 id TEXT PRIMARY KEY,key_digest TEXT NOT NULL UNIQUE,request_digest TEXT NOT NULL,config_digest TEXT NOT NULL,
		 request BLOB NOT NULL,state TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
		 token TEXT NOT NULL DEFAULT '',lease_expires_at TEXT NOT NULL DEFAULT '',cancel_requested INTEGER NOT NULL DEFAULT 0,
		 result BLOB,error_code TEXT NOT NULL DEFAULT '');
		 CREATE INDEX submissions_queue ON submissions(state,config_digest,created_at,id);
		 CREATE INDEX events_submission_start ON events(json_extract(body,'$.data.submission_id'),sequence) WHERE json_extract(body,'$.kind')='task.started';
		 PRAGMA user_version=12;`)
		if err != nil {
			return err
		}
	}
	if version < 13 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE submission_recoveries (id TEXT PRIMARY KEY, submission_id TEXT NOT NULL REFERENCES submissions(id), prior_token_digest TEXT NOT NULL, body BLOB NOT NULL, UNIQUE(submission_id,prior_token_digest)); PRAGMA user_version=13;`)
		if err != nil {
			return err
		}
	}
	if version < 14 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE task_steering (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), key_hash TEXT NOT NULL, text TEXT NOT NULL, state TEXT NOT NULL, created_at TEXT NOT NULL, applied_sequence INTEGER, UNIQUE(task_id,key_hash)); CREATE INDEX task_steering_pending ON task_steering(task_id,state); PRAGMA user_version=14;`)
		if err != nil {
			return err
		}
	}
	if version < 15 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE tool_approvals (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), tool_call_id TEXT NOT NULL, state TEXT NOT NULL, body BLOB NOT NULL, UNIQUE(task_id,tool_call_id)); PRAGMA user_version=15;`)
		if err != nil {
			return err
		}
	}
	if version < 16 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE skill_generation_attempts (id TEXT PRIMARY KEY, scope TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL, body BLOB NOT NULL); CREATE INDEX skill_generation_attempts_scope ON skill_generation_attempts(scope,id); PRAGMA user_version=16;`)
		if err != nil {
			return err
		}
	}
	if version < 17 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE workflow_selections (id TEXT PRIMARY KEY, scope TEXT NOT NULL, name TEXT NOT NULL, body BLOB NOT NULL); CREATE INDEX workflow_selections_scope ON workflow_selections(scope,id); PRAGMA user_version=17;`)
		if err != nil {
			return err
		}
	}
	if version < 18 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE workflow_scan_tasks (
		 seq INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL UNIQUE REFERENCES task_heads(task_id));
		 INSERT INTO workflow_scan_tasks(task_id) SELECT task_id FROM task_heads ORDER BY task_id;
		 CREATE TRIGGER workflow_scan_task_insert AFTER INSERT ON task_heads BEGIN
		 INSERT INTO workflow_scan_tasks(task_id) VALUES(NEW.task_id); END;
		 CREATE TABLE workflow_scans (
		 scope TEXT NOT NULL, name TEXT NOT NULL, domain TEXT NOT NULL, revision INTEGER NOT NULL,
		 body BLOB NOT NULL, PRIMARY KEY(scope,name));
		 CREATE TABLE workflow_scan_pages (
		 scope TEXT NOT NULL, name TEXT NOT NULL, revision INTEGER NOT NULL,
		 body BLOB NOT NULL, PRIMARY KEY(scope,name,revision));
		 PRAGMA user_version=18;`)
		if err != nil {
			return err
		}
	}
	if version < 19 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE workflow_scan_consumers (
		 scope TEXT NOT NULL, name TEXT NOT NULL, revision INTEGER NOT NULL,
		 body BLOB NOT NULL, PRIMARY KEY(scope,name));
		 CREATE TABLE workflow_scan_consumptions (
		 scope TEXT NOT NULL, name TEXT NOT NULL, revision INTEGER NOT NULL,
		 body BLOB NOT NULL, PRIMARY KEY(scope,name,revision));
		 CREATE TABLE workflow_scan_buckets (
		 scope TEXT NOT NULL, name TEXT NOT NULL, epoch INTEGER NOT NULL,
		 id TEXT NOT NULL, revision INTEGER NOT NULL, body BLOB NOT NULL, PRIMARY KEY(scope,name,epoch,id));
		 PRAGMA user_version=19;`)
		if err != nil {
			return err
		}
	}
	if version < 20 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE memory_retired_ids (scope TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY(scope,id)); PRAGMA user_version=20;`)
		if err != nil {
			return err
		}
	}
	if version < 21 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE learning_states (scope TEXT NOT NULL, name TEXT NOT NULL, revision INTEGER NOT NULL, body BLOB NOT NULL, PRIMARY KEY(scope,name)); PRAGMA user_version=21;`)
		if err != nil {
			return err
		}
	}
	if version < 22 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE lease_processes(id TEXT PRIMARY KEY,body BLOB NOT NULL); ALTER TABLE resource_leases ADD COLUMN process_id TEXT REFERENCES lease_processes(id); PRAGMA user_version=22;`)
		if err != nil {
			return err
		}
	}
	if version < 23 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE lease_recoveries(lease_token TEXT PRIMARY KEY REFERENCES resource_leases(token),digest TEXT NOT NULL UNIQUE,body BLOB NOT NULL); PRAGMA user_version=23;`)
		if err != nil {
			return err
		}
	}
	if version < 24 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE lease_attention(id TEXT PRIMARY KEY,lease_token TEXT NOT NULL UNIQUE REFERENCES resource_leases(token),task_id TEXT NOT NULL REFERENCES task_heads(task_id),state TEXT NOT NULL CHECK(state IN('open','resolved')),body BLOB NOT NULL); CREATE INDEX lease_attention_state_id ON lease_attention(state,id); PRAGMA user_version=24;`)
		if err != nil {
			return err
		}
	}
	if version < 25 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE lease_attention_history(attention_id TEXT NOT NULL REFERENCES lease_attention(id),sequence INTEGER NOT NULL CHECK(sequence>0),kind TEXT NOT NULL CHECK(kind IN('baseline','observed')),body BLOB NOT NULL,PRIMARY KEY(attention_id,sequence)); INSERT INTO lease_attention_history(attention_id,sequence,kind,body) SELECT id,1,'baseline',body FROM lease_attention; PRAGMA user_version=25;`)
		if err != nil {
			return err
		}
	}
	if version < 26 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE learning_activation_intents(scope TEXT NOT NULL,name TEXT NOT NULL,selection_id TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,name,selection_id),FOREIGN KEY(scope,name) REFERENCES learning_states(scope,name)); PRAGMA user_version=26;`)
		if err != nil {
			return err
		}
	}
	if version < 27 {
		if err = migrateSkillExposures(ctx, conn); err != nil {
			return err
		}
	}
	if version < 28 {
		// Exact-kind seeks avoid repeatedly decoding unrelated event bodies.
		// This is an accelerator only; readers still validate journal evidence.
		if _, err = conn.ExecContext(ctx, `CREATE INDEX events_task_kind ON events(task_id,json_extract(body,'$.kind'),sequence);
		 PRAGMA user_version=28;`); err != nil {
			return err
		}
	}
	if version < 29 {
		if err = migrateTaskTiming(ctx, conn); err != nil {
			return err
		}
	}
	if version < 30 {
		if err = migrateUsageAccounting(ctx, conn); err != nil {
			return err
		}
	}
	if version < 31 {
		if err = migrateObservationIndex(ctx, conn); err != nil {
			return err
		}
	}
	if version < 32 {
		if err = migrateSubmissionStream(ctx, conn); err != nil {
			return err
		}
	}
	if version < 33 {
		if err = migrateEventLog(ctx, conn); err != nil {
			return err
		}
	}
	if version < 34 {
		if err = migrateBrowserOperations(ctx, conn); err != nil {
			return err
		}
	}
	if version < 35 {
		if err = migrateWorkboards(ctx, conn); err != nil {
			return err
		}
		workboardSchemaValidated = true
		if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
	}
	if version < 36 {
		if err = migrateWorkboardEventCardIdentity(ctx, conn); err != nil {
			return err
		}
		workboardSchemaValidated = true
	}
	if version < 37 {
		if err = migrateWorkspaceIdentity(ctx, conn); err != nil {
			return err
		}
	}
	if version < 38 {
		if err = migrateBrowserOperationRecoveries(ctx, conn); err != nil {
			return err
		}
	}
	if version < 39 {
		if err = migrateWorkboardPausePhase(ctx, conn); err != nil {
			return err
		}
	}
	if version < 40 {
		if err = migrateWorkboardReassignments(ctx, conn, workboardSchemaValidated); err != nil {
			return err
		}
	}
	if version < 41 {
		if err = migrateTaskStartClaims(ctx, conn); err != nil {
			return err
		}
	}
	if version < 42 {
		if err = migrateWorkboardExecutionBudgets(ctx, conn); err != nil {
			return err
		}
	}
	if version < 43 {
		if err = migrateWorkboardAuxiliaryReviews(ctx, conn); err != nil {
			return err
		}
	}
	if version < 44 {
		if err = migrateWorkboardAuxiliaryReviewSuccessorFences(ctx, conn); err != nil {
			return err
		}
	}
	if version < 45 {
		if err = migrateWorkboardAuxiliaryReviewOutcomes(ctx, conn); err != nil {
			return err
		}
	}
	if version < 46 {
		if err = migrateIntentClassificationAttempts(ctx, conn); err != nil {
			return err
		}
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != stateschema.Current {
		return errors.New("migration did not reach current database version")
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// Append commits the immutable event and task projection together. Repeating
// exactly the same event is safe after acknowledgement loss; reuse is rejected.
func (s *Store) Append(ctx context.Context, expected int64, e runtime.Event) error {
	return s.appendOwned(ctx, expected, e, "", "")
}

func (s *Store) AppendSubmission(ctx context.Context, expected int64, e runtime.Event, id, token string) error {
	if id == "" || token == "" {
		return runtime.ErrExecutionLeaseLost
	}
	return s.appendOwned(ctx, expected, e, id, token)
}

func (s *Store) appendOwned(ctx context.Context, expected int64, e runtime.Event, id, token string) error {
	return s.appendFenced(ctx, expected, e, id, token, "", "")
}

// AppendWorker checks worker and optional submission ownership in the same
// transaction as the event. An exact committed retry remains acknowledgement-safe.
func (s *Store) AppendWorker(ctx context.Context, expected int64, e runtime.Event, leaseToken, owner, submissionID, submissionToken string) error {
	if leaseToken == "" || owner == "" {
		return runtime.ErrExecutionLeaseLost
	}
	return s.appendFenced(ctx, expected, e, submissionID, submissionToken, leaseToken, owner)
}

func (s *Store) appendFenced(ctx context.Context, expected int64, e runtime.Event, id, token, leaseToken, owner string) error {
	return s.appendFencedFinal(ctx, expected, e, id, token, leaseToken, owner, false)
}

func (s *Store) appendFencedFinal(ctx context.Context, expected int64, e runtime.Event, id, token, leaseToken, owner string, finish bool) error {
	body, err := e.Encode()
	if err != nil {
		return err
	}
	if expected < 0 || e.Sequence != expected+1 {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A write first acquires SQLite's writer reservation before reading state.
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", e.TaskID); err != nil {
		return err
	}
	if finish {
		if err := uniqueWorkerLease(ctx, tx, e, leaseToken, owner); err != nil {
			return err
		}
	}
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM events WHERE id=?", e.ID).Scan(&previous)
	if err == nil {
		if string(previous) != string(body) {
			return ErrConflict
		}
		if err := validateEventLogRetry(ctx, tx, e, body); err != nil {
			return err
		}
		if err := validateSubmissionStreamRetry(ctx, tx, e, body, id); err != nil {
			return err
		}
		if finish {
			if err := finishedWorkerLease(ctx, tx, e, leaseToken, owner); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if e.Kind == runtime.TaskStarted && e.Data.Compaction != nil && e.Data.Compaction.SummaryAttemptID != "" {
		if err := validateSummaryGate(ctx, tx, e.Data.Compaction); err != nil {
			return err
		}
	}
	var seq int64
	var session, state string
	err = tx.QueryRowContext(ctx, "SELECT sequence, session_id, state FROM task_heads WHERE task_id=?", e.TaskID).Scan(&seq, &session, &state)
	if errors.Is(err, sql.ErrNoRows) {
		if expected != 0 || e.Kind != runtime.TaskStarted {
			return ErrConflict
		}
		session, state = e.SessionID, "running"
		if _, err = tx.ExecContext(ctx, "INSERT INTO task_heads VALUES (?, ?, 0, ?)", e.TaskID, session, state); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if seq != expected || session != e.SessionID || state != "running" || (seq > 0 && e.Kind == runtime.TaskStarted) {
		return ErrConflict
	}
	if e.Kind == runtime.ContextCompacted {
		if err := validateContextCompactionGate(ctx, tx, e); err != nil {
			return err
		}
	}
	if err := validatePostCompactionJournalBudget(ctx, tx, e, body); err != nil {
		return err
	}
	if err := submissionAppendGate(ctx, tx, e, id, token); err != nil {
		return err
	}
	if leaseToken != "" {
		if err := leaseProcessGate(ctx, tx, leaseToken, owner); err != nil {
			return runtime.ErrExecutionLeaseLost
		}
		var held bool
		// Cancellation may durably clean up an expired lease, but release or
		// reassignment fences even cleanup from its former owner.
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_leases
			WHERE token=? AND task_id=? AND owner=? AND released=0 AND (expires>? OR ?) AND (?=0 OR writer=0))`,
			leaseToken, e.TaskID, owner, time.Now().UnixNano(), e.Kind == runtime.TaskCanceled, finish).Scan(&held); err != nil {
			return err
		}
		if !held {
			return runtime.ErrExecutionLeaseLost
		}
	}
	if e.Kind != runtime.ToolCompleted && e.Kind != runtime.TaskCanceled {
		var requested bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_cancellations WHERE task_id=?)", e.TaskID).Scan(&requested); err != nil {
			return err
		}
		if requested && !intentClassificationCleanupEvent(ctx, tx, e) {
			return runtime.ErrCancellationRequested
		}
	}
	if err := steeringAppendGate(ctx, tx, e); err != nil {
		return err
	}
	var intentClassification *classification.Attempt
	if e.Kind == runtime.TaskStarted && e.Data.IntentClassification != nil {
		attempt, validationErr := intentClassificationAttemptForEvent(ctx, tx, e)
		if validationErr != nil {
			return validationErr
		}
		intentClassification = &attempt
	}
	switch e.Kind {
	case runtime.TaskCompleted:
		state = "completed"
	case runtime.TaskFailed:
		state = "failed"
	case runtime.TaskCanceled:
		state = "canceled"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO events VALUES (?, ?, ?, ?)", e.ID, e.TaskID, e.Sequence, body); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	if err = appendEventLog(ctx, tx, e, body); err != nil {
		return err
	}
	if err = appendSubmissionStreamEvent(ctx, tx, e, body, id); err != nil {
		return err
	}
	if err = appendSkillExposures(ctx, tx, e); err != nil {
		return err
	}
	if err = appendTaskTiming(ctx, tx, e); err != nil {
		return err
	}
	if intentClassification != nil {
		if err = appendIntentClassificationUsage(ctx, tx, e, *intentClassification); err != nil {
			return err
		}
	}
	if err = appendRoutedUsage(ctx, tx, e); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=?, state=? WHERE task_id=?", e.Sequence, state, e.TaskID); err != nil {
		return err
	}
	if finish {
		if err := releaseFinishedWorker(ctx, tx, e, leaseToken, owner); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// intentClassificationCleanupEvent recognizes the effect-free synthetic
// lifecycle used only to close and account a terminal classifier attempt after
// normal routing was denied. It never authorizes a provider or tool event.
func intentClassificationCleanupEvent(ctx context.Context, tx *sql.Tx, event runtime.Event) bool {
	if event.Kind == runtime.TaskStarted {
		return event.Data.IntentClassification != nil && event.Data.IntentClassification.Validate() == nil &&
			event.Data.ModelID == "" && event.Data.ProviderID == "" && len(event.Data.Messages) == 0 && event.Data.Route == nil
	}
	if event.Kind != runtime.ErrorRecorded && event.Kind != runtime.TaskFailed {
		return false
	}
	var body []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence=1", event.TaskID).Scan(&body); err != nil {
		return false
	}
	var start runtime.Event
	return json.Unmarshal(body, &start) == nil && start.Validate() == nil && start.Kind == runtime.TaskStarted &&
		start.Data.IntentClassification != nil && start.Data.ModelID == "" && start.Data.ProviderID == "" &&
		len(start.Data.Messages) == 0 && start.Data.Route == nil
}

// Read returns bounded pages; after is an exclusive per-task sequence cursor.
func (s *Store) Read(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid event page")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(e.id AS BLOB))<=128 THEN e.id END,e.sequence,length(CAST(e.body AS BLOB)),l.position,l.event_id
		FROM events e LEFT JOIN event_log l ON l.event_id=e.id AND l.task_id=e.task_id AND l.task_sequence=e.sequence
		WHERE e.task_id=? AND e.sequence>? ORDER BY e.sequence LIMIT ?`, task, after, limit)
	if err != nil {
		return nil, err
	}
	type entry struct {
		id             string
		sequence, size int64
		position       int64
	}
	entries := make([]entry, 0)
	total := int64(0)
	for rows.Next() {
		var item entry
		var id, ledgerID sql.NullString
		var position sql.NullInt64
		if err = rows.Scan(&id, &item.sequence, &item.size, &position, &ledgerID); err != nil {
			rows.Close()
			return nil, err
		}
		if !id.Valid || !position.Valid || !ledgerID.Valid || id.String != ledgerID.String ||
			!sessions.ValidEventLogEventID(id.String) || item.sequence != after+int64(len(entries))+1 || item.size < 1 ||
			item.size > int64(sessions.MaxEventPageBytes)-total {
			rows.Close()
			if len(entries) == 0 && item.size > sessions.MaxEventPageBytes {
				return nil, sessions.ErrEventTooLarge
			}
			return nil, sessions.ErrEventLog
		}
		item.id, item.position = id.String, position.Int64
		total += item.size
		entries = append(entries, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	events := make([]runtime.Event, 0, len(entries))
	for _, item := range entries {
		ledger, body, readErr := readTaskEventLogByPosition(ctx, tx, item.position, item.id)
		if readErr != nil {
			return nil, readErr
		}
		if ledger.taskID != task || ledger.taskSequence != item.sequence || ledger.size != item.size {
			return nil, sessions.ErrEventLog
		}
		var event runtime.Event
		if json.Unmarshal(body, &event) != nil {
			return nil, sessions.ErrEventLog
		}
		events = append(events, event)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}
