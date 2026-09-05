package approvals

// ListOptions selects a bounded task-local page in lexical tool-call ID order.
// The cursor is exclusive. Separate pages are not a point-in-time snapshot:
// intervening inserts before the cursor require restarting the listing.
type ListOptions struct {
	TaskID      string `json:"task_id"`
	AfterCallID string `json:"after_call_id,omitempty"`
	Limit       int    `json:"limit"`
}

func (q ListOptions) Validate() error {
	if !identifier(q.TaskID) || (q.AfterCallID != "" && !identifier(q.AfterCallID)) || q.Limit < 1 || q.Limit > 100 {
		return ErrInvalid
	}
	return nil
}

// Page contains digest-only approval records, never arguments or lease tokens.
type Page struct {
	Version         int         `json:"version"`
	Query           ListOptions `json:"query"`
	Records         []Record    `json:"records"`
	NextAfterCallID string      `json:"next_after_call_id,omitempty"`
}

func (p Page) Validate() error {
	if p.Version != Version || p.Query.Validate() != nil || p.Records == nil || len(p.Records) > p.Query.Limit {
		return ErrInvalid
	}
	previous := p.Query.AfterCallID
	seenIDs := make(map[string]bool, len(p.Records))
	for _, r := range p.Records {
		if r.Validate() != nil || r.Request.TaskID != p.Query.TaskID || r.Request.ToolCallID <= previous || seenIDs[r.Request.ID] {
			return ErrInvalid
		}
		seenIDs[r.Request.ID] = true
		previous = r.Request.ToolCallID
	}
	if p.NextAfterCallID != "" && (len(p.Records) != p.Query.Limit || p.NextAfterCallID != previous) {
		return ErrInvalid
	}
	return nil
}
