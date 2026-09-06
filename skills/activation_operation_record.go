package skills

import (
	"context"
	"encoding/hex"
)

// ActivationOperation is a durable receipt binding an operation to its exact
// precondition, candidate and successful deterministic validation evidence.
type ActivationOperation struct {
	Version     int              `json:"version"`
	OperationID string           `json:"operation_id"`
	Expected    ActivationState  `json:"expected"`
	Candidate   string           `json:"candidate"`
	Record      ActivationRecord `json:"record"`
}

// OperationStore is an optional extension for controllers that need durable
// retry recognition. Retrieval-only stores need not implement mutation control.
type OperationStore interface {
	RevisionStore
	ActivateOnce(context.Context, string, ActivationState, string, Validator, bool) error
	ActivationOperation(context.Context, Key, string) (ActivationOperation, error)
}

var _ OperationStore = (*FileStore)(nil)

func validActivationOperationFields(a ActivationRecord) bool {
	if a.OutcomeOperationID != "" && (!identifier.MatchString(a.OutcomeOperationID) || !a.Rollback || a.Regression != nil || a.Evidence != nil || a.OperationID != "" || a.BeforeRevision != "") {
		return false
	}
	if a.OperationID == "" {
		return a.BeforeRevision == "" && a.Evidence == nil
	}
	if !identifier.MatchString(a.OperationID) || a.Rollback || a.Regression != nil || a.Evidence == nil || !a.Evidence.Passed || !a.Evidence.Deterministic || !identifier.MatchString(a.Evidence.ID) || len(a.BeforeRevision) != 64 {
		return false
	}
	b, err := hex.DecodeString(a.BeforeRevision)
	return err == nil && hex.EncodeToString(b) == a.BeforeRevision
}

func (o ActivationOperation) Validate() error {
	a := o.Record
	_, offset := a.At.Zone()
	if o.Version != 1 || !identifier.MatchString(o.OperationID) || o.Expected.Validate() != nil || !versionID(o.Candidate) || o.Candidate == o.Expected.Active || !validActivationOperationFields(a) || a.OperationID != o.OperationID || a.BeforeRevision != o.Expected.Revision || a.From != o.Expected.Active || a.To != o.Candidate || a.At.Year() < 1970 || a.At.Year() >= 2261 || offset != 0 {
		return ErrInvalid
	}
	return nil
}

func operationForEntry(e entry, index int) (ActivationOperation, error) {
	if index < 0 || index >= len(e.Activations) {
		return ActivationOperation{}, ErrInvalid
	}
	a := e.Activations[index]
	if a.OperationID == "" {
		return ActivationOperation{}, ErrNotFound
	}
	prefix := e
	prefix.Active = a.From
	prefix.Activations = e.Activations[:index]
	expected, err := stateForEntry(prefix)
	if err != nil {
		return ActivationOperation{}, ErrInvalid
	}
	out := ActivationOperation{Version: 1, OperationID: a.OperationID, Expected: expected, Candidate: a.To, Record: a}
	if out.Validate() != nil {
		return ActivationOperation{}, ErrInvalid
	}
	return out, nil
}

func validateActivationOperations(c *catalog) error {
	seen := map[string]bool{}
	for _, e := range c.Skills {
		for _, a := range e.Activations {
			if a.OperationID == "" {
				continue
			}
			if c.Schema < 3 || seen[a.OperationID] {
				return ErrInvalid
			}
			if !validActivationOperationFields(a) {
				return ErrInvalid
			}
			seen[a.OperationID] = true
		}
	}
	return nil
}

// lookupActivationOperation checks global binding and validates only the
// requested receipt's prefix, avoiding quadratic hashing of every receipt.
func lookupActivationOperation(c *catalog, key Key, operationID string) (ActivationOperation, error) {
	for _, e := range c.Skills {
		for i, a := range e.Activations {
			if a.OperationID == operationID {
				if e.Key != key {
					return ActivationOperation{}, ErrConflict
				}
				return operationForEntry(e, i)
			}
		}
	}
	return ActivationOperation{}, ErrNotFound
}

// ActivationOperation inspects a committed receipt without revalidation,
// activation, repair, or catalog mutation. Operation IDs are catalog-global.
func (s *FileStore) ActivationOperation(ctx context.Context, key Key, operationID string) (ActivationOperation, error) {
	if s == nil || ctx == nil || !s.permitted(key) || !identifier.MatchString(operationID) {
		return ActivationOperation{}, ErrInvalid
	}
	var out ActivationOperation
	err := s.with(ctx, func(c *catalog) error {
		e, ok := c.Skills[key.index()]
		if !ok {
			return ErrNotFound
		}
		for i, a := range e.Activations {
			if a.OperationID == operationID {
				var err error
				out, err = operationForEntry(e, i)
				return err
			}
		}
		return ErrNotFound
	}, false)
	if err != nil {
		return ActivationOperation{}, err
	}
	return out, nil
}
