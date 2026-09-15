package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// ApplyCardMutation applies one projection mutation, immutable event and
// replay receipt in a single SQLite transaction. Exact retries are resolved
// before any current revision is inspected.
func (s *Store) ApplyCardMutation(ctx context.Context, mutation workboard.CardMutation) (workboard.CardMutationResult, error) {
	if err := validateStoreMutation(mutation); err != nil {
		return workboard.CardMutationResult{}, err
	}
	requestDigest, err := cardMutationDigest(mutation)
	if err != nil || mutation.RequestDigest != requestDigest {
		return workboard.CardMutationResult{}, invalidWorkboard("request_digest")
	}
	keyDigest := digestBytes([]byte(mutation.IdempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.CardMutationResult{}, err
	}
	if replayResult, found, replayErr := readCardMutationReplay(ctx, tx, mutation.BoardID, keyDigest, requestDigest); found || replayErr != nil {
		if replayErr != nil {
			return workboard.CardMutationResult{}, replayErr
		}
		return replayResult, nil
	}
	board, graphRevision, graphDigest, err := readBoardRow(ctx, tx, mutation.BoardID)
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	if board.State != "active" {
		return workboard.CardMutationResult{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "board_state"}
	}
	if err = compareMutationFences(board, graphRevision, mutation); err != nil {
		return workboard.CardMutationResult{}, err
	}
	graph, actualDigest, err := loadGraphTx(ctx, tx, board.ID, graphRevision)
	if err != nil || actualDigest != graphDigest {
		if err == nil {
			err = ErrWorkboardCorrupt
		}
		return workboard.CardMutationResult{}, err
	}
	card, body, exists, err := mutationCard(ctx, tx, mutation)
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	if mutation.Kind == workboard.MutationCreate {
		card, body, err = newStoredCard(ctx, tx, mutation, board, graph)
		if err != nil {
			return workboard.CardMutationResult{}, err
		}
	} else if !exists {
		return workboard.CardMutationResult{}, ErrWorkboardNotFound
	}
	decomposition, err := prepareDecompositionAdmission(ctx, tx, mutation, graph)
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	if err = mutateStoredCard(ctx, tx, mutation, &card, &body, graph); err != nil {
		return workboard.CardMutationResult{}, err
	}
	graphChanged, layoutChanged := mutationChanges(mutation)
	proposedGraph := applyMutationToGraph(graph, mutation, card)
	if graphChanged {
		proposedGraph.GraphRevision++
	}
	if layoutChanged {
		proposedGraph.LayoutRevision++
	}
	if _, err = workboard.ValidateGraph(proposedGraph, proposedGraph.GraphRevision); err != nil {
		return workboard.CardMutationResult{}, err
	}
	newGraphDigest, err := graphDigestFor(proposedGraph)
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	now := time.Now().UTC()
	if now.Year() < 1970 || now.Year() >= 2261 {
		return workboard.CardMutationResult{}, invalidWorkboard("updated_at")
	}
	card.Revision++
	if mutation.Kind == workboard.MutationCreate {
		card.Revision = 1
		card.CreatedAt = now
	}
	card.UpdatedAt = now
	body = updateStoredBody(body, card)
	bodyBytes, err := json.Marshal(body)
	if err != nil || card.Validate() != nil || !validStoredCardReferences(body) {
		return workboard.CardMutationResult{}, invalidWorkboard("card")
	}
	operationID, eventID := newWorkboardID(), newWorkboardID()
	if operationID == "" || eventID == "" {
		return workboard.CardMutationResult{}, errors.New("secure identifier generation failed")
	}
	board.Revision++
	board.EventSequence++
	board.UpdatedAt = now
	if mutation.Kind == workboard.MutationCreate {
		board.CardCount++
	}
	if layoutChanged {
		board.LayoutRevision++
	}
	boardBody, err := json.Marshal(board)
	if err != nil || board.Validate() != nil {
		return workboard.CardMutationResult{}, ErrWorkboardCorrupt
	}
	event := workboard.BoardEvent{Version: 1, ID: eventID, BoardID: board.ID, Sequence: board.EventSequence,
		OperationID: operationID, Kind: mutationBoardAction(mutation.Kind), ActorID: mutation.Actor.ID, ActorType: mutation.Actor.Type, CardID: card.ID, CreatedAt: now}
	var admission workboard.DecompositionAdmission
	var admissionBody []byte
	if decomposition != nil {
		admission, err = decomposition.finalize(card.ID, operationID, requestDigest, mutation.Actor, now)
		if err != nil {
			return workboard.CardMutationResult{}, err
		}
		if err = event.BindDecompositionAdmission(admission); err != nil {
			return workboard.CardMutationResult{}, err
		}
		admissionBody, err = json.Marshal(admission)
		if err != nil {
			return workboard.CardMutationResult{}, err
		}
	}
	eventBody, err := json.Marshal(event)
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	if event.Validate() != nil {
		return workboard.CardMutationResult{}, invalidWorkboard("event")
	}
	cardRevision := card.Revision
	receipt := workboard.OperationReceipt{Version: 1, BoardID: board.ID, OperationID: operationID, RequestDigest: requestDigest,
		FirstSequence: board.EventSequence, LastSequence: board.EventSequence, EventCount: 1, BoardRevision: board.Revision,
		CardID: card.ID, CardRevision: &cardRevision, Outcome: "committed", CreatedAt: now}
	response, err := finalizeCardMutationReceipt(&receipt, card, len(boardBody)+len(bodyBytes)+len(eventBody)+len(admissionBody))
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	if err = writeCardProjection(ctx, tx, mutation, card, body, bodyBytes, board.EventSequence); err != nil {
		return workboard.CardMutationResult{}, normalizeCardWriteError(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=?,layout_revision=?,event_sequence=?,graph_revision=?,graph_digest=?,card_count=?,updated_at=?,body=? WHERE id=? AND revision=? AND layout_revision=? AND graph_revision=?`,
		board.Revision, board.LayoutRevision, board.EventSequence, proposedGraph.GraphRevision, newGraphDigest, board.CardCount, now.UnixNano(), boardBody,
		board.ID, board.Revision-1, board.LayoutRevision-boolDelta(layoutChanged), graphRevision)
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return workboard.CardMutationResult{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if err = insertWorkboardOperation(ctx, tx, "board", board.ID, keyDigest, receipt, response); err != nil {
		return workboard.CardMutationResult{}, err
	}
	if decomposition != nil {
		if err = insertDecompositionAdmission(ctx, tx, admission, event, admissionBody); err != nil {
			return workboard.CardMutationResult{}, err
		}
	}
	actor := workboard.Actor{ID: event.ActorID, Type: event.ActorType}
	if decomposition == nil {
		err = insertWorkboardEvent(ctx, tx, eventID, board.ID, board.EventSequence, operationID, string(event.Kind), card.ID, actor, now, eventBody)
	} else {
		err = insertDecompositionWorkboardEvent(ctx, tx, event, eventBody)
	}
	if err != nil {
		return workboard.CardMutationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.CardMutationResult{}, err
	}
	return workboard.CardMutationResult{Card: card, Receipt: receipt}, nil
}

func mutationBoardAction(kind workboard.MutationKind) workboard.BoardAction {
	switch kind {
	case workboard.MutationCreate:
		return workboard.CardCreateAction
	case workboard.MutationRevise:
		return workboard.CardReviseAction
	case workboard.MutationMove:
		return workboard.CardMoveAction
	case workboard.MutationReorder:
		return workboard.CardReorderAction
	case workboard.MutationDependencyAdd:
		return workboard.CardDependencyAddAction
	case workboard.MutationDependencyRemove:
		return workboard.CardDependencyRemoveAction
	default:
		return ""
	}
}

func validateStoreMutation(m workboard.CardMutation) error {
	if m.Version != workboard.CardMutationVersion || !validWorkboardID(m.BoardID) || m.Actor.Validate() != nil || len(m.IdempotencyKey) < 16 || len(m.IdempotencyKey) > 128 || !validDigest(m.RequestDigest) {
		return invalidWorkboard("mutation")
	}
	for _, r := range m.IdempotencyKey {
		if r < 0x21 || r > 0x7e {
			return invalidWorkboard("mutation")
		}
	}
	agent := m.Actor.Type == "model" || m.Actor.Type == "worker"
	hierarchy := m.Kind == workboard.MutationCreate || m.Kind == workboard.MutationRevise && m.Patch.ParentID != nil
	if agent && hierarchy {
		if m.Decomposition == nil || m.Decomposition.Validate() != nil {
			return invalidWorkboard("decomposition_policy")
		}
	} else if m.Decomposition != nil {
		return invalidWorkboard("decomposition_policy")
	}
	switch m.Kind {
	case workboard.MutationCreate:
		if m.Create == nil || m.CardID != "" || m.ExpectedBoardRevision < 1 || m.ExpectedGraphRevision < 1 {
			return invalidWorkboard("create")
		}
	case workboard.MutationRevise:
		if !validWorkboardID(m.CardID) || m.ExpectedCardRevision < 1 || m.Patch.ParentID != nil && m.ExpectedGraphRevision < 1 {
			return invalidWorkboard("revise")
		}
	case workboard.MutationMove:
		if !validWorkboardID(m.CardID) || m.ExpectedBoardRevision < 1 || m.ExpectedCardRevision < 1 || m.ExpectedLayoutRevision < 1 {
			return invalidWorkboard("move")
		}
		if m.BeforeCardID != "" && m.AfterCardID != "" || m.BeforeCardID == m.CardID || m.AfterCardID == m.CardID ||
			m.BeforeCardID != "" && !validWorkboardID(m.BeforeCardID) || m.AfterCardID != "" && !validWorkboardID(m.AfterCardID) {
			return invalidWorkboard("move_anchor")
		}
	case workboard.MutationReorder:
		if !validWorkboardID(m.CardID) || m.ExpectedBoardRevision < 1 || m.ExpectedCardRevision < 1 || m.ExpectedLayoutRevision < 1 ||
			(m.BeforeCardID == "") == (m.AfterCardID == "") {
			return invalidWorkboard("reorder")
		}
	case workboard.MutationDependencyAdd, workboard.MutationDependencyRemove:
		if !validWorkboardID(m.CardID) || !validWorkboardID(m.DependencyID) || m.CardID == m.DependencyID || m.ExpectedCardRevision < 1 || m.ExpectedGraphRevision < 1 {
			return invalidWorkboard("dependency")
		}
	default:
		return invalidWorkboard("kind")
	}
	return nil
}

func cardMutationDigest(m workboard.CardMutation) (string, error) {
	m.RequestDigest, m.IdempotencyKey = "", ""
	return digestJSON(m)
}

func compareMutationFences(board workboard.Board, graphRevision int64, m workboard.CardMutation) error {
	checks := []struct {
		expected, actual int64
		field            string
	}{
		{m.ExpectedBoardRevision, board.Revision, "board_revision"},
		{m.ExpectedLayoutRevision, board.LayoutRevision, "layout_revision"},
		{m.ExpectedGraphRevision, graphRevision, "graph_revision"},
	}
	for _, check := range checks {
		if check.expected != 0 && check.expected != check.actual {
			return &workboard.Violation{Code: workboard.CodeStaleRevision, Field: check.field}
		}
	}
	return nil
}

func mutationCard(ctx context.Context, tx *sql.Tx, m workboard.CardMutation) (workboard.Card, storedWorkboardCard, bool, error) {
	if m.Kind == workboard.MutationCreate {
		return workboard.Card{}, storedWorkboardCard{}, false, nil
	}
	card, body, err := readStoredCard(ctx, tx, m.BoardID, m.CardID)
	if errors.Is(err, ErrWorkboardNotFound) {
		return workboard.Card{}, storedWorkboardCard{}, false, nil
	}
	if err != nil {
		return workboard.Card{}, storedWorkboardCard{}, false, err
	}
	if m.ExpectedCardRevision != 0 && card.Revision != m.ExpectedCardRevision {
		return workboard.Card{}, storedWorkboardCard{}, false, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	return card, body, true, nil
}

func newStoredCard(ctx context.Context, tx *sql.Tx, m workboard.CardMutation, board workboard.Board, graph workboard.Graph) (workboard.Card, storedWorkboardCard, error) {
	id := newWorkboardID()
	if id == "" {
		return workboard.Card{}, storedWorkboardCard{}, errors.New("secure identifier generation failed")
	}
	dependencies := append([]string{}, m.Create.Dependencies...)
	sort.Strings(dependencies)
	for _, nodeID := range append(dependencies, m.Create.ParentID) {
		if nodeID != "" {
			if err := requireGraphNode(ctx, tx, board.ID, nodeID, graph, "graph"); err != nil {
				return workboard.Card{}, storedWorkboardCard{}, err
			}
		}
	}
	rank, err := appendRank(ctx, tx, board.ID, workboard.Backlog)
	if err != nil {
		return workboard.Card{}, storedWorkboardCard{}, err
	}
	remaining, err := unresolvedDependencies(ctx, tx, board.ID, dependencies)
	if err != nil {
		return workboard.Card{}, storedWorkboardCard{}, err
	}
	card := workboard.Card{ID: id, BoardID: board.ID, State: workboard.Backlog, Rank: rank, Title: m.Create.Title, Description: m.Create.Description,
		Priority: m.Create.Priority, Labels: append([]string{}, m.Create.Labels...), AssigneeID: m.Create.AssigneeID, ParentID: m.Create.ParentID,
		Dependencies: dependencies, RemainingDependencies: remaining, CriteriaRevision: 1, Budget: m.Create.Budget,
		Criteria: append([]workboard.AcceptanceCriterion{}, m.Create.Criteria...)}
	body := storedWorkboardCard{Version: 1, ID: id, BoardID: board.ID, CriteriaRevision: 1, State: string(workboard.Backlog), Rank: rank,
		Title: card.Title, Description: card.Description, Priority: card.Priority, Labels: append([]string{}, card.Labels...), ParentID: card.ParentID,
		Dependencies: dependencies, RemainingDependencies: remaining, AssigneeID: card.AssigneeID, Budget: storedCardBudget(m.Create.Budget),
		Criteria: storedCardCriteria(m.Create.Criteria)}
	return card, body, nil
}

func mutateStoredCard(ctx context.Context, tx *sql.Tx, m workboard.CardMutation, card *workboard.Card, body *storedWorkboardCard, graph workboard.Graph) error {
	switch m.Kind {
	case workboard.MutationCreate:
		return nil
	case workboard.MutationRevise:
		applyPatch(card, body, m.Patch)
		if m.Patch.ParentID != nil && *m.Patch.ParentID != "" {
			if err := requireGraphNode(ctx, tx, m.BoardID, *m.Patch.ParentID, graph, "parent"); err != nil {
				return err
			}
		}
	case workboard.MutationMove:
		if card.CurrentClaimID != "" || card.State != workboard.Backlog && card.State != workboard.Ready || m.TargetState != workboard.Backlog && m.TargetState != workboard.Ready ||
			card.State == m.TargetState || m.TargetState == workboard.Ready && card.RemainingDependencies != 0 {
			return &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "state"}
		}
		card.State, body.State = m.TargetState, string(m.TargetState)
		var rank string
		var err error
		if m.BeforeCardID != "" || m.AfterCardID != "" {
			rank, err = reorderedRank(ctx, tx, m, m.TargetState)
		} else {
			rank, err = appendRank(ctx, tx, m.BoardID, m.TargetState)
		}
		if err != nil {
			return err
		}
		card.Rank, body.Rank = rank, rank
	case workboard.MutationReorder:
		rank, err := reorderedRank(ctx, tx, m, card.State)
		if err != nil {
			return err
		}
		card.Rank, body.Rank = rank, rank
	case workboard.MutationDependencyAdd:
		if containsString(card.Dependencies, m.DependencyID) {
			return invalidWorkboard("dependency")
		}
		if err := requireGraphNode(ctx, tx, m.BoardID, m.DependencyID, graph, "dependency"); err != nil {
			return err
		}
		card.Dependencies = append(card.Dependencies, m.DependencyID)
		sort.Strings(card.Dependencies)
		body.Dependencies = append([]string{}, card.Dependencies...)
	case workboard.MutationDependencyRemove:
		if !containsString(card.Dependencies, m.DependencyID) {
			return invalidWorkboard("dependency")
		}
		card.Dependencies = removeString(card.Dependencies, m.DependencyID)
		body.Dependencies = append([]string{}, card.Dependencies...)
	}
	if m.Kind == workboard.MutationDependencyAdd || m.Kind == workboard.MutationDependencyRemove {
		remaining, err := unresolvedDependencies(ctx, tx, m.BoardID, card.Dependencies)
		if err != nil {
			return err
		}
		card.RemainingDependencies, body.RemainingDependencies = remaining, remaining
		if card.State == workboard.Ready && remaining != 0 {
			return &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "dependencies"}
		}
	}
	return nil
}

func mutationChanges(m workboard.CardMutation) (graph, layout bool) {
	switch m.Kind {
	case workboard.MutationCreate:
		return true, true
	case workboard.MutationRevise:
		return m.Patch.ParentID != nil, false
	case workboard.MutationMove, workboard.MutationReorder:
		return false, true
	case workboard.MutationDependencyAdd, workboard.MutationDependencyRemove:
		return true, false
	}
	return false, false
}

func applyMutationToGraph(graph workboard.Graph, m workboard.CardMutation, card workboard.Card) workboard.Graph {
	graph.Nodes = append([]workboard.Node{}, graph.Nodes...)
	if m.Kind == workboard.MutationCreate {
		graph.Nodes = append(graph.Nodes, workboard.Node{ID: card.ID, BoardID: card.BoardID, ParentID: card.ParentID, Dependencies: append([]string{}, card.Dependencies...)})
		return graph
	}
	for index := range graph.Nodes {
		if graph.Nodes[index].ID != card.ID {
			continue
		}
		graph.Nodes[index].ParentID = card.ParentID
		graph.Nodes[index].Dependencies = append([]string{}, card.Dependencies...)
	}
	return graph
}

func graphDigestFor(graph workboard.Graph) (string, error) {
	nodes := append([]workboard.Node{}, graph.Nodes...)
	for index := range nodes {
		nodes[index].Dependencies = append([]string{}, nodes[index].Dependencies...)
		sort.Strings(nodes[index].Dependencies)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	if len(nodes) == 0 {
		return digestBytes([]byte("darwin.workboard.graph.v1:empty")), nil
	}
	body, err := json.Marshal(nodes)
	if err != nil {
		return "", err
	}
	return digestBytes(append([]byte("darwin.workboard.graph.v1:"), body...)), nil
}

func updateStoredBody(body storedWorkboardCard, card workboard.Card) storedWorkboardCard {
	body.ID, body.BoardID, body.Revision = card.ID, card.BoardID, card.Revision
	body.State, body.Rank, body.Title, body.Description, body.Priority = string(card.State), card.Rank, card.Title, card.Description, card.Priority
	body.Labels, body.AssigneeID, body.ParentID = append([]string{}, card.Labels...), card.AssigneeID, card.ParentID
	body.Dependencies, body.RemainingDependencies = append([]string{}, card.Dependencies...), card.RemainingDependencies
	body.CriteriaRevision, body.AttemptCount = card.CriteriaRevision, card.AttemptCount
	body.CurrentAttemptID, body.CurrentClaimID, body.AcceptanceID = card.CurrentAttemptID, card.CurrentClaimID, card.AcceptanceID
	body.BlockReason, body.CancelRequested, body.PauseRequested = card.BlockReason, card.CancelRequested, card.PauseRequested
	body.PausePhase = card.PausePhase
	body.Budget, body.Criteria = storedCardBudget(card.Budget), storedCardCriteria(card.Criteria)
	body.CreatedAt, body.UpdatedAt = card.CreatedAt, card.UpdatedAt
	return body
}

func applyPatch(card *workboard.Card, body *storedWorkboardCard, patch workboard.CardPatch) {
	if patch.Title != nil {
		card.Title, body.Title = *patch.Title, *patch.Title
	}
	if patch.Description != nil {
		card.Description, body.Description = *patch.Description, *patch.Description
	}
	if patch.Priority != nil {
		card.Priority, body.Priority = *patch.Priority, *patch.Priority
	}
	if patch.Labels != nil {
		card.Labels = append([]string{}, (*patch.Labels)...)
		body.Labels = append([]string{}, card.Labels...)
	}
	if patch.Budget != nil {
		card.Budget = *patch.Budget
		body.Budget = storedCardBudget(card.Budget)
	}
	if patch.AssigneeID != nil {
		card.AssigneeID, body.AssigneeID = *patch.AssigneeID, *patch.AssigneeID
	}
	if patch.ParentID != nil {
		card.ParentID, body.ParentID = *patch.ParentID, *patch.ParentID
	}
}

func writeCardProjection(ctx context.Context, tx *sql.Tx, m workboard.CardMutation, card workboard.Card, body storedWorkboardCard, bodyBytes []byte, sequence int64) error {
	if m.Kind == workboard.MutationCreate {
		_, err := tx.ExecContext(ctx, `INSERT INTO workboard_cards(id,board_id,revision,criteria_revision,state,rank,title,description,priority,parent_id,assignee_id,block_reason,
			remaining_dependencies,attempt_count,attempt_limit,time_limit_ms,token_limit,cost_micros,current_attempt_id,current_claim_id,acceptance_id,cancel_requested,pause_requested,created_at,updated_at,body)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, card.ID, card.BoardID, card.Revision, body.CriteriaRevision, card.State, card.Rank,
			card.Title, card.Description, card.Priority, nullable(card.ParentID), nullable(card.AssigneeID), nullable(body.BlockReason), card.RemainingDependencies,
			body.AttemptCount, body.Budget.AttemptLimit, body.Budget.TimeLimitMS, body.Budget.TokenLimit, body.Budget.CostMicros, nullable(body.CurrentAttemptID),
			nullable(card.CurrentClaimID), nullable(body.AcceptanceID), boolInt(body.CancelRequested), boolInt(body.PauseRequested), card.CreatedAt.UnixNano(), card.UpdatedAt.UnixNano(), bodyBytes)
		if err != nil {
			return err
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE workboard_cards SET revision=?,state=?,rank=?,title=?,description=?,priority=?,parent_id=?,assignee_id=?,
			remaining_dependencies=?,attempt_limit=?,time_limit_ms=?,token_limit=?,cost_micros=?,updated_at=?,body=? WHERE board_id=? AND id=? AND revision=?`,
			card.Revision, card.State, card.Rank, card.Title, card.Description, card.Priority, nullable(card.ParentID), nullable(card.AssigneeID),
			card.RemainingDependencies, card.Budget.AttemptLimit, card.Budget.TimeLimitMS, card.Budget.TokenLimit, card.Budget.CostMicros, card.UpdatedAt.UnixNano(), bodyBytes,
			card.BoardID, card.ID, card.Revision-1)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
		}
	}
	if m.Kind == workboard.MutationCreate || m.Kind == workboard.MutationRevise && m.Patch.Labels != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workboard_card_labels WHERE board_id=? AND card_id=?`, card.BoardID, card.ID); err != nil {
			return err
		}
		for ordinal, label := range card.Labels {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workboard_card_labels(board_id,card_id,ordinal,label,label_key) VALUES(?,?,?,?,?)`, card.BoardID, card.ID, ordinal, label, strings.ToLower(strings.TrimSpace(label))); err != nil {
				return err
			}
		}
	}
	if m.Kind == workboard.MutationCreate || m.Kind == workboard.MutationDependencyAdd || m.Kind == workboard.MutationDependencyRemove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workboard_dependencies WHERE board_id=? AND card_id=?`, card.BoardID, card.ID); err != nil {
			return err
		}
		for _, dependencyID := range card.Dependencies {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workboard_dependencies(board_id,card_id,dependency_id,created_sequence) VALUES(?,?,?,?)`, card.BoardID, card.ID, dependencyID, sequence); err != nil {
				return err
			}
		}
	}
	if m.Kind == workboard.MutationCreate {
		for ordinal, criterion := range body.Criteria {
			criterionBody, err := json.Marshal(criterion)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_criteria(board_id,card_id,criteria_revision,ordinal,id,kind,required_source,validator_id,description,required,body)
				VALUES(?,?,?,?,?,?,?,?,?,?,?)`, card.BoardID, card.ID, body.CriteriaRevision, ordinal, criterion.ID, criterion.Kind,
				criterion.RequiredSource, criterion.ValidatorID, criterion.Description, boolInt(criterion.Required), criterionBody); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendRank(ctx context.Context, tx *sql.Tx, boardID string, state workboard.State) (string, error) {
	var last string
	err := tx.QueryRowContext(ctx, `SELECT rank FROM workboard_cards WHERE board_id=? AND state=? ORDER BY rank DESC LIMIT 1`, boardID, state).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return formatWorkboardRank(workboardRankStep), nil
	}
	if err != nil {
		return "", err
	}
	value, err := parseWorkboardRank(last)
	if err != nil || ^uint64(0)-value < workboardRankStep {
		return "", &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "rank"}
	}
	return formatWorkboardRank(value + workboardRankStep), nil
}

func reorderedRank(ctx context.Context, tx *sql.Tx, m workboard.CardMutation, state workboard.State) (string, error) {
	anchorID, before := m.AfterCardID, false
	if m.BeforeCardID != "" {
		anchorID, before = m.BeforeCardID, true
	}
	var anchorState, anchorRank string
	if err := tx.QueryRowContext(ctx, `SELECT state,rank FROM workboard_cards WHERE board_id=? AND id=?`, m.BoardID, anchorID).Scan(&anchorState, &anchorRank); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrWorkboardNotFound
		}
		return "", err
	}
	if anchorState != string(state) {
		return "", invalidWorkboard("reorder_state")
	}
	anchor, err := parseWorkboardRank(anchorRank)
	if err != nil {
		return "", err
	}
	var neighborRank string
	operator, order := "<", "DESC"
	if !before {
		operator, order = ">", "ASC"
	}
	query := `SELECT rank FROM workboard_cards WHERE board_id=? AND state=? AND id!=? AND rank ` + operator + ` ? ORDER BY rank ` + order + ` LIMIT 1`
	err = tx.QueryRowContext(ctx, query, m.BoardID, state, m.CardID, anchorRank).Scan(&neighborRank)
	if errors.Is(err, sql.ErrNoRows) {
		if before {
			if anchor < 2 {
				return "", &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "rank"}
			}
			return formatWorkboardRank(anchor / 2), nil
		}
		if ^uint64(0)-anchor < workboardRankStep {
			return "", &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "rank"}
		}
		return formatWorkboardRank(anchor + workboardRankStep), nil
	}
	if err != nil {
		return "", err
	}
	neighbor, err := parseWorkboardRank(neighborRank)
	if err != nil {
		return "", err
	}
	low, high := neighbor, anchor
	if !before {
		low, high = anchor, neighbor
	}
	if high-low < 2 {
		return "", &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "rank"}
	}
	return formatWorkboardRank(low + (high-low)/2), nil
}

func unresolvedDependencies(ctx context.Context, tx *sql.Tx, boardID string, dependencies []string) (int, error) {
	remaining := 0
	for _, id := range dependencies {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM workboard_cards WHERE board_id=? AND id=?`, boardID, id).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, &workboard.Violation{Code: workboard.CodeMissingNode, Field: "dependency"}
			}
			return 0, err
		}
		if state != string(workboard.Done) {
			remaining++
		}
	}
	return remaining, nil
}

func graphHasNode(graph workboard.Graph, id string) bool {
	for _, node := range graph.Nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

func requireGraphNode(ctx context.Context, tx *sql.Tx, boardID, id string, graph workboard.Graph, field string) error {
	if graphHasNode(graph, id) {
		return nil
	}
	var actualBoard string
	err := tx.QueryRowContext(ctx, `SELECT board_id FROM workboard_cards WHERE id=?`, id).Scan(&actualBoard)
	if errors.Is(err, sql.ErrNoRows) {
		return &workboard.Violation{Code: workboard.CodeMissingNode, Field: field}
	}
	if err != nil {
		return err
	}
	if actualBoard != boardID {
		return &workboard.Violation{Code: workboard.CodeCrossBoard, Field: field}
	}
	return ErrWorkboardCorrupt
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeString(values []string, target string) []string {
	result := make([]string, 0, len(values)-1)
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolDelta(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func normalizeCardWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "workboard card limit"):
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "cards"}
	case strings.Contains(message, "workboard dependency limit"):
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "dependencies"}
	case strings.Contains(message, "workboard reverse fanout limit"):
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "reverse_fanout"}
	}
	return err
}
