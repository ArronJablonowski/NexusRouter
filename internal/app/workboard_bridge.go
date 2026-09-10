package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// WorkboardBridge presents one authority-gated domain service through the
// native API and browser BFF contracts. Identity is supplied by the trusted
// transport adapter and never decoded from a BoardRequest.
type WorkboardBridge struct {
	boards           *workboard.BoardService
	cards            *workboard.CardService
	progress         *workboard.ProgressService
	control          *workboard.ControlService
	evaluation       *workboard.EvaluationService
	lifecycle        workboard.LifecycleSnapshotRepository
	history          workboard.LifecycleHistoryRepository
	dependencies     *workboard.DependencyReadService
	supervision      *workboard.SupervisionService
	browserAuthority workboard.Authority
}

type unavailableControlVerifier struct{}

func (unavailableControlVerifier) VerifyControlStop(context.Context, workboard.FinalizeCancelRequest, workboard.Actor) (workboard.RecoveryProof, error) {
	return workboard.RecoveryProof{}, errors.New("workboard cancel finalization unavailable")
}

type unavailableCandidateEvaluator struct{}

func (unavailableCandidateEvaluator) EvaluateCandidate(context.Context, workboard.SubmitCandidateRequest, workboard.Actor) ([]workboard.EvidenceInput, error) {
	return nil, errors.New("workboard candidate evaluation unavailable")
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
	authority, err := BrowserWorkboardAuthority("darwin-embedded-local-workspace-v1")
	if err != nil {
		return nil, err
	}
	return NewWorkboardBridgeWithBrowserAuthority(repository, cards, now, authority)
}

// BrowserWorkboardAuthority derives the stable local workspace operator used
// for domain authorization and idempotency from the database-owned, non-secret
// workspace identity. Bearer tokens authenticate ephemeral transports; they
// must not change the domain actor when a browser session or token rotates.
// Per-session attribution remains in the browser operation journal instead.
func BrowserWorkboardAuthority(workspaceIdentity string) (workboard.Authority, error) {
	if len(workspaceIdentity) < 16 {
		return workboard.Authority{}, errors.New("workboard workspace identity unavailable")
	}
	scope := sha256.Sum256(append([]byte("darwin.workboard.browser.scope.v1\x00"), []byte(workspaceIdentity)...))
	actor := sha256.Sum256(append([]byte("darwin.workboard.browser.actor.v1\x00"), []byte(workspaceIdentity)...))
	authority := workboard.Authority{CreationScope: hex.EncodeToString(scope[:]), Actor: workboard.Actor{ID: hex.EncodeToString(actor[:]), Type: "operator"}}
	if authority.Validate() != nil {
		return workboard.Authority{}, errors.New("invalid workboard browser authority")
	}
	return authority, nil
}

func NewWorkboardBridgeWithBrowserAuthority(repository workboard.BoardRepository, cards workboard.CardStore, now func() time.Time, browserAuthority workboard.Authority) (*WorkboardBridge, error) {
	if browserAuthority.Validate() != nil || browserAuthority.Actor.Type != "operator" {
		return nil, &workboard.Violation{Code: workboard.CodeInvalid, Field: "browser_authority"}
	}
	boards, err := workboard.NewBoardService(repository, contextWorkboardAuthority{}, now)
	if err != nil {
		return nil, err
	}
	cardService, err := workboard.NewCardService(cards, contextWorkboardAuthority{})
	if err != nil {
		return nil, err
	}
	progressRepository, ok := cards.(workboard.ProgressRepository)
	if !ok {
		return nil, &workboard.Violation{Code: workboard.CodeInvalid, Field: "progress_repository"}
	}
	progress, err := workboard.NewProgressService(progressRepository, contextWorkboardAuthority{}, now)
	if err != nil {
		return nil, err
	}
	lifecycle, ok := cards.(workboard.LifecycleSnapshotRepository)
	if !ok {
		return nil, &workboard.Violation{Code: workboard.CodeInvalid, Field: "lifecycle_reader"}
	}
	history, ok := cards.(workboard.LifecycleHistoryRepository)
	if !ok {
		return nil, &workboard.Violation{Code: workboard.CodeInvalid, Field: "lifecycle_history_reader"}
	}
	dependencyRepository, ok := cards.(workboard.DependencyPageRepository)
	if !ok {
		return nil, &workboard.Violation{Code: workboard.CodeInvalid, Field: "dependency_reader"}
	}
	dependencies, err := workboard.NewDependencyReadService(dependencyRepository, contextWorkboardAuthority{})
	if err != nil {
		return nil, err
	}
	controlRepository, ok := cards.(workboard.ControlRepository)
	if !ok {
		return nil, &workboard.Violation{Code: workboard.CodeInvalid, Field: "control_repository"}
	}
	control, err := workboard.NewControlService(controlRepository, contextWorkboardAuthority{}, unavailableControlVerifier{}, now)
	if err != nil {
		return nil, err
	}
	evaluationRepository, ok := cards.(workboard.EvaluationRepository)
	if !ok {
		return nil, &workboard.Violation{Code: workboard.CodeInvalid, Field: "evaluation_repository"}
	}
	evaluation, err := workboard.NewEvaluationService(evaluationRepository, contextWorkboardAuthority{}, unavailableCandidateEvaluator{}, now)
	if err != nil {
		return nil, err
	}
	var supervision *workboard.SupervisionService
	if repository, available := cards.(workboard.SupervisionRepository); available {
		supervision, err = workboard.NewSupervisionService(repository, contextWorkboardAuthority{}, now, 30*time.Second)
		if err != nil {
			return nil, err
		}
	}
	return &WorkboardBridge{boards: boards, cards: cardService, progress: progress, control: control, evaluation: evaluation,
		lifecycle: lifecycle, history: history, dependencies: dependencies, supervision: supervision, browserAuthority: browserAuthority}, nil
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

// rootAgentWorkboardContext grants only the domain identity needed by the
// built-in, read-only root-agent tools. It is deliberately distinct from the
// operator authority used by native and browser mutation surfaces.
func rootAgentWorkboardContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, workboardAuthorityKey{}, workboard.Authority{
		CreationScope: "root-agent-tools",
		Actor:         workboard.Actor{ID: "darwin_root_agent", Type: "model"},
	})
}

func rootAgentMutationWorkboardContext(ctx context.Context) (context.Context, error) {
	identity, ok := tools.ExecutionIdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("workboard mutation identity unavailable")
	}
	// Model identity is not part of the provider-neutral execution context yet.
	// Bind durable attribution to the trusted task origin; the task journal binds
	// that origin to its selected provider and model without exposing either here.
	actor := sha256.Sum256(append([]byte("darwin.workboard.root-agent-task.v1\x00"), []byte(identity.TaskID)...))
	return context.WithValue(ctx, workboardAuthorityKey{}, workboard.Authority{
		CreationScope: "root-agent-tools",
		Actor:         workboard.Actor{ID: hex.EncodeToString(actor[:]), Type: "model"},
	}), nil
}

func browserWorkboardContext(ctx context.Context, subject string, authority workboard.Authority) (context.Context, error) {
	if ctx == nil || !validBrowserSubject(subject) || authority.Validate() != nil || authority.Actor.Type != "operator" {
		return nil, errors.New("workboard authority unavailable")
	}
	return context.WithValue(ctx, workboardAuthorityKey{}, authority), nil
}

func (b *WorkboardBridge) NativeList(ctx context.Context, options webui.BoardListOptions) (webui.Page, error) {
	return b.list(nativeWorkboardContext(ctx), options)
}

func (b *WorkboardBridge) RootAgentList(ctx context.Context, options webui.BoardListOptions) (webui.Page, error) {
	return b.list(rootAgentWorkboardContext(ctx), options)
}

func (b *WorkboardBridge) BrowserList(ctx context.Context, subject string, options webui.BoardListOptions) (webui.Page, error) {
	trusted, err := browserWorkboardContext(ctx, subject, b.browserAuthority)
	if err != nil {
		return webui.Page{}, err
	}
	return b.list(trusted, options)
}

func (b *WorkboardBridge) NativeRead(ctx context.Context, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, error) {
	return b.read(nativeWorkboardContext(ctx), boardID, options)
}

func (b *WorkboardBridge) RootAgentRead(ctx context.Context, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, error) {
	if ctx == nil {
		return webui.BoardSnapshot{}, context.Canceled
	}
	trusted := rootAgentWorkboardContext(ctx)
	const attempts = 3
	for range attempts {
		result, cardIDs, err := b.readBase(trusted, boardID, options)
		if err != nil {
			return webui.BoardSnapshot{}, err
		}
		if err := b.attachLifecycle(trusted, boardID, cardIDs, &result); err != nil {
			return webui.BoardSnapshot{}, err
		}
		confirmed, _, err := b.readBase(trusted, boardID, options)
		if err != nil {
			return webui.BoardSnapshot{}, err
		}
		candidate := result
		candidate.Lifecycle = nil
		if reflect.DeepEqual(candidate, confirmed) {
			if result.Validate() != nil {
				return webui.BoardSnapshot{}, errors.New("invalid workboard snapshot projection")
			}
			return result, nil
		}
		if ctx.Err() != nil {
			return webui.BoardSnapshot{}, ctx.Err()
		}
	}
	return webui.BoardSnapshot{}, errors.New("workboard snapshot changed during read")
}

func (b *WorkboardBridge) RootAgentMutate(ctx context.Context, request webui.BoardRequest) (webui.OperationReceipt, error) {
	trusted, err := rootAgentMutationWorkboardContext(ctx)
	if err != nil {
		return webui.OperationReceipt{}, err
	}
	return b.mutate(trusted, request)
}

func (b *WorkboardBridge) BrowserRead(ctx context.Context, subject, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, error) {
	trusted, err := browserWorkboardContext(ctx, subject, b.browserAuthority)
	if err != nil {
		return webui.BoardSnapshot{}, err
	}
	return b.read(trusted, boardID, options)
}

func (b *WorkboardBridge) NativeAttemptHistory(ctx context.Context, boardID, cardID string, options webui.AttemptHistoryOptions) (webui.AttemptHistoryPage, error) {
	return b.attemptHistory(nativeWorkboardContext(ctx), boardID, cardID, options)
}

func (b *WorkboardBridge) BrowserAttemptHistory(ctx context.Context, subject, boardID, cardID string, options webui.AttemptHistoryOptions) (webui.AttemptHistoryPage, error) {
	trusted, err := browserWorkboardContext(ctx, subject, b.browserAuthority)
	if err != nil {
		return webui.AttemptHistoryPage{}, err
	}
	return b.attemptHistory(trusted, boardID, cardID, options)
}

func (b *WorkboardBridge) attemptHistory(ctx context.Context, boardID, cardID string, options webui.AttemptHistoryOptions) (webui.AttemptHistoryPage, error) {
	if options.Validate() != nil || !authorizedWorkboardRead(ctx) {
		return webui.AttemptHistoryPage{}, errors.New("invalid attempt history read")
	}
	page, err := b.history.ListAttemptHistory(ctx, boardID, cardID, workboard.AttemptHistoryOptions{After: options.After, Limit: options.Limit})
	if err != nil {
		return webui.AttemptHistoryPage{}, err
	}
	return projectAttemptHistory(page)
}

func (b *WorkboardBridge) NativeAttemptDetail(ctx context.Context, boardID, cardID, attemptID string, options webui.AttemptDetailOptions) (webui.AttemptDetailPage, error) {
	return b.attemptDetail(nativeWorkboardContext(ctx), boardID, cardID, attemptID, options)
}

func (b *WorkboardBridge) BrowserAttemptDetail(ctx context.Context, subject, boardID, cardID, attemptID string, options webui.AttemptDetailOptions) (webui.AttemptDetailPage, error) {
	trusted, err := browserWorkboardContext(ctx, subject, b.browserAuthority)
	if err != nil {
		return webui.AttemptDetailPage{}, err
	}
	return b.attemptDetail(trusted, boardID, cardID, attemptID, options)
}

func (b *WorkboardBridge) attemptDetail(ctx context.Context, boardID, cardID, attemptID string, options webui.AttemptDetailOptions) (webui.AttemptDetailPage, error) {
	if options.Validate() != nil || !authorizedWorkboardRead(ctx) {
		return webui.AttemptDetailPage{}, errors.New("invalid attempt detail read")
	}
	page, err := b.history.ReadAttemptDetail(ctx, boardID, cardID, attemptID, workboard.AttemptDetailOptions{After: options.After, Limit: options.Limit})
	if err != nil {
		return webui.AttemptDetailPage{}, err
	}
	return projectAttemptDetail(page)
}

func authorizedWorkboardRead(ctx context.Context) bool {
	authority, err := (contextWorkboardAuthority{}).WorkboardAuthority(ctx)
	return err == nil && authority.Validate() == nil && authority.Actor.Type == "operator"
}

func (b *WorkboardBridge) read(ctx context.Context, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, error) {
	result, cardIDs, err := b.readBase(ctx, boardID, options)
	if err != nil {
		return webui.BoardSnapshot{}, err
	}
	if err := b.attachLifecycle(ctx, boardID, cardIDs, &result); err != nil {
		return webui.BoardSnapshot{}, err
	}
	// Supervision is an independently re-derived, advisory projection. An old
	// or partially populated task binding must not make the canonical board
	// unreadable; omission fails closed by disabling supervision actions.
	_ = b.attachSupervision(ctx, boardID, cardIDs, &result)
	if result.Validate() != nil {
		return webui.BoardSnapshot{}, errors.New("invalid workboard snapshot projection")
	}
	return result, nil
}

func (b *WorkboardBridge) readBase(ctx context.Context, boardID string, options webui.BoardSnapshotOptions) (webui.BoardSnapshot, []string, error) {
	snapshot, err := b.boards.Read(ctx, boardID, workboard.BoardSnapshotOptions{After: options.After, Limit: options.Limit,
		State: workboard.State(options.State), AssigneeID: options.AssigneeID, OwnerID: options.OwnerID, ClaimState: options.ClaimState})
	if err != nil {
		return webui.BoardSnapshot{}, nil, err
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
	cardIDs := make([]string, len(snapshot.Cards))
	for index := range snapshot.Cards {
		cardIDs[index] = snapshot.Cards[index].ID
	}
	if result.Validate() != nil {
		return webui.BoardSnapshot{}, nil, errors.New("invalid workboard snapshot projection")
	}
	return result, cardIDs, nil
}

func (b *WorkboardBridge) attachLifecycle(ctx context.Context, boardID string, cardIDs []string, result *webui.BoardSnapshot) error {
	lifecycle, err := b.lifecycle.ReadCardLifecycleSnapshots(ctx, boardID, cardIDs)
	if err != nil {
		return err
	}
	result.Lifecycle = make([]webui.CardLifecycle, 0, len(lifecycle))
	for _, cardID := range cardIDs {
		if detail, exists := lifecycle[cardID]; exists {
			projected, projectErr := workboardLifecycle(detail)
			if projectErr != nil {
				return projectErr
			}
			result.Lifecycle = append(result.Lifecycle, projected)
		}
	}
	return nil
}

func (b *WorkboardBridge) attachSupervision(ctx context.Context, boardID string, cardIDs []string, result *webui.BoardSnapshot) error {
	if b.supervision == nil || len(cardIDs) == 0 {
		return nil
	}
	wanted := make(map[string]bool, len(cardIDs))
	for _, cardID := range cardIDs {
		wanted[cardID] = true
	}
	for after := ""; ; {
		page, err := b.supervision.Read(ctx, boardID, workboard.SupervisionOptions{After: after, Limit: workboard.MaxSupervisionPageItems})
		if err != nil {
			return err
		}
		if page.BoardRevision != result.Board.Revision {
			return errors.New("workboard supervision changed during read")
		}
		for _, item := range page.Items {
			if !wanted[item.CardID] {
				continue
			}
			result.Supervision = append(result.Supervision, webui.SupervisionItem{Version: webui.ContractVersion, BoardID: item.BoardID,
				CardID: item.CardID, CardRevision: item.CardRevision, State: string(item.State), Reason: string(item.Reason), AttemptID: item.AttemptID,
				ClaimID: item.ClaimID, ClaimRevision: item.ClaimRevision, PausePhase: string(item.PausePhase), WorkerID: item.WorkerID, TaskID: item.TaskID,
				LastHeartbeat: item.LastHeartbeat, ExpiresAt: item.ExpiresAt, Actions: webui.SupervisionActions{Claim: item.Actions.Claim,
					PauseRequest: item.Actions.PauseRequest, ResumeRequest: item.Actions.ResumeRequest, CancelRequest: item.Actions.CancelRequest, RecoveryCheck: item.Actions.RecoveryCheck}})
			delete(wanted, item.CardID)
		}
		if !page.HasMore || len(wanted) == 0 {
			return nil
		}
		after = page.NextCursor
	}
}

func (b *WorkboardBridge) NativeMutate(ctx context.Context, request webui.BoardRequest) (webui.OperationReceipt, error) {
	return b.mutate(nativeWorkboardContext(ctx), request)
}

func (b *WorkboardBridge) BrowserMutate(ctx context.Context, subject string, request webui.BoardRequest) (webui.OperationReceipt, error) {
	trusted, err := browserWorkboardContext(ctx, subject, b.browserAuthority)
	if err != nil {
		return webui.OperationReceipt{}, err
	}
	return b.mutate(trusted, request)
}

// legacyBrowserMutate exists only for schema-38 recovery of a pending browser
// operation created before workspace authority existed. Its durable initiating
// subject is both the legacy creation scope and actor; the recovery session is
// deliberately excluded from domain attribution.
func (b *WorkboardBridge) legacyBrowserMutate(ctx context.Context, initiatingSubject string, request webui.BoardRequest) (webui.OperationReceipt, error) {
	if ctx == nil || !validBrowserSubject(initiatingSubject) {
		return webui.OperationReceipt{}, errors.New("legacy workboard authority unavailable")
	}
	authority := workboard.Authority{CreationScope: initiatingSubject, Actor: workboard.Actor{ID: initiatingSubject, Type: "operator"}}
	return b.mutate(context.WithValue(ctx, workboardAuthorityKey{}, authority), request)
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
	case webui.CriteriaRevise:
		receipt, err = b.progress.ReviseCriteria(ctx, workboard.ReviseCriteriaRequest{BoardID: request.BoardID, CardID: request.CardID,
			IdempotencyKey: request.IdempotencyKey, ExpectedCardRevision: *request.ExpectedCardRevision,
			ExpectedCriteriaRevision: *request.ExpectedCriteriaRevision, Criteria: domainCriteria(request.Criteria)})
	case webui.CardPauseRequest, webui.CardResumeRequest, webui.CardCancelRequest:
		command := workboard.RequestCardControl{BoardID: request.BoardID, CardID: request.CardID,
			IdempotencyKey: request.IdempotencyKey, ExpectedCardRevision: *request.ExpectedCardRevision}
		if request.Action == webui.CardPauseRequest {
			receipt, err = b.control.RequestPause(ctx, command)
		} else if request.Action == webui.CardResumeRequest {
			receipt, err = b.control.RequestResume(ctx, command)
		} else {
			receipt, err = b.control.RequestCancel(ctx, command)
		}
	case webui.AcceptanceAccept, webui.AcceptanceReject:
		command := workboard.DecideCandidateRequest{BoardID: request.BoardID, CardID: request.CardID, AttemptID: request.AttemptID,
			CandidateID: request.CandidateID, IdempotencyKey: request.IdempotencyKey, ExpectedCardRevision: *request.ExpectedCardRevision,
			CriteriaRevision: *request.CriteriaRevision, EvidenceHeadRevision: *request.EvidenceHeadRevision,
			CandidateDigest: request.CandidateDigest, CriteriaDigest: request.CriteriaDigest, EvidenceSetDigest: request.EvidenceSetDigest,
			PolicyDigest: request.PolicyDigest, Evidence: request.Evidence}
		if request.Action == webui.AcceptanceAccept {
			receipt, err = b.evaluation.AcceptCandidate(ctx, command)
		} else {
			receipt, err = b.evaluation.RejectCandidate(ctx, command)
		}
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
	trusted, err := browserWorkboardContext(ctx, subject, b.browserAuthority)
	if err != nil {
		return webui.BoardEventPage{}, err
	}
	return b.events(trusted, boardID, options)
}

func (b *WorkboardBridge) events(ctx context.Context, boardID string, options webui.BoardEventOptions) (webui.BoardEventPage, error) {
	page, err := b.boards.Events(ctx, boardID, workboard.BoardEventOptions{After: options.After, Limit: options.Limit, TailAfterSequence: options.TailAfterSequence})
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
		PauseRequested: card.PauseRequested, PausePhase: string(card.PausePhase), Budget: webui.WorkBudget{AttemptLimit: card.Budget.AttemptLimit,
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
