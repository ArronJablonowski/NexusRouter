package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWorkboardDecompositionAdmissionMigrationPreservesLegacyEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('task','session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	if err = downgradeWorkboardDecomposition49(store.db); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, events, admissions int
	if err = store.db.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM workboard_events),(SELECT count(*) FROM workboard_decomposition_admissions)`).
		Scan(&version, &events, &admissions); err != nil || version != 49 || events != 1 || admissions != 0 {
		t.Fatalf("schema=%d events=%d admissions=%d err=%v", version, events, admissions, err)
	}
}

func TestWorkboardDecompositionAdmissionSchemaBindsAndSealsAuthority(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('task','session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	digest := strings.Repeat("a", 64)
	admissionID := strings.Repeat("b", 64)
	eventID := "decomposition-event"
	operationID := "decomposition-operation"
	body, err := json.Marshal(map[string]any{
		"version": 1, "admission_id": admissionID, "event_id": eventID, "event_digest": digest,
		"board_id": "board", "card_id": "successor", "parent_card_id": "card", "operation_id": operationID,
		"request_digest": digest, "actor_id": "worker", "actor_type": "worker", "config_digest": digest,
		"policy_digest": digest, "max_depth": 4, "max_children": 8, "depth": 2, "direct_children": 1,
		"parent_admission_id": nil, "parent_admission_digest": nil, "created_at": 2, "admission_digest": digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventBody, err := json.Marshal(map[string]any{
		"version": 1, "id": eventID, "board_id": "board", "sequence": 2, "operation_id": operationID,
		"kind": "card.create", "actor_id": "worker", "actor_type": "worker", "card_id": "successor", "created_at": "ignored-by-schema-trigger",
		"decomposition_admission_id": admissionID, "decomposition_admission_digest": digest,
		"decomposition_config_digest": digest, "decomposition_policy_digest": digest,
		"decomposition_max_depth": 4, "decomposition_max_children": 8, "decomposition_depth": 2, "decomposition_direct_children": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO workboard_operations
		(scope_kind,scope_id,key_digest,operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at)
		VALUES('board','board',?,?, 'board',?,?,2,2,1,2,'committed','{}',2)`, digest, operationID, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_events
		(id,board_id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body,
		decomposition_admission_id,decomposition_admission_digest,decomposition_config_digest,decomposition_policy_digest,
		decomposition_max_depth,decomposition_max_children,decomposition_depth,decomposition_direct_children)
		VALUES(?,'board',2,?,'card.create','worker','worker','successor',2,?,?,?,?,?,?,?,?,?)`,
		eventID, operationID, eventBody, admissionID, digest, digest, digest, 4, 8, 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_decomposition_admissions
		(admission_id,event_id,event_digest,board_id,card_id,parent_card_id,operation_id,request_digest,actor_id,actor_type,
		config_digest,policy_digest,max_depth,max_children,depth,direct_children,parent_admission_id,parent_admission_digest,created_at,admission_digest,body)
		VALUES(?,?,?,'board','successor','card',?,?, 'worker','worker',?,?,4,8,2,1,NULL,NULL,2,?,?)`,
		admissionID, eventID, digest, operationID, digest, digest, digest, digest, body); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_decomposition_admissions SET max_depth=5 WHERE admission_id=?`, admissionID); err == nil {
		t.Fatal("immutable admission accepted update")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_decomposition_admissions WHERE admission_id=?`, admissionID); err == nil {
		t.Fatal("immutable admission accepted delete")
	}
	if _, err = store.db.Exec(`UPDATE workboard_events SET decomposition_max_depth=5 WHERE id=?`, eventID); err == nil {
		t.Fatal("immutable event binding accepted update")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_events(id,board_id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body)
		VALUES('unadmitted-event','board',3,?,'card.create','worker','worker','successor',3,'{}')`, operationID); err == nil {
		t.Fatal("worker card.create event without admission accepted")
	}
}

func TestWorkboardDecompositionAdmissionSchemaTamperAndPartialFutureFailClosed(t *testing.T) {
	ctx := context.Background()
	t.Run("tampered current trigger", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`DROP TRIGGER workboard_decomposition_event_binding;
			CREATE TRIGGER workboard_decomposition_event_binding BEFORE INSERT ON workboard_events BEGIN SELECT 1; END`); err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, openErr := Open(ctx, path); openErr == nil {
			reopened.Close()
			t.Fatal("tampered schema-49 trigger accepted")
		}
	})

	t.Run("partial future declaration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err = downgradeWorkboardDecomposition49(store.db); err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`CREATE TABLE workboard_decomposition_admissions(id TEXT); PRAGMA user_version=48`); err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, openErr := Open(ctx, path); openErr == nil {
			reopened.Close()
			t.Fatal("partial future schema accepted")
		}
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		var version, tables int
		if err = raw.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
			(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_decomposition_admissions')`).Scan(&version, &tables); err != nil || version != 48 || tables != 1 {
			t.Fatalf("failed open mutated declaration: version=%d tables=%d err=%v", version, tables, err)
		}
	})
}

func TestWorkboardDecompositionAdmissionMigrationConcurrentOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = downgradeWorkboardDecomposition49(store.db); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			opened, openErr := Open(ctx, path)
			if openErr == nil {
				openErr = opened.Close()
			}
			errs <- openErr
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err = range errs {
		if err != nil {
			t.Fatal("serialized schema-49 open", err)
		}
	}
}

func downgradeWorkboardDecomposition49(db *sql.DB) error {
	_, err := db.Exec(`DROP TRIGGER workboard_decomposition_event_binding;
		DROP TRIGGER workboard_decomposition_event_immutable;
		DROP TRIGGER workboard_decomposition_admission_immutable_delete;
		DROP TRIGGER workboard_decomposition_admission_immutable_update;
		DROP TRIGGER workboard_decomposition_admission_binding;
		ALTER TABLE workboard_events DROP COLUMN decomposition_direct_children;
		ALTER TABLE workboard_events DROP COLUMN decomposition_depth;
		ALTER TABLE workboard_events DROP COLUMN decomposition_max_children;
		ALTER TABLE workboard_events DROP COLUMN decomposition_max_depth;
		ALTER TABLE workboard_events DROP COLUMN decomposition_policy_digest;
		ALTER TABLE workboard_events DROP COLUMN decomposition_config_digest;
		ALTER TABLE workboard_events DROP COLUMN decomposition_admission_digest;
		ALTER TABLE workboard_events DROP COLUMN decomposition_admission_id;
		DROP INDEX workboard_decomposition_admissions_parent;
		DROP TABLE workboard_decomposition_admissions;
		PRAGMA user_version=48`)
	return err
}
