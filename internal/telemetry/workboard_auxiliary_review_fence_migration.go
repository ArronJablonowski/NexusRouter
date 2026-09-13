package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 44 adds successor completion fences only for reviews admitted after
// this migration. Existing schema-43 admissions are intentionally not
// backfilled because they cannot prove the exact revision snapshot.
func migrateWorkboardAuxiliaryReviewSuccessorFences(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardAuxiliaryReviewSchema(ctx, conn); err != nil {
		return err
	}
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type='table' AND name IN('workboard_auxiliary_review_successor_fences','workboard_auxiliary_review_legacy_admissions')`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("workboard auxiliary review successor fences exist before schema 44")
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE workboard_auxiliary_review_legacy_admissions(
		admission_id TEXT PRIMARY KEY REFERENCES workboard_auxiliary_review_admissions(admission_id),
		admission_digest TEXT NOT NULL UNIQUE CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'));
	INSERT INTO workboard_auxiliary_review_legacy_admissions(admission_id,admission_digest)
		SELECT admission_id,admission_digest FROM workboard_auxiliary_review_admissions;
	CREATE TRIGGER workboard_auxiliary_review_legacy_admission_sealed_insert BEFORE INSERT ON workboard_auxiliary_review_legacy_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review legacy admissions are migration sealed'); END;
	CREATE TRIGGER workboard_auxiliary_review_legacy_admission_immutable_update BEFORE UPDATE ON workboard_auxiliary_review_legacy_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review legacy admission is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_legacy_admission_immutable_delete BEFORE DELETE ON workboard_auxiliary_review_legacy_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review legacy admission is immutable'); END;
	CREATE TABLE workboard_auxiliary_review_successor_fences(
		admission_id TEXT PRIMARY KEY REFERENCES workboard_auxiliary_review_admissions(admission_id),
		admission_digest TEXT NOT NULL CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'),
		operation_id TEXT NOT NULL UNIQUE CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
		board_id TEXT NOT NULL,
		card_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		claim_id TEXT NOT NULL,
		card_revision INTEGER NOT NULL CHECK(card_revision>0),
		claim_revision INTEGER NOT NULL CHECK(claim_revision>0),
		criteria_revision INTEGER NOT NULL CHECK(criteria_revision>0),
		candidate_id TEXT NOT NULL CHECK(length(CAST(candidate_id AS BLOB)) BETWEEN 1 AND 128),
		candidate_digest TEXT NOT NULL CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^0-9a-f]*'),
		criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		admitted_at INTEGER NOT NULL CHECK(admitted_at>=0),
		deadline_at INTEGER NOT NULL CHECK(deadline_at>admitted_at),
		fence_digest TEXT NOT NULL UNIQUE CHECK(length(fence_digest)=64 AND fence_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536));
	CREATE TRIGGER workboard_auxiliary_review_successor_fence_binding BEFORE INSERT ON workboard_auxiliary_review_successor_fences
		WHEN NOT EXISTS(SELECT 1 FROM workboard_auxiliary_review_admissions a
			JOIN workboard_cards k ON k.board_id=a.board_id AND k.id=a.card_id
			JOIN workboard_claims c ON c.board_id=a.board_id AND c.card_id=a.card_id
				AND c.attempt_id=a.attempt_id AND c.id=a.claim_id
			WHERE a.admission_id=NEW.admission_id AND a.admission_digest=NEW.admission_digest
			AND a.operation_id=NEW.operation_id AND a.board_id=NEW.board_id AND a.card_id=NEW.card_id
			AND a.attempt_id=NEW.attempt_id AND a.claim_id=NEW.claim_id AND a.candidate_id=NEW.candidate_id
			AND a.candidate_digest=NEW.candidate_digest AND a.criteria_digest=NEW.criteria_digest
			AND a.policy_digest=NEW.policy_digest AND a.admitted_at=NEW.admitted_at
			AND k.revision=NEW.card_revision AND k.criteria_revision=NEW.criteria_revision
			AND c.revision=NEW.claim_revision
			AND NEW.deadline_at=NEW.admitted_at+(a.time_limit_ms*1000000))
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review successor fence binding mismatch'); END;
	CREATE TRIGGER workboard_auxiliary_review_successor_fence_immutable_update BEFORE UPDATE ON workboard_auxiliary_review_successor_fences
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review successor fence is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_successor_fence_immutable_delete BEFORE DELETE ON workboard_auxiliary_review_successor_fences
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review successor fence is immutable'); END;
	PRAGMA user_version=44;`); err != nil {
		return err
	}
	return validateWorkboardAuxiliaryReviewSuccessorFenceSchema(ctx, conn)
}

// Migration and recovery tests may deliberately lower user_version after
// removing newer parent tables. An empty schema-44 child carries no authority
// and can be recreated safely; any retained row fails closed because it may
// represent successor authority that the declared schema cannot validate.
func discardEmptyFutureAuxiliaryReviewSuccessorFences(ctx context.Context, conn *sql.Conn) error {
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type='table' AND name IN('workboard_auxiliary_review_successor_fences','workboard_auxiliary_review_legacy_admissions')`).Scan(&retained); err != nil || retained == 0 {
		return err
	}
	var rows int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM workboard_auxiliary_review_successor_fences)+
		(SELECT count(*) FROM workboard_auxiliary_review_legacy_admissions)`).Scan(&rows); err != nil {
		return err
	}
	if rows != 0 {
		return errors.New("workboard auxiliary review successor fences exist before schema 44")
	}
	_, err := conn.ExecContext(ctx, `DROP TABLE workboard_auxiliary_review_successor_fences;
		DROP TABLE workboard_auxiliary_review_legacy_admissions`)
	return err
}

func validateWorkboardAuxiliaryReviewSuccessorFenceSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardAuxiliaryReviewSchema(ctx, conn); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "workboard_auxiliary_review_legacy_admissions",
		"admission_id:TEXT:0:1,admission_digest:TEXT:1:0") ||
		!browserTableRules(ctx, conn, "workboard_auxiliary_review_legacy_admissions", []string{
			"admission_idtextprimarykeyreferencesworkboard_auxiliary_review_admissions(admission_id)",
			"admission_digesttextnotnullunique",
		}) {
		return errors.New("invalid workboard auxiliary review legacy admission table")
	}
	if !browserTableShape(ctx, conn, "workboard_auxiliary_review_successor_fences",
		"admission_id:TEXT:0:1,admission_digest:TEXT:1:0,operation_id:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,card_revision:INTEGER:1:0,claim_revision:INTEGER:1:0,criteria_revision:INTEGER:1:0,candidate_id:TEXT:1:0,candidate_digest:TEXT:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,admitted_at:INTEGER:1:0,deadline_at:INTEGER:1:0,fence_digest:TEXT:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "workboard_auxiliary_review_successor_fences", []string{
			"admission_idtextprimarykeyreferencesworkboard_auxiliary_review_admissions(admission_id)",
			"operation_idtextnotnullunique", "card_revisionintegernotnullcheck(card_revision>0)",
			"deadline_atintegernotnullcheck(deadline_at>admitted_at)", "fence_digesttextnotnullunique",
		}) {
		return errors.New("invalid workboard auxiliary review successor fence table")
	}
	for name, rules := range map[string][]string{
		"workboard_auxiliary_review_legacy_admission_sealed_insert":    {"beforeinsertonworkboard_auxiliary_review_legacy_admissions", "migrationsealed"},
		"workboard_auxiliary_review_legacy_admission_immutable_update": {"beforeupdateonworkboard_auxiliary_review_legacy_admissions", "legacyadmissionisimmutable"},
		"workboard_auxiliary_review_legacy_admission_immutable_delete": {"beforedeleteonworkboard_auxiliary_review_legacy_admissions", "legacyadmissionisimmutable"},
		"workboard_auxiliary_review_successor_fence_binding":           {"beforeinsertonworkboard_auxiliary_review_successor_fences", "a.admission_digest=new.admission_digest", "k.revision=new.card_revision", "k.criteria_revision=new.criteria_revision", "c.revision=new.claim_revision", "new.deadline_at=new.admitted_at+(a.time_limit_ms*1000000)", "successorfencebindingmismatch"},
		"workboard_auxiliary_review_successor_fence_immutable_update":  {"beforeupdateonworkboard_auxiliary_review_successor_fences", "successorfenceisimmutable"},
		"workboard_auxiliary_review_successor_fence_immutable_delete":  {"beforedeleteonworkboard_auxiliary_review_successor_fences", "successorfenceisimmutable"},
	} {
		if !workboardObjectRules(ctx, conn, "trigger", name, rules) {
			return errors.New("invalid " + name)
		}
	}
	return nil
}
