package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

const workboardRankStep uint64 = 1_000_000_000_000

type cardStoreCursor struct {
	Version        int    `json:"v"`
	BoardID        string `json:"b"`
	BoardRevision  int64  `json:"br"`
	LayoutRevision int64  `json:"lr"`
	GraphRevision  int64  `json:"gr"`
	State          string `json:"s,omitempty"`
	HasParent      bool   `json:"hp,omitempty"`
	ParentID       string `json:"p,omitempty"`
	HasAssignee    bool   `json:"ha,omitempty"`
	AssigneeID     string `json:"a,omitempty"`
	Label          string `json:"l,omitempty"`
	Column         int    `json:"c"`
	Rank           string `json:"r"`
	ID             string `json:"i"`
}

// GetCard reads one card and verifies its normalized labels, dependencies and
// criteria against the canonical durable body before returning it.
func (s *Store) GetCard(ctx context.Context, boardID, cardID string) (workboard.Card, error) {
	if !validWorkboardID(boardID) || !validWorkboardID(cardID) {
		return workboard.Card{}, invalidWorkboard("card")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.Card{}, err
	}
	defer tx.Rollback()
	card, _, err := readStoredCard(ctx, tx, boardID, cardID)
	if err != nil {
		return workboard.Card{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.Card{}, err
	}
	return card, nil
}

// ListCards returns a bounded, authenticated page tied to all board revisions.
// A board mutation invalidates an outstanding cursor rather than skipping or
// duplicating cards across pages.
func (s *Store) ListCards(ctx context.Context, boardID string, filter workboard.CardFilter) (workboard.CardPage, error) {
	if !validWorkboardID(boardID) || filter.Limit < 1 || filter.Limit > workboard.MaxPageItems {
		return workboard.CardPage{}, invalidWorkboard("list")
	}
	cursor, err := s.decodeCardStoreCursor(filter.After)
	if err != nil {
		return workboard.CardPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.CardPage{}, err
	}
	defer tx.Rollback()
	board, graphRevision, _, err := readBoardRow(ctx, tx, boardID)
	if err != nil {
		return workboard.CardPage{}, err
	}
	if filter.After != "" && !cursor.matches(board, graphRevision, boardID, filter) {
		return workboard.CardPage{}, ErrWorkboardCursor
	}
	parent, hasParent := filterValue(filter.ParentID)
	assignee, hasAssignee := filterValue(filter.AssigneeID)
	rows, err := tx.QueryContext(ctx, `SELECT c.body,col.ordinal,col.state,c.id,c.revision,c.criteria_revision,c.state,c.rank,c.title,c.description,c.priority,
		c.parent_id,c.assignee_id,c.block_reason,c.remaining_dependencies,c.attempt_count,c.attempt_limit,c.time_limit_ms,c.token_limit,c.cost_micros,
		c.current_attempt_id,c.current_claim_id,c.acceptance_id,c.cancel_requested,c.pause_requested,c.created_at,c.updated_at
		FROM workboard_cards c JOIN workboard_columns col ON col.board_id=c.board_id AND col.state=c.state
		WHERE c.board_id=? AND (col.ordinal>? OR (col.ordinal=? AND c.rank>?) OR (col.ordinal=? AND c.rank=? AND c.id>?))
		AND (?='' OR c.state=?)
		AND (?=0 OR (?='' AND c.parent_id IS NULL) OR c.parent_id=?)
		AND (?=0 OR (?='' AND c.assignee_id IS NULL) OR c.assignee_id=?)
		AND (?='' OR EXISTS(SELECT 1 FROM workboard_card_labels l WHERE l.board_id=c.board_id AND l.card_id=c.id AND l.label_key=?))
		ORDER BY col.ordinal,c.rank,c.id LIMIT ?`, boardID, cursor.Column, cursor.Column, cursor.Rank, cursor.Column, cursor.Rank, cursor.ID,
		string(filter.State), string(filter.State), hasParent, parent, parent, hasAssignee, assignee, assignee,
		strings.ToLower(strings.TrimSpace(filter.Label)), strings.ToLower(strings.TrimSpace(filter.Label)), filter.Limit+1)
	if err != nil {
		return workboard.CardPage{}, err
	}
	defer rows.Close()
	items := make([]workboard.Card, 0, filter.Limit+1)
	positions := make([]cardStoreCursor, 0, filter.Limit+1)
	for rows.Next() {
		card, position, scanErr := scanWorkboardCard(rows, boardID)
		if scanErr != nil {
			return workboard.CardPage{}, scanErr
		}
		if scanErr = verifyStoredCardRelations(ctx, tx, card.ID, boardID, card); scanErr != nil {
			return workboard.CardPage{}, scanErr
		}
		items = append(items, card)
		positions = append(positions, cardStoreCursor{Column: position.Column, Rank: position.Rank, ID: position.ID})
	}
	if err = rows.Err(); err != nil {
		return workboard.CardPage{}, err
	}
	page := workboard.CardPage{Items: items}
	if len(items) > filter.Limit {
		page.Items, page.HasMore = items[:filter.Limit], true
		cursor = positions[filter.Limit-1]
		cursor.Version, cursor.BoardID, cursor.BoardRevision = 1, boardID, board.Revision
		cursor.LayoutRevision, cursor.GraphRevision, cursor.State = board.LayoutRevision, graphRevision, string(filter.State)
		cursor.HasParent, cursor.ParentID = hasParent != 0, parent
		cursor.HasAssignee, cursor.AssigneeID = hasAssignee != 0, assignee
		cursor.Label = strings.ToLower(strings.TrimSpace(filter.Label))
		page.NextCursor, err = s.encodeCardStoreCursor(cursor)
		if err != nil {
			return workboard.CardPage{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return workboard.CardPage{}, err
	}
	return page, nil
}

// LoadGraph reconstructs and validates the complete normalized graph.
func (s *Store) LoadGraph(ctx context.Context, boardID string) (workboard.Graph, error) {
	if !validWorkboardID(boardID) {
		return workboard.Graph{}, invalidWorkboard("board_id")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.Graph{}, err
	}
	defer tx.Rollback()
	_, graphRevision, graphDigest, err := readBoardRow(ctx, tx, boardID)
	if err != nil {
		return workboard.Graph{}, err
	}
	graph, digestValue, err := loadGraphTx(ctx, tx, boardID, graphRevision)
	if err != nil {
		return workboard.Graph{}, err
	}
	if digestValue != graphDigest {
		return workboard.Graph{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.Graph{}, err
	}
	return graph, nil
}

func readStoredCard(ctx context.Context, tx *sql.Tx, boardID, cardID string) (workboard.Card, storedWorkboardCard, error) {
	row := tx.QueryRowContext(ctx, `SELECT c.body,col.ordinal,col.state,c.id,c.revision,c.criteria_revision,c.state,c.rank,c.title,c.description,c.priority,
		c.parent_id,c.assignee_id,c.block_reason,c.remaining_dependencies,c.attempt_count,c.attempt_limit,c.time_limit_ms,c.token_limit,c.cost_micros,
		c.current_attempt_id,c.current_claim_id,c.acceptance_id,c.cancel_requested,c.pause_requested,c.created_at,c.updated_at
		FROM workboard_cards c JOIN workboard_columns col ON col.board_id=c.board_id AND col.state=c.state WHERE c.board_id=? AND c.id=?`, boardID, cardID)
	card, _, err := scanWorkboardCard(row, boardID)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.Card{}, storedWorkboardCard{}, ErrWorkboardNotFound
	}
	if err != nil {
		return workboard.Card{}, storedWorkboardCard{}, err
	}
	var bodyBytes []byte
	if err = tx.QueryRowContext(ctx, `SELECT body FROM workboard_cards WHERE board_id=? AND id=?`, boardID, cardID).Scan(&bodyBytes); err != nil {
		return workboard.Card{}, storedWorkboardCard{}, err
	}
	var body storedWorkboardCard
	if strictJSON(bodyBytes, &body) != nil || verifyStoredCardRelations(ctx, tx, cardID, boardID, card) != nil {
		return workboard.Card{}, storedWorkboardCard{}, ErrWorkboardCorrupt
	}
	return card, body, nil
}

func verifyStoredCardRelations(ctx context.Context, tx *sql.Tx, cardID, boardID string, card workboard.Card) error {
	var bodyBytes []byte
	if err := tx.QueryRowContext(ctx, `SELECT body FROM workboard_cards WHERE board_id=? AND id=?`, boardID, cardID).Scan(&bodyBytes); err != nil {
		return err
	}
	var body storedWorkboardCard
	if strictJSON(bodyBytes, &body) != nil || body.ID != card.ID || body.BoardID != card.BoardID {
		return ErrWorkboardCorrupt
	}
	labels, err := readStrings(ctx, tx, `SELECT label FROM workboard_card_labels WHERE board_id=? AND card_id=? ORDER BY ordinal`, boardID, cardID)
	if err != nil || !equalStrings(labels, card.Labels) || !equalStrings(labels, body.Labels) {
		return ErrWorkboardCorrupt
	}
	deps, err := readStrings(ctx, tx, `SELECT dependency_id FROM workboard_dependencies WHERE board_id=? AND card_id=? ORDER BY dependency_id`, boardID, cardID)
	if err != nil {
		return err
	}
	want := append([]string{}, card.Dependencies...)
	sort.Strings(want)
	bodyDeps := append([]string{}, body.Dependencies...)
	sort.Strings(bodyDeps)
	if !equalStrings(deps, want) || !equalStrings(deps, bodyDeps) {
		return ErrWorkboardCorrupt
	}
	criteriaRows, err := tx.QueryContext(ctx, `SELECT ordinal,id,kind,required_source,validator_id,description,required,body
		FROM workboard_criteria WHERE board_id=? AND card_id=? AND criteria_revision=? ORDER BY ordinal`, boardID, cardID, body.CriteriaRevision)
	if err != nil {
		return err
	}
	defer criteriaRows.Close()
	criteria := []storedWorkboardCriterion{}
	for criteriaRows.Next() {
		var ordinal, required int
		criterion := storedWorkboardCriterion{Version: 1}
		var criterionBody []byte
		if err = criteriaRows.Scan(&ordinal, &criterion.ID, &criterion.Kind, &criterion.RequiredSource, &criterion.ValidatorID, &criterion.Description, &required, &criterionBody); err != nil {
			return err
		}
		var canonical storedWorkboardCriterion
		criterion.Required = required == 1
		if ordinal != len(criteria) || strictJSON(criterionBody, &canonical) != nil || canonical != criterion {
			return ErrWorkboardCorrupt
		}
		criteria = append(criteria, criterion)
	}
	if err = criteriaRows.Err(); err != nil {
		return err
	}
	if !reflect.DeepEqual(criteria, body.Criteria) {
		return ErrWorkboardCorrupt
	}
	return nil
}

func readStrings(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadGraphTx(ctx context.Context, tx *sql.Tx, boardID string, graphRevision int64) (workboard.Graph, string, error) {
	var layoutRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT layout_revision FROM workboard_boards WHERE id=?`, boardID).Scan(&layoutRevision); err != nil {
		return workboard.Graph{}, "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(parent_id,'') FROM workboard_cards WHERE board_id=? ORDER BY id`, boardID)
	if err != nil {
		return workboard.Graph{}, "", err
	}
	nodes := []workboard.Node{}
	for rows.Next() {
		var node workboard.Node
		node.BoardID = boardID
		if err = rows.Scan(&node.ID, &node.ParentID); err != nil {
			rows.Close()
			return workboard.Graph{}, "", err
		}
		node.Dependencies, err = readStrings(ctx, tx, `SELECT dependency_id FROM workboard_dependencies WHERE board_id=? AND card_id=? ORDER BY dependency_id`, boardID, node.ID)
		if err != nil {
			rows.Close()
			return workboard.Graph{}, "", err
		}
		nodes = append(nodes, node)
	}
	if err = rows.Close(); err != nil {
		return workboard.Graph{}, "", err
	}
	graph := workboard.Graph{BoardID: boardID, GraphRevision: graphRevision, LayoutRevision: layoutRevision, Nodes: nodes}
	if _, err = workboard.ValidateGraph(graph, graphRevision); err != nil {
		return workboard.Graph{}, "", ErrWorkboardCorrupt
	}
	if len(nodes) == 0 {
		return graph, digestBytes([]byte("darwin.workboard.graph.v1:empty")), nil
	}
	body, err := json.Marshal(nodes)
	if err != nil {
		return workboard.Graph{}, "", err
	}
	return graph, digestBytes(append([]byte("darwin.workboard.graph.v1:"), body...)), nil
}

func formatWorkboardRank(value uint64) string { return fmt.Sprintf("%020d", value) }

func parseWorkboardRank(value string) (uint64, error) {
	if len(value) != 20 {
		return 0, ErrWorkboardCorrupt
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, ErrWorkboardCorrupt
	}
	return parsed, nil
}

func filterValue(value *string) (string, int) {
	if value == nil {
		return "", 0
	}
	return *value, 1
}

func (c cardStoreCursor) matches(board workboard.Board, graphRevision int64, boardID string, filter workboard.CardFilter) bool {
	parent, hp := filterValue(filter.ParentID)
	assignee, ha := filterValue(filter.AssigneeID)
	return c.BoardID == boardID && c.BoardRevision == board.Revision && c.LayoutRevision == board.LayoutRevision && c.GraphRevision == graphRevision &&
		c.State == string(filter.State) && c.HasParent == (hp != 0) && c.ParentID == parent && c.HasAssignee == (ha != 0) && c.AssigneeID == assignee &&
		c.Label == strings.ToLower(strings.TrimSpace(filter.Label))
}

func (s *Store) encodeCardStoreCursor(cursor cardStoreCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeCardStoreCursor(value string) (cardStoreCursor, error) {
	if value == "" {
		return cardStoreCursor{Column: -1}, nil
	}
	body, err := s.verifyWorkboardCursor(value)
	if err != nil {
		return cardStoreCursor{}, ErrWorkboardCursor
	}
	var cursor cardStoreCursor
	if strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validWorkboardID(cursor.BoardID) || cursor.BoardRevision < 1 ||
		cursor.LayoutRevision < 1 || cursor.GraphRevision < 1 || cursor.Column < 0 || cursor.Column > 6 || !validWorkboardID(cursor.ID) ||
		len(cursor.Rank) < 1 || len(cursor.Rank) > workboard.MaxRankBytes || cursor.HasParent && cursor.ParentID != "" && !validWorkboardID(cursor.ParentID) ||
		cursor.HasAssignee && cursor.AssigneeID != "" && !validWorkboardID(cursor.AssigneeID) {
		return cardStoreCursor{}, ErrWorkboardCursor
	}
	canonical, _ := s.encodeCardStoreCursor(cursor)
	if canonical != value {
		return cardStoreCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
