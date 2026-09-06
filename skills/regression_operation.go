package skills

import (
	"context"
	"time"
)

// RegressionOperation is a historical deterministic check receipt, not fresh
// validation or permission. Operation IDs are global within regression checks,
// in a namespace separate from activation operation IDs. ValidatorID names a
// trusted host validator contract; it does not authenticate callback code.
type RegressionOperation struct {
	Version         int              `json:"version"`
	OperationID     string           `json:"operation_id"`
	ValidatorID     string           `json:"validator_id"`
	Expected        ActivationState  `json:"expected"`
	Result          RegressionResult `json:"result"`
	After           ActivationState  `json:"after"`
	ActivationCount int              `json:"activation_count"`
	CheckedAt       time.Time        `json:"checked_at"`
}

// RegressionOperationStore adds durable retry recognition without changing the
// base skill retrieval or activation interfaces.
type RegressionOperationStore interface {
	RevisionStore
	RevalidateAndRollbackOnce(context.Context, string, string, ActivationState, Validator) (RegressionOperation, error)
	RegressionOperation(context.Context, Key, string) (RegressionOperation, error)
}

var _ RegressionOperationStore = (*FileStore)(nil)

func (o RegressionOperation) Validate() error {
	_, offset := o.CheckedAt.Zone()
	p := o.Result.Evidence
	if o.Version != 1 || !identifier.MatchString(o.OperationID) || !identifier.MatchString(o.ValidatorID) || o.Expected.Validate() != nil || o.Expected.Active == "" || o.After.Validate() != nil || o.After.Key != o.Expected.Key || o.Result.State != o.Expected || !p.Deterministic || !identifier.MatchString(p.ID) || o.Result.RolledBack == p.Passed || o.ActivationCount < 1 || o.ActivationCount > 10000 || o.CheckedAt.Year() < 1970 || o.CheckedAt.Year() >= 2261 || offset != 0 {
		return ErrInvalid
	}
	if p.Passed && o.After != o.Expected || !p.Passed && (o.After.Active == "" || o.After.Active == o.Expected.Active || o.After.Revision == o.Expected.Revision || o.ActivationCount >= 10000) {
		return ErrInvalid
	}
	return nil
}

func validateRegressionOperations(c *catalog) error {
	if len(c.RegressionOperations) > 1000 || len(c.RegressionOperations) > 0 && c.Schema < 4 {
		return ErrInvalid
	}
	for id, o := range c.RegressionOperations {
		e, exists := c.Skills[o.Expected.Key.index()]
		if id != o.OperationID || o.Validate() != nil || !exists || o.ActivationCount > len(e.Activations) || o.Result.RolledBack && o.ActivationCount >= len(e.Activations) {
			return ErrInvalid
		}
	}
	return nil
}

func lookupRegressionOperation(c *catalog, key Key, operationID string) (RegressionOperation, error) {
	o, exists := c.RegressionOperations[operationID]
	if !exists {
		return RegressionOperation{}, ErrNotFound
	}
	if o.Expected.Key != key {
		return RegressionOperation{}, ErrConflict
	}
	e := c.Skills[key.index()]
	if o.Validate() != nil || o.ActivationCount > len(e.Activations) {
		return RegressionOperation{}, ErrInvalid
	}
	prefix := e
	prefix.Activations = e.Activations[:o.ActivationCount]
	prefix.Active = prefix.Activations[len(prefix.Activations)-1].To
	before, err := stateForEntry(prefix)
	if err != nil || before != o.Expected {
		return RegressionOperation{}, ErrInvalid
	}
	if o.Result.RolledBack {
		if o.ActivationCount >= len(e.Activations) {
			return RegressionOperation{}, ErrInvalid
		}
		a := e.Activations[o.ActivationCount]
		if !a.Rollback || a.From != o.Expected.Active || a.To != o.After.Active || a.Regression == nil || *a.Regression != o.Result.Evidence || !a.At.Equal(o.CheckedAt) || a.OperationID != "" {
			return RegressionOperation{}, ErrInvalid
		}
		prefix.Activations = e.Activations[:o.ActivationCount+1]
		prefix.Active = a.To
	}
	after, err := stateForEntry(prefix)
	if err != nil || after != o.After {
		return RegressionOperation{}, ErrInvalid
	}
	return o, nil
}

// RegressionOperation reads a receipt without checking again or changing state.
func (s *FileStore) RegressionOperation(ctx context.Context, key Key, operationID string) (RegressionOperation, error) {
	if s == nil || ctx == nil || !s.permitted(key) || !identifier.MatchString(operationID) {
		return RegressionOperation{}, ErrInvalid
	}
	var out RegressionOperation
	err := s.with(ctx, func(c *catalog) error {
		var err error
		out, err = lookupRegressionOperation(c, key, operationID)
		return err
	}, false)
	if err != nil {
		return RegressionOperation{}, err
	}
	return out, nil
}
