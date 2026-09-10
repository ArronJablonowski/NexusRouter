package webui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func workboardTime() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) }

func criterion(id, kind string) AcceptanceCriterion {
	source := "deterministic"
	validator := "validator-ci"
	if kind == "subjective" {
		source, validator = "user_feedback", "operator-review"
	}
	return AcceptanceCriterion{Version: 1, ID: id, Kind: kind, RequiredSource: source, ValidatorID: validator, Description: "The result satisfies the requirement.", Required: true}
}

func boardFixture() Board {
	now := workboardTime()
	return Board{Version: 1, ID: "board-a", Revision: 1, LayoutRevision: 1, EventSequence: 1, State: "active", Title: "DarwinRouter", Description: "Work", CardCount: 1, ActiveClaims: 1, CreatedAt: now, UpdatedAt: now}
}

func columnFixtures(boardID string) []Column {
	states := []string{"backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"}
	titles := []string{"Backlog", "Ready", "In progress", "Blocked", "Review", "Done", "Canceled"}
	columns := make([]Column, len(states))
	for index := range states {
		columns[index] = Column{Version: 1, ID: states[index], BoardID: boardID, State: states[index], Title: titles[index], Rank: string(rune('a' + index))}
	}
	return columns
}

func cardFixture() Card {
	now := workboardTime()
	return Card{Version: 1, ID: "card-a", BoardID: "board-a", Revision: 2, CriteriaRevision: 1, State: "in_progress", Rank: "a0", Title: "Build storage", Description: "Implement the durable store.", Priority: "high", Labels: []string{"storage"}, Dependencies: []string{}, AssigneeID: "worker-a", AttemptCount: 1, CurrentAttemptID: "attempt-a", CurrentClaimID: "claim-a", Budget: WorkBudget{AttemptLimit: 3, TimeLimitMS: 86_400_000, TokenLimit: 100_000, CostMicros: 5_000_000}, Criteria: []AcceptanceCriterion{criterion("tests", "objective")}, CreatedAt: now, UpdatedAt: now}
}

func claimFixture(state string) Claim {
	now := workboardTime()
	claim := Claim{Version: 1, ID: "claim-a", BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", Revision: 2, State: state, OwnerID: "worker-a", OwnerType: "worker", TaskID: "task-a", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute)}
	if state == "released" {
		released := now.Add(time.Second)
		claim.ReleasedAt = &released
	}
	return claim
}

func evidenceFixture(candidate Candidate, source string) EvidenceRecord {
	actorType := "validator"
	actor := "validator-ci"
	if source == "user_feedback" {
		actorType, actor = "operator", "operator-a"
	} else if source == "model_audit" {
		actorType, actor = "model", "review-model"
	}
	return EvidenceRecord{Version: 1, ID: "evidence-" + source, Revision: 1, BoardID: candidate.BoardID, CardID: candidate.CardID, AttemptID: candidate.AttemptID, CandidateID: candidate.ID, CriterionID: "tests", Source: source, Outcome: "passed", ActorID: actor, ActorType: actorType, Reference: "result-reference", CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, PolicyDigest: candidate.PolicyDigest, CreatedAt: workboardTime()}
}

func attemptFixture(state string) Attempt {
	criteria := []AcceptanceCriterion{criterion("tests", "objective")}
	criteriaDigest := AcceptanceCriteriaDigest(criteria)
	policyDigest := strings.Repeat("b", 64)
	candidate := Candidate{Version: 1, ID: "candidate-a", BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", Revision: 1, Digest: strings.Repeat("a", 64), CriteriaDigest: criteriaDigest, PolicyDigest: policyDigest, Summary: "All focused tests passed.", ArtifactRefs: []string{"artifact-a"}, SubmittedBy: "worker-a", CreatedAt: workboardTime()}
	evidence := []EvidenceRecord{evidenceFixture(candidate, "deterministic")}
	candidate.EvidenceDigest, candidate.EvidenceCount = EvidenceDigest(evidence), len(evidence)
	a := Attempt{Version: 1, ID: "attempt-a", BoardID: "board-a", CardID: "card-a", Ordinal: 1, Revision: 2, State: state, WorkerID: "worker-a", CriteriaRevision: 1, CriteriaDigest: criteriaDigest, PolicyDigest: policyDigest, Budget: WorkBudget{AttemptLimit: 3}, Criteria: criteria, TaskIDs: []string{"task-a"}, SessionIDs: []string{"session-a"}, Claim: ptrClaim(claimFixture("released")), Candidate: &candidate, Evidence: evidence, StartedAt: workboardTime()}
	ended := workboardTime().Add(time.Minute)
	a.EndedAt = &ended
	if state == "running" {
		a.Claim = ptrClaim(claimFixture("active"))
		a.Candidate, a.Evidence, a.EndedAt = nil, nil, nil
	}
	if state == "accepted" || state == "rejected" {
		a.AcceptanceID, a.DecisionBy = "acceptance-a", "operator-a"
		a.DecisionByType, a.DecisionAuthorityID = "operator", "browser-session-policy"
		a.AcceptanceEvidenceDigest = EvidenceDigest(a.Evidence)
	}
	return a
}

func ptrClaim(claim Claim) *Claim { return &claim }

func TestWorkboardProjectionFixturesValidate(t *testing.T) {
	board := boardFixture()
	card := cardFixture()
	if board.Validate() != nil || card.Validate() != nil {
		t.Fatal("valid board or card rejected")
	}
	for _, state := range []string{"running", "review", "accepted", "rejected"} {
		if err := attemptFixture(state).Validate(); err != nil {
			t.Fatalf("valid %s attempt rejected: %v", state, err)
		}
	}
	snapshot := BoardSnapshot{Version: 1, Board: board, Columns: columnFixtures(board.ID), Cards: []Card{card}, GraphRevision: 1, GraphDigest: strings.Repeat("c", 64)}
	if err := snapshot.Validate(); err != nil {
		t.Fatal("valid snapshot rejected", err)
	}
	page := Page{Version: 1, Items: []Board{board}}
	if err := page.Validate(); err != nil {
		t.Fatal("valid page rejected", err)
	}
	receipt := OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "operation-key-01", RequestDigest: strings.Repeat("d", 64), ResponseDigest: strings.Repeat("e", 64), FirstSequence: 2, LastSequence: 3, EventCount: 2, TransactionBytes: 2048, BoardRevision: 2, CardID: "card-a", CardRevision: int64ptr(3), Outcome: "committed", CreatedAt: workboardTime()}
	if err := receipt.Validate(); err != nil {
		t.Fatal("valid receipt rejected", err)
	}
}

func TestAttemptRejectsBindingAndSelfAcceptance(t *testing.T) {
	a := attemptFixture("accepted")
	a.Candidate.PolicyDigest = strings.Repeat("f", 64)
	if a.Validate() == nil {
		t.Fatal("candidate policy drift accepted")
	}
	a = attemptFixture("accepted")
	a.Evidence[0].CandidateDigest = strings.Repeat("f", 64)
	if a.Validate() == nil {
		t.Fatal("evidence candidate drift accepted")
	}
	a = attemptFixture("accepted")
	a.DecisionBy = a.WorkerID
	if a.Validate() == nil {
		t.Fatal("worker self-acceptance accepted")
	}
	a = attemptFixture("accepted")
	a.Claim.OwnerID = "different-claimant"
	a.DecisionBy = "different-claimant"
	if a.Validate() == nil {
		t.Fatal("claimant identity mismatch or self-acceptance accepted")
	}
}

func TestAcceptanceAndRecoveryReceiptsBindAuthority(t *testing.T) {
	attempt := attemptFixture("accepted")
	record := acceptanceFixture(attempt)
	if err := record.ValidateAgainst(attempt); err != nil {
		t.Fatal("valid acceptance decision rejected", err)
	}
	record.DecidedBy = attempt.WorkerID
	if record.ValidateAgainst(attempt) == nil {
		t.Fatal("worker self-decision accepted")
	}
	recovery := recoveryFixture()
	if err := recovery.Validate(); err != nil {
		t.Fatal("valid recovery receipt rejected", err)
	}
	recovery.EffectResolution = "uncertain"
	if recovery.Validate() == nil {
		t.Fatal("uncertain recovery accepted")
	}
}

func acceptanceFixture(attempt Attempt) AcceptanceDecisionRecord {
	return AcceptanceDecisionRecord{
		Version: 1, ID: "acceptance-a", BoardID: attempt.BoardID, CardID: attempt.CardID,
		AttemptID: attempt.ID, CandidateID: attempt.Candidate.ID, CandidateDigest: attempt.Candidate.Digest,
		CriteriaRevision: attempt.CriteriaRevision, CriteriaDigest: attempt.CriteriaDigest,
		PriorEvidenceHeadRevision: attempt.Evidence[len(attempt.Evidence)-1].Revision,
		PriorEvidenceSetDigest:    EvidenceDigest(attempt.Evidence),
		EvidenceHeadRevision:      attempt.Evidence[len(attempt.Evidence)-1].Revision,
		EvidenceSetDigest:         EvidenceDigest(attempt.Evidence), PolicyDigest: attempt.PolicyDigest,
		Decision: "accepted", DecidedBy: "operator-a", DecidedByType: "operator",
		DecisionAuthorityID: "browser-session-policy", Rationale: "Operator accepted the evidence.", DecidedAt: *attempt.EndedAt,
	}
}

func recoveryFixture() RecoveryReceipt {
	return RecoveryReceipt{Version: 1, ID: "recovery-a", BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", OldClaimID: "claim-a", OldClaimRevision: 2, CardRevision: 3, StopProofID: "process-death-proof", TaskHeadDigest: strings.Repeat("1", 64), ProcessProofDigest: strings.Repeat("2", 64), EffectEvidenceDigest: strings.Repeat("3", 64), EffectResolution: "effect_free", ResultingState: "ready", FirstSequence: 4, LastSequence: 5, RecoveredAt: workboardTime()}
}

func TestEvidenceSourceAuthorityAndAbstention(t *testing.T) {
	a := attemptFixture("review")
	e := a.Evidence[0]
	e.Source, e.ActorType = "user_feedback", "model"
	if e.Validate() == nil {
		t.Fatal("model-authored user feedback accepted")
	}
	e = a.Evidence[0]
	e.Source, e.ActorType, e.Outcome = "model_audit", "model", "abstained"
	if e.Validate() != nil {
		t.Fatal("model audit abstention rejected")
	}
	e.Source, e.ActorType = "user_feedback", "operator"
	if e.Validate() == nil {
		t.Fatal("user abstention accepted")
	}
}

func TestCardLifecycleProjectionInvariants(t *testing.T) {
	c := cardFixture()
	c.State, c.CurrentClaimID = "review", ""
	if c.Validate() != nil {
		t.Fatal("review card rejected")
	}
	c.State, c.AcceptanceID = "done", "acceptance-a"
	if c.Validate() != nil {
		t.Fatal("accepted card rejected")
	}
	c.CurrentClaimID = "claim-a"
	if c.Validate() == nil {
		t.Fatal("done card retained active claim")
	}
	c = cardFixture()
	c.RemainingDependencies, c.State = 1, "ready"
	if c.Validate() == nil {
		t.Fatal("dependency-blocked card reported ready")
	}
	c = cardFixture()
	c.State, c.BlockReason = "blocked", "provider-pressure"
	if c.Validate() != nil {
		t.Fatal("blocked card lost its live ownership fence")
	}
}

func TestPublicClaimContainsNoCapability(t *testing.T) {
	claim := claimFixture("active")
	body, err := jsonMarshal(claim)
	if err != nil || claim.Validate() != nil {
		t.Fatal("valid claim rejected", err)
	}
	for _, forbidden := range []string{"token", "capability", "process_id", "process_ref"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("private claim material exposed", forbidden)
		}
	}
}

func TestWorkboardBoundsAreFiniteAndConsistent(t *testing.T) {
	if MaxCardsPerBoard > MaxGraphVisits || MaxReverseFanout+2 > MaxTransactionEvents ||
		MaxDependencyDepth > MaxGraphVisits || MaxTransactionBytes > MaxRequestBytes {
		t.Fatal("unsafe workboard hard-limit relationship")
	}
	receipt := OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "operation-key-01", RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: int64(MaxTransactionEvents + 1), EventCount: MaxTransactionEvents + 1, TransactionBytes: 1, BoardRevision: 1, Outcome: "committed", CreatedAt: workboardTime()}
	if receipt.Validate() == nil {
		t.Fatal("oversized transaction event count accepted")
	}
	receipt.LastSequence, receipt.EventCount = 2, 1
	if receipt.Validate() == nil {
		t.Fatal("receipt with inconsistent event range accepted")
	}
	receipt.LastSequence, receipt.EventCount = 1, 1
	receipt.ClaimRevision = int64ptr(1)
	if receipt.Validate() == nil {
		t.Fatal("claim revision without card identity and revision accepted")
	}
	receipt.ClaimRevision = nil
	receipt.Outcome = "pending"
	if receipt.Validate() == nil {
		t.Fatal("non-committed idempotency receipt accepted")
	}
	recovery := recoveryFixture()
	recovery.LastSequence = recovery.FirstSequence - 1
	if recovery.Validate() == nil {
		t.Fatal("descending recovery event range accepted")
	}
	recovery = recoveryFixture()
	recovery.LastSequence = recovery.FirstSequence + MaxTransactionEvents
	if recovery.Validate() == nil {
		t.Fatal("oversized recovery event range accepted")
	}
}

func TestPublishedReceiptSchemaDeclaresCrossFieldInvariants(t *testing.T) {
	body, err := os.ReadFile("schema/workboard-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Definitions map[string]struct {
			Invariants []string `json:"x-invariants"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"operation_receipt": {"event_count == last_sequence - first_sequence + 1"},
		"recovery_receipt":  {"last_sequence >= first_sequence", "last_sequence - first_sequence + 1 <= 128"},
		"column":            {"id == state"},
		"claim":             {"last_heartbeat < expires_at <= last_heartbeat + 10 minutes for active or attention claims", "released_at >= last_heartbeat for released claims"},
		"attempt":           {"candidate.submitted_by == worker_id", "claim owner, board, card, and attempt identities match the attempt", "candidate and evidence criteria, policy, candidate, board, card, and attempt bindings match the attempt"},
		"snapshot":          {"included card ids are unique", "visible parent and dependency cards belong to the snapshot board", "included parent and dependency edges are acyclic with depth <= 64", "remaining_dependencies >= included non-done dependencies; absent references may be on another page"},
	}
	for definition, invariants := range want {
		got := document.Definitions[definition].Invariants
		if strings.Join(got, "\n") != strings.Join(invariants, "\n") {
			t.Fatalf("%s invariants = %q, want %q", definition, got, invariants)
		}
	}
}

func TestSharedInvalidReceiptFixturesRequireNormativeInvariantChecks(t *testing.T) {
	body, err := os.ReadFile("testdata/v1/workboard-errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Definition string          `json:"definition"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no invalid receipt fixtures published")
	}
	for _, fixture := range fixtures {
		if ValidateReceiptJSON(fixture.Definition, fixture.Payload) == nil {
			t.Fatalf("%s invariant fixture accepted", fixture.Definition)
		}
	}
}

func TestSnapshotRejectsDuplicateAndOversizedCards(t *testing.T) {
	board := boardFixture()
	card := cardFixture()
	snapshot := BoardSnapshot{Version: 1, Board: board, Columns: columnFixtures(board.ID), Cards: []Card{card, card}, GraphRevision: 1, GraphDigest: strings.Repeat("c", 64)}
	if snapshot.Validate() == nil {
		t.Fatal("duplicate card accepted")
	}
	card.Description = strings.Repeat("x", MaxDescriptionBytes+1)
	if card.Validate() == nil {
		t.Fatal("oversized card accepted")
	}
}

func TestPublishedWorkboardSchemaAcceptsProjectionFixtures(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, item := range []struct{ location, path string }{
		{"https://darwinrouter.local/schema/webui/v1", "schema/v1.schema.json"},
		{"https://darwinrouter.local/schema/webui/workboard-v1", "schema/workboard-v1.schema.json"},
	} {
		body, err := os.ReadFile(item.path)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(body, &document); err != nil || compiler.AddResource(item.location, document) != nil {
			t.Fatal("invalid schema resource", item.path, err)
		}
	}
	board := boardFixture()
	card := cardFixture()
	attempt := attemptFixture("accepted")
	lifecycle := CardLifecycle{Version: 1, CardID: card.ID, Attempt: attempt, Checkpoints: []WorkCheckpoint{},
		CheckpointCount: 0, Acceptance: ptrAcceptance(acceptanceFixture(attempt))}
	history := AttemptHistoryPage{Version: 1, BoardID: board.ID, CardID: card.ID, HighWaterOrdinal: 1,
		Items: []AttemptHistoryRecord{{Version: 1, ID: attempt.ID, BoardID: board.ID, CardID: card.ID, Ordinal: 1,
			Revision: attempt.Revision, State: attempt.State, WorkerID: attempt.WorkerID, CriteriaRevision: attempt.CriteriaRevision,
			CandidateID: attempt.Candidate.ID, AcceptanceID: attempt.AcceptanceID, StartedAt: attempt.StartedAt, EndedAt: attempt.EndedAt}}}
	detail := AttemptDetailPage{Version: 1, Attempt: attempt, Checkpoints: []WorkCheckpoint{}}
	receipt := OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "operation-key-01", RequestDigest: strings.Repeat("d", 64), ResponseDigest: strings.Repeat("e", 64), FirstSequence: 2, LastSequence: 3, EventCount: 2, TransactionBytes: 2048, BoardRevision: 2, CardID: "card-a", CardRevision: int64ptr(3), Outcome: "committed", CreatedAt: workboardTime()}
	values := []struct {
		definition string
		value      any
	}{
		{"board", board},
		{"column", columnFixtures(board.ID)[0]},
		{"card", card},
		{"attempt", attempt},
		{"card_lifecycle", lifecycle},
		{"attempt_history_page", history},
		{"attempt_detail_page", detail},
		{"snapshot", BoardSnapshot{Version: 1, Board: board, Columns: columnFixtures(board.ID), Cards: []Card{card}, GraphRevision: 1, GraphDigest: strings.Repeat("c", 64)}},
		{"page", Page{Version: 1, Items: []Board{board}}},
		{"operation_receipt", receipt},
		{"acceptance_decision", acceptanceFixture(attempt)},
		{"recovery_receipt", recoveryFixture()},
	}
	for _, item := range values {
		body, err := json.Marshal(item.value)
		if err != nil {
			t.Fatal(err)
		}
		validateSchemaValue(t, compiler, "https://darwinrouter.local/schema/webui/workboard-v1#/$defs/"+item.definition, body, true)
	}
	negative := []struct{ definition, body string }{
		{"board", `{"version":1,"id":"board-a","revision":1,"layout_revision":1,"event_sequence":1,"state":"archived","title":"Board","description":"","card_count":1,"active_claims":1,"created_at":"2026-09-09T12:00:00Z","updated_at":"2026-09-09T12:00:00Z"}`},
		{"claim", `{"version":1,"id":"claim-a","board_id":"board-a","card_id":"card-a","attempt_id":"attempt-a","revision":1,"state":"active","owner_id":"operator-a","owner_type":"operator","expires_at":"2026-09-09T12:01:00Z","last_heartbeat":"2026-09-09T12:00:00Z"}`},
		{"claim", `{"version":1,"id":"claim-a","board_id":"board-a","card_id":"card-a","attempt_id":"attempt-a","revision":1,"state":"released","owner_id":"worker-a","owner_type":"worker","expires_at":"2026-09-09T12:01:00Z","last_heartbeat":"2026-09-09T12:00:00Z"}`},
		{"evidence", `{"version":1,"id":"evidence-a","revision":1,"board_id":"board-a","card_id":"card-a","attempt_id":"attempt-a","candidate_id":"candidate-a","criterion_id":"tests","source":"user_feedback","outcome":"abstained","actor_id":"model-a","actor_type":"model","reference":"ref","candidate_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","criteria_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","policy_digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","created_at":"2026-09-09T12:00:00Z"}`},
		{"snapshot", `{"version":1,"board":{},"cards":[],"has_more":true,"graph_revision":1,"graph_digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`},
		{"attempt", `{"version":1,"id":"attempt-a","board_id":"board-a","card_id":"card-a","ordinal":1,"revision":1,"state":"accepted","worker_id":"worker-a","criteria_revision":1,"criteria_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","policy_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","budget":{},"criteria":[],"task_ids":[],"session_ids":[],"evidence":[],"started_at":"2026-09-09T12:00:00Z"}`},
	}
	for _, item := range negative {
		validateSchemaValue(t, compiler, "https://darwinrouter.local/schema/webui/workboard-v1#/$defs/"+item.definition, json.RawMessage(item.body), false)
	}
}

func ptrAcceptance(value AcceptanceDecisionRecord) *AcceptanceDecisionRecord { return &value }

func int64ptr(value int64) *int64 { return &value }

// Keep this indirection local so the capability-leak assertion reads the
// actual public JSON without adding a second encoding policy to production.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
