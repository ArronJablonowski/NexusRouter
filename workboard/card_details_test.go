package workboard

import (
	"strings"
	"testing"
	"time"
)

func validDetailedCard() Card {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	return Card{ID: "card-a", BoardID: "board-a", Revision: 1, CriteriaRevision: 1, State: Backlog, Rank: "rank-a",
		Title: "Implement", Priority: "normal", Labels: []string{}, Dependencies: []string{}, Budget: WorkBudget{AttemptLimit: 3},
		Criteria: []AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
			ValidatorID: "go-test", Description: "Tests pass.", Required: true}}, CreatedAt: now, UpdatedAt: now}
}

func TestDetailedCardValidatesBudgetCriteriaAndLifecycle(t *testing.T) {
	card := validDetailedCard()
	if err := card.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Card){
		"missing criteria":   func(card *Card) { card.Criteria = nil },
		"invalid budget":     func(card *Card) { card.Budget.AttemptLimit = 0 },
		"wrong evidence":     func(card *Card) { card.Criteria[0].RequiredSource = "user_feedback" },
		"duplicate criteria": func(card *Card) { card.Criteria = append(card.Criteria, card.Criteria[0]) },
		"ready blocked": func(card *Card) {
			card.State, card.RemainingDependencies, card.Dependencies = Ready, 1, []string{"dependency"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := copyCard(card)
			mutate(&changed)
			if changed.Validate() == nil {
				t.Fatal("invalid detailed card accepted")
			}
		})
	}
}

func TestCardMutationResultBindsCommittedCardRevision(t *testing.T) {
	card := validDetailedCard()
	revision := card.Revision
	receipt := OperationReceipt{Version: 1, BoardID: card.BoardID, OperationID: "operation-key-01",
		RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1,
		EventCount: 1, TransactionBytes: 128, BoardRevision: 2, CardID: card.ID, CardRevision: &revision,
		Outcome: "committed", CreatedAt: card.UpdatedAt}
	result := CardMutationResult{Card: card, Receipt: receipt}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	wrong := int64(2)
	result.Receipt.CardRevision = &wrong
	if result.Validate() == nil {
		t.Fatal("mismatched committed card revision accepted")
	}
}
