package workboard

import (
	"errors"
	"testing"
)

func transition(from, to State, command Command) Transition {
	value := Transition{
		BoardID: "board-a", CardID: "card-a", Command: command, From: from, To: to,
		CurrentCardRevision: 3, ExpectedCardRevision: 3,
		CurrentGraphRevision: 7, ExpectedGraphRevision: 7,
		DependenciesSatisfied: true, SameClaim: true, HasCandidate: true,
		IndependentAcceptor: true, RecoverySafe: true,
	}
	value.HasLiveClaim = from == InProgress || from == Blocked
	return value
}

func TestAllowedLifecycleTransitions(t *testing.T) {
	tests := []struct {
		name string
		in   Transition
	}{
		{"backlog ready", transition(Backlog, Ready, Move)},
		{"ready backlog", transition(Ready, Backlog, Move)},
		{"claim", transition(Ready, InProgress, ClaimCard)},
		{"block", transition(InProgress, Blocked, BlockCard)},
		{"unblock", transition(Blocked, InProgress, UnblockCard)},
		{"recover running", transition(InProgress, Ready, RecoverClaim)},
		{"recover blocked", transition(Blocked, Ready, RecoverClaim)},
		{"submit", transition(InProgress, Review, SubmitCandidate)},
		{"accept", transition(Review, Done, AcceptCandidate)},
		{"reject", transition(Review, Ready, RejectCandidate)},
	}
	for _, from := range []State{Backlog, Ready, InProgress, Blocked, Review} {
		tests = append(tests, struct {
			name string
			in   Transition
		}{"cancel " + string(from), transition(from, Canceled, FinalizeCancel)})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ValidateTransition(test.in)
			if err != nil || result.State != test.in.To || result.CardRevision != 4 || result.GraphRevision != 7 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestForbiddenLifecycleTransitionsAndFences(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Transition)
		code ErrorCode
	}{
		{"generic execution move", func(v *Transition) { v.Command, v.From, v.To = Move, Ready, InProgress }, CodeIllegalTransition},
		{"terminal done", func(v *Transition) { v.From, v.To, v.Command = Done, Ready, RejectCandidate }, CodeIllegalTransition},
		{"terminal canceled", func(v *Transition) { v.From, v.To, v.Command = Canceled, Ready, RecoverClaim }, CodeIllegalTransition},
		{"unsatisfied dependencies", func(v *Transition) { v.DependenciesSatisfied = false }, CodeIllegalTransition},
		{"active claim on claim", func(v *Transition) { v.HasLiveClaim = true }, CodeIllegalTransition},
		{"stale card", func(v *Transition) { v.ExpectedCardRevision-- }, CodeStaleRevision},
		{"stale graph", func(v *Transition) { v.ExpectedGraphRevision-- }, CodeStaleRevision},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := transition(Ready, InProgress, ClaimCard)
			test.edit(&in)
			_, err := ValidateTransition(in)
			if !errors.Is(err, &Violation{Code: test.code}) {
				t.Fatalf("error=%v, want %s", err, test.code)
			}
		})
	}

	for _, test := range []struct {
		name string
		in   Transition
		edit func(*Transition)
	}{
		{"move live claim", transition(Ready, Backlog, Move), func(v *Transition) { v.HasLiveClaim = true }},
		{"block wrong claim", transition(InProgress, Blocked, BlockCard), func(v *Transition) { v.SameClaim = false }},
		{"submit no candidate", transition(InProgress, Review, SubmitCandidate), func(v *Transition) { v.HasCandidate = false }},
		{"accept claimant", transition(Review, Done, AcceptCandidate), func(v *Transition) { v.IndependentAcceptor = false }},
		{"unsafe recovery", transition(Blocked, Ready, RecoverClaim), func(v *Transition) { v.RecoverySafe = false }},
		{"recovery with dependencies", transition(Blocked, Ready, RecoverClaim), func(v *Transition) { v.DependenciesSatisfied = false }},
		{"recovery wrong claim", transition(Blocked, Ready, RecoverClaim), func(v *Transition) { v.SameClaim = false }},
		{"rejection with dependencies", transition(Review, Ready, RejectCandidate), func(v *Transition) { v.DependenciesSatisfied = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.edit(&test.in)
			if _, err := ValidateTransition(test.in); !errors.Is(err, &Violation{Code: CodeIllegalTransition}) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
