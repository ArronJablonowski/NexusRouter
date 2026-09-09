package webui

import "reflect"

var canonicalColumnStates = [...]string{"backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"}

// CanonicalColumnStates returns the fixed lane identities in presentation
// order. The returned slice is owned by the caller.
func CanonicalColumnStates() []string {
	return append([]string(nil), canonicalColumnStates[:]...)
}

func validateColumns(boardID string, columns []Column) error {
	if len(columns) != len(canonicalColumnStates) {
		return ErrContract
	}
	seenID, seenState, seenRank := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index, column := range columns {
		if column.Validate() != nil || column.BoardID != boardID || column.State != canonicalColumnStates[index] ||
			seenID[column.ID] || seenState[column.State] || seenRank[column.Rank] ||
			index > 0 && columns[index-1].Rank >= column.Rank {
			return ErrContract
		}
		seenID[column.ID], seenState[column.State], seenRank[column.Rank] = true, true, true
	}
	return nil
}

func validateSnapshotGraph(cards []Card) error {
	byID := make(map[string]Card, len(cards))
	for _, card := range cards {
		byID[card.ID] = card
	}
	for _, card := range cards {
		if card.ParentID != "" {
			parent, visible := byID[card.ParentID]
			if visible && parent.BoardID != card.BoardID {
				return ErrContract
			}
		}
		unresolvedVisible := 0
		for _, dependencyID := range card.Dependencies {
			dependency, visible := byID[dependencyID]
			if !visible {
				continue
			}
			if dependency.BoardID != card.BoardID {
				return ErrContract
			}
			if dependency.State != "done" {
				unresolvedVisible++
			}
		}
		if card.RemainingDependencies < unresolvedVisible {
			return ErrContract
		}
	}
	visits := 0
	if graphDepth(cards, byID, true, &visits) != nil || graphDepth(cards, byID, false, &visits) != nil {
		return ErrContract
	}
	return nil
}

// graphDepth validates either the parent forest or the dependency DAG over the
// visible snapshot. References outside a partial page terminate that path.
func graphDepth(cards []Card, byID map[string]Card, parents bool, visits *int) error {
	state, longest := map[string]uint8{}, map[string]int{}
	var visit func(string) (int, error)
	visit = func(id string) (int, error) {
		*visits = *visits + 1
		if *visits > MaxGraphVisits || state[id] == 1 {
			return 0, ErrContract
		}
		if state[id] == 2 {
			return longest[id], nil
		}
		card, visible := byID[id]
		if !visible {
			return 0, nil
		}
		state[id] = 1
		depth := 1
		if parents {
			if card.ParentID != "" {
				childDepth, err := visit(card.ParentID)
				if err != nil {
					return 0, err
				}
				depth += childDepth
			}
		} else {
			for _, dependencyID := range card.Dependencies {
				childDepth, err := visit(dependencyID)
				if err != nil {
					return 0, err
				}
				if childDepth+1 > depth {
					depth = childDepth + 1
				}
			}
		}
		if depth > MaxDependencyDepth {
			return 0, ErrContract
		}
		state[id], longest[id] = 2, depth
		return depth, nil
	}
	for _, card := range cards {
		if _, err := visit(card.ID); err != nil {
			return ErrContract
		}
	}
	return nil
}

// ValidateCardTransition checks the lifecycle and monotonic fields visible to
// durable storage. Proof-gated commands still require their service-specific
// recovery or acceptance validation before this transition is committed.
func ValidateCardTransition(before, after Card) error {
	if before.Validate() != nil || after.Validate() != nil || before.ID != after.ID || before.BoardID != after.BoardID ||
		after.Revision != before.Revision+1 || !after.CreatedAt.Equal(before.CreatedAt) || after.UpdatedAt.Before(before.UpdatedAt) ||
		after.CriteriaRevision < before.CriteriaRevision || after.AttemptCount < before.AttemptCount {
		return ErrContract
	}
	if before.State == "done" || before.State == "canceled" {
		return ErrContract
	}
	allowed := before.State == after.State
	switch before.State + "\x00" + after.State {
	case "backlog\x00ready", "ready\x00backlog", "ready\x00in_progress", "in_progress\x00blocked",
		"blocked\x00in_progress", "blocked\x00ready", "in_progress\x00review", "review\x00ready", "review\x00done",
		"backlog\x00canceled", "ready\x00canceled", "in_progress\x00canceled", "blocked\x00canceled", "review\x00canceled":
		allowed = true
	}
	if !allowed {
		return ErrContract
	}
	if before.State == "in_progress" || before.State == "blocked" || before.State == "review" || before.State == "ready" && after.State == "in_progress" {
		if after.CriteriaRevision != before.CriteriaRevision || AcceptanceCriteriaDigest(after.Criteria) != AcceptanceCriteriaDigest(before.Criteria) {
			return ErrContract
		}
	}
	if before.State == "ready" && after.State == "in_progress" {
		if after.AttemptCount != before.AttemptCount+1 || after.CurrentAttemptID == before.CurrentAttemptID || after.CurrentClaimID == "" {
			return ErrContract
		}
	} else if after.AttemptCount != before.AttemptCount {
		return ErrContract
	}
	return nil
}

// ValidateClaimTransition keeps ownership immutable and makes release terminal.
func ValidateClaimTransition(before, after Claim) error {
	if before.Validate() != nil || after.Validate() != nil || before.ID != after.ID || before.BoardID != after.BoardID ||
		before.CardID != after.CardID || before.AttemptID != after.AttemptID || before.OwnerID != after.OwnerID ||
		before.OwnerType != after.OwnerType || before.TaskID != after.TaskID || after.Revision != before.Revision+1 ||
		after.LastHeartbeat.Before(before.LastHeartbeat) || after.ExpiresAt.Before(before.ExpiresAt) || before.State == "released" {
		return ErrContract
	}
	allowed := before.State == after.State
	if before.State == "active" && (after.State == "attention" || after.State == "released") ||
		before.State == "attention" && (after.State == "active" || after.State == "released") {
		allowed = true
	}
	if !allowed {
		return ErrContract
	}
	return nil
}

// ValidateAttemptTransition protects fields frozen at claim time and permits
// only append-only links/evidence across the durable attempt lifecycle.
func ValidateAttemptTransition(before, after Attempt) error {
	if before.Validate() != nil || after.Validate() != nil || before.ID != after.ID || before.BoardID != after.BoardID || before.CardID != after.CardID ||
		before.Ordinal != after.Ordinal || before.WorkerID != after.WorkerID || before.CriteriaRevision != after.CriteriaRevision ||
		before.CriteriaDigest != after.CriteriaDigest || before.PolicyDigest != after.PolicyDigest || before.Budget != after.Budget ||
		!reflect.DeepEqual(before.Criteria, after.Criteria) || after.Revision != before.Revision+1 || !after.StartedAt.Equal(before.StartedAt) ||
		!slicePrefix(before.TaskIDs, after.TaskIDs) || !slicePrefix(before.SessionIDs, after.SessionIDs) || !evidencePrefix(before.Evidence, after.Evidence) ||
		before.State == "accepted" || before.State == "rejected" || before.State == "failed" || before.State == "canceled" {
		return ErrContract
	}
	allowed := before.State == after.State && before.State == "running"
	switch before.State + "\x00" + after.State {
	case "running\x00review", "running\x00failed", "running\x00canceled", "review\x00accepted", "review\x00rejected":
		allowed = true
	}
	if !allowed || before.Candidate != nil && !reflect.DeepEqual(before.Candidate, after.Candidate) {
		return ErrContract
	}
	return nil
}

func slicePrefix(before, after []string) bool {
	return len(before) == 0 || len(before) <= len(after) && reflect.DeepEqual(before, after[:len(before)])
}

func evidencePrefix(before, after []EvidenceRecord) bool {
	return len(before) == 0 || len(before) <= len(after) && reflect.DeepEqual(before, after[:len(before)])
}
