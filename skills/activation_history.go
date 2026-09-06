package skills

// activationStack derives the remaining undo operations from the durable
// timeline. An activation can be reversed once, even when a version is later
// reactivated. Version identity alone is not an undo cursor.
func activationStack(e entry) ([]activation, error) {
	if len(e.Activations) > 10000 || len(e.Versions) > 1000 {
		return nil, ErrInvalid
	}
	known := make(map[string]bool, len(e.Versions))
	for _, version := range e.Versions {
		known[version.Version] = true
	}
	stack := make([]activation, 0, len(e.Activations))
	current := ""
	for _, a := range e.Activations {
		if !validActivationOperationFields(a) {
			return nil, ErrInvalid
		}
		if a.Regression != nil && (!a.Rollback || a.Regression.Passed || !a.Regression.Deterministic || !identifier.MatchString(a.Regression.ID)) {
			return nil, ErrInvalid
		}
		_, offset := a.At.Zone()
		proof := e.Validated[a.To]
		if a.From != current || a.To == current || !versionID(a.To) || !known[a.To] ||
			!proof.Passed || !proof.Deterministic || !identifier.MatchString(proof.ID) ||
			a.At.Year() < 1970 || a.At.Year() >= 2261 || offset != 0 {
			return nil, ErrInvalid
		}
		if a.Rollback {
			if len(stack) == 0 {
				return nil, ErrInvalid
			}
			last := stack[len(stack)-1]
			if last.From == "" || a.From != last.To || a.To != last.From {
				return nil, ErrInvalid
			}
			stack = stack[:len(stack)-1]
		} else {
			stack = append(stack, a)
		}
		current = a.To
	}
	if current != e.Active {
		return nil, ErrInvalid
	}
	return stack, nil
}
