package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

const maxOutcomeSettlements = 10000

// OutcomeSettlement is a compact, durable tombstone for a retired no-action
// adjudication. It preserves operation and activation-revision ownership after
// bulky selected evidence is removed; it is not evaluation evidence itself.
type OutcomeSettlement struct {
	Version         int             `json:"version"`
	OperationID     string          `json:"operation_id"`
	Expected        ActivationState `json:"expected"`
	ActivationCount int             `json:"activation_count"`
	ReceiptDigest   string          `json:"receipt_digest"`
	CheckedAt       time.Time       `json:"checked_at"`
	PrunedAt        time.Time       `json:"pruned_at"`
}

func (s OutcomeSettlement) Validate() error {
	_, checkedOffset := s.CheckedAt.Zone()
	_, prunedOffset := s.PrunedAt.Zone()
	if s.Version != 1 || !identifier.MatchString(s.OperationID) || s.Expected.Validate() != nil ||
		s.Expected.Active == "" || s.ActivationCount < 2 || s.ActivationCount > 10000 ||
		!outcomeDigestString(s.ReceiptDigest) || !outcomeCatalogTime(s.CheckedAt, checkedOffset) ||
		!outcomeCatalogTime(s.PrunedAt, prunedOffset) || s.PrunedAt.Before(s.CheckedAt) {
		return ErrInvalid
	}
	return nil
}

func outcomeDigestString(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func outcomeCatalogTime(value time.Time, offset int) bool {
	return offset == 0 && value.Year() >= 1970 && value.Year() < 2261
}

func outcomeReceiptDigest(receipt OutcomeRollbackReceipt) (string, error) {
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", ErrInvalid
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func validateOutcomeSettlements(c *catalog) error {
	if len(c.OutcomeSettlements) > maxOutcomeSettlements || len(c.OutcomeSettlements) > 0 && c.Schema < 10 {
		return ErrInvalid
	}
	seenRevisions := map[string]bool{}
	for id, settlement := range c.OutcomeSettlements {
		if id != settlement.OperationID || settlement.Validate() != nil {
			return ErrInvalid
		}
		if _, exists := c.OutcomeOperations[id]; exists {
			return ErrInvalid
		}
		if _, exists := c.OutcomeIntents[id]; exists {
			return ErrInvalid
		}
		if _, exists := c.OutcomeSelections[id]; exists {
			return ErrInvalid
		}
		entry, exists := c.Skills[settlement.Expected.Key.index()]
		if !exists || settlement.ActivationCount > len(entry.Activations) {
			return ErrInvalid
		}
		prefix := entry
		prefix.Activations = entry.Activations[:settlement.ActivationCount]
		prefix.Active = prefix.Activations[len(prefix.Activations)-1].To
		state, err := stateForEntry(prefix)
		if err != nil || state != settlement.Expected {
			return ErrInvalid
		}
		current, err := stateForEntry(entry)
		if err != nil || current.Revision == settlement.Expected.Revision {
			return ErrInvalid
		}
		binding := settlement.Expected.Key.index() + ":" + settlement.Expected.Revision
		if seenRevisions[binding] {
			return ErrInvalid
		}
		seenRevisions[binding] = true
	}
	return nil
}

func lookupOutcomeSettlement(c *catalog, key Key, operationID string) (OutcomeSettlement, error) {
	settlement, ok := c.OutcomeSettlements[operationID]
	if !ok {
		return OutcomeSettlement{}, ErrNotFound
	}
	if settlement.Expected.Key != key {
		return OutcomeSettlement{}, ErrConflict
	}
	if settlement.Validate() != nil {
		return OutcomeSettlement{}, ErrInvalid
	}
	entry, ok := c.Skills[key.index()]
	if !ok || settlement.ActivationCount > len(entry.Activations) {
		return OutcomeSettlement{}, ErrInvalid
	}
	prefix := entry
	prefix.Activations = entry.Activations[:settlement.ActivationCount]
	prefix.Active = prefix.Activations[len(prefix.Activations)-1].To
	state, err := stateForEntry(prefix)
	if err != nil || state != settlement.Expected {
		return OutcomeSettlement{}, ErrInvalid
	}
	return settlement, nil
}

// OutcomeSettlement reads a retention tombstone without restoring its removed
// selected evidence or granting permission to reuse its operation identifier.
func (s *FileStore) OutcomeSettlement(ctx context.Context, key Key, operationID string) (OutcomeSettlement, error) {
	if s == nil || ctx == nil || !s.permitted(key) || !identifier.MatchString(operationID) {
		return OutcomeSettlement{}, ErrInvalid
	}
	var result OutcomeSettlement
	err := s.with(ctx, func(c *catalog) error {
		var err error
		result, err = lookupOutcomeSettlement(c, key, operationID)
		return err
	}, false)
	if err != nil {
		return OutcomeSettlement{}, err
	}
	return result, nil
}

// OutcomeRetentionRequest binds an explicit retention pass to the caller's
// exact view of the current activation. Before is an exclusive UTC cutoff.
type OutcomeRetentionRequest struct {
	Version  int             `json:"version"`
	Expected ActivationState `json:"expected"`
	Before   time.Time       `json:"before"`
	Limit    int             `json:"limit"`
}

// OutcomeRetentionStore is an optional local catalog maintenance extension.
// Hosts decide when an explicit retention request is authorized; it is never
// invoked as a side effect of outcome supervision.
type OutcomeRetentionStore interface {
	Store
	OutcomeSettlement(context.Context, Key, string) (OutcomeSettlement, error)
	PruneSettledOutcomeHistory(context.Context, OutcomeRetentionRequest) ([]OutcomeSettlement, error)
}

var _ OutcomeRetentionStore = (*FileStore)(nil)

func (r OutcomeRetentionRequest) Validate() error {
	_, offset := r.Before.Zone()
	if r.Version != 1 || r.Expected.Validate() != nil || r.Expected.Active == "" ||
		r.Limit < 1 || r.Limit > 1000 || !outcomeCatalogTime(r.Before, offset) {
		return ErrInvalid
	}
	return nil
}

// PruneSettledOutcomeHistory replaces old, completed no-action records with
// compact tombstones. Pending work, rollback receipts, the current activation
// revision, and records at the cutoff are never eligible.
func (s *FileStore) PruneSettledOutcomeHistory(ctx context.Context, request OutcomeRetentionRequest) ([]OutcomeSettlement, error) {
	if s == nil || ctx == nil || request.Validate() != nil || !s.permitted(request.Expected.Key) {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.readOnly {
		return nil, ErrDisabled
	}
	var result []OutcomeSettlement
	err := s.with(ctx, func(c *catalog) error {
		if err := validateOutcomeAdmission(ctx, c); err != nil {
			return err
		}
		entry, ok := c.Skills[request.Expected.Key.index()]
		if !ok {
			return ErrNotFound
		}
		current, err := stateForEntry(entry)
		if err != nil {
			return err
		}
		if current != request.Expected {
			return ErrConflict
		}
		type candidate struct {
			id      string
			receipt OutcomeRollbackReceipt
		}
		candidates := make([]candidate, 0)
		for id, receipt := range c.OutcomeOperations {
			if receipt.Expected.Key != request.Expected.Key || receipt.Decision != "no_action" ||
				receipt.Expected.Revision == current.Revision || !receipt.CheckedAt.Before(request.Before) {
				continue
			}
			verified, lookupErr := lookupOutcomeOperation(c, request.Expected.Key, id)
			if lookupErr != nil {
				return lookupErr
			}
			candidates = append(candidates, candidate{id: id, receipt: verified})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].receipt.CheckedAt.Equal(candidates[j].receipt.CheckedAt) {
				return candidates[i].id < candidates[j].id
			}
			return candidates[i].receipt.CheckedAt.Before(candidates[j].receipt.CheckedAt)
		})
		available := maxOutcomeSettlements - len(c.OutcomeSettlements)
		limit := min(request.Limit, available)
		if limit == 0 && len(candidates) > 0 {
			return ErrInvalid
		}
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		if len(candidates) == 0 {
			return errCatalogUnchanged
		}
		now := time.Now().UTC()
		if c.OutcomeSettlements == nil {
			c.OutcomeSettlements = map[string]OutcomeSettlement{}
		}
		for _, candidate := range candidates {
			digest, digestErr := outcomeReceiptDigest(candidate.receipt)
			if digestErr != nil {
				return digestErr
			}
			settlement := OutcomeSettlement{Version: 1, OperationID: candidate.id, Expected: candidate.receipt.Expected,
				ActivationCount: candidate.receipt.ActivationCount, ReceiptDigest: digest,
				CheckedAt: candidate.receipt.CheckedAt, PrunedAt: now}
			if settlement.Validate() != nil {
				return ErrInvalid
			}
			delete(c.OutcomeOperations, candidate.id)
			if candidate.receipt.IntentID != "" {
				delete(c.OutcomeIntents, candidate.receipt.IntentID)
			}
			if candidate.receipt.SelectionID != "" {
				delete(c.OutcomeSelections, candidate.receipt.SelectionID)
			}
			c.OutcomeSettlements[candidate.id] = settlement
			result = append(result, settlement)
		}
		c.Schema = 10
		if err = validateOutcomeSettlements(c); err != nil {
			return err
		}
		if err = validateOutcomeOperations(c); err != nil {
			return err
		}
		if err = validateOutcomeIntents(c); err != nil {
			return err
		}
		if err = validateOutcomeSelections(c); err != nil {
			return err
		}
		return validateOutcomeAdmission(ctx, c)
	}, true)
	if err != nil {
		return nil, err
	}
	return result, nil
}
