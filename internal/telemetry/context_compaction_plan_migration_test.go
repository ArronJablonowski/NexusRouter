package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
)

func TestContextCompactionPlanMigrationFreshReopenAndSchema49Preservation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('legacy-task','legacy-session',1,'completed')`); err != nil {
		t.Fatal(err)
	}
	if err = downgradeContextCompaction50(store.db); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version, legacy, tables, indexes, triggers int
	if err = store.db.QueryRow(`SELECT
		(SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM task_heads WHERE task_id='legacy-task'),
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name GLOB 'context_compaction_*'),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name GLOB 'context_compaction_*'),
		(SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name GLOB 'context_compaction_*')`).
		Scan(&version, &legacy, &tables, &indexes, &triggers); err != nil || version != stateschema.Current || legacy != 1 || tables != 5 || indexes != 7 || triggers != 15 {
		t.Fatalf("version=%d legacy=%d tables=%d indexes=%d triggers=%d err=%v", version, legacy, tables, indexes, triggers, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != stateschema.Current {
		t.Fatalf("reopened version=%d err=%v", version, err)
	}
}

func TestContextCompactionPlanMigrationSealsBindingsAndLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	digest := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	hash := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	if _, err = store.db.Exec(`INSERT INTO task_heads VALUES('task','session',1,'completed');
		INSERT INTO lease_processes VALUES('process','{}'),('recovery-process','{}');
		INSERT INTO summary_attempts(id,task_id,body,process_id) VALUES('attempt','task','{}','process'),('failed-attempt','task','{}','process');
		INSERT INTO summary_reviews VALUES('review','attempt','{}');`); err != nil {
		t.Fatal(err)
	}
	insertOperation := func(operation, request, attempt string) {
		t.Helper()
		body, marshalErr := json.Marshal(map[string]any{
			"version": 1, "operation_id": operation, "operation_digest": hash(operation),
			"request_id": request, "request_digest": digest, "task_id": "task", "source_sequence": 1,
			"source_digest": digest, "attempt_id": attempt, "model": "model", "provider": "provider",
			"keep": 1, "estimated_cost": 0, "config_digest": digest, "policy_digest": digest,
			"engine": map[string]any{"digest": digest}, "tiers": map[string]any{"digest": digest},
			"process_id": "process", "started_at": "2026-09-15T12:00:00Z", "status": "started",
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, insertErr := store.db.Exec(`INSERT INTO context_compaction_operations(
			operation_id,operation_digest,request_id,request_digest,task_id,source_sequence,source_digest,attempt_id,model,provider,keep,
			estimated_cost,config_digest,policy_digest,engine_digest,tier_digest,process_id,started_at,status,body)
			VALUES(?,?,?,?, 'task',1,?,?, 'model','provider',1,0,?,?,?,?, 'process','2026-09-15T12:00:00Z','started',?)`,
			operation, hash(operation), request, digest, digest, attempt, digest, digest, digest, digest, body); insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	insertFact := func(id, operation string, sequence int, previous any, kind string, plan, attempt, review, activation, code any) error {
		bodyValue := map[string]any{"version": 1, "id": id, "digest": hash(id), "operation_id": operation,
			"sequence": sequence, "kind": kind, "created_at": "2026-09-15T12:00:00Z"}
		for name, value := range map[string]any{"previous_id": previous, "plan_digest": plan,
			"summary_attempt_id": attempt, "summary_review_id": review, "code": code} {
			if value != nil {
				bodyValue[name] = value
			}
		}
		if activation != nil {
			bodyValue["activation"] = map[string]any{"activation_digest": activation}
		}
		body, marshalErr := json.Marshal(bodyValue)
		if marshalErr != nil {
			return marshalErr
		}
		_, insertErr := store.db.Exec(`INSERT INTO context_compaction_plan_facts(
			fact_id,fact_digest,operation_id,sequence,previous_fact_id,kind,plan_digest,summary_attempt_id,summary_review_id,activation_digest,code,created_at,body)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,'2026-09-15T12:00:00Z',?)`,
			id, hash(id), operation, sequence, previous, kind, plan, attempt, review, activation, code, body)
		return insertErr
	}

	insertOperation("operation", "request", "attempt")
	if err = insertFact("started", "operation", 1, nil, "started", nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	planBody, err := json.Marshal(map[string]any{
		"version": 1, "operation_id": "operation", "plan_digest": digest, "request_id": "request", "request_digest": digest,
		"compaction":   map[string]any{"summary_attempt_id": "attempt", "summary_review_id": "review", "before_context_tokens": 2, "after_context_tokens": 1},
		"draft_digest": digest, "original_prefix_digest": digest, "replacement_prefix_digest": other,
		"live_suffix_boundary": 1, "live_suffix_boundary_digest": digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO context_compaction_plans(
		operation_id,plan_digest,request_id,request_digest,summary_attempt_id,summary_review_id,draft_digest,original_prefix_digest,
		replacement_prefix_digest,live_suffix_boundary,live_suffix_boundary_digest,before_tokens,after_tokens,body)
		VALUES('operation',?,'request',?,'attempt','review',?,?,?,1,?,2,1,?)`, digest, digest, digest, digest, other, digest, planBody); err != nil {
		t.Fatal(err)
	}
	if err = insertFact("prepared", "operation", 2, "started", "prepared", digest, "attempt", "review", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = insertFact("approved-too-soon", "operation", 3, "prepared", "approved", digest, "attempt", "review", nil, nil); err == nil {
		t.Fatal("illegal lifecycle transition accepted")
	}
	if err = insertFact("validated", "operation", 3, "prepared", "validated", digest, "attempt", "review", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = insertFact("approved", "operation", 4, "validated", "approved", digest, "attempt", "review", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = insertFact("activated", "operation", 5, "approved", "activated", digest, "attempt", "review", other, nil); err != nil {
		t.Fatal(err)
	}

	insertOperation("failed-operation", "failed-request", "failed-attempt")
	if err = insertFact("failed-started", "failed-operation", 1, nil, "started", nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = insertFact("failed", "failed-operation", 2, "failed-started", "failed", nil, nil, nil, nil, "owner_interrupted"); err != nil {
		t.Fatal(err)
	}
	failedDigest := hash("failed")
	recoveryBody, err := json.Marshal(map[string]any{"version": 1, "id": "recovery", "digest": other,
		"operation_id": "failed-operation", "process_id": "recovery-process", "failed_fact_id": "failed",
		"failed_fact_digest": failedDigest, "reason": "owner_interrupted", "recovered_at": "2026-09-15T12:01:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO context_compaction_plan_recoveries(
		recovery_id,recovery_digest,operation_id,process_id,failed_fact_id,failed_fact_digest,reason,recovered_at,body)
		VALUES('recovery',?,'failed-operation','recovery-process','failed',?,'owner_interrupted','2026-09-15T12:01:00Z',?)`, other, failedDigest, recoveryBody); err != nil {
		t.Fatal(err)
	}

	for name, query := range map[string]string{
		"operation update": `UPDATE context_compaction_operations SET keep=2 WHERE operation_id='operation'`,
		"operation delete": `DELETE FROM context_compaction_operations WHERE operation_id='operation'`,
		"plan update":      `UPDATE context_compaction_plans SET before_tokens=3 WHERE operation_id='operation'`,
		"plan delete":      `DELETE FROM context_compaction_plans WHERE operation_id='operation'`,
		"fact update":      `UPDATE context_compaction_plan_facts SET code='tampered' WHERE fact_id='started'`,
		"fact delete":      `DELETE FROM context_compaction_plan_facts WHERE fact_id='started'`,
		"recovery update":  `UPDATE context_compaction_plan_recoveries SET recovered_at='2026-09-15T12:02:00Z' WHERE recovery_id='recovery'`,
		"recovery delete":  `DELETE FROM context_compaction_plan_recoveries WHERE recovery_id='recovery'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, mutationErr := store.db.Exec(query); mutationErr == nil {
				t.Fatal("immutable record accepted mutation")
			}
		})
	}
	// Reopen validation detects indexed/body divergence even if an attacker
	// temporarily removes and faithfully restores the immutability trigger.
	if _, err = store.db.Exec(`DROP TRIGGER context_compaction_operation_immutable_update;
		UPDATE context_compaction_operations SET body='{"version":1}' WHERE operation_id='operation';
		CREATE TRIGGER context_compaction_operation_immutable_update BEFORE UPDATE ON context_compaction_operations
		BEGIN SELECT RAISE(ABORT,'context compaction operation immutable'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("indexed/body divergence reopened")
	}
}

func TestContextCompactionPlanMigrationCleanupTamperAndRollback(t *testing.T) {
	ctx := context.Background()
	t.Run("complete empty future declaration is replaced", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`PRAGMA user_version=49`); err != nil {
			t.Fatal(err)
		}
		store.Close()
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var version int
		if err = store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != stateschema.Current {
			t.Fatalf("version=%d err=%v", version, err)
		}
	})

	t.Run("schema tampering fails closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`DROP TRIGGER context_compaction_plan_fact_binding`); err != nil {
			t.Fatal(err)
		}
		store.Close()
		if reopened, openErr := Open(ctx, path); openErr == nil {
			reopened.Close()
			t.Fatal("tampered schema reopened")
		}
	})

	t.Run("partial future declaration rolls back", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err = downgradeContextCompaction50(store.db); err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`CREATE TABLE context_compaction_operations(sentinel TEXT)`); err != nil {
			t.Fatal(err)
		}
		store.Close()
		if reopened, openErr := Open(ctx, path); openErr == nil {
			reopened.Close()
			t.Fatal("partial future declaration reopened")
		}
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		var version, columns int
		if err = raw.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
			(SELECT count(*) FROM pragma_table_info('context_compaction_operations'))`).Scan(&version, &columns); err != nil || version != 49 || columns != 1 {
			t.Fatalf("version=%d columns=%d err=%v", version, columns, err)
		}
	})
}

func downgradeContextCompaction50(db *sql.DB) error {
	_, err := db.Exec(`DROP TRIGGER context_compaction_plan_recovery_binding;
		DROP TRIGGER context_compaction_plan_fact_binding;
		DROP TRIGGER context_compaction_plan_binding;
		DROP TRIGGER context_compaction_plan_recovery_immutable_delete;
		DROP TRIGGER context_compaction_plan_recovery_immutable_update;
		DROP TRIGGER context_compaction_plan_fact_immutable_delete;
		DROP TRIGGER context_compaction_plan_fact_immutable_update;
		DROP TRIGGER context_compaction_plan_immutable_delete;
		DROP TRIGGER context_compaction_plan_immutable_update;
		DROP TRIGGER context_compaction_operation_immutable_delete;
		DROP TRIGGER context_compaction_operation_immutable_update;
		DROP TRIGGER context_compaction_operation_binding;
		DROP INDEX context_compaction_plan_facts_operation;
		DROP INDEX context_compaction_plans_summary;
		DROP INDEX context_compaction_operations_process;
		DROP INDEX context_compaction_operations_task;
		DROP TABLE context_compaction_plan_recoveries;
		DROP TABLE context_compaction_plan_facts;
		DROP TABLE context_compaction_plans;
		DROP TABLE context_compaction_operations;
		PRAGMA user_version=49;`)
	return err
}
