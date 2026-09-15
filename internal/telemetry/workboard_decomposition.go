package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type decompositionAdmissionPlan struct {
	decision workboard.DecompositionDecision
	parent   *workboard.DecompositionAdmission
	origin   workboard.DecompositionRuntimeOrigin
}

func prepareDecompositionAdmission(ctx context.Context, tx *sql.Tx, mutation workboard.CardMutation, graph workboard.Graph) (*decompositionAdmissionPlan, error) {
	if mutation.Decomposition == nil {
		return nil, nil
	}
	parentID := ""
	cardID := mutation.CardID
	if mutation.Kind == workboard.MutationCreate {
		parentID = mutation.Create.ParentID
		cardID = ""
	} else if mutation.Kind == workboard.MutationRevise && mutation.Patch.ParentID != nil {
		parentID = *mutation.Patch.ParentID
	} else {
		return nil, invalidWorkboard("decomposition_policy")
	}
	policy := *mutation.Decomposition
	var parent *workboard.DecompositionAdmission
	if parentID != "" {
		prior, found, err := readCurrentDecompositionAdmission(ctx, tx, mutation.BoardID, parentID)
		if err != nil {
			return nil, err
		}
		if found {
			inherited, inheritErr := policy.Inherit(prior, nil)
			if inheritErr != nil {
				return nil, inheritErr
			}
			policy = inherited
			parent = &prior
		}
	}
	decision, err := workboard.AdmitDecomposition(graph, mutation.ExpectedGraphRevision, cardID, parentID, policy)
	if err != nil {
		return nil, err
	}
	origin, err := decompositionRuntimeOrigin(ctx, tx)
	if err != nil {
		return nil, err
	}
	return &decompositionAdmissionPlan{decision: decision, parent: parent, origin: origin}, nil
}

func (p decompositionAdmissionPlan) finalize(cardID, operationID, requestDigest string, actor workboard.Actor, now time.Time) (workboard.DecompositionAdmission, error) {
	admission := workboard.DecompositionAdmission{Version: workboard.DecompositionPolicyVersion, AdmissionID: newWorkboardID(), OperationID: operationID,
		RequestDigest: requestDigest, DecisionDigest: p.decision.Digest, BoardID: p.decision.BoardID, CardID: cardID, ParentID: p.decision.ParentID,
		Actor: actor, Origin: p.origin, Limits: p.decision.Limits, ConfigDigest: p.decision.ConfigDigest, PolicyDigest: p.decision.PolicyDigest,
		Depth: p.decision.Depth, DirectChildren: p.decision.DirectChildren, AdmittedAt: now.UTC()}
	if p.parent != nil {
		admission.ParentAdmissionID = p.parent.AdmissionID
		admission.ParentAdmissionDigest = p.parent.AdmissionDigest
	}
	var err error
	admission.AdmissionDigest, err = admission.CanonicalDigest()
	if err != nil || admission.Validate() != nil {
		return workboard.DecompositionAdmission{}, invalidWorkboard("decomposition_admission")
	}
	return admission, nil
}

func decompositionRuntimeOrigin(ctx context.Context, tx *sql.Tx) (workboard.DecompositionRuntimeOrigin, error) {
	identity, ok := tools.ExecutionIdentityFromContext(ctx)
	if !ok {
		return workboard.DecompositionRuntimeOrigin{}, nil
	}
	origin := workboard.DecompositionRuntimeOrigin{TaskID: identity.TaskID, SessionID: identity.SessionID, TurnID: identity.TurnID,
		AttemptID: identity.AttemptID, ToolCallID: identity.ToolCallID, ToolName: identity.ToolName}
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, identity.TaskID, &events)
	if err != nil || snapshot.SessionID != identity.SessionID {
		return workboard.DecompositionRuntimeOrigin{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "runtime_provenance"}
	}
	toolSeen := false
	for _, event := range events {
		if event.TurnID != identity.TurnID || event.AttemptID != identity.AttemptID {
			continue
		}
		if event.Kind == runtime.TurnStarted {
			if origin.ModelID != "" || event.Data.ModelID == "" || event.Data.ProviderID == "" {
				return workboard.DecompositionRuntimeOrigin{}, ErrWorkboardCorrupt
			}
			origin.ModelID, origin.ProviderID = event.Data.ModelID, event.Data.ProviderID
		}
		if event.Kind == runtime.ToolStarted && event.Data.ToolCallID == identity.ToolCallID {
			if toolSeen || event.Data.ToolName != identity.ToolName {
				return workboard.DecompositionRuntimeOrigin{}, ErrWorkboardCorrupt
			}
			toolSeen = true
		}
	}
	if origin.ModelID == "" || !toolSeen {
		return workboard.DecompositionRuntimeOrigin{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "runtime_provenance"}
	}
	return origin, nil
}

func readCurrentDecompositionAdmission(ctx context.Context, tx *sql.Tx, boardID, cardID string) (workboard.DecompositionAdmission, bool, error) {
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT body FROM workboard_decomposition_admissions
		WHERE board_id=? AND card_id=? ORDER BY event_sequence DESC,admission_id DESC LIMIT 1`, boardID, cardID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.DecompositionAdmission{}, false, nil
	}
	if err != nil {
		return workboard.DecompositionAdmission{}, false, err
	}
	var admission workboard.DecompositionAdmission
	if strictJSON(body, &admission) != nil || admission.Validate() != nil || admission.BoardID != boardID || admission.CardID != cardID {
		return workboard.DecompositionAdmission{}, true, ErrWorkboardCorrupt
	}
	return admission, true, nil
}

func insertDecompositionWorkboardEvent(ctx context.Context, tx *sql.Tx, event workboard.BoardEvent, body []byte) error {
	var canonical workboard.BoardEvent
	if strictJSON(body, &canonical) != nil || canonical != event || event.Validate() != nil || !event.HasDecompositionAdmission() {
		return ErrWorkboardCorrupt
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workboard_events(
		id,board_id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, event.ID, event.BoardID, event.Sequence, event.OperationID, string(event.Kind), event.ActorID,
		event.ActorType, event.CardID, event.CreatedAt.UnixNano(), body)
	return err
}

func insertDecompositionAdmission(ctx context.Context, tx *sql.Tx, admission workboard.DecompositionAdmission, event workboard.BoardEvent, body []byte) error {
	var canonical workboard.DecompositionAdmission
	if strictJSON(body, &canonical) != nil || canonical != admission || admission.Validate() != nil || event.Validate() != nil ||
		!event.HasDecompositionAdmission() || event.DecompositionAdmissionID != admission.AdmissionID || event.BoardID != admission.BoardID ||
		event.CardID != admission.CardID || event.OperationID != admission.OperationID || event.ActorID != admission.Actor.ID || event.ActorType != admission.Actor.Type ||
		!event.CreatedAt.Equal(admission.AdmittedAt) {
		return ErrWorkboardCorrupt
	}
	nullable := func(value string) any {
		if value == "" {
			return nil
		}
		return value
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workboard_decomposition_admissions(
		admission_id,board_id,card_id,parent_card_id,operation_id,event_id,event_sequence,event_created_at,request_digest,decision_digest,actor_id,actor_type,
		origin_task_id,origin_session_id,origin_turn_id,origin_attempt_id,origin_tool_call_id,origin_tool_name,origin_model_id,origin_provider_id,
		config_digest,policy_digest,max_depth,max_children,depth,direct_children,parent_admission_id,parent_admission_digest,admitted_at,admission_digest,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, admission.AdmissionID, admission.BoardID, admission.CardID,
		nullable(admission.ParentID), admission.OperationID, event.ID, event.Sequence, event.CreatedAt.UnixNano(), admission.RequestDigest, admission.DecisionDigest, admission.Actor.ID, admission.Actor.Type,
		nullable(admission.Origin.TaskID), nullable(admission.Origin.SessionID), nullable(admission.Origin.TurnID), nullable(admission.Origin.AttemptID),
		nullable(admission.Origin.ToolCallID), nullable(admission.Origin.ToolName), nullable(admission.Origin.ModelID), nullable(admission.Origin.ProviderID),
		admission.ConfigDigest, admission.PolicyDigest, admission.Limits.MaxDepth, admission.Limits.MaxChildren, admission.Depth,
		admission.DirectChildren, nullable(admission.ParentAdmissionID), nullable(admission.ParentAdmissionDigest),
		admission.AdmittedAt.Format(time.RFC3339Nano), admission.AdmissionDigest, body)
	return err
}
