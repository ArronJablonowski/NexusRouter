package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// Schema 40 adds an immutable, normalized link from a proved recovery to the
// exact successor attempt and claim. The row is intentionally separate from
// recovery evidence: a recovery can exist without ever being reassigned.
func migrateWorkboardReassignments(ctx context.Context, conn *sql.Conn, baseValidated bool) error {
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type='table' AND name='workboard_reassignments'`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		if err := validateWorkboardSchema40(ctx, conn); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, "PRAGMA user_version=40")
		return err
	}
	if !baseValidated {
		if err := validateWorkboardSchema36(ctx, conn); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `CREATE UNIQUE INDEX workboard_recoveries_identity
		ON workboard_recoveries(board_id,card_id,attempt_id,old_claim_id,id);
		CREATE TABLE workboard_reassignments(
		 recovery_id TEXT NOT NULL CHECK(length(CAST(recovery_id AS BLOB)) BETWEEN 1 AND 128),
		 board_id TEXT NOT NULL CHECK(length(CAST(board_id AS BLOB)) BETWEEN 1 AND 128),
		 card_id TEXT NOT NULL CHECK(length(CAST(card_id AS BLOB)) BETWEEN 1 AND 128),
		 predecessor_attempt_id TEXT NOT NULL CHECK(length(CAST(predecessor_attempt_id AS BLOB)) BETWEEN 1 AND 128),
		 predecessor_claim_id TEXT NOT NULL CHECK(length(CAST(predecessor_claim_id AS BLOB)) BETWEEN 1 AND 128),
		 successor_attempt_id TEXT NOT NULL CHECK(length(CAST(successor_attempt_id AS BLOB)) BETWEEN 1 AND 128),
		 successor_claim_id TEXT NOT NULL CHECK(length(CAST(successor_claim_id AS BLOB)) BETWEEN 1 AND 128),
		 created_at INTEGER NOT NULL CHECK(created_at>=0),
		 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16384),
		 PRIMARY KEY(recovery_id),
		 UNIQUE(board_id,card_id,successor_attempt_id),
		 UNIQUE(board_id,card_id,successor_claim_id),
		 FOREIGN KEY(board_id,card_id,predecessor_attempt_id,predecessor_claim_id,recovery_id)
		  REFERENCES workboard_recoveries(board_id,card_id,attempt_id,old_claim_id,id),
		 FOREIGN KEY(board_id,card_id,predecessor_attempt_id,predecessor_claim_id)
		  REFERENCES workboard_claims(board_id,card_id,attempt_id,id),
		 FOREIGN KEY(board_id,card_id,successor_attempt_id,successor_claim_id)
		  REFERENCES workboard_claims(board_id,card_id,attempt_id,id),
		 CHECK(predecessor_attempt_id!=successor_attempt_id),
		 CHECK(predecessor_claim_id!=successor_claim_id));
		CREATE INDEX workboard_reassignments_card
		 ON workboard_reassignments(board_id,card_id,created_at,recovery_id);
		CREATE TRIGGER workboard_reassignment_immutable_update BEFORE UPDATE ON workboard_reassignments
		 BEGIN SELECT RAISE(ABORT,'workboard reassignment is immutable'); END;
		CREATE TRIGGER workboard_reassignment_immutable_delete BEFORE DELETE ON workboard_reassignments
		 BEGIN SELECT RAISE(ABORT,'workboard reassignment is immutable'); END;`); err != nil {
		return err
	}
	if err := backfillWorkboardReassignments(ctx, conn); err != nil {
		return err
	}
	if err := validateWorkboardReassignmentAdditions(ctx, conn); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "PRAGMA user_version=40")
	return err
}

func validateWorkboardReassignmentAdditions(ctx context.Context, conn *sql.Conn) error {
	if !browserTableShape(ctx, conn, "workboard_reassignments",
		"recovery_id:TEXT:1:1,board_id:TEXT:1:0,card_id:TEXT:1:0,predecessor_attempt_id:TEXT:1:0,predecessor_claim_id:TEXT:1:0,successor_attempt_id:TEXT:1:0,successor_claim_id:TEXT:1:0,created_at:INTEGER:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "workboard_reassignments", []string{
			"primarykey(recovery_id)",
			"unique(board_id,card_id,successor_attempt_id)",
			"unique(board_id,card_id,successor_claim_id)",
			"foreignkey(board_id,card_id,predecessor_attempt_id,predecessor_claim_id,recovery_id)referencesworkboard_recoveries(board_id,card_id,attempt_id,old_claim_id,id)",
			"foreignkey(board_id,card_id,predecessor_attempt_id,predecessor_claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)",
			"foreignkey(board_id,card_id,successor_attempt_id,successor_claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)",
			"check(predecessor_attempt_id!=successor_attempt_id)",
			"check(predecessor_claim_id!=successor_claim_id)",
		}) ||
		!workboardObjectRules(ctx, conn, "index", "workboard_reassignments_card",
			[]string{"onworkboard_reassignments(board_id,card_id,created_at,recovery_id)"}) ||
		!workboardObjectRules(ctx, conn, "index", "workboard_recoveries_identity",
			[]string{"uniqueindexworkboard_recoveries_identityonworkboard_recoveries(board_id,card_id,attempt_id,old_claim_id,id)"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_reassignment_immutable_update",
			[]string{"beforeupdateonworkboard_reassignments", "raise(abort,'workboardreassignmentisimmutable')"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_reassignment_immutable_delete",
			[]string{"beforedeleteonworkboard_reassignments", "raise(abort,'workboardreassignmentisimmutable')"}) {
		return sql.ErrNoRows
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return nil
}

type legacyWorkboardReassignment struct {
	recoveryID, boardID, cardID              string
	predecessorAttemptID, predecessorClaimID string
	successorAttemptID, successorClaimID     sql.NullString
	recoveredAt, successorStartedAt          sql.NullInt64
	predecessorOrdinal                       int
	predecessorState, predecessorClaimState  string
}

// Schema 39 already guaranteed unique attempt ordinals and one claim per
// attempt. That permits a conservative backfill only when ordinal+1 exists
// with its exact claim and was started after the recovery. A recovery with no
// successor remains intentionally unlinked.
func backfillWorkboardReassignments(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT r.id,r.board_id,r.card_id,r.attempt_id,r.old_claim_id,r.recovered_at,
		p.ordinal,p.state,pc.state,s.id,s.started_at,sc.id
		FROM workboard_recoveries r
		JOIN workboard_attempts p ON p.board_id=r.board_id AND p.card_id=r.card_id AND p.id=r.attempt_id
		JOIN workboard_claims pc ON pc.board_id=r.board_id AND pc.card_id=r.card_id AND pc.attempt_id=r.attempt_id AND pc.id=r.old_claim_id
		LEFT JOIN workboard_attempts s ON s.board_id=r.board_id AND s.card_id=r.card_id AND s.ordinal=p.ordinal+1
		LEFT JOIN workboard_claims sc ON sc.board_id=s.board_id AND sc.card_id=s.card_id AND sc.attempt_id=s.id
		ORDER BY r.id`)
	if err != nil {
		return err
	}
	var candidates []legacyWorkboardReassignment
	for rows.Next() {
		var item legacyWorkboardReassignment
		if err = rows.Scan(&item.recoveryID, &item.boardID, &item.cardID, &item.predecessorAttemptID, &item.predecessorClaimID,
			&item.recoveredAt, &item.predecessorOrdinal, &item.predecessorState, &item.predecessorClaimState,
			&item.successorAttemptID, &item.successorStartedAt, &item.successorClaimID); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range candidates {
		if item.predecessorOrdinal < 1 || item.predecessorState != "failed" || item.predecessorClaimState != "released" || !item.recoveredAt.Valid {
			return ErrWorkboardCorrupt
		}
		if !item.successorAttemptID.Valid {
			if item.successorStartedAt.Valid || item.successorClaimID.Valid {
				return ErrWorkboardCorrupt
			}
			continue
		}
		if !item.successorStartedAt.Valid || !item.successorClaimID.Valid || item.successorStartedAt.Int64 < item.recoveredAt.Int64 {
			return ErrWorkboardCorrupt
		}
		record := workboard.ReassignmentRecord{Version: 1, RecoveryID: item.recoveryID, BoardID: item.boardID, CardID: item.cardID,
			PredecessorAttemptID: item.predecessorAttemptID, PredecessorClaimID: item.predecessorClaimID,
			SuccessorAttemptID: item.successorAttemptID.String, SuccessorClaimID: item.successorClaimID.String,
			CreatedAt: time.Unix(0, item.successorStartedAt.Int64).UTC()}
		if record.Validate() != nil {
			return ErrWorkboardCorrupt
		}
		body, marshalErr := json.Marshal(record)
		if marshalErr != nil || len(body) == 0 || len(body) > 16<<10 {
			if marshalErr != nil {
				return marshalErr
			}
			return errors.New("workboard reassignment body exceeds storage limit")
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO workboard_reassignments(
			recovery_id,board_id,card_id,predecessor_attempt_id,predecessor_claim_id,
			successor_attempt_id,successor_claim_id,created_at,body) VALUES(?,?,?,?,?,?,?,?,?)`,
			record.RecoveryID, record.BoardID, record.CardID, record.PredecessorAttemptID, record.PredecessorClaimID,
			record.SuccessorAttemptID, record.SuccessorClaimID, record.CreatedAt.UnixNano(), body); err != nil {
			return err
		}
	}
	return nil
}
