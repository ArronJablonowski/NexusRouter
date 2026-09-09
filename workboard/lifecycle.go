package workboard

// Transition contains only facts already established by the application
// service. The store must still apply the returned revisions atomically.
type Transition struct {
	BoardID               string
	CardID                string
	Command               Command
	From                  State
	To                    State
	CurrentCardRevision   int64
	ExpectedCardRevision  int64
	CurrentGraphRevision  int64
	ExpectedGraphRevision int64
	DependenciesSatisfied bool
	HasLiveClaim          bool
	SameClaim             bool
	HasCandidate          bool
	IndependentAcceptor   bool
	RecoverySafe          bool
}

type TransitionResult struct {
	State         State
	CardRevision  int64
	GraphRevision int64
}

func ValidateTransition(t Transition) (TransitionResult, error) {
	if !validID(t.BoardID) || !validID(t.CardID) || !validState(t.From) || !validState(t.To) ||
		t.CurrentCardRevision < 1 || t.CurrentGraphRevision < 1 {
		return TransitionResult{}, fail(CodeInvalid, "transition")
	}
	if t.ExpectedCardRevision != t.CurrentCardRevision {
		return TransitionResult{}, fail(CodeStaleRevision, "card_revision")
	}
	if t.ExpectedGraphRevision != t.CurrentGraphRevision {
		return TransitionResult{}, fail(CodeStaleRevision, "graph_revision")
	}
	if t.From == Done || t.From == Canceled {
		return TransitionResult{}, fail(CodeIllegalTransition, "terminal_state")
	}
	allowed := false
	switch t.Command {
	case Move:
		allowed = t.From == Backlog && t.To == Ready && t.DependenciesSatisfied ||
			t.From == Ready && t.To == Backlog && !t.HasLiveClaim
	case ClaimCard:
		allowed = t.From == Ready && t.To == InProgress && t.DependenciesSatisfied && !t.HasLiveClaim
	case BlockCard:
		allowed = t.From == InProgress && t.To == Blocked && t.HasLiveClaim && t.SameClaim
	case UnblockCard:
		allowed = t.From == Blocked && t.To == InProgress && t.HasLiveClaim && t.SameClaim
	case RecoverClaim:
		allowed = (t.From == InProgress || t.From == Blocked) && t.To == Ready && t.DependenciesSatisfied &&
			t.HasLiveClaim && t.SameClaim && t.RecoverySafe
	case SubmitCandidate:
		allowed = t.From == InProgress && t.To == Review && t.HasLiveClaim && t.SameClaim && t.HasCandidate
	case AcceptCandidate:
		allowed = t.From == Review && t.To == Done && t.HasCandidate && t.IndependentAcceptor && t.DependenciesSatisfied
	case RejectCandidate:
		allowed = t.From == Review && t.To == Ready && t.DependenciesSatisfied && t.HasCandidate && t.IndependentAcceptor
	case FinalizeCancel:
		allowed = t.To == Canceled && t.RecoverySafe
	}
	if !allowed {
		return TransitionResult{}, fail(CodeIllegalTransition, "state")
	}
	return TransitionResult{State: t.To, CardRevision: t.CurrentCardRevision + 1, GraphRevision: t.CurrentGraphRevision}, nil
}
