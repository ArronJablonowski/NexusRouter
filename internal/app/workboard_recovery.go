package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

const (
	workboardRecoveryBoardLimit = 8
	workboardRecoveryClaimLimit = 16
)

// WorkboardCancelFinalizationRequest is the proof-free operator contract used
// by the native daemon endpoint. Proof digests are never accepted from its
// transport payload.
type WorkboardCancelFinalizationRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedCardRevision, ExpectedClaimRevision         int64
}

// WorkboardRecoveryCoordinator is the daemon-only bridge between durable
// supervision observations and proof-gated recovery. It never executes,
// retries, or assigns card work.
type WorkboardRecoveryCoordinator struct {
	db        *telemetry.Store
	attention *workboard.AttentionService
	system    *telemetry.WorkboardRecoveryExecutor
	operator  *telemetry.WorkboardRecoveryExecutor
	cursorMu  sync.Mutex
	cursors   map[string]string
}

func NewWorkboardRecoveryCoordinator(db *telemetry.Store, now func() time.Time) (*WorkboardRecoveryCoordinator, error) {
	if db == nil || now == nil {
		return nil, ErrAdmission
	}
	systemActor := workboard.Actor{ID: "workboard-supervisor", Type: "system"}
	operatorActor := workboard.Actor{ID: "api_operator", Type: "operator"}
	authority := fixedWorkboardAuthority{authority: workboard.Authority{CreationScope: "daemon-supervisor", Actor: systemActor}}
	attention, err := workboard.NewAttentionService(db, authority, now, workboard.MaxLeaseTTL)
	if err != nil {
		return nil, err
	}
	system, err := telemetry.NewWorkboardRecoveryExecutor(db, systemActor, now)
	if err != nil {
		return nil, err
	}
	operator, err := telemetry.NewWorkboardRecoveryExecutor(db, operatorActor, now)
	if err != nil {
		return nil, err
	}
	return &WorkboardRecoveryCoordinator{db: db, attention: attention, system: system, operator: operator,
		cursors: make(map[string]string)}, nil
}

// RecoverAttentionPage scans a bounded page, then releases only claims whose
// independently bound task/process/effect evidence qualifies. A proof failure
// leaves the attention claim untouched and is not a supervisor health failure.
func (c *WorkboardRecoveryCoordinator) RecoverAttentionPage(ctx context.Context, after string) (string, int, error) {
	if c == nil || c.db == nil || c.attention == nil || c.system == nil || ctx == nil || ctx.Err() != nil {
		return after, 0, ErrAdmission
	}
	page, err := c.db.ListWorkboards(ctx, workboard.BoardListOptions{After: after, Limit: workboardRecoveryBoardLimit, State: "active"})
	if err != nil {
		return after, 0, err
	}
	recovered := 0
	for _, board := range page.Items {
		// Serialize each bounded traversal so two supervisor calls cannot race a
		// cursor and repeatedly select the same unrecoverable prefix.
		c.cursorMu.Lock()
		attentionAfter := c.cursors[board.ID]
		if _, err = c.attention.Scan(ctx, board.ID, workboardRecoveryClaimLimit); err != nil {
			c.cursorMu.Unlock()
			return after, recovered, err
		}
		items, nextAttention, listErr := c.db.ListClaimAttentionPage(ctx, board.ID, attentionAfter, workboardRecoveryClaimLimit)
		if listErr != nil {
			c.cursorMu.Unlock()
			return after, recovered, listErr
		}
		for _, item := range items {
			card, readErr := c.db.GetCard(ctx, board.ID, item.CardID)
			if readErr != nil {
				c.cursorMu.Unlock()
				return after, recovered, readErr
			}
			if card.CurrentAttemptID != item.AttemptID || card.CurrentClaimID != item.ClaimID ||
				(card.State != workboard.InProgress && card.State != workboard.Blocked) {
				continue
			}
			_, recoverErr := c.system.RecoverClaim(ctx, telemetry.WorkboardClaimRecoveryRequest{BoardID: board.ID,
				CardID: card.ID, AttemptID: item.AttemptID, ClaimID: item.ClaimID,
				IdempotencyKey: autoRecoveryKey(item, card.Revision), ExpectedCardRevision: card.Revision,
				ExpectedClaimRevision: item.ClaimRevision})
			if recoverErr == nil {
				recovered++
				continue
			}
			if errors.Is(recoverErr, telemetry.ErrWorkboardRecoveryProof) ||
				errors.Is(recoverErr, &workboard.Violation{Code: workboard.CodeStaleRevision}) ||
				errors.Is(recoverErr, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
				continue
			}
			c.cursorMu.Unlock()
			return after, recovered, recoverErr
		}
		c.cursors[board.ID] = nextAttention
		c.cursorMu.Unlock()
	}
	if page.HasMore {
		return page.NextCursor, recovered, nil
	}
	return "", recovered, nil
}

func (c *WorkboardRecoveryCoordinator) FinalizeCancel(ctx context.Context, request WorkboardCancelFinalizationRequest) (webui.OperationReceipt, error) {
	if c == nil || c.operator == nil {
		return webui.OperationReceipt{}, ErrAdmission
	}
	receipt, err := c.operator.FinalizeCancel(ctx, telemetry.WorkboardCancelFinalizationRequest{
		BoardID: request.BoardID, CardID: request.CardID, AttemptID: request.AttemptID, ClaimID: request.ClaimID,
		IdempotencyKey: request.IdempotencyKey, ExpectedCardRevision: request.ExpectedCardRevision,
		ExpectedClaimRevision: request.ExpectedClaimRevision})
	if err != nil {
		return webui.OperationReceipt{}, err
	}
	projected := workboardReceipt(receipt)
	if projected.Validate() != nil {
		return webui.OperationReceipt{}, ErrAdmission
	}
	return projected, nil
}

func autoRecoveryKey(item workboard.ClaimAttention, cardRevision int64) string {
	seed := item.BoardID + "\x00" + item.CardID + "\x00" + item.AttemptID + "\x00" + item.ClaimID + "\x00" +
		strconv.FormatInt(item.ClaimRevision, 10) + "\x00" + strconv.FormatInt(cardRevision, 10)
	digest := sha256.Sum256([]byte(seed))
	return "workboard-auto-recover-" + hex.EncodeToString(digest[:16])
}
