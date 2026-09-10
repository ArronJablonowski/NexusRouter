package workboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLifecycleHistoryPageContracts(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	items := []AttemptHistoryRecord{
		{Version: 1, ID: "attempt-two", BoardID: "board", CardID: "card", Ordinal: 2, Revision: 1, State: "running", WorkerID: "worker", CriteriaRevision: 1, StartedAt: now},
		{Version: 1, ID: "attempt-one", BoardID: "board", CardID: "card", Ordinal: 1, Revision: 2, State: "failed", WorkerID: "worker", CriteriaRevision: 1, StartedAt: now, EndedAt: timePointer(now.Add(time.Minute))},
	}
	page := AttemptHistoryPage{Version: 1, BoardID: "board", CardID: "card", HighWaterOrdinal: 2, Items: items}
	if page.Validate() != nil {
		t.Fatal("valid history rejected")
	}
	page.Items[0], page.Items[1] = page.Items[1], page.Items[0]
	if page.Validate() == nil {
		t.Fatal("ascending attempt page accepted")
	}
	page = AttemptHistoryPage{Version: 1, BoardID: "board", CardID: "card", HighWaterOrdinal: 2, Items: items[:1], HasMore: true, NextCursor: "cursor"}
	if page.Validate() != nil {
		t.Fatal("bounded partial page rejected")
	}
	page.NextCursor = ""
	if page.Validate() == nil {
		t.Fatal("missing continuation cursor accepted")
	}
}

func TestAttemptDetailPageRequiresDescendingFrozenCheckpointPrefix(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	criteria := []AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Tests pass.", Required: true}}
	body, _ := json.Marshal(criteria)
	sum := sha256.Sum256(body)
	criteriaDigest := hex.EncodeToString(sum[:])
	claim := &ClaimSnapshot{ID: "claim", BoardID: "board", CardID: "card", AttemptID: "attempt", Revision: 1, State: "active", OwnerID: "worker", OwnerType: "worker", ExpiresAt: now.Add(time.Minute), LastHeartbeat: now}
	attempt := AttemptSnapshot{ID: "attempt", BoardID: "board", CardID: "card", Ordinal: 1, Revision: 1, State: "running", WorkerID: "worker",
		CriteriaRevision: 1, CriteriaDigest: criteriaDigest, PolicyDigest: strings.Repeat("a", 64), Budget: WorkBudget{AttemptLimit: 1}, Criteria: criteria,
		TaskIDs: []string{}, SessionIDs: []string{}, Claim: claim, Evidence: []EvidenceRecord{}, StartedAt: now}
	checkpoint := func(revision int64) CheckpointRecord {
		evidence := "checkpoint " + string(rune('a'+revision))
		return CheckpointRecord{Version: 1, ID: "checkpoint-" + string(rune('a'+revision)), BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim",
			Revision: revision, ClaimRevision: 1, CriteriaRevision: 1, CriteriaDigest: criteriaDigest, PolicyDigest: strings.Repeat("a", 64), Evidence: evidence,
			EvidenceDigest: digestText(evidence), ActorID: "worker", ActorType: "worker", CreatedAt: now}
	}
	page := AttemptDetailPage{Version: 1, Attempt: attempt, CheckpointHighWaterRevision: 2, Checkpoints: []CheckpointRecord{checkpoint(2)}, HasMore: true, NextCursor: "cursor"}
	if page.Validate() != nil {
		t.Fatal("valid detail page rejected")
	}
	page.Checkpoints = []CheckpointRecord{checkpoint(1), checkpoint(2)}
	page.HasMore, page.NextCursor = false, ""
	if page.Validate() == nil {
		t.Fatal("ascending checkpoint page accepted")
	}
}

func timePointer(value time.Time) *time.Time { return &value }
