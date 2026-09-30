package v1

import (
	"reflect"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

// ContextModelBinding is copied from the effective configuration used by this
// client. It excludes credentials and cannot change provider dispatch.
type ContextModelBinding struct {
	ModelID, Model, ProviderID, Kind, Endpoint string
}

// ContextEstimatorFactory creates trusted process-local accounting after
// configuration validation and before service construction. It must return
// promptly; like New itself it has no cancellation context. Returning an error,
// panicking, or returning a nil estimator rejects client construction.
type ContextEstimatorFactory func([]ContextModelBinding) (ContextEstimator, error)

func configuredContextEstimator(cfg config.Settings, factory ContextEstimatorFactory) (estimator ContextEstimator, err error) {
	defer func() {
		if recover() != nil {
			estimator = nil
			err = ErrAdmission
		}
	}()
	providersByID := make(map[string]config.Provider, len(cfg.Providers))
	for _, p := range cfg.Providers {
		providersByID[p.ID] = p
	}
	bindings := make([]ContextModelBinding, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		p, ok := providersByID[m.Provider]
		if !ok {
			return nil, ErrAdmission
		}
		bindings = append(bindings, ContextModelBinding{ModelID: m.ID, Model: m.Model, ProviderID: p.ID, Kind: p.Kind, Endpoint: p.Endpoint})
	}
	estimator, err = factory(bindings)
	if err != nil || estimator == nil {
		return nil, ErrAdmission
	}
	v := reflect.ValueOf(estimator)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil, ErrAdmission
		}
	}
	return estimator, nil
}
