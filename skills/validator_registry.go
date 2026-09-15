package skills

// ValidatorRegistry binds stable policy identities to trusted host callbacks.
// It owns an immutable copy of the supplied map, not the callback state. The
// host must provide read-only, repeat-safe, concurrency-safe validators that
// cooperate with cancellation. Changed validation semantics require a new ID;
// this registry neither authenticates callback code nor executes a sandbox.
type ValidatorRegistry struct{ validators map[string]Validator }

const maxValidators = 64

func NewValidatorRegistry(input map[string]Validator) (*ValidatorRegistry, error) {
	if len(input) > maxValidators {
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

// WithProtectedValidator returns a new registry containing one product-owned
// validator. It never mutates the host registry and refuses an existing
// binding, even when the callback appears identical, so host code cannot
// replace or impersonate a protected validator identity.
func WithProtectedValidator(host *ValidatorRegistry, id string, validator Validator) (*ValidatorRegistry, error) {
	if !identifier.MatchString(id) || validator == nil || nilRegressionValidator(validator) {
		return nil, ErrInvalid
	}
	size := 1
	if host != nil {
		size += len(host.validators)
	}
	if size > maxValidators {
		return nil, ErrInvalid
	}
	owned := make(map[string]Validator, size)
	if host != nil {
		for existingID, existing := range host.validators {
			if !identifier.MatchString(existingID) || existing == nil || nilRegressionValidator(existing) || existingID == id {
				return nil, ErrInvalid
			}
			owned[existingID] = existing
		}
	}
	owned[id] = validator
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
