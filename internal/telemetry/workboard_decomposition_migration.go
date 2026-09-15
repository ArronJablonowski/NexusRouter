package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 49 adds immutable admission authority for agent-created Workboard
// children. Existing operator history is deliberately left without synthetic
// admissions. A new model/worker card.create event can commit only beside the
// exact admission row that binds its limits and redaction-safe config digest.
func migrateWorkboardDecompositionAdmissions(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE workboard_decomposition_admissions(
		admission_id TEXT PRIMARY KEY CHECK(length(CAST(admission_id AS BLOB)) BETWEEN 1 AND 128),
		board_id TEXT NOT NULL,
		card_id TEXT NOT NULL,
		parent_card_id TEXT,
		operation_id TEXT NOT NULL UNIQUE,
		event_id TEXT NOT NULL UNIQUE CHECK(length(CAST(event_id AS BLOB)) BETWEEN 1 AND 128),
		event_sequence INTEGER NOT NULL CHECK(event_sequence>0),
		event_created_at INTEGER NOT NULL CHECK(event_created_at>=0),
		request_digest TEXT NOT NULL CHECK(length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
		decision_digest TEXT NOT NULL CHECK(length(decision_digest)=64 AND decision_digest NOT GLOB '*[^0-9a-f]*'),
		actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128),
		actor_type TEXT NOT NULL CHECK(actor_type IN('model','worker')),
		origin_task_id TEXT, origin_session_id TEXT, origin_turn_id TEXT, origin_attempt_id TEXT,
		origin_tool_call_id TEXT, origin_tool_name TEXT, origin_model_id TEXT, origin_provider_id TEXT,
		config_digest TEXT NOT NULL CHECK(length(config_digest)=64 AND config_digest NOT GLOB '*[^0-9a-f]*'),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		max_depth INTEGER NOT NULL CHECK(max_depth BETWEEN 1 AND 64),
		max_children INTEGER NOT NULL CHECK(max_children BETWEEN 1 AND 64),
		depth INTEGER NOT NULL CHECK(depth BETWEEN 1 AND max_depth),
		direct_children INTEGER NOT NULL CHECK(direct_children BETWEEN 0 AND max_children),
		parent_admission_id TEXT,
		parent_admission_digest TEXT,
		admitted_at TEXT NOT NULL CHECK(length(CAST(admitted_at AS BLOB)) BETWEEN 20 AND 64),
		admission_digest TEXT NOT NULL UNIQUE CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16384),
		FOREIGN KEY(board_id,card_id) REFERENCES workboard_cards(board_id,id) DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(board_id,parent_card_id) REFERENCES workboard_cards(board_id,id) DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(board_id,operation_id) REFERENCES workboard_operations(board_id,operation_id) DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(event_id) REFERENCES workboard_events(id) DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(board_id,event_sequence) REFERENCES workboard_events(board_id,sequence) DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(parent_admission_id) REFERENCES workboard_decomposition_admissions(admission_id),
		CHECK(card_id!=parent_card_id),
		CHECK((parent_card_id IS NULL AND depth=1 AND direct_children=0) OR
			(parent_card_id IS NOT NULL AND depth>=2 AND direct_children>=1)),
		CHECK((parent_admission_id IS NULL AND parent_admission_digest IS NULL) OR
			(parent_admission_id IS NOT NULL AND length(parent_admission_digest)=64 AND parent_admission_digest NOT GLOB '*[^0-9a-f]*')),
		CHECK((origin_task_id IS NULL AND origin_session_id IS NULL AND origin_turn_id IS NULL AND origin_attempt_id IS NULL
			AND origin_tool_call_id IS NULL AND origin_tool_name IS NULL AND origin_model_id IS NULL AND origin_provider_id IS NULL)
			OR (origin_task_id IS NOT NULL AND origin_session_id IS NOT NULL AND origin_turn_id IS NOT NULL AND origin_attempt_id IS NOT NULL
			AND origin_tool_call_id IS NOT NULL AND origin_tool_name IS NOT NULL AND origin_model_id IS NOT NULL AND origin_provider_id IS NOT NULL)));
	CREATE INDEX workboard_decomposition_admissions_parent
		ON workboard_decomposition_admissions(board_id,parent_card_id,depth,admission_id);
	CREATE INDEX workboard_decomposition_admissions_card
		ON workboard_decomposition_admissions(board_id,card_id,admission_id);
	CREATE TRIGGER workboard_decomposition_admission_immutable_update BEFORE UPDATE ON workboard_decomposition_admissions
		BEGIN SELECT RAISE(ABORT,'workboard decomposition admission immutable'); END;
	CREATE TRIGGER workboard_decomposition_admission_immutable_delete BEFORE DELETE ON workboard_decomposition_admissions
		BEGIN SELECT RAISE(ABORT,'workboard decomposition admission immutable'); END;
	CREATE TRIGGER workboard_decomposition_admission_binding BEFORE INSERT ON workboard_decomposition_admissions
		WHEN json_extract(NEW.body,'$.version')!=1
			OR json_extract(NEW.body,'$.admission_id') IS NOT NEW.admission_id
			OR json_extract(NEW.body,'$.board_id') IS NOT NEW.board_id
			OR json_extract(NEW.body,'$.card_id') IS NOT NEW.card_id
			OR json_extract(NEW.body,'$.parent_id') IS NOT NEW.parent_card_id
			OR json_extract(NEW.body,'$.operation_id') IS NOT NEW.operation_id
			OR json_extract(NEW.body,'$.request_digest') IS NOT NEW.request_digest
			OR json_extract(NEW.body,'$.decision_digest') IS NOT NEW.decision_digest
			OR json_extract(NEW.body,'$.actor.id') IS NOT NEW.actor_id
			OR json_extract(NEW.body,'$.actor.type') IS NOT NEW.actor_type
			OR json_extract(NEW.body,'$.origin.task_id') IS NOT NEW.origin_task_id
			OR json_extract(NEW.body,'$.origin.session_id') IS NOT NEW.origin_session_id
			OR json_extract(NEW.body,'$.origin.turn_id') IS NOT NEW.origin_turn_id
			OR json_extract(NEW.body,'$.origin.attempt_id') IS NOT NEW.origin_attempt_id
			OR json_extract(NEW.body,'$.origin.tool_call_id') IS NOT NEW.origin_tool_call_id
			OR json_extract(NEW.body,'$.origin.tool_name') IS NOT NEW.origin_tool_name
			OR json_extract(NEW.body,'$.origin.model_id') IS NOT NEW.origin_model_id
			OR json_extract(NEW.body,'$.origin.provider_id') IS NOT NEW.origin_provider_id
			OR json_extract(NEW.body,'$.config_digest') IS NOT NEW.config_digest
			OR json_extract(NEW.body,'$.policy_digest') IS NOT NEW.policy_digest
			OR json_extract(NEW.body,'$.limits.version')!=1
			OR json_extract(NEW.body,'$.limits.max_depth') IS NOT NEW.max_depth
			OR json_extract(NEW.body,'$.limits.max_children') IS NOT NEW.max_children
			OR json_extract(NEW.body,'$.depth') IS NOT NEW.depth
			OR json_extract(NEW.body,'$.direct_children') IS NOT NEW.direct_children
			OR json_extract(NEW.body,'$.parent_admission_id') IS NOT NEW.parent_admission_id
			OR json_extract(NEW.body,'$.parent_admission_digest') IS NOT NEW.parent_admission_digest
			OR json_extract(NEW.body,'$.admitted_at') IS NOT NEW.admitted_at
			OR json_extract(NEW.body,'$.admission_digest') IS NOT NEW.admission_digest
			OR EXISTS(SELECT 1 FROM workboard_events event
				WHERE event.id=NEW.event_id OR (event.board_id=NEW.board_id AND event.sequence=NEW.event_sequence))
			OR (NEW.parent_admission_id IS NOT NULL AND NOT EXISTS(
				SELECT 1 FROM workboard_decomposition_admissions parent
				WHERE parent.admission_id=NEW.parent_admission_id
					AND parent.admission_digest=NEW.parent_admission_digest
					AND parent.board_id=NEW.board_id AND parent.card_id=NEW.parent_card_id
					AND NEW.max_depth<=parent.max_depth AND NEW.max_children<=parent.max_children))
			OR (NEW.parent_card_id IS NOT NULL
				AND EXISTS(SELECT 1 FROM workboard_decomposition_admissions prior
					WHERE prior.board_id=NEW.board_id AND prior.card_id=NEW.parent_card_id)
				AND NEW.parent_admission_id IS NOT (SELECT latest.admission_id FROM workboard_decomposition_admissions latest
					WHERE latest.board_id=NEW.board_id AND latest.card_id=NEW.parent_card_id
					ORDER BY latest.event_sequence DESC,latest.admission_id DESC LIMIT 1))
		BEGIN SELECT RAISE(ABORT,'workboard decomposition admission binding'); END;
	CREATE TRIGGER workboard_decomposition_event_binding BEFORE INSERT ON workboard_events
		WHEN NOT ((json_type(NEW.body,'$.decomposition_admission_id') IS NULL
			AND json_type(NEW.body,'$.decomposition_admission_digest') IS NULL
			AND json_type(NEW.body,'$.decomposition_decision_digest') IS NULL
			AND json_type(NEW.body,'$.decomposition_config_digest') IS NULL
			AND json_type(NEW.body,'$.decomposition_policy_digest') IS NULL
			AND json_type(NEW.body,'$.decomposition_max_depth') IS NULL
			AND json_type(NEW.body,'$.decomposition_max_children') IS NULL
			AND json_type(NEW.body,'$.decomposition_depth') IS NULL
			AND json_type(NEW.body,'$.decomposition_direct_children') IS NULL
			AND NOT EXISTS(SELECT 1 FROM workboard_decomposition_admissions admission
				WHERE admission.event_id=NEW.id OR (admission.board_id=NEW.board_id AND admission.event_sequence=NEW.sequence))
			AND NOT (NEW.kind='card.create' AND NEW.actor_type IN('model','worker')))
		OR (json_extract(NEW.body,'$.decomposition_admission_id') IS NOT NULL
			AND json_extract(NEW.body,'$.version')=1
			AND json_extract(NEW.body,'$.id') IS NEW.id
			AND json_extract(NEW.body,'$.board_id') IS NEW.board_id
			AND json_extract(NEW.body,'$.sequence') IS NEW.sequence
			AND json_extract(NEW.body,'$.operation_id') IS NEW.operation_id
			AND json_extract(NEW.body,'$.kind') IS NEW.kind
			AND json_extract(NEW.body,'$.actor_id') IS NEW.actor_id
			AND json_extract(NEW.body,'$.actor_type') IS NEW.actor_type
			AND json_extract(NEW.body,'$.card_id') IS NEW.card_id
			AND NEW.kind IN('card.create','card.revise') AND NEW.actor_type IN('model','worker') AND NEW.card_id IS NOT NULL
			AND EXISTS(SELECT 1 FROM workboard_decomposition_admissions admission
				WHERE admission.admission_id=json_extract(NEW.body,'$.decomposition_admission_id')
					AND admission.admission_digest=json_extract(NEW.body,'$.decomposition_admission_digest')
					AND admission.decision_digest=json_extract(NEW.body,'$.decomposition_decision_digest')
					AND admission.config_digest=json_extract(NEW.body,'$.decomposition_config_digest')
					AND admission.policy_digest=json_extract(NEW.body,'$.decomposition_policy_digest')
					AND admission.max_depth=json_extract(NEW.body,'$.decomposition_max_depth')
					AND admission.max_children=json_extract(NEW.body,'$.decomposition_max_children')
					AND admission.depth=json_extract(NEW.body,'$.decomposition_depth')
					AND admission.direct_children=coalesce(json_extract(NEW.body,'$.decomposition_direct_children'),0)
					AND admission.board_id=NEW.board_id AND admission.card_id=NEW.card_id
					AND admission.operation_id=NEW.operation_id
					AND admission.event_id=NEW.id AND admission.event_sequence=NEW.sequence
					AND admission.event_created_at=NEW.created_at
					AND admission.actor_id=NEW.actor_id AND admission.actor_type=NEW.actor_type
					AND admission.admitted_at=json_extract(NEW.body,'$.created_at'))))
		BEGIN SELECT RAISE(ABORT,'workboard decomposition event admission required'); END;
	CREATE TRIGGER workboard_decomposition_event_immutable BEFORE UPDATE ON workboard_events
		WHEN json_extract(OLD.body,'$.decomposition_admission_id') IS NOT NULL
			OR json_extract(NEW.body,'$.decomposition_admission_id') IS NOT NULL
			OR EXISTS(SELECT 1 FROM workboard_decomposition_admissions admission
				WHERE admission.event_id=OLD.id OR (admission.board_id=OLD.board_id AND admission.event_sequence=OLD.sequence))
		BEGIN SELECT RAISE(ABORT,'workboard decomposition event immutable'); END;
	CREATE TRIGGER workboard_decomposition_event_immutable_delete BEFORE DELETE ON workboard_events
		WHEN json_extract(OLD.body,'$.decomposition_admission_id') IS NOT NULL
			OR EXISTS(SELECT 1 FROM workboard_decomposition_admissions admission
				WHERE admission.event_id=OLD.id OR (admission.board_id=OLD.board_id AND admission.event_sequence=OLD.sequence))
		BEGIN SELECT RAISE(ABORT,'workboard decomposition event immutable'); END;
	PRAGMA user_version=49;`); err != nil {
		return err
	}
	return validateWorkboardDecompositionAdmissionObjects(ctx, conn)
}

// Lowered-version recovery may discard only an entirely empty schema-49
// declaration. Retained admissions are authority and therefore fail closed.
func discardEmptyFutureWorkboardDecompositionAdmissions(ctx context.Context, conn *sql.Conn) error {
	var table, indexes, triggers, columns int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_decomposition_admissions'),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN('workboard_decomposition_admissions_parent','workboard_decomposition_admissions_card')),
		(SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN(
			'workboard_decomposition_admission_binding','workboard_decomposition_admission_immutable_update',
			'workboard_decomposition_admission_immutable_delete','workboard_decomposition_event_binding',
			'workboard_decomposition_event_immutable','workboard_decomposition_event_immutable_delete')),
		(SELECT count(*) FROM pragma_table_info('workboard_events') WHERE name GLOB 'decomposition_*')`).Scan(&table, &indexes, &triggers, &columns); err != nil {
		return err
	}
	if table == 0 && indexes == 0 && triggers == 0 && columns == 0 {
		return nil
	}
	if table != 1 || indexes != 2 || triggers != 6 || columns != 0 {
		return errors.New("incomplete workboard decomposition admission schema before schema 49")
	}
	var admissions int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM workboard_decomposition_admissions`).Scan(&admissions); err != nil {
		return err
	}
	if admissions != 0 {
		return errors.New("workboard decomposition admission authority exists before schema 49")
	}
	// Drop only triggers here. ALTER TABLE reparses every remaining database
	// trigger, and older migration fixtures may still contain later triggers
	// whose referenced tables are about to be discarded by their own cleanup.
	// Columns and tables are removed after all earlier future-object cleanup.
	_, err := conn.ExecContext(ctx, `DROP TRIGGER workboard_decomposition_event_binding;
		DROP TRIGGER workboard_decomposition_event_immutable_delete;
		DROP TRIGGER workboard_decomposition_event_immutable;
		DROP TRIGGER workboard_decomposition_admission_immutable_delete;
		DROP TRIGGER workboard_decomposition_admission_immutable_update;
		DROP TRIGGER workboard_decomposition_admission_binding;`)
	return err
}

func discardFutureWorkboardDecompositionAdmissionObjects(ctx context.Context, conn *sql.Conn) error {
	var table, indexes, columns int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_decomposition_admissions'),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN('workboard_decomposition_admissions_parent','workboard_decomposition_admissions_card')),
		(SELECT count(*) FROM pragma_table_info('workboard_events') WHERE name GLOB 'decomposition_*')`).Scan(&table, &indexes, &columns); err != nil {
		return err
	}
	if table == 0 && indexes == 0 && columns == 0 {
		return nil
	}
	if table != 1 || indexes != 2 || columns != 0 {
		return errors.New("incomplete workboard decomposition admission schema before schema 49")
	}
	var admissions int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM workboard_decomposition_admissions`).Scan(&admissions); err != nil {
		return err
	}
	if admissions != 0 {
		return errors.New("workboard decomposition admission authority exists before schema 49")
	}
	_, err := conn.ExecContext(ctx, `DROP INDEX workboard_decomposition_admissions_card;
		DROP INDEX workboard_decomposition_admissions_parent;
		DROP TABLE workboard_decomposition_admissions;`)
	return err
}

func validateWorkboardDecompositionAdmissionSchema(ctx context.Context, conn *sql.Conn) error {
	// Base validation deliberately ignores only the closed schema-49 object set;
	// every earlier durable boundary is still checked before these additions.
	if err := validateSummaryAttemptRecoverySchema(ctx, conn); err != nil {
		return err
	}
	return validateWorkboardDecompositionAdmissionObjects(ctx, conn)
}

// validateWorkboardDecompositionAdmissionObjects proves the schema-49
// extension itself. Migration callers have already validated schema 48 in the
// same transaction, so repeating the full historical validator here only
// reparses hundreds of unchanged objects under race instrumentation.
func validateWorkboardDecompositionAdmissionObjects(ctx context.Context, conn *sql.Conn) error {
	if !browserTableShape(ctx, conn, "workboard_decomposition_admissions",
		"admission_id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,parent_card_id:TEXT:0:0,operation_id:TEXT:1:0,event_id:TEXT:1:0,event_sequence:INTEGER:1:0,event_created_at:INTEGER:1:0,request_digest:TEXT:1:0,decision_digest:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,origin_task_id:TEXT:0:0,origin_session_id:TEXT:0:0,origin_turn_id:TEXT:0:0,origin_attempt_id:TEXT:0:0,origin_tool_call_id:TEXT:0:0,origin_tool_name:TEXT:0:0,origin_model_id:TEXT:0:0,origin_provider_id:TEXT:0:0,config_digest:TEXT:1:0,policy_digest:TEXT:1:0,max_depth:INTEGER:1:0,max_children:INTEGER:1:0,depth:INTEGER:1:0,direct_children:INTEGER:1:0,parent_admission_id:TEXT:0:0,parent_admission_digest:TEXT:0:0,admitted_at:TEXT:1:0,admission_digest:TEXT:1:0,body:BLOB:1:0") ||
		!browserTableShape(ctx, conn, "workboard_events", "id:TEXT:1:0,board_id:TEXT:1:1,sequence:INTEGER:1:2,operation_id:TEXT:1:0,kind:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,card_id:TEXT:0:0,created_at:INTEGER:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "workboard_decomposition_admissions", []string{
			"admission_idtextprimarykeycheck(length(cast(admission_idasblob))between1and128)",
			"operation_idtextnotnullunique", "event_idtextnotnullunique",
			"event_sequenceintegernotnullcheck(event_sequence>0)", "event_created_atintegernotnullcheck(event_created_at>=0)",
			"admission_digesttextnotnullunique",
			"actor_typetextnotnullcheck(actor_typein('model','worker'))",
			"foreignkey(board_id,card_id)referencesworkboard_cards(board_id,id)deferrableinitiallydeferred",
			"foreignkey(board_id,parent_card_id)referencesworkboard_cards(board_id,id)deferrableinitiallydeferred",
			"foreignkey(board_id,operation_id)referencesworkboard_operations(board_id,operation_id)deferrableinitiallydeferred",
			"foreignkey(event_id)referencesworkboard_events(id)deferrableinitiallydeferred",
			"foreignkey(board_id,event_sequence)referencesworkboard_events(board_id,sequence)deferrableinitiallydeferred",
			"foreignkey(parent_admission_id)referencesworkboard_decomposition_admissions(admission_id)",
			"check(depthbetween1andmax_depth)", "check(direct_childrenbetween0andmax_children)", "check(card_id!=parent_card_id)",
		}) ||
		!workboardObjectRules(ctx, conn, "index", "workboard_decomposition_admissions_parent", []string{
			"onworkboard_decomposition_admissions(board_id,parent_card_id,depth,admission_id)",
		}) ||
		!workboardObjectRules(ctx, conn, "index", "workboard_decomposition_admissions_card", []string{
			"onworkboard_decomposition_admissions(board_id,card_id,admission_id)",
		}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_decomposition_admission_binding", []string{
			"beforeinsertonworkboard_decomposition_admissions",
			"json_extract(new.body,'$.decision_digest')isnotnew.decision_digest",
			"json_extract(new.body,'$.actor.id')isnotnew.actor_id",
			"json_extract(new.body,'$.origin.task_id')isnotnew.origin_task_id",
			"json_extract(new.body,'$.limits.max_depth')isnotnew.max_depth",
			"json_extract(new.body,'$.admitted_at')isnotnew.admitted_at",
			"json_extract(new.body,'$.config_digest')isnotnew.config_digest",
			"event.id=new.event_idor(event.board_id=new.board_idandevent.sequence=new.event_sequence)",
			"new.parent_admission_idisnotnullandnotexists(",
			"orderbylatest.event_sequencedesc,latest.admission_iddesclimit1",
			"raise(abort,'workboarddecompositionadmissionbinding')",
		}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_decomposition_admission_immutable_update", []string{
			"beforeupdateonworkboard_decomposition_admissions", "raise(abort,'workboarddecompositionadmissionimmutable')",
		}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_decomposition_admission_immutable_delete", []string{
			"beforedeleteonworkboard_decomposition_admissions", "raise(abort,'workboarddecompositionadmissionimmutable')",
		}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_decomposition_event_binding", []string{
			"beforeinsertonworkboard_events", "json_type(new.body,'$.decomposition_admission_id')isnull", "json_type(new.body,'$.decomposition_direct_children')isnull",
			"admission.event_id=new.idor(admission.board_id=new.board_idandadmission.event_sequence=new.sequence)",
			"json_extract(new.body,'$.operation_id')isnew.operation_id", "new.kindin('card.create','card.revise')andnew.actor_typein('model','worker')",
			"admission.admission_id=json_extract(new.body,'$.decomposition_admission_id')", "admission.event_id=new.idandadmission.event_sequence=new.sequence",
			"admission.event_created_at=new.created_at", "exists(select1fromworkboard_decomposition_admissionsadmission", "raise(abort,'workboarddecompositioneventadmissionrequired')",
		}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_decomposition_event_immutable", []string{
			"beforeupdateonworkboard_events", "json_extract(old.body,'$.decomposition_admission_id')isnotnull", "admission.event_id=old.id", "raise(abort,'workboarddecompositioneventimmutable')",
		}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_decomposition_event_immutable_delete", []string{
			"beforedeleteonworkboard_events", "json_extract(old.body,'$.decomposition_admission_id')isnotnull", "admission.event_id=old.id", "raise(abort,'workboarddecompositioneventimmutable')",
		}) {
		return errors.New("invalid workboard decomposition admission schema")
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	var corrupt int
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM workboard_decomposition_admissions a
		LEFT JOIN workboard_events e ON e.id=a.event_id AND e.board_id=a.board_id AND e.sequence=a.event_sequence
		WHERE e.id IS NULL OR e.operation_id!=a.operation_id OR e.card_id IS NOT a.card_id
			OR e.actor_id!=a.actor_id OR e.actor_type!=a.actor_type OR e.created_at!=a.event_created_at
			OR json_extract(e.body,'$.decomposition_admission_id') IS NOT a.admission_id
			OR json_extract(e.body,'$.decomposition_admission_digest') IS NOT a.admission_digest
			OR json_extract(e.body,'$.decomposition_decision_digest') IS NOT a.decision_digest
			OR json_extract(e.body,'$.decomposition_config_digest') IS NOT a.config_digest
			OR json_extract(e.body,'$.decomposition_policy_digest') IS NOT a.policy_digest
			OR json_extract(e.body,'$.decomposition_max_depth') IS NOT a.max_depth
			OR json_extract(e.body,'$.decomposition_max_children') IS NOT a.max_children
			OR json_extract(e.body,'$.decomposition_depth') IS NOT a.depth
			OR coalesce(json_extract(e.body,'$.decomposition_direct_children'),0) IS NOT a.direct_children
		UNION ALL
		SELECT 1 FROM workboard_events e
		WHERE json_type(e.body,'$.decomposition_admission_id') IS NOT NULL
			AND NOT EXISTS(SELECT 1 FROM workboard_decomposition_admissions a
				WHERE a.event_id=e.id AND a.board_id=e.board_id AND a.event_sequence=e.sequence
					AND a.admission_id=json_extract(e.body,'$.decomposition_admission_id'))
	)`).Scan(&corrupt); err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("corrupt workboard decomposition admission binding")
	}
	return nil
}
