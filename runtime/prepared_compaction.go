package runtime

import (
	"bytes"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// ApprovedCompaction is a host-materialized, operator-approved replacement for
// the initial message prefix of a running task. The runtime never generates or
// approves summaries. It may activate this replacement once, at a safe later
// turn boundary, while retaining every message appended by the live task.
type ApprovedCompaction struct {
	Compaction        *ContextCompaction  `json:"compaction"`
	OriginalPrefix    []providers.Message `json:"original_prefix"`
	ReplacementPrefix []providers.Message `json:"replacement_prefix"`
}

func (p *ApprovedCompaction) validate(parent string, initial []providers.Message) error {
	if p == nil || p.Compaction == nil || p.Compaction.SummaryAttemptID == "" || p.Compaction.SummaryReviewID == "" ||
		p.Compaction.Validate(parent) != nil || len(p.OriginalPrefix) == 0 || len(p.ReplacementPrefix) == 0 ||
		providers.ValidateMessages(p.OriginalPrefix) != nil || providers.ValidateMessages(p.ReplacementPrefix) != nil {
		return ErrInvalidRun
	}
	want, err := json.Marshal(initial)
	if err != nil {
		return ErrInvalidRun
	}
	original, err := json.Marshal(p.OriginalPrefix)
	if err != nil || !bytes.Equal(want, original) {
		return ErrInvalidRun
	}
	return nil
}

func cloneApprovedCompaction(p *ApprovedCompaction) (*ApprovedCompaction, error) {
	if p == nil {
		return nil, nil
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > 8<<20 {
		return nil, ErrInvalidRun
	}
	var clone ApprovedCompaction
	if json.Unmarshal(body, &clone) != nil {
		return nil, ErrInvalidRun
	}
	return &clone, nil
}
