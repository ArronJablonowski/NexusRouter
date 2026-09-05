package skills

// WorkflowScanConsumption binds one source page to its consumption-time result.
// Buckets are only those touched by this page, not the entire epoch catalog.
// Sources must still be revalidated before generation; a receipt grants no authority.
type WorkflowScanConsumption struct {
	Version    int              `json:"version"`
	Scope      string           `json:"scope"`
	Name       string           `json:"name"`
	Domain     string           `json:"domain"`
	Epoch      int64            `json:"epoch"`
	Revision   int64            `json:"revision"`
	PageDigest string           `json:"page_digest"`
	Considered int              `json:"considered"`
	Eligible   int              `json:"eligible"`
	Buckets    []WorkflowBucket `json:"buckets"`
}

func (c WorkflowScanConsumption) Validate() error {
	if c.Version != 1 || !identifier.MatchString(c.Scope) || !identifier.MatchString(c.Name) || !identifier.MatchString(c.Domain) || c.Epoch < 1 || c.Revision < c.Epoch || c.Revision > 1_000_000_000 || !selectionDigest(c.PageDigest) || c.Considered < 0 || c.Considered > 20 || c.Eligible < 0 || c.Eligible > c.Considered || c.Buckets == nil || len(c.Buckets) > c.Eligible || (c.Eligible > 0 && len(c.Buckets) == 0) {
		return ErrInvalid
	}
	previous := ""
	for _, bucket := range c.Buckets {
		if bucket.Validate() != nil || bucket.Domain != c.Domain || bucket.ID <= previous {
			return ErrInvalid
		}
		previous = bucket.ID
	}
	return nil
}

// ValidateAfter checks the durable scan transition, in addition to page shape.
func (p WorkflowScanPage) ValidateAfter(previous *WorkflowScan) error {
	if p.Validate() != nil {
		return ErrInvalid
	}
	if previous == nil {
		if p.Scan.Revision != 1 || p.Scan.Epoch != 1 || p.After != "" {
			return ErrInvalid
		}
		return nil
	}
	if previous.Validate() != nil || p.Scan.Revision != previous.Revision+1 || p.Scan.Scope != previous.Scope || p.Scan.Name != previous.Name || p.Scan.Domain != previous.Domain {
		return ErrInvalid
	}
	if previous.Complete {
		if p.Scan.Epoch != previous.Epoch+1 || p.After != "" || p.Scan.Fence < previous.Fence || p.Scan.Upper < previous.Upper {
			return ErrInvalid
		}
	} else if p.Scan.Epoch != previous.Epoch || p.Scan.Fence != previous.Fence || p.Scan.Upper != previous.Upper || p.After != previous.Cursor {
		return ErrInvalid
	}
	return nil
}
