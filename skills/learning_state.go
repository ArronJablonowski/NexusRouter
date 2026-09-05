package skills

// LearningState is a restart-safe cursor over the discovery, consumption and
// generation phases. PolicyDigest binds operator configuration, not proof of
// successful execution. Advancing this cursor never authorizes activation.
type LearningState struct {
	Version            int    `json:"version"`
	Scope              string `json:"scope"`
	Name               string `json:"name"`
	Domain             string `json:"domain"`
	PolicyDigest       string `json:"policy_digest"`
	Revision           int64  `json:"revision"`
	Phase              string `json:"phase"`
	ScanRevision       int64  `json:"scan_revision"`
	ConsumeRevision    int64  `json:"consume_revision"`
	Epoch              int64  `json:"epoch"`
	BucketAfter        string `json:"bucket_after"`
	PendingSelectionID string `json:"pending_selection_id,omitempty"`
	PendingBucketID    string `json:"pending_bucket_id,omitempty"`
}

func (s LearningState) Validate() error {
	if (s.PendingSelectionID == "") != (s.PendingBucketID == "") || (s.PendingSelectionID != "" && (s.Phase != "generate" || !selectionDigest(s.PendingSelectionID) || !selectionDigest(s.PendingBucketID) || s.PendingBucketID <= s.BucketAfter)) {
		return ErrInvalid
	}
	if s.Version != 1 || !identifier.MatchString(s.Scope) || !identifier.MatchString(s.Name) || !identifier.MatchString(s.Domain) || !selectionDigest(s.PolicyDigest) || s.Revision < 1 || s.Revision > 1_000_000_000 || s.ScanRevision < 0 || s.ScanRevision > 1_000_000_000 || s.ConsumeRevision < 0 || s.ConsumeRevision > s.ScanRevision || s.Epoch < 0 || s.Epoch > s.ScanRevision || (s.BucketAfter != "" && !selectionDigest(s.BucketAfter)) {
		return ErrInvalid
	}
	if (s.ScanRevision == 0) != (s.Epoch == 0) {
		return ErrInvalid
	}
	switch s.Phase {
	case "discover":
		if s.ScanRevision != s.ConsumeRevision || s.BucketAfter != "" {
			return ErrInvalid
		}
	case "consume":
		if s.ScanRevision != s.ConsumeRevision+1 || s.Epoch < 1 || s.BucketAfter != "" {
			return ErrInvalid
		}
	case "generate":
		if s.ScanRevision != s.ConsumeRevision || s.ScanRevision < 1 || s.Epoch < 1 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if s.Revision == 1 && (s.Phase != "discover" || s.ScanRevision != 0 || s.ConsumeRevision != 0 || s.Epoch != 0 || s.BucketAfter != "") {
		return ErrInvalid
	}
	return nil
}

// ValidateAfter checks trusted-host cursor progression, not the provenance of
// scan records or authority to dispatch a generation. Dispatch still requires
// the separate single-use generation claim for the pinned selection.
func (s LearningState) ValidateAfter(previous LearningState) error {
	if s.Validate() != nil || previous.Validate() != nil || s.Revision != previous.Revision+1 || s.Scope != previous.Scope || s.Name != previous.Name || s.Domain != previous.Domain || s.PolicyDigest != previous.PolicyDigest {
		return ErrInvalid
	}
	p := previous
	switch p.Phase {
	case "discover":
		if s.Phase == "consume" && s.ScanRevision == p.ScanRevision+1 && s.ConsumeRevision == p.ConsumeRevision && (s.Epoch == p.Epoch || s.Epoch == p.Epoch+1) {
			return nil
		}
	case "consume":
		if (s.Phase == "discover" || s.Phase == "generate") && s.ScanRevision == p.ScanRevision && s.ConsumeRevision == p.ScanRevision && s.Epoch == p.Epoch && s.BucketAfter == "" && s.PendingSelectionID == "" {
			return nil
		}
	case "generate":
		if s.ScanRevision != p.ScanRevision || s.ConsumeRevision != p.ConsumeRevision || s.Epoch != p.Epoch {
			return ErrInvalid
		}
		if p.PendingSelectionID != "" {
			if s.Phase == "generate" && s.PendingSelectionID == "" && s.BucketAfter == p.PendingBucketID {
				return nil
			}
		} else if s.Phase == "discover" || (s.Phase == "generate" && ((s.PendingSelectionID != "" && s.BucketAfter == p.BucketAfter) || (s.PendingSelectionID == "" && s.BucketAfter > p.BucketAfter))) {
			return nil
		}
	}
	return ErrInvalid
}
