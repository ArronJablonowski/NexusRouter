package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func usageID(role accounting.Role, operation string) string {
	sum := sha256.Sum256([]byte("usage-operation-v1\x00" + string(role) + "\x00" + operation))
	return "usage-" + hex.EncodeToString(sum[:])
}

// appendRoutedUsage writes the one task-operation aggregate inside the same
// transaction as its terminal event. Legacy/manual tasks without a declared
// model route remain explicitly uncovered instead of receiving fake identity.
func appendRoutedUsage(ctx context.Context, tx *sql.Tx, terminal runtime.Event) error {
	if terminal.Kind != runtime.TaskCompleted && terminal.Kind != runtime.TaskFailed && terminal.Kind != runtime.TaskCanceled {
		return nil
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence=1", terminal.TaskID).Scan(&raw); err != nil {
		return err
	}
	var start runtime.Event
	if json.Unmarshal(raw, &start) != nil || start.Validate() != nil || start.Kind != runtime.TaskStarted {
		return accounting.ErrUsage
	}
	if start.Data.ProviderID == "" || start.Data.ModelID == "" {
		return nil
	}
	role := accounting.PrimaryExecution
	if start.Data.RetryOfTaskID != "" {
		if err := validateRetryChain(ctx, tx, terminal.TaskID, start.Data.RetryOfTaskID); err != nil {
			// Preserve the terminal journal fact but leave coverage explicitly
			// incomplete rather than inventing fallback lineage.
			return nil
		}
		role = accounting.Fallback
	}
	routeID := start.ID
	err := tx.QueryRowContext(ctx, "SELECT json_extract(body,'$.route_id') FROM events WHERE task_id=? AND json_extract(body,'$.kind')='route.selected' ORDER BY sequence LIMIT 1", terminal.TaskID).Scan(&routeID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	usage, err := routedUsage(ctx, tx, terminal.TaskID, start.Data.ProviderID, start.Data.ModelID)
	if err != nil {
		return err
	}
	disposition, retryClass := accounting.Completed, accounting.NotApplicable
	if terminal.Kind == runtime.TaskFailed {
		disposition, retryClass = accounting.Failed, terminalRetryClass(terminal.Data.Code)
	} else if terminal.Kind == runtime.TaskCanceled {
		disposition, retryClass = accounting.Canceled, accounting.NonRetryable
	}
	r := accounting.Record{Version: 1, ID: usageID(role, terminal.TaskID), TaskID: terminal.TaskID, SessionID: terminal.SessionID, OperationID: terminal.TaskID, RouteID: routeID, EvidenceID: terminal.ID, Provider: start.Data.ProviderID, Model: start.Data.ModelID, Role: role, EvidenceKind: accounting.EventEvidence, Usage: usage, Disposition: disposition, RetryClass: retryClass, OccurredAt: terminal.Time.UTC()}
	if start.Data.RouteEstimatedCost != nil {
		cost := *start.Data.RouteEstimatedCost
		digest := sha256.Sum256([]byte(start.Data.ProviderID + "\x00" + start.Data.ModelID + "\x00" + strconv.FormatFloat(cost, 'g', -1, 64)))
		r.NormalizedCost = &cost
		r.Pricing = &accounting.PricingProvenance{Version: 1, ID: "runtime-route-estimate-v1", Source: "task.started.route_estimated_cost", Digest: hex.EncodeToString(digest[:]), Currency: "USD", Method: accounting.FlatRate, Basis: accounting.ConfiguredEstimate, FixedCost: cost, EffectiveAt: start.Time.UTC()}
	}
	if r.Validate() != nil {
		return accounting.ErrUsage
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return recordUsage(ctx, tx, r, body, false)
}

func validateRetryChain(ctx context.Context, tx *sql.Tx, task, predecessor string) error {
	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT rowid FROM task_heads WHERE task_id=?`, task).Scan(&before); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return accounting.ErrUsage
		}
		before = math.MaxInt64
	}
	return validateRetryChainBefore(ctx, tx, task, predecessor, before)
}

func validateRetryChainBefore(ctx context.Context, tx *sql.Tx, task, predecessor string, before int64) error {
	if before < 1 {
		return accounting.ErrUsage
	}
	seen := map[string]bool{task: true}
	// The runtime admits at most 32 route attempts for one automatic fallback
	// lineage. The task being validated is already one of those attempts, so it
	// may have no more than 31 predecessors.
	for attempts := 1; predecessor != "" && attempts < sessions.MaxTerminalRouteAttempts; attempts++ {
		if seen[predecessor] {
			return accounting.ErrUsage
		}
		seen[predecessor] = true
		var predecessorRowID int64
		if err := tx.QueryRowContext(ctx, `SELECT rowid FROM task_heads WHERE task_id=?`, predecessor).Scan(&predecessorRowID); err != nil || predecessorRowID < 1 || predecessorRowID >= before {
			return accounting.ErrUsage
		}
		before = predecessorRowID
		next, err := safeRetryPredecessor(ctx, tx, predecessor)
		if err != nil {
			return accounting.ErrUsage
		}
		predecessor = next
		if predecessor == "" {
			return nil
		}
	}
	return accounting.ErrUsage
}

// safeRetryPredecessor accepts only the exact durable lifecycle emitted when
// the runtime authorizes automatic fallback: one model attempt failed with
// explicit retryability without output, or a known stream failure before tools.
// A terminal state alone is insufficient evidence because completed, canceled,
// context-overflow, completed turns, steering and side-effecting tasks never confer
// fallback attribution.
func safeRetryPredecessor(ctx context.Context, tx *sql.Tx, task string) (string, error) {
	var session, state string
	var head int64
	if err := tx.QueryRowContext(ctx, `SELECT session_id,sequence,state FROM task_heads WHERE task_id=?`, task).Scan(&session, &head, &state); err != nil || state != "failed" || head < 3 || head > sessions.MaxTaskEvents {
		return "", accounting.ErrUsage
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,sequence,body FROM events WHERE task_id=? ORDER BY sequence`, task)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	events := make([]runtime.Event, 0, head)
	bytesRead := 0
	for rows.Next() {
		var storedID string
		var storedSequence int64
		var raw []byte
		var event runtime.Event
		if rows.Scan(&storedID, &storedSequence, &raw) != nil || json.Unmarshal(raw, &event) != nil || event.Validate() != nil || event.ID != storedID || event.TaskID != task || event.SessionID != session || event.CorrelationID != task || event.Sequence != storedSequence || event.Sequence != int64(len(events)+1) {
			return "", accounting.ErrUsage
		}
		if len(raw) > sessions.MaxEventPageBytes-bytesRead {
			return "", accounting.ErrUsage
		}
		bytesRead += len(raw)
		canonical, encodeErr := event.Encode()
		if encodeErr != nil || !bytes.Equal(canonical, raw) {
			return "", accounting.ErrUsage
		}
		// Only incomplete model text can precede the new recovery boundary.
		// A tool proposal, dispatch, effect or other output always fails closed.
		if (event.Data.Text != "" && event.Kind != runtime.ModelDelta) || len(event.Data.ToolCalls) != 0 || event.Data.ToolCallID != "" || event.Data.ToolName != "" || event.Data.Effect != "" {
			return "", accounting.ErrUsage
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if int64(len(events)) != head || events[0].Kind != runtime.TaskStarted {
		return "", accounting.ErrUsage
	}
	start := events[0]
	i := 1
	if events[i].Kind == runtime.RouteSelected {
		if events[i].Data.ProviderID != start.Data.ProviderID || events[i].Data.ModelID != start.Data.ModelID {
			return "", accounting.ErrUsage
		}
		i++
	}
	if start.Data.ProviderID == "" || start.Data.ModelID == "" || i >= len(events)-1 || events[i].Kind != runtime.TurnStarted || events[i].Data.ProviderID != start.Data.ProviderID || events[i].Data.ModelID != start.Data.ModelID {
		return "", accounting.ErrUsage
	}
	terminal := events[len(events)-1]
	if terminal.Kind != runtime.TaskFailed || (terminal.Data.Code != "provider_retryable_no_output" && terminal.Data.Code != "provider_failed_before_tools") || terminal.TurnID != events[i].TurnID || terminal.AttemptID != events[i].AttemptID {
		return "", accounting.ErrUsage
	}
	for _, delta := range events[i+1 : len(events)-1] {
		if terminal.Data.Code != "provider_failed_before_tools" || delta.Kind != runtime.ModelDelta || delta.TurnID != events[i].TurnID || delta.AttemptID != events[i].AttemptID {
			return "", accounting.ErrUsage
		}
	}
	return start.Data.RetryOfTaskID, nil
}

func routedUsage(ctx context.Context, tx *sql.Tx, task, provider, model string) (*providers.Usage, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM events WHERE task_id=? AND json_extract(body,'$.kind') IN('turn.started','turn.completed') ORDER BY sequence", task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &providers.Usage{}
	completed, unknown := 0, false
	started := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var e runtime.Event
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &e) != nil || e.Validate() != nil {
			return nil, accounting.ErrUsage
		}
		key := e.TurnID + "\x00" + e.AttemptID
		if e.Kind == runtime.TurnStarted {
			if e.Data.ProviderID != provider || e.Data.ModelID != model || started[key] {
				return nil, accounting.ErrUsage
			}
			started[key] = true
			continue
		}
		if !started[key] || (e.Data.ProviderID != "" && e.Data.ProviderID != provider) || (e.Data.ModelID != "" && e.Data.ModelID != model) {
			return nil, accounting.ErrUsage
		}
		delete(started, key)
		completed++
		if e.Data.Usage == nil || e.Data.Usage.InputTokens > math.MaxInt64-out.InputTokens || e.Data.Usage.OutputTokens > math.MaxInt64-out.OutputTokens {
			unknown = true
			continue
		}
		out.InputTokens += e.Data.Usage.InputTokens
		out.OutputTokens += e.Data.Usage.OutputTokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if completed == 0 || unknown || len(started) != 0 {
		return nil, nil
	}
	return out, nil
}

func terminalRetryClass(code string) accounting.RetryClass {
	if code == "provider_retryable_no_output" || code == "provider_failed_before_tools" {
		return accounting.Retryable
	}
	switch code {
	case "interrupted_model", "interrupted_read_only_model", "interrupted_read_only_tool", "worker_owner_interrupted", "interrupted_after_delegation":
		return accounting.Uncertain
	default:
		return accounting.NonRetryable
	}
}
