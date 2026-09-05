package approvals

// Command requests an operator decision against an exact observed approval.
// Actor identity and decision time are supplied by the authenticated server,
// never by this client payload. ID is a stable retry key for this decision.
type Command struct {
	Expected Request `json:"expected"`
	ID       string  `json:"id"`
	Allowed  bool    `json:"allowed"`
}

func (c Command) Validate() error {
	if c.Expected.Validate() != nil || !identifier(c.ID) {
		return ErrInvalid
	}
	return nil
}
