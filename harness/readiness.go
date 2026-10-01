package harness

// Readiness contains observed prerequisites plus configured compatibility. A
// matching executable is not proof of all installed dependencies or behavior;
// provider inventory is not attestation of model weights or tool capabilities.
type Readiness struct {
	Identity          Identity
	ExecutableMatched bool
	CredentialState   string // present, not_required, missing
	ModelState        string // present, absent, unknown
	Compatible        bool
	Local             bool
	Capabilities      []string
	ContextTokens     int64
	EstimatedCost     float64
}

func (r Readiness) Validate() error {
	if r.Identity.Validate() != nil || !labels(r.Capabilities) || r.ContextTokens < 8192 || !nonnegative(r.EstimatedCost) {
		return ErrInvalid
	}
	switch r.CredentialState {
	case "present", "not_required", "missing":
	default:
		return ErrInvalid
	}
	switch r.ModelState {
	case "present", "absent", "unknown":
	default:
		return ErrInvalid
	}
	return nil
}
