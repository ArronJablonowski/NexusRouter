package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// Tests and migration rehearsals may lower user_version while leaving a newer
// empty declaration behind. Only the complete, empty schema may be discarded.
func discardEmptyFutureContextCompactionDelegations(ctx context.Context, conn *sql.Conn) error {
	var tables, indexes, triggers int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='context_compaction_delegations'),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN(
			'context_compaction_delegations_operation','context_compaction_delegations_tasks','context_compaction_delegations_events')),
		(SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN(
			'context_compaction_delegation_immutable_update','context_compaction_delegation_immutable_delete','context_compaction_delegation_binding'))`).
		Scan(&tables, &indexes, &triggers); err != nil {
		return err
	}
	if tables == 0 && indexes == 0 && triggers == 0 {
		return nil
	}
	if tables != 1 || indexes != 3 || triggers != 3 {
		return errors.New("incomplete context compaction delegation schema before schema 52")
	}
	var records int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM context_compaction_delegations").Scan(&records); err != nil {
		return err
	}
	if records != 0 {
		return errors.New("context compaction delegation authority exists before schema 52")
	}
	_, err := conn.ExecContext(ctx, `DROP TRIGGER context_compaction_delegation_binding;
		DROP TRIGGER context_compaction_delegation_immutable_delete;
		DROP TRIGGER context_compaction_delegation_immutable_update;
		DROP INDEX context_compaction_delegations_events;
		DROP INDEX context_compaction_delegations_tasks;
		DROP INDEX context_compaction_delegations_operation;
		DROP TABLE context_compaction_delegations;`)
	return err
}

// Schema 52 adds an immutable normalized companion for each delegation bound
// into an activated compaction fact. The fact remains authoritative replay
// evidence; the companion makes omissions, orphans, and binding drift visible.
func migrateContextCompactionDelegations(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE context_compaction_delegations(
		activation_digest TEXT NOT NULL CHECK(length(activation_digest)=64 AND activation_digest NOT GLOB '*[^0-9a-f]*'),
		ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 63),
		operation_id TEXT NOT NULL REFERENCES context_compaction_operations(operation_id),
		fact_id TEXT NOT NULL REFERENCES context_compaction_plan_facts(fact_id),
		version INTEGER NOT NULL CHECK(version=1),
		parent_task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		work_task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		execution_task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		worker_id TEXT NOT NULL,
		scope TEXT NOT NULL,
		origin_version INTEGER NOT NULL CHECK(origin_version=1),
		origin_turn_id TEXT NOT NULL,
		origin_attempt_id TEXT NOT NULL,
		origin_tool_call_id TEXT NOT NULL,
		origin_tool_name TEXT NOT NULL CHECK(origin_tool_name IN('delegate','delegate_batch')),
		origin_batch_index INTEGER CHECK(origin_batch_index BETWEEN 0 AND 3),
		work_start_event_id TEXT NOT NULL REFERENCES events(id) DEFERRABLE INITIALLY DEFERRED,
		work_start_event_digest TEXT NOT NULL CHECK(length(work_start_event_digest)=64 AND work_start_event_digest NOT GLOB '*[^0-9a-f]*'),
		work_terminal_event_id TEXT NOT NULL REFERENCES events(id) DEFERRABLE INITIALLY DEFERRED,
		work_terminal_event_digest TEXT NOT NULL CHECK(length(work_terminal_event_digest)=64 AND work_terminal_event_digest NOT GLOB '*[^0-9a-f]*'),
		execution_start_event_id TEXT NOT NULL REFERENCES events(id) DEFERRABLE INITIALLY DEFERRED,
		execution_start_event_digest TEXT NOT NULL CHECK(length(execution_start_event_digest)=64 AND execution_start_event_digest NOT GLOB '*[^0-9a-f]*'),
		execution_context_digest TEXT NOT NULL CHECK(length(execution_context_digest)=64 AND execution_context_digest NOT GLOB '*[^0-9a-f]*'),
		execution_terminal_event_id TEXT NOT NULL REFERENCES events(id) DEFERRABLE INITIALLY DEFERRED,
		execution_terminal_event_digest TEXT NOT NULL CHECK(length(execution_terminal_event_digest)=64 AND execution_terminal_event_digest NOT GLOB '*[^0-9a-f]*'),
		plan_digest TEXT NOT NULL CHECK(length(plan_digest)=64 AND plan_digest NOT GLOB '*[^0-9a-f]*'),
		authority_digest TEXT NOT NULL CHECK(length(authority_digest)=64 AND authority_digest NOT GLOB '*[^0-9a-f]*'),
		engine_digest TEXT NOT NULL CHECK(length(engine_digest)=64 AND engine_digest NOT GLOB '*[^0-9a-f]*'),
		parent_policy_digest TEXT NOT NULL CHECK(length(parent_policy_digest)=64 AND parent_policy_digest NOT GLOB '*[^0-9a-f]*'),
		child_policy_digest TEXT NOT NULL CHECK(length(child_policy_digest)=64 AND child_policy_digest NOT GLOB '*[^0-9a-f]*'),
		result_digest TEXT NOT NULL CHECK(length(result_digest)=64 AND result_digest NOT GLOB '*[^0-9a-f]*'),
		binding_digest TEXT NOT NULL CHECK(length(binding_digest)=64 AND binding_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
		PRIMARY KEY(activation_digest,ordinal),
		UNIQUE(fact_id,ordinal));
	CREATE INDEX context_compaction_delegations_operation ON context_compaction_delegations(operation_id,fact_id,ordinal);
	CREATE INDEX context_compaction_delegations_tasks ON context_compaction_delegations(parent_task_id,work_task_id,execution_task_id);
	CREATE INDEX context_compaction_delegations_events ON context_compaction_delegations(work_start_event_id,work_terminal_event_id,execution_start_event_id,execution_terminal_event_id);
	CREATE TRIGGER context_compaction_delegation_immutable_update BEFORE UPDATE ON context_compaction_delegations
		BEGIN SELECT RAISE(ABORT,'context compaction delegation immutable'); END;
	CREATE TRIGGER context_compaction_delegation_immutable_delete BEFORE DELETE ON context_compaction_delegations
		BEGIN SELECT RAISE(ABORT,'context compaction delegation immutable'); END;
	CREATE TRIGGER context_compaction_delegation_binding BEFORE INSERT ON context_compaction_delegations
		WHEN json_extract(NEW.body,'$.version') IS NOT NEW.version
			OR json_extract(NEW.body,'$.parent_task_id') IS NOT NEW.parent_task_id
			OR json_extract(NEW.body,'$.work_task_id') IS NOT NEW.work_task_id
			OR json_extract(NEW.body,'$.execution_task_id') IS NOT NEW.execution_task_id
			OR json_extract(NEW.body,'$.worker_id') IS NOT NEW.worker_id
			OR json_extract(NEW.body,'$.scope') IS NOT NEW.scope
			OR json_extract(NEW.body,'$.origin.version') IS NOT NEW.origin_version
			OR json_extract(NEW.body,'$.origin.turn_id') IS NOT NEW.origin_turn_id
			OR json_extract(NEW.body,'$.origin.attempt_id') IS NOT NEW.origin_attempt_id
			OR json_extract(NEW.body,'$.origin.tool_call_id') IS NOT NEW.origin_tool_call_id
			OR json_extract(NEW.body,'$.origin.tool_name') IS NOT NEW.origin_tool_name
			OR json_extract(NEW.body,'$.origin.batch_index') IS NOT NEW.origin_batch_index
			OR json_extract(NEW.body,'$.work_start_event_id') IS NOT NEW.work_start_event_id
			OR json_extract(NEW.body,'$.work_start_event_digest') IS NOT NEW.work_start_event_digest
			OR json_extract(NEW.body,'$.work_terminal_event_id') IS NOT NEW.work_terminal_event_id
			OR json_extract(NEW.body,'$.work_terminal_event_digest') IS NOT NEW.work_terminal_event_digest
			OR json_extract(NEW.body,'$.execution_start_event_id') IS NOT NEW.execution_start_event_id
			OR json_extract(NEW.body,'$.execution_start_event_digest') IS NOT NEW.execution_start_event_digest
			OR json_extract(NEW.body,'$.execution_context_digest') IS NOT NEW.execution_context_digest
			OR json_extract(NEW.body,'$.execution_terminal_event_id') IS NOT NEW.execution_terminal_event_id
			OR json_extract(NEW.body,'$.execution_terminal_event_digest') IS NOT NEW.execution_terminal_event_digest
			OR json_extract(NEW.body,'$.plan_digest') IS NOT NEW.plan_digest
			OR json_extract(NEW.body,'$.authority_digest') IS NOT NEW.authority_digest
			OR json_extract(NEW.body,'$.engine_digest') IS NOT NEW.engine_digest
			OR json_extract(NEW.body,'$.parent_policy_digest') IS NOT NEW.parent_policy_digest
			OR json_extract(NEW.body,'$.child_policy_digest') IS NOT NEW.child_policy_digest
			OR json_extract(NEW.body,'$.result_digest') IS NOT NEW.result_digest
			OR json_extract(NEW.body,'$.binding_digest') IS NOT NEW.binding_digest
			OR NOT EXISTS(SELECT 1 FROM context_compaction_plan_facts fact
				WHERE fact.fact_id=NEW.fact_id AND fact.operation_id=NEW.operation_id AND fact.kind='activated'
				AND fact.activation_digest=NEW.activation_digest AND fact.plan_digest=NEW.plan_digest
				AND json_extract(fact.body,'$.activation.activation_digest')=NEW.activation_digest
				AND json_extract(fact.body,'$.activation.task_id')=NEW.parent_task_id
				AND json(json_extract(fact.body,'$.activation.delegations['||NEW.ordinal||']'))=json(NEW.body))
			OR NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=NEW.work_start_event_id AND event.task_id=NEW.work_task_id
				AND json_extract(event.body,'$.kind')='task.started' AND json_extract(event.body,'$.worker_id')=NEW.worker_id
				AND json_extract(event.body,'$.data.parent_task_id')=NEW.parent_task_id
				AND json_extract(event.body,'$.data.delegation_origin.turn_id')=NEW.origin_turn_id
				AND json_extract(event.body,'$.data.delegation_origin.attempt_id')=NEW.origin_attempt_id
				AND json_extract(event.body,'$.data.delegation_origin.tool_call_id')=NEW.origin_tool_call_id
				AND json_extract(event.body,'$.data.delegation_origin.tool_name')=NEW.origin_tool_name
				AND json_extract(event.body,'$.data.delegation_origin.batch_index') IS NEW.origin_batch_index
				AND json_extract(event.body,'$.data.delegation_compaction_authority.root_task_id')=NEW.parent_task_id
				AND json_extract(event.body,'$.data.delegation_compaction_authority.plan_digest')=NEW.plan_digest
				AND json_extract(event.body,'$.data.delegation_compaction_authority.authority_digest')=NEW.authority_digest
				AND json_extract(event.body,'$.data.delegation_compaction_authority.scope')=NEW.scope
				AND json_extract(event.body,'$.data.delegation_compaction_authority.inherited_engine_digest')=NEW.engine_digest
				AND json_extract(event.body,'$.data.delegation_compaction_authority.parent_policy_digest')=NEW.parent_policy_digest
				AND json_extract(event.body,'$.data.delegation_compaction_authority.child_policy_digest')=NEW.child_policy_digest
				AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=NEW.work_start_event_digest)
			OR NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=NEW.work_terminal_event_id AND event.task_id=NEW.work_task_id
				AND json_extract(event.body,'$.kind')='task.completed' AND json_extract(event.body,'$.worker_id')=NEW.worker_id
				AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=NEW.work_terminal_event_digest)
			OR NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=NEW.execution_start_event_id AND event.task_id=NEW.execution_task_id
				AND json_extract(event.body,'$.kind')='task.started' AND json_extract(event.body,'$.data.parent_task_id')=NEW.work_task_id
				AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=NEW.execution_start_event_digest)
			OR NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=NEW.execution_terminal_event_id AND event.task_id=NEW.execution_task_id
				AND json_extract(event.body,'$.kind')='task.completed'
				AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=NEW.execution_terminal_event_digest)
		BEGIN SELECT RAISE(ABORT,'context compaction delegation binding'); END;
	INSERT INTO context_compaction_delegations(
		activation_digest,ordinal,operation_id,fact_id,version,parent_task_id,work_task_id,execution_task_id,worker_id,scope,
		origin_version,origin_turn_id,origin_attempt_id,origin_tool_call_id,origin_tool_name,origin_batch_index,
		work_start_event_id,work_start_event_digest,work_terminal_event_id,work_terminal_event_digest,
		execution_start_event_id,execution_start_event_digest,execution_context_digest,execution_terminal_event_id,execution_terminal_event_digest,
		plan_digest,authority_digest,engine_digest,parent_policy_digest,child_policy_digest,result_digest,binding_digest,body)
	SELECT fact.activation_digest,delegation.key,fact.operation_id,fact.fact_id,
		json_extract(delegation.value,'$.version'),json_extract(delegation.value,'$.parent_task_id'),json_extract(delegation.value,'$.work_task_id'),
		json_extract(delegation.value,'$.execution_task_id'),json_extract(delegation.value,'$.worker_id'),json_extract(delegation.value,'$.scope'),
		json_extract(delegation.value,'$.origin.version'),json_extract(delegation.value,'$.origin.turn_id'),json_extract(delegation.value,'$.origin.attempt_id'),
		json_extract(delegation.value,'$.origin.tool_call_id'),json_extract(delegation.value,'$.origin.tool_name'),json_extract(delegation.value,'$.origin.batch_index'),
		json_extract(delegation.value,'$.work_start_event_id'),json_extract(delegation.value,'$.work_start_event_digest'),
		json_extract(delegation.value,'$.work_terminal_event_id'),json_extract(delegation.value,'$.work_terminal_event_digest'),
		json_extract(delegation.value,'$.execution_start_event_id'),json_extract(delegation.value,'$.execution_start_event_digest'),
		json_extract(delegation.value,'$.execution_context_digest'),json_extract(delegation.value,'$.execution_terminal_event_id'),
		json_extract(delegation.value,'$.execution_terminal_event_digest'),json_extract(delegation.value,'$.plan_digest'),
		json_extract(delegation.value,'$.authority_digest'),json_extract(delegation.value,'$.engine_digest'),
		json_extract(delegation.value,'$.parent_policy_digest'),json_extract(delegation.value,'$.child_policy_digest'),
		json_extract(delegation.value,'$.result_digest'),json_extract(delegation.value,'$.binding_digest'),CAST(delegation.value AS BLOB)
	FROM context_compaction_plan_facts fact,json_each(fact.body,'$.activation.delegations') delegation
	WHERE fact.kind='activated';
	PRAGMA user_version=52;`); err != nil {
		return err
	}
	return validateContextCompactionDelegationObjects(ctx, conn)
}

func validateContextCompactionDelegationSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateContextLineageSchema(ctx, conn); err != nil {
		return err
	}
	return validateContextCompactionDelegationObjects(ctx, conn)
}

func validateContextCompactionDelegationObjects(ctx context.Context, conn *sql.Conn) error {
	want := "activation_digest:TEXT:1:1,ordinal:INTEGER:1:2,operation_id:TEXT:1:0,fact_id:TEXT:1:0,version:INTEGER:1:0,parent_task_id:TEXT:1:0,work_task_id:TEXT:1:0,execution_task_id:TEXT:1:0,worker_id:TEXT:1:0,scope:TEXT:1:0,origin_version:INTEGER:1:0,origin_turn_id:TEXT:1:0,origin_attempt_id:TEXT:1:0,origin_tool_call_id:TEXT:1:0,origin_tool_name:TEXT:1:0,origin_batch_index:INTEGER:0:0,work_start_event_id:TEXT:1:0,work_start_event_digest:TEXT:1:0,work_terminal_event_id:TEXT:1:0,work_terminal_event_digest:TEXT:1:0,execution_start_event_id:TEXT:1:0,execution_start_event_digest:TEXT:1:0,execution_context_digest:TEXT:1:0,execution_terminal_event_id:TEXT:1:0,execution_terminal_event_digest:TEXT:1:0,plan_digest:TEXT:1:0,authority_digest:TEXT:1:0,engine_digest:TEXT:1:0,parent_policy_digest:TEXT:1:0,child_policy_digest:TEXT:1:0,result_digest:TEXT:1:0,binding_digest:TEXT:1:0,body:BLOB:1:0"
	if !browserTableShape(ctx, conn, "context_compaction_delegations", want) {
		return errors.New("invalid context compaction delegation table")
	}
	for _, object := range []struct {
		kind, name string
		rules      []string
	}{
		{"index", "context_compaction_delegations_operation", []string{"oncontext_compaction_delegations(operation_id,fact_id,ordinal)"}},
		{"index", "context_compaction_delegations_tasks", []string{"oncontext_compaction_delegations(parent_task_id,work_task_id,execution_task_id)"}},
		{"index", "context_compaction_delegations_events", []string{"oncontext_compaction_delegations(work_start_event_id,work_terminal_event_id,execution_start_event_id,execution_terminal_event_id)"}},
		{"trigger", "context_compaction_delegation_immutable_update", []string{"beforeupdateoncontext_compaction_delegations", "raise(abort,'contextcompactiondelegationimmutable')"}},
		{"trigger", "context_compaction_delegation_immutable_delete", []string{"beforedeleteoncontext_compaction_delegations", "raise(abort,'contextcompactiondelegationimmutable')"}},
		{"trigger", "context_compaction_delegation_binding", []string{"beforeinsertoncontext_compaction_delegations", "fact.fact_id=new.fact_id", "log.body_digest=new.work_start_event_digest", "raise(abort,'contextcompactiondelegationbinding')"}},
	} {
		if !workboardObjectRules(ctx, conn, object.kind, object.name, object.rules) {
			return errors.New("invalid context compaction delegation schema")
		}
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	if rows.Next() || rows.Err() != nil {
		rows.Close()
		return errors.New("invalid context compaction delegation foreign keys")
	}
	if err = rows.Close(); err != nil {
		return err
	}
	return validateContextCompactionDelegationBindings(ctx, conn)
}

func validateContextCompactionDelegationBindings(ctx context.Context, conn *sql.Conn) error {
	var corrupt int
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM context_compaction_plan_facts fact,json_each(fact.body,'$.activation.delegations') delegation
		LEFT JOIN context_compaction_delegations binding ON binding.activation_digest=fact.activation_digest
			AND binding.ordinal=delegation.key AND binding.fact_id=fact.fact_id
		WHERE fact.kind='activated' AND (binding.activation_digest IS NULL OR json(binding.body)!=json(delegation.value))
		UNION ALL
		SELECT 1 FROM context_compaction_delegations binding
		LEFT JOIN context_compaction_plan_facts fact ON fact.fact_id=binding.fact_id AND fact.operation_id=binding.operation_id
			AND fact.kind='activated' AND fact.activation_digest=binding.activation_digest
		WHERE fact.fact_id IS NULL OR json(json_extract(fact.body,'$.activation.delegations['||binding.ordinal||']'))!=json(binding.body)
	)`).Scan(&corrupt); err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("corrupt context compaction delegation bindings")
	}
	rows, err := conn.QueryContext(ctx, `SELECT version,parent_task_id,work_task_id,execution_task_id,worker_id,scope,
		origin_version,origin_turn_id,origin_attempt_id,origin_tool_call_id,origin_tool_name,origin_batch_index,
		work_start_event_id,work_start_event_digest,work_terminal_event_id,work_terminal_event_digest,
		execution_start_event_id,execution_start_event_digest,execution_context_digest,execution_terminal_event_id,execution_terminal_event_digest,
		plan_digest,authority_digest,engine_digest,parent_policy_digest,child_policy_digest,result_digest,binding_digest,body
		FROM context_compaction_delegations ORDER BY activation_digest,ordinal`)
	if err != nil {
		return err
	}
	type storedBinding struct {
		version                                                          int
		parent, work, execution, worker, scope                           string
		originVersion                                                    int
		originTurn, originAttempt, originCall, originTool                string
		originBatch                                                      sql.NullInt64
		workStartID, workStartDigest, workTerminalID, workTerminalDigest string
		executionStartID, executionStartDigest, executionContextDigest   string
		executionTerminalID, executionTerminalDigest                     string
		planDigest, authorityDigest, engineDigest                        string
		parentPolicyDigest, childPolicyDigest, resultDigest, digest      string
		body                                                             []byte
	}
	items := []storedBinding{}
	for rows.Next() {
		var item storedBinding
		if err = rows.Scan(&item.version, &item.parent, &item.work, &item.execution, &item.worker, &item.scope,
			&item.originVersion, &item.originTurn, &item.originAttempt, &item.originCall, &item.originTool, &item.originBatch,
			&item.workStartID, &item.workStartDigest, &item.workTerminalID, &item.workTerminalDigest,
			&item.executionStartID, &item.executionStartDigest, &item.executionContextDigest,
			&item.executionTerminalID, &item.executionTerminalDigest, &item.planDigest, &item.authorityDigest,
			&item.engineDigest, &item.parentPolicyDigest,
			&item.childPolicyDigest, &item.resultDigest, &item.digest, &item.body); err != nil {
			rows.Close()
			return errors.New("corrupt context compaction delegation semantics")
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		var binding sessions.DelegationCompactionBinding
		if json.Unmarshal(item.body, &binding) != nil || binding.Validate() != nil ||
			binding.Version != item.version || binding.ParentTaskID != item.parent || binding.WorkTaskID != item.work ||
			binding.ExecutionTaskID != item.execution || binding.WorkerID != item.worker || binding.Scope != item.scope ||
			binding.Origin.Version != item.originVersion || binding.Origin.TurnID != item.originTurn ||
			binding.Origin.AttemptID != item.originAttempt || binding.Origin.ToolCallID != item.originCall || binding.Origin.ToolName != item.originTool ||
			(binding.Origin.BatchIndex == nil) != !item.originBatch.Valid || binding.Origin.BatchIndex != nil && int64(*binding.Origin.BatchIndex) != item.originBatch.Int64 ||
			binding.WorkStartEventID != item.workStartID || binding.WorkStartEventDigest != item.workStartDigest ||
			binding.WorkTerminalEventID != item.workTerminalID || binding.WorkTerminalEventDigest != item.workTerminalDigest ||
			binding.ExecutionStartEventID != item.executionStartID || binding.ExecutionStartEventDigest != item.executionStartDigest ||
			binding.ExecutionContextDigest != item.executionContextDigest || binding.ExecutionTerminalEventID != item.executionTerminalID ||
			binding.ExecutionTerminalEventDigest != item.executionTerminalDigest || binding.PlanDigest != item.planDigest ||
			binding.AuthorityDigest != item.authorityDigest || binding.EngineDigest != item.engineDigest ||
			binding.ParentPolicyDigest != item.parentPolicyDigest || binding.ChildPolicyDigest != item.childPolicyDigest ||
			binding.ResultDigest != item.resultDigest || binding.BindingDigest != item.digest {
			return errors.New("corrupt context compaction delegation semantics")
		}
		canonical, marshalErr := json.Marshal(binding)
		if marshalErr != nil || !bytes.Equal(canonical, item.body) {
			return errors.New("non-canonical context compaction delegation body")
		}
		work, historyErr := contextCompactionDelegationHistory(ctx, conn, item.work, true)
		if historyErr != nil {
			return errors.New("corrupt context compaction delegation work history")
		}
		execution, historyErr := contextCompactionDelegationHistory(ctx, conn, item.execution, true)
		if historyErr != nil {
			return errors.New("corrupt context compaction delegation execution history")
		}
		evidence, projectionErr := sessions.ProjectCompletedDelegation(work, execution)
		if projectionErr != nil || evidence.Validate() != nil ||
			evidence.ParentTaskID != binding.ParentTaskID || evidence.WorkTaskID != binding.WorkTaskID ||
			evidence.ExecutionTaskID != binding.ExecutionTaskID || evidence.WorkerID != binding.WorkerID ||
			evidence.Scope != binding.Scope || !reflect.DeepEqual(evidence.Origin, binding.Origin) ||
			evidence.WorkStart.ID != binding.WorkStartEventID || evidence.WorkStartDigest != binding.WorkStartEventDigest ||
			evidence.WorkTerminal.ID != binding.WorkTerminalEventID || evidence.WorkTerminalDigest != binding.WorkTerminalEventDigest ||
			evidence.ExecutionStart.ID != binding.ExecutionStartEventID || evidence.ExecutionStartDigest != binding.ExecutionStartEventDigest ||
			evidence.ExecutionContextDigest != binding.ExecutionContextDigest ||
			evidence.ExecutionTerminal.ID != binding.ExecutionTerminalEventID || evidence.ExecutionTerminalDigest != binding.ExecutionTerminalEventDigest ||
			evidence.Authority.RootTaskID != binding.ParentTaskID || evidence.Authority.Scope != binding.Scope ||
			evidence.Authority.PlanDigest != binding.PlanDigest || evidence.Authority.AuthorityDigest != binding.AuthorityDigest ||
			evidence.Authority.InheritedEngineDigest != binding.EngineDigest ||
			evidence.Authority.ParentPolicyDigest != binding.ParentPolicyDigest ||
			evidence.Authority.ChildPolicyDigest != binding.ChildPolicyDigest || evidence.ResultDigest != binding.ResultDigest {
			return errors.New("corrupt context compaction delegation durable projection")
		}
		parent, historyErr := contextCompactionDelegationHistory(ctx, conn, item.parent, false)
		if historyErr != nil || validateContextCompactionDelegationParentProjection(binding, evidence, parent) != nil {
			return errors.New("corrupt context compaction delegation parent projection")
		}
		var eventCorrupt int
		if err = conn.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=? AND event.task_id=? AND json_extract(event.body,'$.kind')='task.started'
				AND json_extract(event.body,'$.worker_id')=? AND json_extract(event.body,'$.data.parent_task_id')=?
				AND json_extract(event.body,'$.data.delegation_origin.version')=?
				AND json_extract(event.body,'$.data.delegation_origin.turn_id')=?
				AND json_extract(event.body,'$.data.delegation_origin.attempt_id')=?
				AND json_extract(event.body,'$.data.delegation_origin.tool_call_id')=?
				AND json_extract(event.body,'$.data.delegation_origin.tool_name')=?
				AND json_extract(event.body,'$.data.delegation_origin.batch_index') IS ?
				AND json_extract(event.body,'$.data.delegation_compaction_authority.scope')=?
				AND json_extract(event.body,'$.data.delegation_compaction_authority.inherited_engine_digest')=?
				AND json_extract(event.body,'$.data.delegation_compaction_authority.parent_policy_digest')=?
				AND json_extract(event.body,'$.data.delegation_compaction_authority.child_policy_digest')=?
				AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=?)
			OR NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=? AND event.task_id=? AND json_extract(event.body,'$.kind')='task.completed'
				AND json_extract(event.body,'$.worker_id')=? AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=?)
			OR NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=? AND event.task_id=? AND json_extract(event.body,'$.kind')='task.started'
				AND json_extract(event.body,'$.data.parent_task_id')=? AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=?)
			OR NOT EXISTS(SELECT 1 FROM events event JOIN event_log log ON log.event_id=event.id
				WHERE event.id=? AND event.task_id=? AND json_extract(event.body,'$.kind')='task.completed'
				AND log.task_id=event.task_id AND log.task_sequence=event.sequence AND log.body_digest=?))`,
			item.workStartID, item.work, item.worker, item.parent, item.originVersion, item.originTurn, item.originAttempt,
			item.originCall, item.originTool, item.originBatch, item.scope, item.engineDigest, item.parentPolicyDigest, item.childPolicyDigest, item.workStartDigest,
			item.workTerminalID, item.work, item.worker, item.workTerminalDigest,
			item.executionStartID, item.execution, item.work, item.executionStartDigest,
			item.executionTerminalID, item.execution, item.executionTerminalDigest).Scan(&eventCorrupt); err != nil {
			return err
		}
		if eventCorrupt != 0 {
			return errors.New("corrupt context compaction delegation event binding")
		}
	}
	return nil
}

// contextCompactionDelegationHistory reads one complete durable task stream
// while the initialization connection holds its migration/reopen transaction.
// The projector below must see exactly the canonical bytes covered by the
// append-only event ledger, not a permissive JSON reconstruction.
func contextCompactionDelegationHistory(ctx context.Context, conn *sql.Conn, task string, requireCompleted bool) ([]runtime.Event, error) {
	var head int64
	var state string
	if err := conn.QueryRowContext(ctx, `SELECT sequence,state FROM task_heads WHERE task_id=?`, task).Scan(&head, &state); err != nil ||
		head < 1 || head > sessions.MaxTaskEvents || requireCompleted && state != "completed" {
		return nil, sessions.ErrHistory
	}
	rows, err := conn.QueryContext(ctx, `SELECT e.id,e.sequence,e.body,l.task_id,l.task_sequence,l.body_digest
		FROM events e LEFT JOIN event_log l ON l.event_id=e.id
		WHERE e.task_id=? ORDER BY e.sequence LIMIT 10001`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]runtime.Event, 0, head)
	total := 0
	for rows.Next() {
		var id, ledgerTask, ledgerDigest sql.NullString
		var sequence, ledgerSequence sql.NullInt64
		var body []byte
		if err = rows.Scan(&id, &sequence, &body, &ledgerTask, &ledgerSequence, &ledgerDigest); err != nil ||
			!id.Valid || !sequence.Valid || !ledgerTask.Valid || !ledgerSequence.Valid || !ledgerDigest.Valid ||
			sequence.Int64 != int64(len(events)+1) || ledgerTask.String != task || ledgerSequence.Int64 != sequence.Int64 ||
			len(body) == 0 || len(body) > sessions.MaxEventPageBytes-total || ledgerDigest.String != streamBodyDigest(body) {
			return nil, sessions.ErrHistory
		}
		total += len(body)
		var event runtime.Event
		if json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != id.String || event.TaskID != task ||
			event.Sequence != sequence.Int64 {
			return nil, sessions.ErrHistory
		}
		canonical, encodeErr := event.Encode()
		if encodeErr != nil || !bytes.Equal(canonical, body) {
			return nil, sessions.ErrHistory
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if int64(len(events)) != head {
		return nil, sessions.ErrHistory
	}
	return events, nil
}

func validateContextCompactionDelegationParentProjection(binding sessions.DelegationCompactionBinding, evidence sessions.CompletedDelegationEvidence, parent []runtime.Event) error {
	var proposal providers.ToolCall
	var completion *runtime.Event
	for i := range parent {
		event := &parent[i]
		if event.TurnID != binding.Origin.TurnID || event.AttemptID != binding.Origin.AttemptID {
			continue
		}
		if event.Kind == runtime.TurnCompleted {
			for _, call := range event.Data.ToolCalls {
				if call.ID == binding.Origin.ToolCallID && call.Name == binding.Origin.ToolName {
					if proposal.ID != "" {
						return sessions.ErrHistory
					}
					proposal = call
				}
			}
		}
		if event.Kind == runtime.ToolCompleted && event.Data.ToolCallID == binding.Origin.ToolCallID && event.Data.ToolName == binding.Origin.ToolName {
			if completion != nil {
				return sessions.ErrHistory
			}
			completion = event
		}
	}
	if proposal.ID == "" || completion == nil {
		return sessions.ErrHistory
	}
	suffix := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{proposal}},
		{Role: "tool", ToolCallID: proposal.ID, Content: completion.Data.Text, ToolFailed: completion.Data.Code == "tool_failed"},
	}
	calls, err := completedDelegationCalls(suffix, parent)
	call, ok := calls[proposal.ID]
	if err != nil || !ok || evidence.SessionID != call.session || evidence.ExecutionStart.SessionID == call.session {
		return sessions.ErrHistory
	}
	index := 0
	if binding.Origin.ToolName == "delegate" {
		if binding.Origin.BatchIndex != nil {
			return sessions.ErrHistory
		}
	} else {
		if binding.Origin.BatchIndex == nil {
			return sessions.ErrHistory
		}
		index = *binding.Origin.BatchIndex
	}
	if index < 0 || index >= len(call.prompts) || index >= len(call.results) || binding.ResultDigest != call.results[index] ||
		len(evidence.ExecutionStart.Data.Messages) != 1 || evidence.ExecutionStart.Data.Messages[0].Role != "user" ||
		evidence.ExecutionStart.Data.Messages[0].Content != call.prompts[index] || len(evidence.ExecutionStart.Data.Messages[0].ToolCalls) != 0 ||
		evidence.ExecutionStart.Data.Messages[0].ToolCallID != "" || evidence.ExecutionStart.Data.Messages[0].ToolFailed {
		return sessions.ErrHistory
	}
	return nil
}
