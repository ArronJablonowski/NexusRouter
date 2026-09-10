package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type dependencyPageCursor struct {
	Version       int                           `json:"v"`
	BoardID       string                        `json:"b"`
	CardID        string                        `json:"c"`
	Direction     workboard.DependencyDirection `json:"d"`
	GraphRevision int64                         `json:"r"`
	GraphDigest   string                        `json:"g"`
	Limit         int                           `json:"l"`
	After         string                        `json:"a"`
}

func (s *Store) ListDependencyEdges(ctx context.Context, boardID, cardID string, options workboard.DependencyOptions) (workboard.DependencyPage, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !validWorkboardID(boardID) || !validWorkboardID(cardID) || options.Validate() != nil {
		return workboard.DependencyPage{}, invalidWorkboard("dependencies")
	}
	cursor, err := s.decodeDependencyPageCursor(options.After)
	if err != nil || options.After != "" && !cursor.matches(boardID, cardID, options) {
		return workboard.DependencyPage{}, ErrWorkboardCursor
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.DependencyPage{}, err
	}
	defer tx.Rollback()
	_, graphRevision, graphDigest, err := readBoardRow(ctx, tx, boardID)
	if err != nil {
		return workboard.DependencyPage{}, err
	}
	if _, _, err = readStoredCard(ctx, tx, boardID, cardID); err != nil {
		return workboard.DependencyPage{}, err
	}
	// The board row is only a projection of the normalized graph. Recompute the
	// canonical graph before trusting either a fresh page or a cursor so missing
	// edges and a forged/stale stored digest fail closed instead of silently
	// changing traversal results.
	if _, actualDigest, graphErr := loadGraphTx(ctx, tx, boardID, graphRevision); graphErr != nil || actualDigest != graphDigest {
		if graphErr != nil {
			return workboard.DependencyPage{}, graphErr
		}
		return workboard.DependencyPage{}, ErrWorkboardCorrupt
	}
	if options.After == "" {
		cursor = dependencyPageCursor{Version: 1, BoardID: boardID, CardID: cardID, Direction: options.Direction,
			GraphRevision: graphRevision, GraphDigest: graphDigest, Limit: options.Limit}
	} else if cursor.GraphRevision != graphRevision || cursor.GraphDigest != graphDigest {
		return workboard.DependencyPage{}, ErrWorkboardCursor
	}
	where, endpoint := "card_id=?", "dependency_id"
	if options.Direction == workboard.DependencyDependents {
		where, endpoint = "dependency_id=?", "card_id"
	}
	var count int
	countQuery := `SELECT count(*) FROM workboard_dependencies WHERE board_id=? AND ` + where
	if err = tx.QueryRowContext(ctx, countQuery, boardID, cardID).Scan(&count); err != nil {
		return workboard.DependencyPage{}, err
	}
	maxEdges := workboard.MaxDependencies
	if options.Direction == workboard.DependencyDependents {
		// The per-card dependency cap bounds prerequisites. A popular root may
		// still have one incoming edge from every card on the board.
		maxEdges = workboard.MaxCardsPerBoard
	}
	if count < 0 || count > maxEdges {
		return workboard.DependencyPage{}, ErrWorkboardCorrupt
	}
	if cursor.After != "" {
		var anchorCount int
		anchorQuery := `SELECT count(*) FROM workboard_dependencies WHERE board_id=? AND ` + where + ` AND ` + endpoint + `=?`
		if err = tx.QueryRowContext(ctx, anchorQuery, boardID, cardID, cursor.After).Scan(&anchorCount); err != nil || anchorCount != 1 {
			return workboard.DependencyPage{}, ErrWorkboardCursor
		}
	}
	query := `SELECT card_id,dependency_id,` + endpoint + ` FROM workboard_dependencies WHERE board_id=? AND ` + where +
		` AND ` + endpoint + `>? ORDER BY ` + endpoint + ` LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, boardID, cardID, cursor.After, options.Limit+1)
	if err != nil {
		return workboard.DependencyPage{}, err
	}
	defer rows.Close()
	items := make([]workboard.DependencyLink, 0, min(options.Limit+1, count))
	last := cursor.After
	for rows.Next() {
		var link workboard.DependencyLink
		var position string
		link.Version, link.BoardID = 1, boardID
		if rows.Scan(&link.CardID, &link.DependencyID, &position) != nil || link.Validate() != nil || position <= last {
			return workboard.DependencyPage{}, ErrWorkboardCorrupt
		}
		if options.Direction == workboard.DependencyPrerequisites && link.CardID != cardID ||
			options.Direction == workboard.DependencyDependents && link.DependencyID != cardID {
			return workboard.DependencyPage{}, ErrWorkboardCorrupt
		}
		endpointCardID := link.DependencyID
		if options.Direction == workboard.DependencyDependents {
			endpointCardID = link.CardID
		}
		if _, _, err = readStoredCard(ctx, tx, boardID, endpointCardID); err != nil {
			return workboard.DependencyPage{}, err
		}
		items, last = append(items, link), position
	}
	if err = rows.Err(); err != nil {
		return workboard.DependencyPage{}, err
	}
	page := workboard.DependencyPage{Version: 1, BoardID: boardID, CardID: cardID, Direction: options.Direction,
		GraphRevision: graphRevision, GraphDigest: graphDigest, Items: items}
	if len(items) > options.Limit {
		page.Items, page.HasMore = items[:options.Limit], true
		cursor.After = lastDependencyEndpoint(page.Items[len(page.Items)-1], options.Direction)
		page.NextCursor, err = s.encodeDependencyPageCursor(cursor)
		if err != nil {
			return workboard.DependencyPage{}, err
		}
	}
	if page.Validate() != nil {
		return workboard.DependencyPage{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.DependencyPage{}, err
	}
	return page, nil
}

func lastDependencyEndpoint(link workboard.DependencyLink, direction workboard.DependencyDirection) string {
	if direction == workboard.DependencyDependents {
		return link.CardID
	}
	return link.DependencyID
}

func (c dependencyPageCursor) matches(boardID, cardID string, options workboard.DependencyOptions) bool {
	return c.Version == 1 && c.BoardID == boardID && c.CardID == cardID && c.Direction == options.Direction && c.Limit == options.Limit
}

func (s *Store) encodeDependencyPageCursor(cursor dependencyPageCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeDependencyPageCursor(value string) (dependencyPageCursor, error) {
	if value == "" {
		return dependencyPageCursor{}, nil
	}
	if len(value) > workboard.MaxCursorBytes {
		return dependencyPageCursor{}, ErrWorkboardCursor
	}
	body, err := s.verifyWorkboardCursor(value)
	if err != nil {
		return dependencyPageCursor{}, ErrWorkboardCursor
	}
	var cursor dependencyPageCursor
	if strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validWorkboardID(cursor.BoardID) || !validWorkboardID(cursor.CardID) ||
		!validWorkboardID(cursor.After) || cursor.GraphRevision < 1 || !validDigest(cursor.GraphDigest) || cursor.Limit < 1 ||
		cursor.Limit > workboard.MaxPageItems || (cursor.Direction != workboard.DependencyPrerequisites && cursor.Direction != workboard.DependencyDependents) {
		return dependencyPageCursor{}, ErrWorkboardCursor
	}
	canonical, err := s.encodeDependencyPageCursor(cursor)
	if err != nil || canonical != value {
		return dependencyPageCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}
