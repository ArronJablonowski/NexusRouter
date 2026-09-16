package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

type frozenDelegationCompactionPolicy struct {
	Version   int                  `json:"version"`
	Enabled   bool                 `json:"enabled"`
	ReadTools bool                 `json:"read_tools"`
	Parent    tools.PolicySnapshot `json:"parent"`
	Child     tools.PolicySnapshot `json:"child"`
}

func delegationCompactionPolicyEvidence(raw json.RawMessage) (frozenDelegationCompactionPolicy, error) {
	var envelope struct {
		Delegation frozenDelegationCompactionPolicy `json:"delegation"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &envelope) != nil || envelope.Delegation.Version != 1 || !envelope.Delegation.Enabled ||
		envelope.Delegation.Parent.Validate() != nil || envelope.Delegation.Child.Validate() != nil ||
		!envelope.Delegation.Child.AtMost(envelope.Delegation.Parent) || !exactDelegationPolicySurface(envelope.Delegation) {
		return frozenDelegationCompactionPolicy{}, sessions.ErrContextCompactionLifecycle
	}
	return envelope.Delegation, nil
}

func exactDelegationPolicySurface(policy frozenDelegationCompactionPolicy) bool {
	want := [][2]string{{"delegate", "delegation"}, {"delegate_batch", "delegation"}, {"read_file", "workspace"}}
	if len(policy.Parent.Entries) != len(want) || len(policy.Child.Entries) != len(want) {
		return false
	}
	for i := range want {
		if policy.Parent.Entries[i].Tool != want[i][0] || policy.Parent.Entries[i].Scope != want[i][1] ||
			policy.Parent.Entries[i].Decision != tools.Allow || policy.Child.Entries[i].Tool != want[i][0] || policy.Child.Entries[i].Scope != want[i][1] {
			return false
		}
	}
	return policy.Child.Entries[0].Decision == tools.Deny && policy.Child.Entries[1].Decision == tools.Deny &&
		policy.Child.Entries[2].Decision == map[bool]tools.Decision{false: tools.Deny, true: tools.Allow}[policy.ReadTools]
}

type completedDelegationCall struct {
	name, turn, attempt, session string
	prompts                      []string
	results                      []string
}

func completedDelegationCalls(suffix []providers.Message, parentEvents []runtime.Event) (map[string]completedDelegationCall, error) {
	calls, replies := map[string]providers.ToolCall{}, map[string]bool{}
	for _, message := range suffix {
		for _, call := range message.ToolCalls {
			if call.Name != "delegate" && call.Name != "delegate_batch" {
				continue
			}
			if call.ID == "" {
				return nil, sessions.ErrHistory
			}
			if _, exists := calls[call.ID]; exists {
				return nil, sessions.ErrHistory
			}
			calls[call.ID] = call
		}
		if message.Role == "tool" && message.ToolCallID != "" {
			if replies[message.ToolCallID] {
				return nil, sessions.ErrHistory
			}
			replies[message.ToolCallID] = true
		}
	}
	out := map[string]completedDelegationCall{}
	for id, proposed := range calls {
		if !replies[id] {
			return nil, sessions.ErrHistory
		}
		name := proposed.Name
		var started, completed *runtime.Event
		for i := range parentEvents {
			event := &parentEvents[i]
			if event.Data.ToolCallID != id || event.Data.ToolName != name {
				continue
			}
			switch event.Kind {
			case runtime.ToolStarted:
				if started != nil {
					return nil, sessions.ErrHistory
				}
				started = event
			case runtime.ToolCompleted:
				if completed != nil {
					return nil, sessions.ErrHistory
				}
				completed = event
			}
		}
		if started == nil || completed == nil || started.TurnID == "" || started.AttemptID == "" ||
			started.TurnID != completed.TurnID || started.AttemptID != completed.AttemptID || started.SessionID != completed.SessionID || started.Sequence >= completed.Sequence {
			return nil, sessions.ErrHistory
		}
		if completed.Data.Code != "" || completed.Data.Effect != runtime.NoEffect {
			return nil, sessions.ErrHistory
		}
		prompts, err := completedDelegationPrompts(proposed)
		if err != nil {
			return nil, err
		}
		results, err := completedDelegationResultDigests(proposed.Name, len(prompts), completed.Data.Text)
		if err != nil {
			return nil, err
		}
		out[id] = completedDelegationCall{name: name, turn: started.TurnID, attempt: started.AttemptID, session: started.SessionID, prompts: prompts, results: results}
	}
	return out, nil
}

func completedDelegationPrompts(call providers.ToolCall) ([]string, error) {
	type input struct {
		Prompt     string `json:"prompt"`
		Validation string `json:"validation"`
	}
	validate := func(item input) bool {
		return item.Prompt != "" && (item.Validation == "text" || item.Validation == "go_source")
	}
	if call.Name == "delegate" {
		var item input
		if json.Unmarshal(call.Arguments, &item) != nil || !validate(item) {
			return nil, sessions.ErrHistory
		}
		return []string{item.Prompt}, nil
	}
	var batch struct {
		Tasks []input `json:"tasks"`
	}
	if json.Unmarshal(call.Arguments, &batch) != nil || len(batch.Tasks) < 2 || len(batch.Tasks) > 4 {
		return nil, sessions.ErrHistory
	}
	prompts := make([]string, len(batch.Tasks))
	for i, item := range batch.Tasks {
		if !validate(item) {
			return nil, sessions.ErrHistory
		}
		prompts[i] = item.Prompt
	}
	return prompts, nil
}

func completedDelegationResultDigests(name string, count int, result string) ([]string, error) {
	items := []json.RawMessage{json.RawMessage(result)}
	if name == "delegate_batch" {
		var output struct {
			Results []json.RawMessage `json:"results"`
		}
		if json.Unmarshal([]byte(result), &output) != nil || len(output.Results) != count {
			return nil, sessions.ErrHistory
		}
		items = output.Results
	}
	digests := make([]string, len(items))
	for i := range items {
		if len(items[i]) == 0 || !json.Valid(items[i]) {
			return nil, sessions.ErrHistory
		}
		digests[i] = rawJSONDigest(items[i])
	}
	return digests, nil
}

func deriveDelegationCompactionBindings(ctx context.Context, tx *sql.Tx, parentEvents []runtime.Event, suffix []providers.Message,
	parentTaskID string, start sessions.ContextCompactionPlanStart, plan runtime.ContextCompactionPlan) ([]sessions.DelegationCompactionBinding, error) {
	calls, err := completedDelegationCalls(suffix, parentEvents)
	if err != nil {
		return nil, err
	}
	workIDs, err := delegationWorkTaskIDs(ctx, tx, parentTaskID, calls)
	if err != nil || len(workIDs) > sessions.MaxDelegationCompactionBindings {
		return nil, sessions.ErrHistory
	}
	if len(calls) == 0 && len(workIDs) == 0 {
		return []sessions.DelegationCompactionBinding{}, nil
	}
	var parentCanceled bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_cancellations WHERE task_id=?)`, parentTaskID).Scan(&parentCanceled); err != nil || parentCanceled {
		return nil, sessions.ErrHistory
	}
	frozen, err := delegationCompactionPolicyEvidence(start.PolicySnapshot)
	if err != nil {
		return nil, err
	}
	bindings := make([]sessions.DelegationCompactionBinding, 0, len(workIDs))
	for _, workID := range workIDs {
		var workEvents []runtime.Event
		if _, err = taskSnapshotWithEvents(ctx, tx, workID, &workEvents); err != nil || len(workEvents) == 0 {
			return nil, sessions.ErrHistory
		}
		origin := workEvents[0].Data.DelegationOrigin
		if origin == nil {
			continue
		}
		call, observed := calls[origin.ToolCallID]
		if !observed {
			return nil, sessions.ErrHistory
		}
		if origin.ToolName != call.name || origin.TurnID != call.turn || origin.AttemptID != call.attempt {
			return nil, sessions.ErrHistory
		}
		executionIDs, queryErr := delegationChildTaskIDs(ctx, tx, workID, 2, false)
		if queryErr != nil || len(executionIDs) != 1 {
			return nil, sessions.ErrHistory
		}
		var executionEvents []runtime.Event
		if _, err = taskSnapshotWithEvents(ctx, tx, executionIDs[0], &executionEvents); err != nil {
			return nil, sessions.ErrHistory
		}
		evidence, projectErr := sessions.ProjectCompletedDelegation(workEvents, executionEvents)
		index := 0
		if origin.BatchIndex != nil {
			index = *origin.BatchIndex
		}
		isolatedContext := index >= 0 && index < len(call.prompts) && len(evidence.ExecutionStart.Data.Messages) == 1 &&
			evidence.ExecutionStart.Data.Messages[0].Role == "user" && evidence.ExecutionStart.Data.Messages[0].Content == call.prompts[index] &&
			len(evidence.ExecutionStart.Data.Messages[0].ToolCalls) == 0 && evidence.ExecutionStart.Data.Messages[0].ToolCallID == "" && !evidence.ExecutionStart.Data.Messages[0].ToolFailed
		var authorityParent, authorityChild tools.PolicySnapshot
		policyErr := json.Unmarshal(evidence.Authority.ParentPolicy, &authorityParent)
		if policyErr == nil {
			policyErr = json.Unmarshal(evidence.Authority.ChildPolicy, &authorityChild)
		}
		parentPolicy, parentPolicyErr := frozen.Parent.CanonicalJSON()
		childPolicy, childPolicyErr := frozen.Child.CanonicalJSON()
		expectedAuthority, expectedAuthorityErr := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
			RootTaskID: parentTaskID, PlanDigest: plan.PlanDigest, InheritedEngineDigest: plan.Engine.Digest,
			Scope: "delegation-" + parentTaskID, ParentPolicy: parentPolicy, ChildPolicy: childPolicy,
		})
		if projectErr != nil || evidence.Validate() != nil || !isolatedContext || policyErr != nil || parentPolicyErr != nil || childPolicyErr != nil || expectedAuthorityErr != nil ||
			authorityParent.Validate() != nil || authorityChild.Validate() != nil ||
			evidence.SessionID != call.session || evidence.ExecutionStart.SessionID == evidence.SessionID ||
			!reflect.DeepEqual(evidence.Authority, expectedAuthority) || !reflect.DeepEqual(authorityParent, frozen.Parent) ||
			!reflect.DeepEqual(authorityChild, frozen.Child) || !authorityChild.AtMost(authorityParent) {
			return nil, sessions.ErrHistory
		}
		if err = validateCompletedDelegationRelease(ctx, tx, evidence); err != nil {
			return nil, err
		}
		binding, sealErr := sessions.SealDelegationCompactionBinding(sessions.DelegationCompactionBinding{
			ParentTaskID: evidence.ParentTaskID, WorkTaskID: evidence.WorkTaskID, ExecutionTaskID: evidence.ExecutionTaskID,
			WorkerID: evidence.WorkerID, Scope: evidence.Scope, Origin: evidence.Origin,
			WorkStartEventID: evidence.WorkStart.ID, WorkStartEventDigest: evidence.WorkStartDigest,
			WorkTerminalEventID: evidence.WorkTerminal.ID, WorkTerminalEventDigest: evidence.WorkTerminalDigest,
			ExecutionStartEventID: evidence.ExecutionStart.ID, ExecutionStartEventDigest: evidence.ExecutionStartDigest,
			ExecutionContextDigest:   evidence.ExecutionContextDigest,
			ExecutionTerminalEventID: evidence.ExecutionTerminal.ID, ExecutionTerminalEventDigest: evidence.ExecutionTerminalDigest,
			PlanDigest: evidence.Authority.PlanDigest, AuthorityDigest: evidence.Authority.AuthorityDigest,
			EngineDigest: evidence.Authority.InheritedEngineDigest, ParentPolicyDigest: evidence.Authority.ParentPolicyDigest,
			ChildPolicyDigest: evidence.Authority.ChildPolicyDigest, ResultDigest: evidence.ResultDigest,
		})
		if sealErr != nil {
			return nil, sealErr
		}
		bindings = append(bindings, binding)
	}
	observed := make(map[string]map[int]bool, len(calls))
	for _, binding := range bindings {
		call := calls[binding.Origin.ToolCallID]
		index := 0
		if binding.Origin.BatchIndex != nil {
			index = *binding.Origin.BatchIndex
		}
		if index >= len(call.results) || binding.ResultDigest != call.results[index] {
			return nil, sessions.ErrHistory
		}
		if observed[binding.Origin.ToolCallID] == nil {
			observed[binding.Origin.ToolCallID] = map[int]bool{}
		}
		if observed[binding.Origin.ToolCallID][index] {
			return nil, sessions.ErrHistory
		}
		observed[binding.Origin.ToolCallID][index] = true
	}
	for id, call := range calls {
		if len(observed[id]) != len(call.results) {
			return nil, sessions.ErrHistory
		}
		for i := range call.results {
			if !observed[id][i] {
				return nil, sessions.ErrHistory
			}
		}
	}
	sort.Slice(bindings, func(i, j int) bool {
		a, b := bindings[i], bindings[j]
		left := []string{a.Origin.TurnID, a.Origin.AttemptID, a.Origin.ToolCallID, a.Origin.ToolName, delegationBatchKey(a.Origin), a.WorkTaskID}
		right := []string{b.Origin.TurnID, b.Origin.AttemptID, b.Origin.ToolCallID, b.Origin.ToolName, delegationBatchKey(b.Origin), b.WorkTaskID}
		for k := range left {
			if left[k] != right[k] {
				return left[k] < right[k]
			}
		}
		return false
	})
	return bindings, nil
}

func delegationBatchKey(origin runtime.DelegationOrigin) string {
	if origin.BatchIndex == nil {
		return "-"
	}
	return string(rune('0' + *origin.BatchIndex))
}

func rawJSONDigest(raw json.RawMessage) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func delegationChildTaskIDs(ctx context.Context, tx *sql.Tx, parent string, limit int, delegatedOnly bool) ([]string, error) {
	query := `SELECT task_id FROM events WHERE sequence=1 AND json_extract(body,'$.kind')=?
		AND json_extract(body,'$.data.parent_task_id')=?`
	if delegatedOnly {
		query += ` AND json_type(body,'$.data.delegation_origin')='object'`
	}
	query += ` ORDER BY task_id LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, runtime.TaskStarted, parent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func delegationWorkTaskIDs(ctx context.Context, tx *sql.Tx, parent string, calls map[string]completedDelegationCall) ([]string, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(calls))
	for id := range calls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	query := `SELECT task_id FROM events WHERE sequence=1 AND json_extract(body,'$.kind')=?
		AND json_extract(body,'$.data.parent_task_id')=? AND json_type(body,'$.data.delegation_origin')='object' AND json_extract(body,'$.data.delegation_origin.tool_call_id') IN (`
	args := []any{runtime.TaskStarted, parent}
	for i, id := range ids {
		if i > 0 {
			query += ","
		}
		query += "?"
		args = append(args, id)
	}
	query += `) ORDER BY task_id LIMIT ?`
	args = append(args, sessions.MaxDelegationCompactionBindings+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var work []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		work = append(work, id)
	}
	return work, rows.Err()
}

func validateCompletedDelegationRelease(ctx context.Context, tx *sql.Tx, evidence sessions.CompletedDelegationEvidence) error {
	for _, task := range []string{evidence.WorkTaskID, evidence.ExecutionTaskID} {
		var canceled, recovered bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM task_cancellations WHERE task_id=?),
			EXISTS(SELECT 1 FROM lease_recoveries recovery JOIN resource_leases lease ON lease.token=recovery.lease_token WHERE lease.task_id=?)`,
			task, task).Scan(&canceled, &recovered); err != nil || canceled || recovered {
			return sessions.ErrHistory
		}
		if submission := map[string]string{evidence.WorkTaskID: evidence.WorkStart.Data.SubmissionID, evidence.ExecutionTaskID: evidence.ExecutionStart.Data.SubmissionID}[task]; submission != "" {
			var submissionRecovered bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM submission_recoveries WHERE submission_id=?)`, submission).Scan(&submissionRecovered); err != nil || submissionRecovered {
				return sessions.ErrHistory
			}
		}
		var count, released int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(released),0) FROM resource_leases WHERE task_id=?`, task).Scan(&count, &released); err != nil || released != count {
			return sessions.ErrHistory
		}
		if task == evidence.WorkTaskID {
			var exact int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM resource_leases
				WHERE task_id=? AND owner=? AND scope=? AND writer=0 AND released=1`, task, evidence.WorkerID, evidence.Scope).Scan(&exact); err != nil || count != 1 || exact != 1 {
				return sessions.ErrHistory
			}
		}
	}
	return nil
}

func insertContextCompactionDelegations(ctx context.Context, tx *sql.Tx, fact sessions.ContextCompactionLifecycleFact) error {
	if fact.Activation == nil {
		return sessions.ErrContextCompactionLifecycle
	}
	for ordinal, binding := range fact.Activation.Delegations {
		body, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		var batch any
		if binding.Origin.BatchIndex != nil {
			batch = *binding.Origin.BatchIndex
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO context_compaction_delegations(
			activation_digest,ordinal,operation_id,fact_id,version,parent_task_id,work_task_id,execution_task_id,worker_id,scope,
			origin_version,origin_turn_id,origin_attempt_id,origin_tool_call_id,origin_tool_name,origin_batch_index,
			work_start_event_id,work_start_event_digest,work_terminal_event_id,work_terminal_event_digest,
			execution_start_event_id,execution_start_event_digest,execution_context_digest,execution_terminal_event_id,execution_terminal_event_digest,
			plan_digest,authority_digest,engine_digest,parent_policy_digest,child_policy_digest,result_digest,binding_digest,body)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fact.Activation.ActivationDigest, ordinal, fact.OperationID, fact.ID, binding.Version, binding.ParentTaskID, binding.WorkTaskID,
			binding.ExecutionTaskID, binding.WorkerID, binding.Scope, binding.Origin.Version, binding.Origin.TurnID, binding.Origin.AttemptID,
			binding.Origin.ToolCallID, binding.Origin.ToolName, batch, binding.WorkStartEventID, binding.WorkStartEventDigest,
			binding.WorkTerminalEventID, binding.WorkTerminalEventDigest, binding.ExecutionStartEventID, binding.ExecutionStartEventDigest,
			binding.ExecutionContextDigest, binding.ExecutionTerminalEventID, binding.ExecutionTerminalEventDigest,
			binding.PlanDigest, binding.AuthorityDigest, binding.EngineDigest, binding.ParentPolicyDigest, binding.ChildPolicyDigest,
			binding.ResultDigest, binding.BindingDigest, body)
		if err != nil {
			return err
		}
	}
	return nil
}

func validateContextCompactionDelegationRetry(ctx context.Context, tx *sql.Tx, fact sessions.ContextCompactionLifecycleFact, exact []sessions.DelegationCompactionBinding) error {
	if fact.Activation == nil || len(fact.Activation.Delegations) != len(exact) ||
		(len(exact) != 0 && !reflect.DeepEqual(fact.Activation.Delegations, exact)) {
		return ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT ordinal,body FROM context_compaction_delegations WHERE activation_digest=? ORDER BY ordinal`, fact.Activation.ActivationDigest)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var ordinal int
		var body []byte
		if err = rows.Scan(&ordinal, &body); err != nil || ordinal != seen || seen >= len(exact) {
			return ErrConflict
		}
		canonical, marshalErr := json.Marshal(exact[seen])
		if marshalErr != nil || !bytes.Equal(body, canonical) {
			return ErrConflict
		}
		seen++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if seen != len(exact) {
		return ErrConflict
	}
	return nil
}

func parentEventsBeforeCompaction(ctx context.Context, tx *sql.Tx, task string, sequence int64) ([]runtime.Event, []providers.Message, error) {
	var events []runtime.Event
	if _, err := taskSnapshotWithEvents(ctx, tx, task, &events); err != nil || sequence < 2 || sequence > int64(len(events)) {
		return nil, nil, sessions.ErrHistory
	}
	prefix := append([]runtime.Event(nil), events[:sequence-1]...)
	snapshot, err := sessions.Replay(ctx, snapshotEvents(prefix), task)
	if err != nil {
		return nil, nil, err
	}
	return prefix, snapshot.Messages, nil
}
