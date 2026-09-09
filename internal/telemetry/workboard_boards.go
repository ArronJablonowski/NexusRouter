package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

var (
	ErrWorkboardNotFound = errors.New("workboard not found")
	ErrWorkboardCorrupt  = errors.New("workboard durable state is corrupt")
	ErrWorkboardCursor   = errors.New("invalid workboard cursor")
)

var workboardIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type cardPageCursor struct {
	Version        int    `json:"v"`
	BoardID        string `json:"b"`
	BoardRevision  int64  `json:"br"`
	LayoutRevision int64  `json:"lr"`
	GraphRevision  int64  `json:"gr"`
	State          string `json:"s,omitempty"`
	AssigneeID     string `json:"a,omitempty"`
	OwnerID        string `json:"o,omitempty"`
	ClaimState     string `json:"q,omitempty"`
	Column         int    `json:"c"`
	Rank           string `json:"r"`
	ID             string `json:"i"`
}

type boardPageCursor struct {
	Version int    `json:"v"`
	High    int64  `json:"h"`
	After   int64  `json:"a"`
	State   string `json:"s,omitempty"`
}

var canonicalWorkboardColumns = []struct {
	state workboard.State
	title string
}{
	{workboard.Backlog, "Backlog"},
	{workboard.Ready, "Ready"},
	{workboard.InProgress, "In Progress"},
	{workboard.Blocked, "Blocked"},
	{workboard.Review, "Review"},
	{workboard.Done, "Done"},
	{workboard.Canceled, "Canceled"},
}

// CreateWorkboard atomically creates the board projection, canonical columns,
// immutable event, and committed replay receipt. The raw idempotency key is
// never persisted.
func (s *Store) CreateWorkboard(ctx context.Context, scope string, request workboard.CreateBoardRequest, actor workboard.Actor, now time.Time) (workboard.OperationReceipt, error) {
	if request.Validate() != nil || !validWorkboardID(scope) || actor.Validate() != nil {
		return workboard.OperationReceipt{}, invalidWorkboard("request")
	}
	now = now.UTC()
	if now.Year() < 1970 || now.Year() >= 2261 {
		return workboard.OperationReceipt{}, invalidWorkboard("created_at")
	}
	requestDigest, err := digestJSON(struct {
		Version     int             `json:"version"`
		Action      string          `json:"action"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Actor       workboard.Actor `json:"actor"`
	}{request.Version, string(workboard.BoardCreateAction), request.Title, request.Description, actor})
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	keyDigest := digestBytes([]byte(request.IdempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if receipt, found, replayErr := readWorkboardReceipt(ctx, tx, "creation", scope, keyDigest, requestDigest); found || replayErr != nil {
		return receipt, replayErr
	}

	boardID, operationID, eventID, err := newWorkboardIDs()
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	board := workboard.Board{
		Version: 1, ID: boardID, Revision: 1, LayoutRevision: 1, EventSequence: 1,
		State: "active", Title: request.Title, Description: request.Description, CardCount: 0, ActiveClaims: 0,
		CreatedAt: now, UpdatedAt: now,
	}
	if board.Validate() != nil {
		return workboard.OperationReceipt{}, invalidWorkboard("board")
	}
	graphDigest := digestBytes([]byte("darwin.workboard.graph.v1:empty"))
	boardBody, err := json.Marshal(board)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	event := workboard.BoardEvent{Version: 1, ID: eventID, BoardID: boardID, Sequence: 1, OperationID: operationID, Kind: workboard.BoardCreateAction, ActorID: actor.ID, ActorType: actor.Type, CreatedAt: now}
	eventBody, err := json.Marshal(event)
	if err != nil || event.Validate() != nil || len(eventBody) == 0 || len(eventBody) > workboard.MaxTransactionBytes {
		return workboard.OperationReceipt{}, invalidWorkboard("event")
	}
	receipt := workboard.OperationReceipt{
		Version: 1, BoardID: boardID, OperationID: operationID, RequestDigest: requestDigest,
		FirstSequence: 1, LastSequence: 1, EventCount: 1,
		BoardRevision: 1, Outcome: "committed", CreatedAt: now,
	}
	columns, columnBytes, err := newCanonicalColumns(boardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	response, err := finalizeWorkboardReceipt(&receipt, len(boardBody)+len(eventBody)+columnBytes)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_boards
		(id,revision,layout_revision,event_sequence,graph_revision,graph_digest,state,title,description,card_count,active_claims,created_at,updated_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, board.ID, 1, 1, 1, 1, graphDigest, board.State, board.Title, board.Description, 0, 0, now.UnixNano(), now.UnixNano(), boardBody); err != nil {
		return workboard.OperationReceipt{}, normalizeWorkboardWriteError(err)
	}
	for ordinal, column := range columns {
		if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_columns(board_id,version,id,state,ordinal,rank,title) VALUES(?,?,?,?,?,?,?)`, boardID, column.Version, column.ID, string(column.State), ordinal, column.Rank, column.Title); err != nil {
			return workboard.OperationReceipt{}, err
		}
	}
	if err = insertWorkboardOperation(ctx, tx, "creation", scope, keyDigest, receipt, response); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = insertWorkboardEvent(ctx, tx, eventID, boardID, 1, operationID, string(workboard.BoardCreateAction), actor, now, eventBody); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

// ArchiveWorkboard archives an unclaimed active board behind a board revision
// fence. Exact retries replay before checking the now-stale revision.
func (s *Store) ArchiveWorkboard(ctx context.Context, request workboard.ArchiveBoardRequest, actor workboard.Actor, now time.Time) (workboard.OperationReceipt, error) {
	if request.Validate() != nil || actor.Validate() != nil {
		return workboard.OperationReceipt{}, invalidWorkboard("request")
	}
	now = now.UTC()
	if now.Year() < 1970 || now.Year() >= 2261 {
		return workboard.OperationReceipt{}, invalidWorkboard("created_at")
	}
	requestDigest, err := digestJSON(struct {
		Version          int             `json:"version"`
		Action           string          `json:"action"`
		BoardID          string          `json:"board_id"`
		ExpectedRevision int64           `json:"expected_board_revision"`
		Actor            workboard.Actor `json:"actor"`
	}{request.Version, string(workboard.BoardArchiveAction), request.BoardID, request.ExpectedRevision, actor})
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	keyDigest := digestBytes([]byte(request.IdempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if receipt, found, replayErr := readWorkboardReceipt(ctx, tx, "board", request.BoardID, keyDigest, requestDigest); found || replayErr != nil {
		return receipt, replayErr
	}
	board, _, _, err := readBoardRow(ctx, tx, request.BoardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if board.Revision != request.ExpectedRevision {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if board.State != "active" || board.ActiveClaims != 0 {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "board_state"}
	}
	operationID, eventID, err := newWorkboardID(), newWorkboardID(), error(nil)
	if operationID == "" || eventID == "" {
		err = errors.New("secure identifier generation failed")
	}
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	board.Revision++
	board.EventSequence++
	board.State = "archived"
	board.UpdatedAt = now
	if board.Validate() != nil {
		return workboard.OperationReceipt{}, ErrWorkboardCorrupt
	}
	boardBody, err := json.Marshal(board)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	event := workboard.BoardEvent{Version: 1, ID: eventID, BoardID: board.ID, Sequence: board.EventSequence, OperationID: operationID, Kind: workboard.BoardArchiveAction, ActorID: actor.ID, ActorType: actor.Type, CreatedAt: now}
	eventBody, err := json.Marshal(event)
	if err != nil || event.Validate() != nil || len(eventBody) == 0 || len(eventBody) > workboard.MaxTransactionBytes {
		return workboard.OperationReceipt{}, invalidWorkboard("event")
	}
	receipt := workboard.OperationReceipt{
		Version: 1, BoardID: board.ID, OperationID: operationID, RequestDigest: requestDigest,
		FirstSequence: board.EventSequence, LastSequence: board.EventSequence, EventCount: 1,
		BoardRevision: board.Revision, Outcome: "committed", CreatedAt: now,
	}
	response, err := finalizeWorkboardReceipt(&receipt, len(boardBody)+len(eventBody))
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=?,event_sequence=?,state=?,updated_at=?,body=? WHERE id=? AND revision=? AND state='active' AND active_claims=0`, board.Revision, board.EventSequence, board.State, now.UnixNano(), boardBody, board.ID, request.ExpectedRevision)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if err = insertWorkboardOperation(ctx, tx, "board", board.ID, keyDigest, receipt, response); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = insertWorkboardEvent(ctx, tx, eventID, board.ID, board.EventSequence, operationID, string(workboard.BoardArchiveAction), actor, now, eventBody); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

// ListWorkboards returns a stable ID-ordered bounded page.
func (s *Store) ListWorkboards(ctx context.Context, options workboard.BoardListOptions) (workboard.BoardPage, error) {
	if options.Validate() != nil {
		return workboard.BoardPage{}, ErrWorkboardCursor
	}
	cursor, err := s.decodeBoardCursor(options.After)
	if err != nil || options.After != "" && cursor.State != options.State {
		return workboard.BoardPage{}, ErrWorkboardCursor
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.BoardPage{}, err
	}
	defer tx.Rollback()
	if options.After == "" {
		cursor = boardPageCursor{Version: 1, State: options.State}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM workboard_boards WHERE (?='' OR state=?)`, options.State, options.State).Scan(&cursor.High); err != nil {
			return workboard.BoardPage{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid,id,revision,layout_revision,event_sequence,state,title,description,card_count,active_claims,created_at,updated_at,body,graph_revision,graph_digest
		FROM workboard_boards WHERE rowid>? AND rowid<=? AND (?='' OR state=?) ORDER BY rowid LIMIT ?`, cursor.After, cursor.High, options.State, options.State, options.Limit+1)
	if err != nil {
		return workboard.BoardPage{}, err
	}
	defer rows.Close()
	items := make([]workboard.Board, 0, options.Limit)
	rowIDs := make([]int64, 0, options.Limit+1)
	for rows.Next() {
		var rowID int64
		board, err := scanBoardAfterRowID(rows, &rowID)
		if err != nil {
			return workboard.BoardPage{}, err
		}
		items = append(items, board)
		rowIDs = append(rowIDs, rowID)
	}
	if err = rows.Err(); err != nil {
		return workboard.BoardPage{}, err
	}
	page := workboard.BoardPage{Version: 1, Items: items}
	if len(page.Items) > options.Limit {
		page.Items = page.Items[:options.Limit]
		page.HasMore = true
		cursor.After = rowIDs[options.Limit-1]
		page.NextCursor, err = s.encodeBoardCursor(cursor)
		if err != nil {
			return workboard.BoardPage{}, err
		}
	}
	if page.Validate() != nil {
		return workboard.BoardPage{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.BoardPage{}, err
	}
	return page, nil
}

// ReadWorkboard reconstructs a bounded board projection and canonical columns.
func (s *Store) ReadWorkboard(ctx context.Context, boardID string, options workboard.BoardSnapshotOptions) (workboard.BoardSnapshot, error) {
	if !validWorkboardID(boardID) || options.Validate() != nil {
		return workboard.BoardSnapshot{}, invalidWorkboard("request")
	}
	cursor, err := s.decodeCardCursor(options.After)
	if err != nil {
		return workboard.BoardSnapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.BoardSnapshot{}, err
	}
	defer tx.Rollback()
	board, graphRevision, graphDigest, err := readBoardRow(ctx, tx, boardID)
	if err != nil {
		return workboard.BoardSnapshot{}, err
	}
	columns, err := readWorkboardColumns(ctx, tx, boardID)
	if err != nil {
		return workboard.BoardSnapshot{}, err
	}
	if options.After != "" && (cursor.BoardID != boardID || cursor.BoardRevision != board.Revision || cursor.LayoutRevision != board.LayoutRevision ||
		cursor.GraphRevision != graphRevision || cursor.State != string(options.State) || cursor.AssigneeID != options.AssigneeID ||
		cursor.OwnerID != options.OwnerID || cursor.ClaimState != options.ClaimState) {
		return workboard.BoardSnapshot{}, ErrWorkboardCursor
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.body,col.ordinal,col.state,c.id,c.revision,c.criteria_revision,c.state,c.rank,c.title,c.description,c.priority,
		c.parent_id,c.assignee_id,c.block_reason,c.remaining_dependencies,c.attempt_count,c.attempt_limit,c.time_limit_ms,c.token_limit,c.cost_micros,
		c.current_attempt_id,c.current_claim_id,c.acceptance_id,c.cancel_requested,c.pause_requested,c.created_at,c.updated_at
		FROM workboard_cards c JOIN workboard_columns col ON col.board_id=c.board_id AND col.state=c.state
		LEFT JOIN workboard_claims claim ON claim.board_id=c.board_id AND claim.card_id=c.id AND claim.id=c.current_claim_id
		WHERE c.board_id=? AND (col.ordinal>? OR (col.ordinal=? AND c.rank>?) OR (col.ordinal=? AND c.rank=? AND c.id>?))
		AND (?='' OR c.state=?)
		AND (?='' OR (?='unassigned' AND c.assignee_id IS NULL) OR c.assignee_id=?)
		AND (?='' OR (?='unassigned' AND claim.owner_id IS NULL) OR claim.owner_id=?)
		AND (?='' OR (?='unclaimed' AND claim.id IS NULL) OR claim.state=?)
		ORDER BY col.ordinal,c.rank,c.id LIMIT ?`, boardID, cursor.Column, cursor.Column, cursor.Rank, cursor.Column, cursor.Rank, cursor.ID,
		options.State, options.State, options.AssigneeID, options.AssigneeID, options.AssigneeID,
		options.OwnerID, options.OwnerID, options.OwnerID, options.ClaimState, options.ClaimState, options.ClaimState, options.Limit+1)
	if err != nil {
		return workboard.BoardSnapshot{}, err
	}
	defer rows.Close()
	cards := make([]workboard.Card, 0, options.Limit+1)
	positions := make([]cardPageCursor, 0, options.Limit+1)
	for rows.Next() {
		card, position, scanErr := scanWorkboardCard(rows, boardID)
		if scanErr != nil {
			return workboard.BoardSnapshot{}, scanErr
		}
		cards = append(cards, card)
		positions = append(positions, position)
	}
	if err = rows.Err(); err != nil {
		return workboard.BoardSnapshot{}, err
	}
	snapshot := workboard.BoardSnapshot{Version: 1, Board: board, Columns: columns, Cards: cards, GraphRevision: graphRevision, GraphDigest: graphDigest}
	if len(cards) > options.Limit {
		snapshot.Cards = cards[:options.Limit]
		snapshot.HasMore = true
		position := positions[options.Limit-1]
		position.Version, position.BoardID, position.BoardRevision = 1, boardID, board.Revision
		position.LayoutRevision, position.GraphRevision = board.LayoutRevision, graphRevision
		position.State, position.AssigneeID, position.OwnerID, position.ClaimState = string(options.State), options.AssigneeID, options.OwnerID, options.ClaimState
		snapshot.NextCursor, err = s.encodeCardCursor(position)
		if err != nil {
			return workboard.BoardSnapshot{}, err
		}
	}
	if snapshot.Validate() != nil {
		return workboard.BoardSnapshot{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.BoardSnapshot{}, err
	}
	return snapshot, nil
}

func reserveWorkboardWriter(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=revision WHERE id=''`)
	return err
}

func insertWorkboardOperation(ctx context.Context, tx *sql.Tx, scopeKind, scopeID, keyDigest string, receipt workboard.OperationReceipt, response []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workboard_operations(scope_kind,scope_id,key_digest,operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,'committed',?,?)`, scopeKind, scopeID, keyDigest, receipt.OperationID, receipt.BoardID, receipt.RequestDigest, receipt.ResponseDigest, receipt.FirstSequence, receipt.LastSequence, receipt.EventCount, receipt.TransactionBytes, response, receipt.CreatedAt.UnixNano())
	return err
}

func insertWorkboardEvent(ctx context.Context, tx *sql.Tx, id, boardID string, sequence int64, operationID, kind string, actor workboard.Actor, now time.Time, body []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workboard_events(id,board_id,sequence,operation_id,kind,actor_id,actor_type,created_at,body) VALUES(?,?,?,?,?,?,?,?,?)`, id, boardID, sequence, operationID, kind, actor.ID, actor.Type, now.UnixNano(), body)
	return err
}

func readWorkboardReceipt(ctx context.Context, tx *sql.Tx, scopeKind, scopeID, keyDigest, requestDigest string) (workboard.OperationReceipt, bool, error) {
	var indexed workboard.OperationReceipt
	var outcome string
	var createdAt int64
	var response []byte
	err := tx.QueryRowContext(ctx, `SELECT operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at
		FROM workboard_operations WHERE scope_kind=? AND scope_id=? AND key_digest=?`, scopeKind, scopeID, keyDigest).
		Scan(&indexed.OperationID, &indexed.BoardID, &indexed.RequestDigest, &indexed.ResponseDigest, &indexed.FirstSequence, &indexed.LastSequence, &indexed.EventCount, &indexed.TransactionBytes, &outcome, &response, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.OperationReceipt{}, false, nil
	}
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	if indexed.RequestDigest != requestDigest {
		return workboard.OperationReceipt{}, true, ErrConflict
	}
	var receipt workboard.OperationReceipt
	indexed.Version, indexed.Outcome, indexed.CreatedAt = 1, outcome, time.Unix(0, createdAt).UTC()
	if strictJSON(response, &receipt) != nil {
		return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
	}
	indexed.BoardRevision = receipt.BoardRevision
	indexed.CardID, indexed.CardRevision, indexed.ClaimRevision = receipt.CardID, receipt.CardRevision, receipt.ClaimRevision
	if receipt.Validate() != nil || receipt != indexed || receipt.RequestDigest != requestDigest || receipt.ResponseDigest != receiptDigest(receipt) {
		return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
	}
	return receipt, true, nil
}

func readBoardRow(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (workboard.Board, int64, string, error) {
	row := q.QueryRowContext(ctx, `SELECT id,revision,layout_revision,event_sequence,state,title,description,card_count,active_claims,created_at,updated_at,body,graph_revision,graph_digest FROM workboard_boards WHERE id=?`, id)
	board, graphRevision, graphDigest, err := scanBoardWithGraph(row)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.Board{}, 0, "", ErrWorkboardNotFound
	}
	return board, graphRevision, graphDigest, err
}

type rowScanner interface{ Scan(...any) error }

func scanBoardAfterRowID(row rowScanner, rowID *int64) (workboard.Board, error) {
	var indexed workboard.Board
	var created, updated int64
	var body []byte
	var graphRevision int64
	var graphDigest string
	if err := row.Scan(rowID, &indexed.ID, &indexed.Revision, &indexed.LayoutRevision, &indexed.EventSequence, &indexed.State, &indexed.Title, &indexed.Description, &indexed.CardCount, &indexed.ActiveClaims, &created, &updated, &body, &graphRevision, &graphDigest); err != nil {
		return workboard.Board{}, err
	}
	board, _, _, err := validateScannedBoard(indexed, created, updated, body, graphRevision, graphDigest)
	return board, err
}

func scanBoardWithGraph(row rowScanner) (workboard.Board, int64, string, error) {
	var indexed workboard.Board
	var created, updated int64
	var body []byte
	var graphRevision int64
	var graphDigest string
	if err := row.Scan(&indexed.ID, &indexed.Revision, &indexed.LayoutRevision, &indexed.EventSequence, &indexed.State, &indexed.Title, &indexed.Description, &indexed.CardCount, &indexed.ActiveClaims, &created, &updated, &body, &graphRevision, &graphDigest); err != nil {
		return workboard.Board{}, 0, "", err
	}
	return validateScannedBoard(indexed, created, updated, body, graphRevision, graphDigest)
}

func validateScannedBoard(indexed workboard.Board, created, updated int64, body []byte, graphRevision int64, graphDigest string) (workboard.Board, int64, string, error) {
	var board workboard.Board
	if strictJSON(body, &board) != nil {
		return workboard.Board{}, 0, "", ErrWorkboardCorrupt
	}
	indexed.Version, indexed.CreatedAt, indexed.UpdatedAt = 1, time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	if board.Validate() != nil || board != indexed || graphRevision < 1 || !validDigest(graphDigest) {
		return workboard.Board{}, 0, "", ErrWorkboardCorrupt
	}
	return board, graphRevision, graphDigest, nil
}

func readWorkboardColumns(ctx context.Context, tx *sql.Tx, boardID string) ([]workboard.Column, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version,id,state,title,rank,ordinal FROM workboard_columns WHERE board_id=? ORDER BY ordinal`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]workboard.Column, 0, len(canonicalWorkboardColumns))
	for rows.Next() {
		var column workboard.Column
		var ordinal int
		column.BoardID = boardID
		if err = rows.Scan(&column.Version, &column.ID, &column.State, &column.Title, &column.Rank, &ordinal); err != nil {
			return nil, err
		}
		if ordinal != len(columns) || ordinal >= len(canonicalWorkboardColumns) || column.State != canonicalWorkboardColumns[ordinal].state || column.Title != canonicalWorkboardColumns[ordinal].title || column.Rank != fmt.Sprintf("%02d", ordinal) || column.Validate() != nil {
			return nil, ErrWorkboardCorrupt
		}
		columns = append(columns, column)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) != len(canonicalWorkboardColumns) {
		return nil, ErrWorkboardCorrupt
	}
	return columns, nil
}

func finalizeWorkboardReceipt(receipt *workboard.OperationReceipt, durableBytes int) ([]byte, error) {
	if durableBytes < 1 || durableBytes > workboard.MaxTransactionBytes {
		return nil, &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "transaction_bytes"}
	}
	for range 8 {
		receipt.ResponseDigest = receiptDigest(*receipt)
		response, err := json.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		total := durableBytes + len(response)
		if total > workboard.MaxTransactionBytes {
			return nil, &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "transaction_bytes"}
		}
		if receipt.TransactionBytes == total {
			if receipt.Validate() != nil {
				return nil, ErrWorkboardCorrupt
			}
			return response, nil
		}
		receipt.TransactionBytes = total
	}
	return nil, ErrWorkboardCorrupt
}

func newCanonicalColumns(boardID string) ([]workboard.Column, int, error) {
	columns := make([]workboard.Column, 0, len(canonicalWorkboardColumns))
	total := 0
	for ordinal, canonical := range canonicalWorkboardColumns {
		column := workboard.Column{Version: 1, ID: string(canonical.state), BoardID: boardID, State: canonical.state, Title: canonical.title, Rank: fmt.Sprintf("%02d", ordinal)}
		body, err := json.Marshal(column)
		if err != nil || column.Validate() != nil {
			return nil, 0, ErrWorkboardCorrupt
		}
		total += len(body)
		columns = append(columns, column)
	}
	return columns, total, nil
}

func receiptDigest(receipt workboard.OperationReceipt) string {
	receipt.ResponseDigest = ""
	body, _ := json.Marshal(receipt)
	return digestBytes(body)
}

func newWorkboardIDs() (string, string, string, error) {
	ids := []string{newWorkboardID(), newWorkboardID(), newWorkboardID()}
	if ids[0] == "" || ids[1] == "" || ids[2] == "" {
		return "", "", "", errors.New("secure identifier generation failed")
	}
	return ids[0], ids[1], ids[2], nil
}

func newWorkboardID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}

func digestJSON(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func digestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func strictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrWorkboardCorrupt
	}
	return nil
}

func (s *Store) encodeCardCursor(cursor cardPageCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeCardCursor(value string) (cardPageCursor, error) {
	if value == "" {
		return cardPageCursor{Column: -1}, nil
	}
	if len(value) > workboard.MaxCursorBytes {
		return cardPageCursor{}, ErrWorkboardCursor
	}
	body, err := s.verifyWorkboardCursor(value)
	if err != nil {
		return cardPageCursor{}, ErrWorkboardCursor
	}
	var cursor cardPageCursor
	if strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validWorkboardID(cursor.BoardID) || cursor.BoardRevision < 1 || cursor.LayoutRevision < 1 || cursor.GraphRevision < 1 ||
		cursor.Column < 0 || cursor.Column > 6 || !validWorkboardID(cursor.ID) || len(cursor.Rank) < 1 || len(cursor.Rank) > workboard.MaxRankBytes {
		return cardPageCursor{}, ErrWorkboardCursor
	}
	canonical, _ := s.encodeCardCursor(cursor)
	if canonical != value {
		return cardPageCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}

func (s *Store) encodeBoardCursor(cursor boardPageCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeBoardCursor(value string) (boardPageCursor, error) {
	if value == "" {
		return boardPageCursor{}, nil
	}
	if len(value) > workboard.MaxCursorBytes {
		return boardPageCursor{}, ErrWorkboardCursor
	}
	body, err := s.verifyWorkboardCursor(value)
	if err != nil {
		return boardPageCursor{}, ErrWorkboardCursor
	}
	var cursor boardPageCursor
	if strictJSON(body, &cursor) != nil || cursor.Version != 1 || cursor.High < 1 || cursor.After < 1 || cursor.After >= cursor.High ||
		cursor.State != "" && cursor.State != "active" && cursor.State != "archived" {
		return boardPageCursor{}, ErrWorkboardCursor
	}
	canonical, _ := s.encodeBoardCursor(cursor)
	if canonical != value {
		return boardPageCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}

// Cursor authentication is repository-local and intentionally process-epoch
// scoped. A daemon restart invalidates outstanding pages safely; clients start
// a fresh bounded traversal rather than silently skipping durable records.
func (s *Store) signWorkboardCursor(body []byte) (string, error) {
	key, err := s.currentWorkboardCursorKey()
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	value := payload + "." + hex.EncodeToString(mac.Sum(nil))
	if len(value) > workboard.MaxCursorBytes {
		return "", ErrWorkboardCursor
	}
	return value, nil
}

func (s *Store) verifyWorkboardCursor(value string) ([]byte, error) {
	if len(value) < 66 || len(value) > workboard.MaxCursorBytes {
		return nil, ErrWorkboardCursor
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[1]) != sha256.Size*2 {
		return nil, ErrWorkboardCursor
	}
	signature, err := hex.DecodeString(parts[1])
	if err != nil {
		return nil, ErrWorkboardCursor
	}
	key, err := s.currentWorkboardCursorKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, ErrWorkboardCursor
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return nil, ErrWorkboardCursor
	}
	return body, nil
}

func (s *Store) currentWorkboardCursorKey() ([]byte, error) {
	s.workboardCursorOnce.Do(func() {
		_, s.workboardCursorErr = rand.Read(s.workboardCursorKey[:])
	})
	if s.workboardCursorErr != nil {
		return nil, s.workboardCursorErr
	}
	return s.workboardCursorKey[:], nil
}

func validWorkboardID(value string) bool { return workboardIDPattern.MatchString(value) }

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func invalidWorkboard(field string) error {
	return &workboard.Violation{Code: workboard.CodeInvalid, Field: field}
}

func normalizeWorkboardWriteError(err error) error {
	if err != nil && strings.Contains(err.Error(), "workboard board limit") {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "boards"}
	}
	return err
}
