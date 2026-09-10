package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// WorkboardBridge presents one authority-gated domain service through the
// native API and browser BFF contracts. Identity is supplied by the trusted
// transport adapter and never decoded from a BoardRequest.
type WorkboardBridge struct {
	boards *workboard.BoardService
	cards  *workboard.CardService
}

type workboardAuthorityKey struct{}

type contextWorkboardAuthority struct{}

func (contextWorkboardAuthority) WorkboardAuthority(ctx context.Context) (workboard.Authority, error) {
	if ctx == nil {
		return workboard.Authority{}, context.Canceled
	}
	authority, ok := ctx.Value(workboardAuthorityKey{}).(workboard.Authority)
	if !ok || authority.Validate() != nil {
		return workboard.Authority{}, errors.New("workboard authority unavailable")
	}
	return authority, nil
}

func NewWorkboardBridge(repository workboard.BoardRepository, cards workboard.CardStore, now func() time.Time) (*WorkboardBridge, error) {
	boards, err := workboard.NewBoardService(repository, contextWorkboardAuthority{}, now)
	if err != nil {
		return nil, err
	}
	cardService, err := workboard.NewCardService(cards, contextWorkboardAuthority{})
	if err != nil {
		return nil, err
	}
	return &WorkboardBridge{boards: boards, cards: cardService}, nil
}

func nativeWorkboardContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, workboardAuthorityKey{}, workboard.Authority{
		CreationScope: "native-api",
		Actor:         workboard.Actor{ID: "api_operator", Type: "operator"},
	})
}

func browserWorkboardContext(ctx context.Context, subject string) (context.Context, error) {
	authority := workboard.Authority{CreationScope: subject, Actor: workboard.Actor{ID: subject, Type: "operator"}}
	if ctx == nil || !validBrowserSubject(subject) || authority.Validate() != nil {
		return nil, errors.New("workboard authority unavailable")
	}
	return context.WithValue(ctx, workboardAuthorityKey{}, authority), nil
}

func (b *WorkboardBridge) NativeList(ctx context.Context, options webui.BoardListOptions) (webui.Page, error) {
	return b.list(nativeWorkboardContext(ctx), options)
}

func (b *WorkboardBridge) BrowserList(ctx context.Context, subject string, options webui.BoardListOptions) (webui.Page, error) {
	trusted, err := browserWorkboardContext(ctx, subject)
	if err != nil {
		return webui.Page{}, err
	}
	return b.list(trusted, options)
}

func (b *WorkboardBridge) NativeRead(ctx context.Context, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, error) {
	return b.read(nativeWorkboardContext(ctx), boardID, options)
}

func (b *WorkboardBridge) BrowserRead(ctx context.Context, subject, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, error) {
	trusted, err := browserWorkboardContext(ctx, subject)
	if err != nil {
		return webui.BoardSnapshot{}, err
	}
	return b.read(trusted, boardID, options)
}

func (b *WorkboardBridge) read(ctx context.Context, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, error) {
	snapshot, err := b.boards.Read(ctx, boardID, workboard.BoardSnapshotOptions{After: options.After, Limit: options.Limit,
		State: workboard.State(options.State), AssigneeID: options.AssigneeID, OwnerID: options.OwnerID, ClaimState: options.ClaimState})
	if err != nil {
		return webui.BoardSnapshot{}, err
	}
	result := webui.BoardSnapshot{Version: webui.ContractVersion, Board: workboardBoard(snapshot.Board), NextCursor: snapshot.NextCursor,
		HasMore: snapshot.HasMore, GraphRevision: snapshot.GraphRevision, GraphDigest: snapshot.GraphDigest,
		Columns: make([]webui.Column, len(snapshot.Columns)), Cards: make([]webui.Card, len(snapshot.Cards))}
	for index, column := range snapshot.Columns {
		result.Columns[index] = webui.Column{Version: webui.ContractVersion, ID: column.ID, BoardID: column.BoardID,
			State: string(column.State), Title: column.Title, Rank: column.Rank}
	}
	for index, card := range snapshot.Cards {
		result.Cards[index] = workboardCard(card)
	}
	if result.Validate() != nil {
		return webui.BoardSnapshot{}, errors.New("invalid workboard snapshot projection")
	}
	return result, nil
}

func (b *WorkboardBridge) NativeMutate(ctx context.Context, request webui.BoardRequest) (webui.OperationReceipt, error) {
	return b.mutate(nativeWorkboardContext(ctx), request)
}

func (b *WorkboardBridge) BrowserMutate(ctx context.Context, subject string, request webui.BoardRequest) (webui.OperationReceipt, error) {
	trusted, err := browserWorkboardContext(ctx, subject)
	if err != nil {
		return webui.OperationReceipt{}, err
	}
	return b.mutate(trusted, request)
}

func (b *WorkboardBridge) mutate(ctx context.Context, request webui.BoardRequest) (webui.OperationReceipt, error) {
	if request.Validate() != nil {
		return webui.OperationReceipt{}, errors.New("invalid workboard request")
	}
	var receipt workboard.OperationReceipt
	var err error
	switch request.Action {
	case webui.BoardCreate:
		receipt, err = b.boards.Create(ctx, workboard.CreateBoardRequest{Version: workboard.SchemaVersion, IdempotencyKey: request.IdempotencyKey,
			Title: *request.Title, Description: optionalText(request.Description)})
	case webui.BoardRevise:
		receipt, err = b.boards.Revise(ctx, workboard.ReviseBoardRequest{Version: workboard.SchemaVersion, BoardID: request.BoardID,
			IdempotencyKey: request.IdempotencyKey, ExpectedRevision: *request.ExpectedBoardRevision, Title: request.Title, Description: request.Description})
	case webui.BoardArchive:
		receipt, err = b.boards.Archive(ctx, workboard.ArchiveBoardRequest{Version: workboard.SchemaVersion, BoardID: request.BoardID,
			IdempotencyKey: request.IdempotencyKey, ExpectedRevision: *request.ExpectedBoardRevision})
	case webui.CardCreate:
		budget := workboard.WorkBudget{AttemptLimit: 1}
		if request.Budget != nil {
			budget = domainBudget(*request.Budget)
		}
		result, applyErr := b.cards.CreateCard(ctx, workboard.CreateCardRequest{BoardID: request.BoardID, IdempotencyKey: request.IdempotencyKey,
			ExpectedBoardRevision: *request.ExpectedBoardRevision, ExpectedGraphRevision: *request.ExpectedGraphRevision,
			Card: workboard.NewCard{Title: *request.Title, Description: optionalText(request.Description), Priority: defaultPriority(request.Priority),
				Labels: nonnilStrings(request.Labels), AssigneeID: request.AssigneeID, ParentID: request.ParentID,
				Dependencies: nonnilStrings(request.Dependencies), Budget: budget, Criteria: domainCriteria(request.Criteria)}})
		err, receipt = applyErr, result.Receipt
	case webui.CardRevise:
		result, applyErr := b.cards.ReviseCard(ctx, workboard.ReviseCardRequest{BoardID: request.BoardID, CardID: request.CardID,
			IdempotencyKey: request.IdempotencyKey, ExpectedCardRevision: *request.ExpectedCardRevision,
			ExpectedGraphRevision: optionalRevision(request.ExpectedGraphRevision), Patch: domainCardPatch(request)})
		err, receipt = applyErr, result.Receipt
	case webui.CardMove:
		result, applyErr := b.cards.MoveCard(ctx, workboard.MoveCardRequest{BoardID: request.BoardID, CardID: request.CardID,
			IdempotencyKey: request.IdempotencyKey, TargetState: workboard.State(request.TargetState), BeforeCardID: request.BeforeCardID,
			AfterCardID: request.AfterCardID, ExpectedBoardRevision: *request.ExpectedBoardRevision,
			ExpectedCardRevision: *request.ExpectedCardRevision, ExpectedLayoutRevision: *request.ExpectedLayoutRevision})
		err, receipt = applyErr, result.Receipt
	case webui.CardReorder:
		result, applyErr := b.cards.ReorderCard(ctx, workboard.ReorderCardRequest{BoardID: request.BoardID, CardID: request.CardID,
			IdempotencyKey: request.IdempotencyKey, BeforeCardID: request.BeforeCardID, AfterCardID: request.AfterCardID,
			ExpectedBoardRevision: *request.ExpectedBoardRevision, ExpectedCardRevision: *request.ExpectedCardRevision,
			ExpectedLayoutRevision: *request.ExpectedLayoutRevision})
		err, receipt = applyErr, result.Receipt
	case webui.DependencyAdd, webui.DependencyRemove:
		command := workboard.DependencyRequest{BoardID: request.BoardID, CardID: request.CardID, DependencyID: request.DependencyID,
			IdempotencyKey: request.IdempotencyKey, ExpectedCardRevision: *request.ExpectedCardRevision, ExpectedGraphRevision: *request.ExpectedGraphRevision}
		var result workboard.CardMutationResult
		if request.Action == webui.DependencyAdd {
			result, err = b.cards.AddDependency(ctx, command)
		} else {
			result, err = b.cards.RemoveDependency(ctx, command)
		}
		receipt = result.Receipt
	default:
		return webui.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "action_unavailable"}
	}
	if err != nil {
		return webui.OperationReceipt{}, err
	}
	result := workboardReceipt(receipt)
	if result.Validate() != nil {
		return webui.OperationReceipt{}, errors.New("invalid workboard receipt projection")
	}
	return result, nil
}

func (b *WorkboardBridge) list(ctx context.Context, options webui.BoardListOptions) (webui.Page, error) {
	page, err := b.boards.List(ctx, workboard.BoardListOptions{After: options.After, Limit: options.Limit, State: options.State})
	if err != nil {
		return webui.Page{}, err
	}
	result := webui.Page{Version: webui.ContractVersion, NextCursor: page.NextCursor, HasMore: page.HasMore, Items: make([]webui.Board, len(page.Items))}
	for index := range page.Items {
		result.Items[index] = workboardBoard(page.Items[index])
	}
	if result.Validate() != nil {
		return webui.Page{}, errors.New("invalid workboard projection")
	}
	return result, nil
}

func (b *WorkboardBridge) NativeEvents(ctx context.Context, boardID string, options webui.BoardEventOptions) (webui.BoardEventPage, error) {
	return b.events(nativeWorkboardContext(ctx), boardID, options)
}

func (b *WorkboardBridge) BrowserEvents(ctx context.Context, subject, boardID string, options webui.BoardEventOptions) (webui.BoardEventPage, error) {
	trusted, err := browserWorkboardContext(ctx, subject)
	if err != nil {
		return webui.BoardEventPage{}, err
	}
	return b.events(trusted, boardID, options)
}

func (b *WorkboardBridge) events(ctx context.Context, boardID string, options webui.BoardEventOptions) (webui.BoardEventPage, error) {
	page, err := b.boards.Events(ctx, boardID, workboard.BoardEventOptions{After: options.After, Limit: options.Limit})
	if err != nil {
		return webui.BoardEventPage{}, err
	}
	result := webui.BoardEventPage{Version: webui.ContractVersion, BoardID: page.BoardID, HighWaterSequence: page.HighWaterSequence,
		NextCursor: page.NextCursor, HasMore: page.HasMore, Items: make([]webui.BoardEvent, len(page.Items))}
	for index, event := range page.Items {
		result.Items[index] = webui.BoardEvent{Version: webui.ContractVersion, ID: event.ID, BoardID: event.BoardID, Sequence: event.Sequence,
			OperationID: event.OperationID, Kind: webui.BoardAction(event.Kind), ActorID: event.ActorID, ActorType: event.ActorType, CardID: event.CardID, CreatedAt: event.CreatedAt}
	}
	if result.Validate() != nil {
		return webui.BoardEventPage{}, errors.New("invalid workboard event projection")
	}
	return result, nil
}

func workboardBoard(board workboard.Board) webui.Board {
	return webui.Board{Version: webui.ContractVersion, ID: board.ID, Revision: board.Revision, LayoutRevision: board.LayoutRevision,
		EventSequence: board.EventSequence, State: board.State, Title: board.Title, Description: board.Description,
		CardCount: board.CardCount, ActiveClaims: board.ActiveClaims, CreatedAt: board.CreatedAt, UpdatedAt: board.UpdatedAt}
}

func workboardReceipt(receipt workboard.OperationReceipt) webui.OperationReceipt {
	return webui.OperationReceipt{Version: webui.ContractVersion, BoardID: receipt.BoardID, OperationID: receipt.OperationID,
		RequestDigest: receipt.RequestDigest, ResponseDigest: receipt.ResponseDigest, FirstSequence: receipt.FirstSequence,
		LastSequence: receipt.LastSequence, EventCount: receipt.EventCount, TransactionBytes: receipt.TransactionBytes,
		BoardRevision: receipt.BoardRevision, CardID: receipt.CardID, CardRevision: receipt.CardRevision,
		ClaimRevision: receipt.ClaimRevision, Outcome: receipt.Outcome, CreatedAt: receipt.CreatedAt}
}

func workboardCard(card workboard.Card) webui.Card {
	criteria := make([]webui.AcceptanceCriterion, len(card.Criteria))
	for index, criterion := range card.Criteria {
		criteria[index] = webui.AcceptanceCriterion{Version: webui.ContractVersion, ID: criterion.ID, Kind: criterion.Kind,
			RequiredSource: criterion.RequiredSource, ValidatorID: criterion.ValidatorID, Description: criterion.Description, Required: criterion.Required}
	}
	return webui.Card{Version: webui.ContractVersion, ID: card.ID, BoardID: card.BoardID, Revision: card.Revision,
		CriteriaRevision: card.CriteriaRevision, State: string(card.State), Rank: card.Rank, Title: card.Title,
		Description: card.Description, Priority: card.Priority, Labels: nonnilStrings(card.Labels), ParentID: card.ParentID,
		Dependencies: nonnilStrings(card.Dependencies), RemainingDependencies: card.RemainingDependencies, AssigneeID: card.AssigneeID,
		AttemptCount: card.AttemptCount, CurrentAttemptID: card.CurrentAttemptID, CurrentClaimID: card.CurrentClaimID,
		AcceptanceID: card.AcceptanceID, BlockReason: card.BlockReason, CancelRequested: card.CancelRequested,
		PauseRequested: card.PauseRequested, Budget: webui.WorkBudget{AttemptLimit: card.Budget.AttemptLimit,
			TimeLimitMS: card.Budget.TimeLimitMS, TokenLimit: card.Budget.TokenLimit, CostMicros: card.Budget.CostMicros},
		Criteria: criteria, CreatedAt: card.CreatedAt, UpdatedAt: card.UpdatedAt}
}

func domainCriteria(criteria []webui.AcceptanceCriterion) []workboard.AcceptanceCriterion {
	result := make([]workboard.AcceptanceCriterion, len(criteria))
	for index, criterion := range criteria {
		result[index] = workboard.AcceptanceCriterion{Version: workboard.SchemaVersion, ID: criterion.ID, Kind: criterion.Kind,
			RequiredSource: criterion.RequiredSource, ValidatorID: criterion.ValidatorID, Description: criterion.Description, Required: criterion.Required}
	}
	return result
}

func domainBudget(budget webui.WorkBudget) workboard.WorkBudget {
	return workboard.WorkBudget{AttemptLimit: budget.AttemptLimit, TimeLimitMS: budget.TimeLimitMS,
		TokenLimit: budget.TokenLimit, CostMicros: budget.CostMicros}
}

func domainCardPatch(request webui.BoardRequest) workboard.CardPatch {
	patch := workboard.CardPatch{Title: request.Title, Description: request.Description, Labels: optionalStrings(request.Labels)}
	if request.Priority != "" {
		patch.Priority = &request.Priority
	}
	if request.Budget != nil {
		budget := domainBudget(*request.Budget)
		patch.Budget = &budget
	}
	if request.ClearParent != nil {
		empty := ""
		patch.ParentID = &empty
	} else if request.ParentID != "" {
		patch.ParentID = &request.ParentID
	}
	if request.ClearAssignee != nil {
		empty := ""
		patch.AssigneeID = &empty
	} else if request.AssigneeID != "" {
		patch.AssigneeID = &request.AssigneeID
	}
	return patch
}

func optionalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalRevision(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func defaultPriority(value string) string {
	if value == "" {
		return "normal"
	}
	return value
}

func optionalStrings(values []string) *[]string {
	if values == nil {
		return nil
	}
	copy := nonnilStrings(values)
	return &copy
}

func nonnilStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string{}, values...)
}
