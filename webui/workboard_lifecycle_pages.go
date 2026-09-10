package webui

import "time"

const (
	MaxLifecyclePageItems = 25
	MaxLifecyclePageBytes = 3 << 20
)

type AttemptHistoryOptions struct {
	After string
	Limit int
}

func (o AttemptHistoryOptions) Validate() error {
	if o.Limit < 1 || o.Limit > MaxLifecyclePageItems || o.After != "" && !boundedPrintable(o.After, 1, MaxCursorBytes) {
		return ErrContract
	}
	return nil
}

type AttemptHistoryRecord struct {
	Version          int        `json:"version"`
	ID               string     `json:"id"`
	BoardID          string     `json:"board_id"`
	CardID           string     `json:"card_id"`
	Ordinal          int        `json:"ordinal"`
	Revision         int64      `json:"revision"`
	State            string     `json:"state"`
	WorkerID         string     `json:"worker_id"`
	CriteriaRevision int64      `json:"criteria_revision"`
	CheckpointCount  int        `json:"checkpoint_count"`
	CandidateID      string     `json:"candidate_id,omitempty"`
	AcceptanceID     string     `json:"acceptance_id,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
}

func (r AttemptHistoryRecord) Validate() error {
	if r.Version != ContractVersion || !validID(r.ID) || !validID(r.BoardID) || !validID(r.CardID) || !validID(r.WorkerID) ||
		r.Ordinal < 1 || r.Ordinal > MaxAttemptsPerCard || r.Revision < 1 || r.CriteriaRevision < 1 ||
		r.CheckpointCount < 0 || r.CheckpointCount > 10_000 || !optionalID(r.CandidateID) || !optionalID(r.AcceptanceID) ||
		!validAttemptState(r.State) || !validWorkboardTime(r.StartedAt) || (r.EndedAt == nil) != (r.State == "running") ||
		r.EndedAt != nil && (!validWorkboardTime(*r.EndedAt) || r.EndedAt.Before(r.StartedAt)) {
		return ErrContract
	}
	if (r.CandidateID != "") != (r.State == "review" || r.State == "accepted" || r.State == "rejected") ||
		(r.AcceptanceID != "") != (r.State == "accepted" || r.State == "rejected") {
		return ErrContract
	}
	return nil
}

type AttemptHistoryPage struct {
	Version          int                    `json:"version"`
	BoardID          string                 `json:"board_id"`
	CardID           string                 `json:"card_id"`
	HighWaterOrdinal int                    `json:"high_water_ordinal"`
	Items            []AttemptHistoryRecord `json:"items"`
	NextCursor       string                 `json:"next_cursor,omitempty"`
	HasMore          bool                   `json:"has_more"`
}

func (p AttemptHistoryPage) Validate() error {
	if p.Version != ContractVersion || !validID(p.BoardID) || !validID(p.CardID) || p.HighWaterOrdinal < 0 || p.HighWaterOrdinal > MaxAttemptsPerCard ||
		len(p.Items) > MaxLifecyclePageItems || p.HasMore != (p.NextCursor != "") || p.NextCursor != "" && !boundedPrintable(p.NextCursor, 1, MaxCursorBytes) ||
		p.HasMore && len(p.Items) == 0 || (p.HighWaterOrdinal == 0) != (len(p.Items) == 0) {
		return ErrContract
	}
	previous := p.HighWaterOrdinal + 1
	for _, item := range p.Items {
		if item.Validate() != nil || item.BoardID != p.BoardID || item.CardID != p.CardID || item.Ordinal >= previous || item.Ordinal > p.HighWaterOrdinal {
			return ErrContract
		}
		previous = item.Ordinal
	}
	if !p.HasMore && len(p.Items) > 0 && previous != 1 {
		return ErrContract
	}
	return encodedWithin(p, MaxLifecyclePageBytes)
}

type AttemptDetailOptions struct {
	After string
	Limit int
}

func (o AttemptDetailOptions) Validate() error {
	if o.Limit < 1 || o.Limit > MaxLifecyclePageItems || o.After != "" && !boundedPrintable(o.After, 1, MaxCursorBytes) {
		return ErrContract
	}
	return nil
}

type AttemptDetailPage struct {
	Version                     int              `json:"version"`
	Attempt                     Attempt          `json:"attempt"`
	CheckpointHighWaterRevision int64            `json:"checkpoint_high_water_revision"`
	Checkpoints                 []WorkCheckpoint `json:"checkpoints"`
	NextCursor                  string           `json:"next_cursor,omitempty"`
	HasMore                     bool             `json:"has_more"`
}

func (p AttemptDetailPage) Validate() error {
	if p.Version != ContractVersion || p.Attempt.Validate() != nil || p.CheckpointHighWaterRevision < 0 || p.CheckpointHighWaterRevision > 10_000 ||
		len(p.Checkpoints) > MaxLifecyclePageItems || p.HasMore != (p.NextCursor != "") || p.NextCursor != "" && !boundedPrintable(p.NextCursor, 1, MaxCursorBytes) ||
		p.HasMore && len(p.Checkpoints) == 0 || (p.CheckpointHighWaterRevision == 0) != (len(p.Checkpoints) == 0) {
		return ErrContract
	}
	previous := p.CheckpointHighWaterRevision + 1
	for _, checkpoint := range p.Checkpoints {
		if checkpoint.Validate() != nil || checkpoint.BoardID != p.Attempt.BoardID || checkpoint.CardID != p.Attempt.CardID || checkpoint.AttemptID != p.Attempt.ID ||
			checkpoint.Revision >= previous || checkpoint.Revision > p.CheckpointHighWaterRevision {
			return ErrContract
		}
		previous = checkpoint.Revision
	}
	if !p.HasMore && len(p.Checkpoints) > 0 && previous != 1 {
		return ErrContract
	}
	return encodedWithin(p, MaxLifecyclePageBytes)
}

func validAttemptState(value string) bool {
	return value == "running" || value == "review" || value == "accepted" || value == "rejected" || value == "failed" || value == "canceled"
}
