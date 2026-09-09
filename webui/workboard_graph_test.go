package webui

import (
	"fmt"
	"testing"
	"time"
)

func plainCard(id, state string) Card {
	card := cardFixture()
	card.ID, card.State, card.Revision = id, state, 1
	card.CurrentAttemptID, card.CurrentClaimID, card.AcceptanceID, card.BlockReason = "", "", "", ""
	card.AttemptCount, card.RemainingDependencies = 0, 0
	if state == "done" {
		card.CurrentAttemptID, card.AcceptanceID, card.AttemptCount = "attempt-"+id, "acceptance-"+id, 1
	}
	return card
}

func snapshotFixture(cards ...Card) BoardSnapshot {
	board := boardFixture()
	board.CardCount, board.ActiveClaims = len(cards), 0
	return BoardSnapshot{Version: 1, Board: board, Columns: columnFixtures(board.ID), Cards: cards, GraphRevision: 1, GraphDigest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
}

func TestCanonicalColumnsAreExactAndBoardBound(t *testing.T) {
	states := CanonicalColumnStates()
	states[0] = "changed"
	if CanonicalColumnStates()[0] != "backlog" {
		t.Fatal("caller mutated canonical column identities")
	}
	snapshot := snapshotFixture(plainCard("card-a", "backlog"))
	if snapshot.Validate() != nil {
		t.Fatal("canonical columns rejected")
	}
	for name, mutate := range map[string]func([]Column){
		"missing":          func(columns []Column) { columns[0] = Column{} },
		"foreign board":    func(columns []Column) { columns[0].BoardID = "board-b" },
		"renamed identity": func(columns []Column) { columns[0].ID = "other" },
		"duplicate rank":   func(columns []Column) { columns[1].Rank = columns[0].Rank },
		"wrong order":      func(columns []Column) { columns[0], columns[1] = columns[1], columns[0] },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := snapshotFixture(plainCard("card-a", "backlog"))
			mutate(candidate.Columns)
			if candidate.Validate() == nil {
				t.Fatal("invalid columns accepted")
			}
		})
	}
}

func TestSnapshotValidatesVisibleGraphWithoutAssumingPageCompleteness(t *testing.T) {
	parent := plainCard("parent", "backlog")
	done := plainCard("done-dependency", "done")
	child := plainCard("child", "backlog")
	child.ParentID = parent.ID
	child.Dependencies = []string{done.ID}
	if snapshotFixture(parent, done, child).Validate() != nil {
		t.Fatal("valid parent and completed dependency rejected")
	}

	missing := snapshotFixture(child)
	if missing.Validate() != nil {
		t.Fatal("page-local validation treated absent graph references as corruption")
	}

	unresolved := plainCard("unresolved", "backlog")
	child.ParentID = ""
	child.Dependencies, child.RemainingDependencies = []string{unresolved.ID}, 0
	if snapshotFixture(unresolved, child).Validate() == nil {
		t.Fatal("derived remaining dependency mismatch accepted")
	}

	first, second := plainCard("first", "backlog"), plainCard("second", "backlog")
	first.Dependencies, first.RemainingDependencies = []string{second.ID}, 1
	second.Dependencies, second.RemainingDependencies = []string{first.ID}, 1
	if snapshotFixture(first, second).Validate() == nil {
		t.Fatal("dependency cycle accepted")
	}
	first.Dependencies, first.RemainingDependencies, first.ParentID = nil, 0, second.ID
	second.Dependencies, second.RemainingDependencies, second.ParentID = nil, 0, first.ID
	if snapshotFixture(first, second).Validate() == nil {
		t.Fatal("parent cycle accepted")
	}
}

func TestSnapshotRejectsSixtyFiveNodePathsInEitherOrder(t *testing.T) {
	for _, parentEdges := range []bool{true, false} {
		for _, reversed := range []bool{false, true} {
			name := fmt.Sprintf("parents=%t/reversed=%t", parentEdges, reversed)
			t.Run(name, func(t *testing.T) {
				cards := make([]Card, MaxDependencyDepth+1)
				for index := range cards {
					cards[index] = plainCard(fmt.Sprintf("card-%02d", index), "backlog")
					if index+1 < len(cards) {
						if parentEdges {
							cards[index].ParentID = fmt.Sprintf("card-%02d", index+1)
						} else {
							cards[index].Dependencies = []string{fmt.Sprintf("card-%02d", index+1)}
							cards[index].RemainingDependencies = 1
						}
					}
				}
				if reversed {
					for left, right := 0, len(cards)-1; left < right; left, right = left+1, right-1 {
						cards[left], cards[right] = cards[right], cards[left]
					}
				}
				if snapshotFixture(cards...).Validate() == nil {
					t.Fatal("65-node path accepted")
				}
			})
		}
	}
}

func TestGraphTraversalHasIndependentVisitBudget(t *testing.T) {
	cards := make([]Card, MaxGraphVisits+1)
	byID := make(map[string]Card, len(cards))
	for index := range cards {
		cards[index] = plainCard(fmt.Sprintf("visit-%05d", index), "backlog")
		byID[cards[index].ID] = cards[index]
	}
	visits := 0
	if graphDepth(cards, byID, false, &visits) == nil || visits != MaxGraphVisits+1 {
		t.Fatal("graph traversal did not fail at its independent visit bound", visits)
	}
}

func TestPartialSnapshotDoesNotInventMissingGraphState(t *testing.T) {
	card := plainCard("child", "backlog")
	card.ParentID, card.Dependencies, card.RemainingDependencies = "off-page-parent", []string{"off-page-dependency"}, 1
	snapshot := snapshotFixture(card)
	snapshot.HasMore, snapshot.NextCursor = true, "next"
	snapshot.Board.CardCount = 2
	if snapshot.Validate() != nil {
		t.Fatal("partial page rejected an explicitly off-page reference")
	}
}

func TestArchivedBoardAndLiveClaimTimeAreClosed(t *testing.T) {
	board := boardFixture()
	board.State = "archived"
	if board.Validate() == nil {
		t.Fatal("archived board retained active claims")
	}
	claim := claimFixture("active")
	claim.ExpiresAt = claim.LastHeartbeat
	if claim.Validate() == nil {
		t.Fatal("live claim accepted a non-future expiry")
	}
	claim = claimFixture("attention")
	claim.ExpiresAt = claim.LastHeartbeat.Add(MaxClaimLease + time.Nanosecond)
	if claim.Validate() == nil {
		t.Fatal("live claim accepted an oversized lease interval")
	}
	claim = claimFixture("active")
	claim.OwnerType = "operator"
	if claim.Validate() == nil {
		t.Fatal("durable claim accepted a non-worker owner")
	}
}

func TestDurableLifecycleTransitionsAreClosedAndMonotonic(t *testing.T) {
	ready := plainCard("card-a", "ready")
	running := cardFixture()
	running.Revision, running.CreatedAt = ready.Revision+1, ready.CreatedAt
	running.UpdatedAt = ready.UpdatedAt.Add(1)
	if ValidateCardTransition(ready, running) != nil {
		t.Fatal("ready claim transition rejected")
	}
	done := running
	done.Revision, done.State, done.CurrentClaimID, done.AcceptanceID = running.Revision+1, "done", "", "acceptance-a"
	if ValidateCardTransition(running, done) == nil {
		t.Fatal("in-progress card skipped review and acceptance")
	}
	running.CriteriaRevision++
	if ValidateCardTransition(ready, running) == nil {
		t.Fatal("claim transition changed criteria revision")
	}
}

func TestClaimAndAttemptTransitionsPreserveFrozenIdentity(t *testing.T) {
	before := claimFixture("active")
	after := before
	after.Revision++
	after.LastHeartbeat = after.LastHeartbeat.Add(1)
	after.ExpiresAt = after.ExpiresAt.Add(1)
	if ValidateClaimTransition(before, after) != nil {
		t.Fatal("valid heartbeat rejected")
	}
	after.OwnerID = "worker-b"
	if ValidateClaimTransition(before, after) == nil {
		t.Fatal("claim ownership changed")
	}

	running := attemptFixture("running")
	review := attemptFixture("review")
	review.Revision = running.Revision + 1
	if ValidateAttemptTransition(running, review) != nil {
		t.Fatal("valid candidate submission rejected")
	}
	review.WorkerID = "worker-b"
	if ValidateAttemptTransition(running, review) == nil {
		t.Fatal("attempt worker changed")
	}
}

func TestAttemptBindsCandidateSubmitterToWorker(t *testing.T) {
	attempt := attemptFixture("review")
	attempt.Candidate.SubmittedBy = "worker-b"
	if attempt.Validate() == nil {
		t.Fatal("foreign candidate submitter accepted")
	}
}
