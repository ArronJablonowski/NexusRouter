package skills

import "github.com/ArronJablonowski/DarwinRouter/sessions"

// WorkflowScan freezes epoch membership with an append-only task sequence fence
// and lexical upper ID. Evidence stays live; later epochs revisit changed tasks.
// Scope/name identify the scan; epoch/revision fence changes, not content hashes.
type WorkflowScan struct {
	Version  int    `json:"version"`
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Epoch    int64  `json:"epoch"`
	Revision int64  `json:"revision"`
	Fence    int64  `json:"fence"`
	Cursor   string `json:"cursor"`
	Upper    string `json:"upper"`
	Complete bool   `json:"complete"`
}

func (s WorkflowScan) Validate() error {
	if s.Fence < 0 || (s.Fence == 0) != (s.Upper == "") {
		return ErrInvalid
	}
	if s.Version != 1 || !identifier.MatchString(s.Scope) || !identifier.MatchString(s.Name) || !identifier.MatchString(s.Domain) || s.Epoch < 1 || s.Revision < s.Epoch || s.Revision > 1_000_000_000 || !scanCursor(s.Cursor) || !scanCursor(s.Upper) || s.Cursor > s.Upper {
		return ErrInvalid
	}
	if s.Upper == "" && (s.Cursor != "" || !s.Complete) {
		return ErrInvalid
	}
	if !s.Complete && (s.Upper == "" || s.Cursor >= s.Upper) {
		return ErrInvalid
	}
	return nil
}

// WorkflowScanPage binds one coherent persisted discovery observation to its
// resulting scan revision. Membership is frozen, not task or evidence state.
type WorkflowScanPage struct {
	Version int                   `json:"version"`
	Scan    WorkflowScan          `json:"scan"`
	After   string                `json:"after"`
	Limit   int                   `json:"limit"`
	Page    WorkflowCandidatePage `json:"page"`
}

func (p WorkflowScanPage) Validate() error {
	if p.Version != 1 || p.Scan.Validate() != nil || p.Limit < 1 || p.Limit > 20 || !scanCursor(p.After) || p.After > p.Scan.Cursor || p.Page.Domain != p.Scan.Domain || p.Page.Validate(p.After, p.Limit) != nil {
		return ErrInvalid
	}
	if (p.Page.Scanned == 0 && p.Scan.Cursor != p.After) || (p.Page.Scanned > 0 && p.Scan.Cursor <= p.After) {
		return ErrInvalid
	}
	if p.Page.Next != "" && p.Page.Next != p.Scan.Cursor {
		return ErrInvalid
	}
	for _, candidate := range p.Page.Candidates {
		if candidate.TaskID > p.Scan.Cursor || candidate.TaskID > p.Scan.Upper {
			return ErrInvalid
		}
	}
	if p.Scan.Complete != (p.Page.Scanned < p.Limit || p.Scan.Cursor == p.Scan.Upper) {
		return ErrInvalid
	}
	return nil
}

func scanCursor(value string) bool { return value == "" || sessions.ValidEventPageID(value) }
