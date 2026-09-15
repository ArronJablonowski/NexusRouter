package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 50 adds the immutable, idempotent context-compaction operation and
// plan journal. It is deliberately additive: schema-49 task, summary, review,
// and workboard history is neither rewritten nor granted synthetic authority.
func migrateContextCompactionPlans(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE context_compaction_operations(
		operation_id TEXT PRIMARY KEY CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
		operation_digest TEXT NOT NULL UNIQUE CHECK(length(operation_digest)=64 AND operation_digest NOT GLOB '*[^0-9a-f]*'),
		request_id TEXT NOT NULL UNIQUE CHECK(length(CAST(request_id AS BLOB)) BETWEEN 1 AND 128),
		request_digest TEXT NOT NULL CHECK(length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
		task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		source_sequence INTEGER NOT NULL CHECK(source_sequence>0),
		source_digest TEXT NOT NULL CHECK(length(source_digest)=64 AND source_digest NOT GLOB '*[^0-9a-f]*'),
		attempt_id TEXT NOT NULL UNIQUE REFERENCES summary_attempts(id) DEFERRABLE INITIALLY DEFERRED,
		model TEXT NOT NULL CHECK(length(CAST(model AS BLOB)) BETWEEN 1 AND 128),
		provider TEXT NOT NULL CHECK(length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
		keep INTEGER NOT NULL CHECK(keep BETWEEN 1 AND 100000),
		estimated_cost REAL NOT NULL CHECK(estimated_cost>=0),
		config_digest TEXT NOT NULL CHECK(length(config_digest)=64 AND config_digest NOT GLOB '*[^0-9a-f]*'),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		engine_digest TEXT NOT NULL CHECK(length(engine_digest)=64 AND engine_digest NOT GLOB '*[^0-9a-f]*'),
		tier_digest TEXT NOT NULL CHECK(length(tier_digest)=64 AND tier_digest NOT GLOB '*[^0-9a-f]*'),
		process_id TEXT NOT NULL REFERENCES lease_processes(id),
		started_at TEXT NOT NULL CHECK(length(CAST(started_at AS BLOB)) BETWEEN 20 AND 64),
		status TEXT NOT NULL CHECK(status='started'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16777216));
	CREATE INDEX context_compaction_operations_task
		ON context_compaction_operations(task_id,source_sequence,operation_id);
	CREATE INDEX context_compaction_operations_process
		ON context_compaction_operations(process_id,operation_id);

	CREATE TABLE context_compaction_plans(
		operation_id TEXT PRIMARY KEY REFERENCES context_compaction_operations(operation_id),
		plan_digest TEXT NOT NULL UNIQUE CHECK(length(plan_digest)=64 AND plan_digest NOT GLOB '*[^0-9a-f]*'),
		request_id TEXT NOT NULL UNIQUE,
		request_digest TEXT NOT NULL CHECK(length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
		summary_attempt_id TEXT NOT NULL REFERENCES summary_attempts(id),
		summary_review_id TEXT NOT NULL REFERENCES summary_reviews(id),
		draft_digest TEXT NOT NULL CHECK(length(draft_digest)=64 AND draft_digest NOT GLOB '*[^0-9a-f]*'),
		original_prefix_digest TEXT NOT NULL CHECK(length(original_prefix_digest)=64 AND original_prefix_digest NOT GLOB '*[^0-9a-f]*'),
		replacement_prefix_digest TEXT NOT NULL CHECK(length(replacement_prefix_digest)=64 AND replacement_prefix_digest NOT GLOB '*[^0-9a-f]*'),
		live_suffix_boundary INTEGER NOT NULL CHECK(live_suffix_boundary>0),
		live_suffix_boundary_digest TEXT NOT NULL CHECK(length(live_suffix_boundary_digest)=64 AND live_suffix_boundary_digest NOT GLOB '*[^0-9a-f]*'),
		before_tokens INTEGER NOT NULL CHECK(before_tokens>0),
		after_tokens INTEGER NOT NULL CHECK(after_tokens>=0 AND after_tokens<before_tokens),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16777216),
		CHECK(original_prefix_digest!=replacement_prefix_digest));
	CREATE INDEX context_compaction_plans_summary
		ON context_compaction_plans(summary_attempt_id,summary_review_id,operation_id);

	CREATE TABLE context_compaction_plan_facts(
		fact_id TEXT PRIMARY KEY CHECK(length(CAST(fact_id AS BLOB)) BETWEEN 1 AND 128),
		fact_digest TEXT NOT NULL UNIQUE CHECK(length(fact_digest)=64 AND fact_digest NOT GLOB '*[^0-9a-f]*'),
		operation_id TEXT NOT NULL REFERENCES context_compaction_operations(operation_id),
		sequence INTEGER NOT NULL CHECK(sequence>0),
		previous_fact_id TEXT REFERENCES context_compaction_plan_facts(fact_id),
		kind TEXT NOT NULL CHECK(kind IN('started','prepared','validated','approved','revoked','activated','failed')),
		plan_digest TEXT,
		summary_attempt_id TEXT REFERENCES summary_attempts(id),
		summary_review_id TEXT REFERENCES summary_reviews(id),
		activation_digest TEXT,
		code TEXT,
		created_at TEXT NOT NULL CHECK(length(CAST(created_at AS BLOB)) BETWEEN 20 AND 64),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16777216),
		UNIQUE(operation_id,sequence), UNIQUE(operation_id,kind),
		CHECK(plan_digest IS NULL OR (length(plan_digest)=64 AND plan_digest NOT GLOB '*[^0-9a-f]*')),
		CHECK(activation_digest IS NULL OR (length(activation_digest)=64 AND activation_digest NOT GLOB '*[^0-9a-f]*')),
		CHECK(code IS NULL OR length(CAST(code AS BLOB)) BETWEEN 1 AND 64),
		CHECK(sequence<=7),
		CHECK((sequence=1 AND previous_fact_id IS NULL AND kind='started') OR
			(sequence>1 AND previous_fact_id IS NOT NULL AND kind!='started')),
		CHECK((kind='started' AND plan_digest IS NULL AND summary_attempt_id IS NULL AND summary_review_id IS NULL AND activation_digest IS NULL AND code IS NULL) OR
			(kind='prepared' AND plan_digest IS NOT NULL AND summary_review_id IS NOT NULL AND activation_digest IS NULL AND code IS NULL) OR
			(kind IN('validated','approved') AND plan_digest IS NOT NULL AND summary_review_id IS NOT NULL AND activation_digest IS NULL AND code IS NULL) OR
			(kind='revoked' AND plan_digest IS NOT NULL AND summary_review_id IS NOT NULL AND activation_digest IS NULL AND code IS NOT NULL) OR
			(kind='activated' AND plan_digest IS NOT NULL AND summary_review_id IS NOT NULL AND activation_digest IS NOT NULL AND code IS NULL) OR
			(kind='failed' AND activation_digest IS NULL AND code IS NOT NULL AND
				((plan_digest IS NULL AND summary_attempt_id IS NULL AND summary_review_id IS NULL) OR
				 (plan_digest IS NOT NULL AND summary_attempt_id IS NOT NULL AND summary_review_id IS NOT NULL)))));
	CREATE INDEX context_compaction_plan_facts_operation
		ON context_compaction_plan_facts(operation_id,sequence,fact_id);

	CREATE TABLE context_compaction_plan_recoveries(
		recovery_id TEXT PRIMARY KEY CHECK(length(CAST(recovery_id AS BLOB)) BETWEEN 1 AND 128),
		recovery_digest TEXT NOT NULL UNIQUE CHECK(length(recovery_digest)=64 AND recovery_digest NOT GLOB '*[^0-9a-f]*'),
		operation_id TEXT NOT NULL UNIQUE REFERENCES context_compaction_operations(operation_id),
		process_id TEXT NOT NULL REFERENCES lease_processes(id),
		failed_fact_id TEXT NOT NULL UNIQUE REFERENCES context_compaction_plan_facts(fact_id),
		failed_fact_digest TEXT NOT NULL CHECK(length(failed_fact_digest)=64 AND failed_fact_digest NOT GLOB '*[^0-9a-f]*'),
		reason TEXT NOT NULL CHECK(reason='owner_interrupted'),
		recovered_at TEXT NOT NULL CHECK(length(CAST(recovered_at AS BLOB)) BETWEEN 20 AND 64),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16777216));

	CREATE TRIGGER context_compaction_operation_immutable_update BEFORE UPDATE ON context_compaction_operations
		BEGIN SELECT RAISE(ABORT,'context compaction operation immutable'); END;
	CREATE TRIGGER context_compaction_operation_immutable_delete BEFORE DELETE ON context_compaction_operations
		BEGIN SELECT RAISE(ABORT,'context compaction operation immutable'); END;
	CREATE TRIGGER context_compaction_operation_binding BEFORE INSERT ON context_compaction_operations
		WHEN json_extract(NEW.body,'$.version')!=1
			OR json_extract(NEW.body,'$.operation_id') IS NOT NEW.operation_id
			OR json_extract(NEW.body,'$.operation_digest') IS NOT NEW.operation_digest
			OR json_extract(NEW.body,'$.request_id') IS NOT NEW.request_id
			OR json_extract(NEW.body,'$.request_digest') IS NOT NEW.request_digest
			OR json_extract(NEW.body,'$.task_id') IS NOT NEW.task_id
			OR json_extract(NEW.body,'$.source_sequence') IS NOT NEW.source_sequence
			OR json_extract(NEW.body,'$.source_digest') IS NOT NEW.source_digest
			OR json_extract(NEW.body,'$.attempt_id') IS NOT NEW.attempt_id
			OR json_extract(NEW.body,'$.model') IS NOT NEW.model
			OR json_extract(NEW.body,'$.provider') IS NOT NEW.provider
			OR json_extract(NEW.body,'$.keep') IS NOT NEW.keep
			OR json_extract(NEW.body,'$.estimated_cost') IS NOT NEW.estimated_cost
			OR json_extract(NEW.body,'$.config_digest') IS NOT NEW.config_digest
			OR json_extract(NEW.body,'$.policy_digest') IS NOT NEW.policy_digest
			OR json_extract(NEW.body,'$.engine.digest') IS NOT NEW.engine_digest
			OR json_extract(NEW.body,'$.tiers.digest') IS NOT NEW.tier_digest
			OR json_extract(NEW.body,'$.process_id') IS NOT NEW.process_id
			OR json_extract(NEW.body,'$.started_at') IS NOT NEW.started_at
			OR json_extract(NEW.body,'$.status') IS NOT NEW.status
		BEGIN SELECT RAISE(ABORT,'context compaction operation binding'); END;
	CREATE TRIGGER context_compaction_plan_immutable_update BEFORE UPDATE ON context_compaction_plans
		BEGIN SELECT RAISE(ABORT,'context compaction plan immutable'); END;
	CREATE TRIGGER context_compaction_plan_immutable_delete BEFORE DELETE ON context_compaction_plans
		BEGIN SELECT RAISE(ABORT,'context compaction plan immutable'); END;
	CREATE TRIGGER context_compaction_plan_fact_immutable_update BEFORE UPDATE ON context_compaction_plan_facts
		BEGIN SELECT RAISE(ABORT,'context compaction plan fact immutable'); END;
	CREATE TRIGGER context_compaction_plan_fact_immutable_delete BEFORE DELETE ON context_compaction_plan_facts
		BEGIN SELECT RAISE(ABORT,'context compaction plan fact immutable'); END;
	CREATE TRIGGER context_compaction_plan_recovery_immutable_update BEFORE UPDATE ON context_compaction_plan_recoveries
		BEGIN SELECT RAISE(ABORT,'context compaction plan recovery immutable'); END;
	CREATE TRIGGER context_compaction_plan_recovery_immutable_delete BEFORE DELETE ON context_compaction_plan_recoveries
		BEGIN SELECT RAISE(ABORT,'context compaction plan recovery immutable'); END;
	CREATE TRIGGER context_compaction_plan_binding BEFORE INSERT ON context_compaction_plans
		WHEN json_extract(NEW.body,'$.version')!=1
			OR json_extract(NEW.body,'$.operation_id') IS NOT NEW.operation_id
			OR json_extract(NEW.body,'$.plan_digest') IS NOT NEW.plan_digest
			OR json_extract(NEW.body,'$.request_id') IS NOT NEW.request_id
			OR json_extract(NEW.body,'$.request_digest') IS NOT NEW.request_digest
			OR json_extract(NEW.body,'$.compaction.summary_attempt_id') IS NOT NEW.summary_attempt_id
			OR json_extract(NEW.body,'$.compaction.summary_review_id') IS NOT NEW.summary_review_id
			OR json_extract(NEW.body,'$.draft_digest') IS NOT NEW.draft_digest
			OR json_extract(NEW.body,'$.original_prefix_digest') IS NOT NEW.original_prefix_digest
			OR json_extract(NEW.body,'$.replacement_prefix_digest') IS NOT NEW.replacement_prefix_digest
			OR json_extract(NEW.body,'$.live_suffix_boundary') IS NOT NEW.live_suffix_boundary
			OR json_extract(NEW.body,'$.live_suffix_boundary_digest') IS NOT NEW.live_suffix_boundary_digest
			OR json_extract(NEW.body,'$.compaction.before_context_tokens') IS NOT NEW.before_tokens
			OR json_extract(NEW.body,'$.compaction.after_context_tokens') IS NOT NEW.after_tokens
			OR NOT EXISTS(SELECT 1 FROM context_compaction_operations operation
			WHERE operation.operation_id=NEW.operation_id AND operation.request_id=NEW.request_id
				AND operation.request_digest=NEW.request_digest AND operation.attempt_id=NEW.summary_attempt_id)
			OR NOT EXISTS(SELECT 1 FROM summary_reviews review
				WHERE review.id=NEW.summary_review_id AND review.attempt_id=NEW.summary_attempt_id)
		BEGIN SELECT RAISE(ABORT,'context compaction plan binding'); END;
	CREATE TRIGGER context_compaction_plan_fact_binding BEFORE INSERT ON context_compaction_plan_facts
		WHEN json_extract(NEW.body,'$.version')!=1
			OR json_extract(NEW.body,'$.id') IS NOT NEW.fact_id
			OR json_extract(NEW.body,'$.digest') IS NOT NEW.fact_digest
			OR json_extract(NEW.body,'$.operation_id') IS NOT NEW.operation_id
			OR json_extract(NEW.body,'$.sequence') IS NOT NEW.sequence
			OR json_extract(NEW.body,'$.previous_id') IS NOT NEW.previous_fact_id
			OR json_extract(NEW.body,'$.kind') IS NOT NEW.kind
			OR json_extract(NEW.body,'$.plan_digest') IS NOT NEW.plan_digest
			OR json_extract(NEW.body,'$.summary_attempt_id') IS NOT NEW.summary_attempt_id
			OR json_extract(NEW.body,'$.summary_review_id') IS NOT NEW.summary_review_id
			OR json_extract(NEW.body,'$.activation.activation_digest') IS NOT NEW.activation_digest
			OR json_extract(NEW.body,'$.code') IS NOT NEW.code
			OR json_extract(NEW.body,'$.created_at') IS NOT NEW.created_at
			OR NOT EXISTS(SELECT 1 FROM context_compaction_operations operation
			WHERE operation.operation_id=NEW.operation_id)
			OR (NEW.previous_fact_id IS NOT NULL AND NOT EXISTS(
				SELECT 1 FROM context_compaction_plan_facts previous
				WHERE previous.fact_id=NEW.previous_fact_id AND previous.operation_id=NEW.operation_id
					AND previous.sequence=NEW.sequence-1
					AND ((previous.kind='started' AND NEW.kind IN('prepared','failed'))
						OR (previous.kind='prepared' AND NEW.kind IN('validated','failed'))
						OR (previous.kind='validated' AND NEW.kind IN('approved','revoked','failed'))
						OR (previous.kind='approved' AND NEW.kind IN('revoked','activated','failed')))))
			OR (NEW.plan_digest IS NOT NULL AND NOT EXISTS(
				SELECT 1 FROM context_compaction_operations operation
				JOIN context_compaction_plans plan ON plan.operation_id=operation.operation_id
				WHERE plan.operation_id=NEW.operation_id AND operation.attempt_id=NEW.summary_attempt_id
					AND plan.plan_digest=NEW.plan_digest AND plan.summary_attempt_id=NEW.summary_attempt_id
					AND plan.summary_review_id=NEW.summary_review_id))
		BEGIN SELECT RAISE(ABORT,'context compaction plan fact binding'); END;
	CREATE TRIGGER context_compaction_plan_recovery_binding BEFORE INSERT ON context_compaction_plan_recoveries
		WHEN json_extract(NEW.body,'$.version')!=1
			OR json_extract(NEW.body,'$.id') IS NOT NEW.recovery_id
			OR json_extract(NEW.body,'$.digest') IS NOT NEW.recovery_digest
			OR json_extract(NEW.body,'$.operation_id') IS NOT NEW.operation_id
			OR json_extract(NEW.body,'$.process_id') IS NOT NEW.process_id
			OR json_extract(NEW.body,'$.failed_fact_id') IS NOT NEW.failed_fact_id
			OR json_extract(NEW.body,'$.failed_fact_digest') IS NOT NEW.failed_fact_digest
			OR json_extract(NEW.body,'$.reason') IS NOT NEW.reason
			OR json_extract(NEW.body,'$.recovered_at') IS NOT NEW.recovered_at
			OR NOT EXISTS(SELECT 1 FROM context_compaction_operations operation
				WHERE operation.operation_id=NEW.operation_id AND operation.process_id!=NEW.process_id)
			OR NOT EXISTS(SELECT 1 FROM context_compaction_plan_facts fact
				WHERE fact.fact_id=NEW.failed_fact_id AND fact.fact_digest=NEW.failed_fact_digest
					AND fact.operation_id=NEW.operation_id AND fact.kind='failed')
		BEGIN SELECT RAISE(ABORT,'context compaction plan recovery binding'); END;
	PRAGMA user_version=50;`); err != nil {
		return err
	}
	return validateContextCompactionPlanObjects(ctx, conn)
}

// A database claiming an older version may contain only a completely empty,
// complete schema-50 declaration left by an interrupted downgrade fixture.
// Authority-bearing rows are never silently discarded.
func discardEmptyFutureContextCompactionPlans(ctx context.Context, conn *sql.Conn) error {
	var tables, indexes, triggers int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN(
			'context_compaction_operations','context_compaction_plans','context_compaction_plan_facts','context_compaction_plan_recoveries')),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN(
			'context_compaction_operations_task','context_compaction_operations_process','context_compaction_plans_summary','context_compaction_plan_facts_operation')),
		(SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN(
			'context_compaction_operation_binding','context_compaction_operation_immutable_update','context_compaction_operation_immutable_delete',
			'context_compaction_plan_immutable_update','context_compaction_plan_immutable_delete',
			'context_compaction_plan_fact_immutable_update','context_compaction_plan_fact_immutable_delete',
			'context_compaction_plan_recovery_immutable_update','context_compaction_plan_recovery_immutable_delete',
			'context_compaction_plan_binding','context_compaction_plan_fact_binding','context_compaction_plan_recovery_binding'))`).Scan(&tables, &indexes, &triggers); err != nil {
		return err
	}
	if tables == 0 && indexes == 0 && triggers == 0 {
		return nil
	}
	if tables != 4 || indexes != 4 || triggers != 12 {
		return errors.New("incomplete context compaction plan schema before schema 50")
	}
	var records int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM context_compaction_operations)+
		(SELECT count(*) FROM context_compaction_plans)+
		(SELECT count(*) FROM context_compaction_plan_facts)+
		(SELECT count(*) FROM context_compaction_plan_recoveries)`).Scan(&records); err != nil {
		return err
	}
	if records != 0 {
		return errors.New("context compaction plan authority exists before schema 50")
	}
	_, err := conn.ExecContext(ctx, `DROP TRIGGER context_compaction_plan_recovery_binding;
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
		DROP TABLE context_compaction_operations;`)
	return err
}

func validateContextCompactionPlanSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardDecompositionAdmissionSchema(ctx, conn); err != nil {
		return err
	}
	return validateContextCompactionPlanObjects(ctx, conn)
}

func validateContextCompactionPlanObjects(ctx context.Context, conn *sql.Conn) error {
	shapes := []struct{ name, shape string }{
		{"context_compaction_operations", "operation_id:TEXT:0:1,operation_digest:TEXT:1:0,request_id:TEXT:1:0,request_digest:TEXT:1:0,task_id:TEXT:1:0,source_sequence:INTEGER:1:0,source_digest:TEXT:1:0,attempt_id:TEXT:1:0,model:TEXT:1:0,provider:TEXT:1:0,keep:INTEGER:1:0,estimated_cost:REAL:1:0,config_digest:TEXT:1:0,policy_digest:TEXT:1:0,engine_digest:TEXT:1:0,tier_digest:TEXT:1:0,process_id:TEXT:1:0,started_at:TEXT:1:0,status:TEXT:1:0,body:BLOB:1:0"},
		{"context_compaction_plans", "operation_id:TEXT:0:1,plan_digest:TEXT:1:0,request_id:TEXT:1:0,request_digest:TEXT:1:0,summary_attempt_id:TEXT:1:0,summary_review_id:TEXT:1:0,draft_digest:TEXT:1:0,original_prefix_digest:TEXT:1:0,replacement_prefix_digest:TEXT:1:0,live_suffix_boundary:INTEGER:1:0,live_suffix_boundary_digest:TEXT:1:0,before_tokens:INTEGER:1:0,after_tokens:INTEGER:1:0,body:BLOB:1:0"},
		{"context_compaction_plan_facts", "fact_id:TEXT:0:1,fact_digest:TEXT:1:0,operation_id:TEXT:1:0,sequence:INTEGER:1:0,previous_fact_id:TEXT:0:0,kind:TEXT:1:0,plan_digest:TEXT:0:0,summary_attempt_id:TEXT:0:0,summary_review_id:TEXT:0:0,activation_digest:TEXT:0:0,code:TEXT:0:0,created_at:TEXT:1:0,body:BLOB:1:0"},
		{"context_compaction_plan_recoveries", "recovery_id:TEXT:0:1,recovery_digest:TEXT:1:0,operation_id:TEXT:1:0,process_id:TEXT:1:0,failed_fact_id:TEXT:1:0,failed_fact_digest:TEXT:1:0,reason:TEXT:1:0,recovered_at:TEXT:1:0,body:BLOB:1:0"},
	}
	for _, shape := range shapes {
		if !browserTableShape(ctx, conn, shape.name, shape.shape) {
			var actual string
			_ = conn.QueryRowContext(ctx, `SELECT group_concat(name||':'||type||':'||"notnull"||':'||pk,',') FROM pragma_table_info(?)`, shape.name).Scan(&actual)
			return errors.New("invalid context compaction plan table: " + shape.name + ": " + actual)
		}
	}
	for _, object := range []struct {
		kind, name string
		rules      []string
	}{
		{"index", "context_compaction_operations_task", []string{"oncontext_compaction_operations(task_id,source_sequence,operation_id)"}},
		{"index", "context_compaction_operations_process", []string{"oncontext_compaction_operations(process_id,operation_id)"}},
		{"index", "context_compaction_plans_summary", []string{"oncontext_compaction_plans(summary_attempt_id,summary_review_id,operation_id)"}},
		{"index", "context_compaction_plan_facts_operation", []string{"oncontext_compaction_plan_facts(operation_id,sequence,fact_id)"}},
		{"trigger", "context_compaction_operation_immutable_update", []string{"beforeupdateoncontext_compaction_operations", "raise(abort,'contextcompactionoperationimmutable')"}},
		{"trigger", "context_compaction_operation_immutable_delete", []string{"beforedeleteoncontext_compaction_operations", "raise(abort,'contextcompactionoperationimmutable')"}},
		{"trigger", "context_compaction_operation_binding", []string{"beforeinsertoncontext_compaction_operations", "json_extract(new.body,'$.operation_digest')isnotnew.operation_digest", "json_extract(new.body,'$.engine.digest')isnotnew.engine_digest", "raise(abort,'contextcompactionoperationbinding')"}},
		{"trigger", "context_compaction_plan_immutable_update", []string{"beforeupdateoncontext_compaction_plans", "raise(abort,'contextcompactionplanimmutable')"}},
		{"trigger", "context_compaction_plan_immutable_delete", []string{"beforedeleteoncontext_compaction_plans", "raise(abort,'contextcompactionplanimmutable')"}},
		{"trigger", "context_compaction_plan_fact_immutable_update", []string{"beforeupdateoncontext_compaction_plan_facts", "raise(abort,'contextcompactionplanfactimmutable')"}},
		{"trigger", "context_compaction_plan_fact_immutable_delete", []string{"beforedeleteoncontext_compaction_plan_facts", "raise(abort,'contextcompactionplanfactimmutable')"}},
		{"trigger", "context_compaction_plan_recovery_immutable_update", []string{"beforeupdateoncontext_compaction_plan_recoveries", "raise(abort,'contextcompactionplanrecoveryimmutable')"}},
		{"trigger", "context_compaction_plan_recovery_immutable_delete", []string{"beforedeleteoncontext_compaction_plan_recoveries", "raise(abort,'contextcompactionplanrecoveryimmutable')"}},
		{"trigger", "context_compaction_plan_binding", []string{"beforeinsertoncontext_compaction_plans", "json_extract(new.body,'$.plan_digest')isnotnew.plan_digest", "operation.request_id=new.request_id", "review.id=new.summary_review_id", "raise(abort,'contextcompactionplanbinding')"}},
		{"trigger", "context_compaction_plan_fact_binding", []string{"beforeinsertoncontext_compaction_plan_facts", "json_extract(new.body,'$.digest')isnotnew.fact_digest", "previous.sequence=new.sequence-1", "previous.kind='approved'andnew.kindin('revoked','activated','failed')", "raise(abort,'contextcompactionplanfactbinding')"}},
		{"trigger", "context_compaction_plan_recovery_binding", []string{"beforeinsertoncontext_compaction_plan_recoveries", "json_extract(new.body,'$.failed_fact_digest')isnotnew.failed_fact_digest", "operation.process_id!=new.process_id", "fact.fact_digest=new.failed_fact_digest", "fact.kind='failed'", "raise(abort,'contextcompactionplanrecoverybinding')"}},
	} {
		if !workboardObjectRules(ctx, conn, object.kind, object.name, object.rules) {
			return errors.New("invalid context compaction plan schema")
		}
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
		SELECT 1 FROM context_compaction_operations operation
		WHERE json_extract(operation.body,'$.version')!=1
			OR json_extract(operation.body,'$.operation_id') IS NOT operation.operation_id
			OR json_extract(operation.body,'$.operation_digest') IS NOT operation.operation_digest
			OR json_extract(operation.body,'$.request_id') IS NOT operation.request_id
			OR json_extract(operation.body,'$.request_digest') IS NOT operation.request_digest
			OR json_extract(operation.body,'$.task_id') IS NOT operation.task_id
			OR json_extract(operation.body,'$.source_sequence') IS NOT operation.source_sequence
			OR json_extract(operation.body,'$.source_digest') IS NOT operation.source_digest
			OR json_extract(operation.body,'$.attempt_id') IS NOT operation.attempt_id
			OR json_extract(operation.body,'$.model') IS NOT operation.model
			OR json_extract(operation.body,'$.provider') IS NOT operation.provider
			OR json_extract(operation.body,'$.keep') IS NOT operation.keep
			OR json_extract(operation.body,'$.estimated_cost') IS NOT operation.estimated_cost
			OR json_extract(operation.body,'$.config_digest') IS NOT operation.config_digest
			OR json_extract(operation.body,'$.policy_digest') IS NOT operation.policy_digest
			OR json_extract(operation.body,'$.engine.digest') IS NOT operation.engine_digest
			OR json_extract(operation.body,'$.tiers.digest') IS NOT operation.tier_digest
			OR json_extract(operation.body,'$.process_id') IS NOT operation.process_id
			OR json_extract(operation.body,'$.started_at') IS NOT operation.started_at
			OR json_extract(operation.body,'$.status') IS NOT operation.status
		UNION ALL
		SELECT 1 FROM context_compaction_plans plan
		JOIN context_compaction_operations operation ON operation.operation_id=plan.operation_id
		LEFT JOIN summary_reviews review ON review.id=plan.summary_review_id AND review.attempt_id=plan.summary_attempt_id
		WHERE operation.request_id!=plan.request_id OR operation.request_digest!=plan.request_digest
			OR operation.attempt_id!=plan.summary_attempt_id OR review.id IS NULL
			OR json_extract(plan.body,'$.version')!=1
			OR json_extract(plan.body,'$.operation_id') IS NOT plan.operation_id
			OR json_extract(plan.body,'$.plan_digest') IS NOT plan.plan_digest
			OR json_extract(plan.body,'$.request_id') IS NOT plan.request_id
			OR json_extract(plan.body,'$.request_digest') IS NOT plan.request_digest
			OR json_extract(plan.body,'$.compaction.summary_attempt_id') IS NOT plan.summary_attempt_id
			OR json_extract(plan.body,'$.compaction.summary_review_id') IS NOT plan.summary_review_id
			OR json_extract(plan.body,'$.draft_digest') IS NOT plan.draft_digest
			OR json_extract(plan.body,'$.original_prefix_digest') IS NOT plan.original_prefix_digest
			OR json_extract(plan.body,'$.replacement_prefix_digest') IS NOT plan.replacement_prefix_digest
			OR json_extract(plan.body,'$.live_suffix_boundary') IS NOT plan.live_suffix_boundary
			OR json_extract(plan.body,'$.live_suffix_boundary_digest') IS NOT plan.live_suffix_boundary_digest
			OR json_extract(plan.body,'$.compaction.before_context_tokens') IS NOT plan.before_tokens
			OR json_extract(plan.body,'$.compaction.after_context_tokens') IS NOT plan.after_tokens
		UNION ALL
		SELECT 1 FROM context_compaction_plan_facts fact
		JOIN context_compaction_operations operation ON operation.operation_id=fact.operation_id
		LEFT JOIN context_compaction_plan_facts previous ON previous.fact_id=fact.previous_fact_id
		LEFT JOIN context_compaction_plans plan ON plan.operation_id=fact.operation_id AND plan.plan_digest=fact.plan_digest
		WHERE (fact.sequence=1 AND (fact.kind!='started' OR fact.previous_fact_id IS NOT NULL))
			OR (fact.sequence>1 AND (previous.fact_id IS NULL OR previous.operation_id!=fact.operation_id
				OR previous.sequence!=fact.sequence-1 OR NOT (
					(previous.kind='started' AND fact.kind IN('prepared','failed'))
					OR (previous.kind='prepared' AND fact.kind IN('validated','failed'))
					OR (previous.kind='validated' AND fact.kind IN('approved','revoked','failed'))
					OR (previous.kind='approved' AND fact.kind IN('revoked','activated','failed')))))
			OR (fact.plan_digest IS NOT NULL AND (plan.operation_id IS NULL
				OR operation.attempt_id IS NOT fact.summary_attempt_id
				OR plan.summary_attempt_id IS NOT fact.summary_attempt_id
				OR plan.summary_review_id IS NOT fact.summary_review_id))
			OR json_extract(fact.body,'$.version')!=1
			OR json_extract(fact.body,'$.id') IS NOT fact.fact_id
			OR json_extract(fact.body,'$.digest') IS NOT fact.fact_digest
			OR json_extract(fact.body,'$.operation_id') IS NOT fact.operation_id
			OR json_extract(fact.body,'$.sequence') IS NOT fact.sequence
			OR json_extract(fact.body,'$.previous_id') IS NOT fact.previous_fact_id
			OR json_extract(fact.body,'$.kind') IS NOT fact.kind
			OR json_extract(fact.body,'$.plan_digest') IS NOT fact.plan_digest
			OR json_extract(fact.body,'$.summary_attempt_id') IS NOT fact.summary_attempt_id
			OR json_extract(fact.body,'$.summary_review_id') IS NOT fact.summary_review_id
			OR json_extract(fact.body,'$.activation.activation_digest') IS NOT fact.activation_digest
			OR json_extract(fact.body,'$.code') IS NOT fact.code
			OR json_extract(fact.body,'$.created_at') IS NOT fact.created_at
		UNION ALL
		SELECT 1 FROM context_compaction_plan_recoveries recovery
		JOIN context_compaction_operations operation ON operation.operation_id=recovery.operation_id
		LEFT JOIN context_compaction_plan_facts fact ON fact.fact_id=recovery.failed_fact_id
		WHERE operation.process_id=recovery.process_id OR fact.operation_id IS NOT recovery.operation_id
			OR fact.kind IS NOT 'failed' OR fact.fact_digest IS NOT recovery.failed_fact_digest
			OR json_extract(recovery.body,'$.version')!=1
			OR json_extract(recovery.body,'$.id') IS NOT recovery.recovery_id
			OR json_extract(recovery.body,'$.digest') IS NOT recovery.recovery_digest
			OR json_extract(recovery.body,'$.operation_id') IS NOT recovery.operation_id
			OR json_extract(recovery.body,'$.process_id') IS NOT recovery.process_id
			OR json_extract(recovery.body,'$.failed_fact_id') IS NOT recovery.failed_fact_id
			OR json_extract(recovery.body,'$.failed_fact_digest') IS NOT recovery.failed_fact_digest
			OR json_extract(recovery.body,'$.reason') IS NOT recovery.reason
			OR json_extract(recovery.body,'$.recovered_at') IS NOT recovery.recovered_at
	)`).Scan(&corrupt); err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("corrupt context compaction plan bindings")
	}
	return nil
}
