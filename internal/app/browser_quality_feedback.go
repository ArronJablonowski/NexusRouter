package app

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/browserops"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"time"
)

func (b *BrowserMutations) Feedback(ctx context.Context, subject string, request contract.FeedbackRequest) (contract.FeedbackReceipt, error) {
	if b == nil || ctx == nil || request.Validate() != nil || !validBrowserSubject(subject) {
		return contract.FeedbackReceipt{}, ErrAdmission
	}
	record, replay, err := b.begin(ctx, subject, string(request.Action), request.IdempotencyKey, request)
	if err != nil {
		return contract.FeedbackReceipt{}, err
	}
	if replay {
		var receipt contract.FeedbackReceipt
		if decodeReceipt(record.Response, &receipt) != nil || receipt.OperationID != record.OperationID {
			return contract.FeedbackReceipt{}, ErrBrowserMutation
		}
		return receipt, nil
	}
	snapshot, err := InspectTask(ctx, b.service.settings.Telemetry.Database, request.TaskID)
	if err != nil || snapshot.State != "completed" || snapshot.UncertainEffects || snapshot.InterruptedTurn || len(snapshot.Pending) != 0 {
		return contract.FeedbackReceipt{}, b.reject(ctx, subject, record, "task", request.TaskID, ErrAdmission)
	}
	db, err := telemetry.Open(ctx, b.service.settings.Telemetry.Database)
	if err != nil {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	defer db.Close()
	history, err := db.BrowserFeedbackHistory(ctx, request.TaskID)
	if err != nil {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	expected := int64(0)
	supersedes := ""
	cost := float64(0)
	if request.Action == contract.FeedbackRecord && request.AttemptCost != nil {
		cost = *request.AttemptCost
	}
	if request.Action == contract.FeedbackRevise {
		expected = *request.ExpectedRevision
		if len(history) < int(expected) || history[expected-1].ID != request.FeedbackID {
			return contract.FeedbackReceipt{}, b.reject(ctx, subject, record, "feedback", request.FeedbackID, browserops.ErrConflict)
		}
		supersedes = request.FeedbackID
		cost = history[expected-1].AttemptCost
	}
	feedbackID := browserFeedbackID(record.OperationID)
	entry := telemetry.BrowserFeedback{Version: 1, ID: feedbackID, TaskID: request.TaskID, Supersedes: supersedes, Accepted: request.Accepted, AttemptCost: cost, CreatedAt: time.Now().UTC()}
	if request.Rating > 0 {
		if err = b.fillCreativeFeedback(ctx, db, snapshot, &entry, request.Rating); err != nil {
			return contract.FeedbackReceipt{}, err
		}
		for _, prior := range history {
			if prior.ID == feedbackID && prior.Rating == request.Rating {
				entry.AttemptCost, entry.CostStatus, entry.Model, entry.Provider, entry.CreativeKind = prior.AttemptCost, prior.CostStatus, prior.Model, prior.Provider, prior.CreativeKind
			}
		}
		if request.Action == contract.FeedbackRevise {
			prior := history[expected-1]
			if prior.Rating > 0 {
				entry.AttemptCost, entry.CostStatus = prior.AttemptCost, prior.CostStatus
			}
		}
	}
	if err = db.AppendBrowserFeedback(ctx, entry, expected); err != nil {
		if errors.Is(err, telemetry.ErrBrowserOperationConflict) {
			return contract.FeedbackReceipt{}, b.reject(ctx, subject, record, "task", request.TaskID, browserops.ErrConflict)
		}
		return contract.FeedbackReceipt{}, err
	}
	history, err = db.BrowserFeedbackHistory(ctx, request.TaskID)
	if err != nil || len(history) == 0 {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	// A later revision may commit before this operation journals its receipt.
	// Recover the exact durable entry rather than claiming the current head.
	var current telemetry.BrowserFeedback
	var revision int64
	for i, item := range history {
		if item.ID == feedbackID {
			current, revision = item, int64(i+1)
			break
		}
	}
	if revision == 0 || current.Accepted != request.Accepted || current.Rating != request.Rating {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	state := "recorded"
	if request.Action == contract.FeedbackRevise {
		state = "revised"
	}
	receipt := contract.FeedbackReceipt{Version: 1, OperationID: record.OperationID, TaskID: request.TaskID, FeedbackID: current.ID, Revision: revision, State: state, Accepted: request.Accepted, EvidenceClass: contract.SubjectiveEvidence, Source: "user_feedback"}
	if receipt.Validate() != nil {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	return receipt, b.commit(ctx, subject, record, receipt)
}
