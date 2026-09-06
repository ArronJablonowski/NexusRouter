package skills

// ValidatorRegistry binds stable policy identities to trusted host callbacks.
// It owns an immutable copy of the supplied map, not the callback state. The
// host must provide read-only, repeat-safe, concurrency-safe validators that
// cooperate with cancellation. Changed validation semantics require a new ID;
// this registry neither authenticates callback code nor executes a sandbox.
type ValidatorRegistry struct{ validators map[string]Validator }

func NewValidatorRegistry(input map[string]Validator) (*ValidatorRegistry, error) {
	if len(input) > 64 {
		return nil, ErrInvalid
	}
	owned := make(map[string]Validator, len(input))
	for id, validator := range input {
		if !identifier.MatchString(id) || validator == nil || nilRegressionValidator(validator) {
			return nil, ErrInvalid
		}
		owned[id] = validator
	}
	return &ValidatorRegistry{validators: owned}, nil
}

// Resolve never invokes validation. Empty registries have no available policy;
// a missing identity is not replaced with an arbitrary/default validator.
func (r *ValidatorRegistry) Resolve(id string) (Validator, error) {
	if r == nil || !identifier.MatchString(id) {
		return nil, ErrInvalid
	}
	validator, ok := r.validators[id]
	if !ok {
		return nil, ErrNotFound
	}
	if validator == nil || nilRegressionValidator(validator) {
		return nil, ErrInvalid
	}
	return validator, nil
}
