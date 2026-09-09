package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

type ConfigurationMismatchPage struct {
	Items      []submissions.Summary
	NextCursor string
}

type configurationMismatchCursor struct {
	Version      int    `json:"version"`
	Last         int64  `json:"last"`
	HighWater    int64  `json:"high_water"`
	ConfigDigest string `json:"config_digest"`
}

func encodeConfigurationMismatchCursor(cursor configurationMismatchCursor) (string, error) {
	if cursor.Version != 1 || cursor.Last < 0 || cursor.HighWater < cursor.Last || !submissionDigest(cursor.ConfigDigest) {
		return "", submissions.ErrInvalid
	}
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", submissions.ErrInvalid
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeConfigurationMismatchCursor(encoded, configDigest string) (configurationMismatchCursor, error) {
	if encoded == "" || len(encoded) > 1024 || !submissionDigest(configDigest) {
		return configurationMismatchCursor{}, submissions.ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return configurationMismatchCursor{}, submissions.ErrInvalid
	}
	var cursor configurationMismatchCursor
	if json.Unmarshal(body, &cursor) != nil || cursor.ConfigDigest != configDigest {
		return configurationMismatchCursor{}, submissions.ErrInvalid
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(canonical, body) {
		return configurationMismatchCursor{}, submissions.ErrInvalid
	}
	if _, err = encodeConfigurationMismatchCursor(cursor); err != nil {
		return configurationMismatchCursor{}, err
	}
	return cursor, nil
}

// ConfigurationMismatchCandidatesPage returns only queued or expired-running
// rows from older configuration generations. The cursor preserves an insertion
// fence while a page set is active, then resets after exhaustion so later
// sweeps reconsider rows that expire or are repaired. Corrupt candidates are
// omitted, reported as ErrInvalid, and cannot pin the cursor or hide later work.
func (s *Store) ConfigurationMismatchCandidatesPage(ctx context.Context, currentDigest, after string, limit int, now time.Time) (ConfigurationMismatchPage, error) {
	page := ConfigurationMismatchPage{Items: []submissions.Summary{}}
	if !submissionDigest(currentDigest) || limit < 1 || limit > 100 || now.IsZero() {
		return page, submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	cursor := configurationMismatchCursor{Version: 1, ConfigDigest: currentDigest}
	if after != "" {
		cursor, err = decodeConfigurationMismatchCursor(after, currentDigest)
		if err != nil {
			return page, err
		}
	}
	if after == "" || cursor.Last == cursor.HighWater {
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM submissions`).Scan(&cursor.HighWater); err != nil {
			return page, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid FROM submissions
		WHERE rowid>? AND rowid<=? AND config_digest!=?
		AND (state='queued' OR (state='running' AND lease_expires_at<=?))
		ORDER BY rowid LIMIT ?`, cursor.Last, cursor.HighWater, currentDigest, submissionTime(now), limit+1)
	if err != nil {
		return page, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return page, err
	}
	rows.Close()
	corrupt := false
	for i, id := range ids {
		if i == limit {
			break
		}
		cursor.Last = id
		item, readErr := readSubmissionSummary(ctx, tx, id, now)
		if readErr != nil {
			corrupt = true
			continue
		}
		page.Items = append(page.Items, item)
	}
	if len(ids) <= limit {
		// A completed sweep resets its row watermark. The next call establishes
		// a fresh insertion fence and reconsiders older rows that have since
		// expired or whose corruption was repaired. SQL still selects only
		// currently eligible mismatch candidates.
		cursor.Last = 0
		cursor.HighWater = 0
	}
	page.NextCursor, err = encodeConfigurationMismatchCursor(cursor)
	if err != nil {
		return ConfigurationMismatchPage{}, err
	}
	if err = tx.Commit(); err != nil {
		return ConfigurationMismatchPage{}, err
	}
	if corrupt {
		return page, submissions.ErrInvalid
	}
	return page, nil
}
