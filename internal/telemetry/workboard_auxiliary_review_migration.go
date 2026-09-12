package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 43 adds append-only admission and settlement facts for card-owned
// auxiliary reviews. Older candidate evaluations are not backfilled: no
// durable pre-dispatch fact exists from which an admission could be proved.
func migrateWorkboardAuxiliaryReviews(ctx context.Context, conn *sql.Conn) error {
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN
		('workboard_auxiliary_review_admissions','workboard_auxiliary_review_settlements')`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("workboard auxiliary review tables exist before schema 43")
	}
	if err := validateWorkboardExecutionBudgetSchema(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE workboard_auxiliary_review_admissions(
		admission_id TEXT PRIMARY KEY CHECK(length(CAST(admission_id AS BLOB)) BETWEEN 1 AND 128),
		operation_id TEXT NOT NULL UNIQUE CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
		board_id TEXT NOT NULL,
		card_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		claim_id TEXT NOT NULL,
		candidate_id TEXT NOT NULL CHECK(length(CAST(candidate_id AS BLOB)) BETWEEN 1 AND 128),
		candidate_digest TEXT NOT NULL CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^0-9a-f]*'),
		criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		reviewer_id TEXT NOT NULL CHECK(length(CAST(reviewer_id AS BLOB)) BETWEEN 1 AND 128),
		model_id TEXT NOT NULL CHECK(length(CAST(model_id AS BLOB)) BETWEEN 1 AND 512),
		provider_id TEXT NOT NULL CHECK(length(CAST(provider_id AS BLOB)) BETWEEN 1 AND 128),
		config_id TEXT NOT NULL CHECK(length(config_id)=64 AND config_id NOT GLOB '*[^0-9a-f]*'),
		time_limit_ms INTEGER NOT NULL CHECK(time_limit_ms BETWEEN 100 AND 300000),
		token_limit INTEGER NOT NULL CHECK(token_limit BETWEEN 1 AND 1000000000),
		cost_micros INTEGER NOT NULL CHECK(cost_micros BETWEEN 0 AND 1000000000000),
		admitted_at INTEGER NOT NULL CHECK(admitted_at>=0),
		reservation_digest TEXT NOT NULL UNIQUE CHECK(length(reservation_digest)=64 AND reservation_digest NOT GLOB '*[^0-9a-f]*'),
		admission_digest TEXT NOT NULL UNIQUE CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),
		UNIQUE(board_id,card_id,attempt_id,claim_id,candidate_id),
		FOREIGN KEY(board_id,card_id,attempt_id,claim_id)
		 REFERENCES workboard_claims(board_id,card_id,attempt_id,id));
	CREATE TABLE workboard_auxiliary_review_settlements(
		settlement_id TEXT PRIMARY KEY CHECK(length(CAST(settlement_id AS BLOB)) BETWEEN 1 AND 128),
		admission_id TEXT NOT NULL UNIQUE REFERENCES workboard_auxiliary_review_admissions(admission_id),
		admission_digest TEXT NOT NULL CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'),
		reservation_digest TEXT NOT NULL CHECK(length(reservation_digest)=64 AND reservation_digest NOT GLOB '*[^0-9a-f]*'),
		operation_id TEXT NOT NULL UNIQUE CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
		board_id TEXT NOT NULL,
		card_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		claim_id TEXT NOT NULL,
		candidate_id TEXT NOT NULL CHECK(length(CAST(candidate_id AS BLOB)) BETWEEN 1 AND 128),
		candidate_digest TEXT NOT NULL CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^0-9a-f]*'),
		criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		reviewer_id TEXT NOT NULL CHECK(length(CAST(reviewer_id AS BLOB)) BETWEEN 1 AND 128),
		model_id TEXT NOT NULL CHECK(length(CAST(model_id AS BLOB)) BETWEEN 1 AND 512),
		provider_id TEXT NOT NULL CHECK(length(CAST(provider_id AS BLOB)) BETWEEN 1 AND 128),
		config_id TEXT NOT NULL CHECK(length(config_id)=64 AND config_id NOT GLOB '*[^0-9a-f]*'),
		time_limit_ms INTEGER NOT NULL CHECK(time_limit_ms BETWEEN 100 AND 300000),
		token_limit INTEGER NOT NULL CHECK(token_limit BETWEEN 1 AND 1000000000),
		cost_micros INTEGER NOT NULL CHECK(cost_micros BETWEEN 0 AND 1000000000000),
		admitted_at INTEGER NOT NULL CHECK(admitted_at>=0),
		disposition TEXT NOT NULL CHECK(disposition IN('completed','failed','canceled')),
		charged_time_ms INTEGER NOT NULL CHECK(charged_time_ms BETWEEN 0 AND 2592000000),
		charged_tokens INTEGER NOT NULL CHECK(charged_tokens BETWEEN 0 AND 1000000000),
		charged_cost_micros INTEGER NOT NULL CHECK(charged_cost_micros BETWEEN 0 AND 1000000000000),
		time_charge_mode TEXT NOT NULL CHECK(time_charge_mode IN('measured','conservative')),
		token_charge_mode TEXT NOT NULL CHECK(token_charge_mode IN('measured','conservative')),
		cost_charge_mode TEXT NOT NULL CHECK(cost_charge_mode IN('measured','conservative')),
		settled_at INTEGER NOT NULL CHECK(settled_at>=admitted_at),
		settlement_digest TEXT NOT NULL UNIQUE CHECK(length(settlement_digest)=64 AND settlement_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),
		CHECK(time_charge_mode!='conservative' OR charged_time_ms=time_limit_ms),
		CHECK(token_charge_mode!='conservative' OR charged_tokens=token_limit),
		CHECK(cost_charge_mode!='conservative' OR charged_cost_micros=cost_micros));
	CREATE INDEX workboard_auxiliary_review_admissions_board
		ON workboard_auxiliary_review_admissions(board_id,card_id,admitted_at,admission_id);
	CREATE INDEX workboard_auxiliary_review_settlements_board
		ON workboard_auxiliary_review_settlements(board_id,card_id,settled_at,settlement_id);
	CREATE TRIGGER workboard_auxiliary_review_admission_binding BEFORE INSERT ON workboard_auxiliary_review_admissions
		WHEN NOT EXISTS(SELECT 1 FROM workboard_cards k
			JOIN workboard_attempts a ON a.board_id=k.board_id AND a.card_id=k.id
			JOIN workboard_claims c ON c.board_id=a.board_id AND c.card_id=a.card_id AND c.attempt_id=a.id
			WHERE k.board_id=NEW.board_id AND k.id=NEW.card_id AND k.state='in_progress'
			AND k.current_attempt_id=NEW.attempt_id AND k.current_claim_id=NEW.claim_id
			AND a.id=NEW.attempt_id AND a.state='running' AND a.criteria_digest=NEW.criteria_digest
			AND a.policy_digest=NEW.policy_digest AND c.id=NEW.claim_id AND c.state='active')
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review admission binding mismatch'); END;
	CREATE TRIGGER workboard_auxiliary_review_settlement_binding BEFORE INSERT ON workboard_auxiliary_review_settlements
		WHEN NOT EXISTS(SELECT 1 FROM workboard_auxiliary_review_admissions a
			WHERE a.admission_id=NEW.admission_id AND a.admission_digest=NEW.admission_digest
			AND a.reservation_digest=NEW.reservation_digest AND a.operation_id=NEW.operation_id
			AND a.board_id=NEW.board_id AND a.card_id=NEW.card_id AND a.attempt_id=NEW.attempt_id
			AND a.claim_id=NEW.claim_id AND a.candidate_id=NEW.candidate_id AND a.candidate_digest=NEW.candidate_digest
			AND a.criteria_digest=NEW.criteria_digest AND a.policy_digest=NEW.policy_digest
			AND a.reviewer_id=NEW.reviewer_id AND a.model_id=NEW.model_id AND a.provider_id=NEW.provider_id
			AND a.config_id=NEW.config_id AND a.time_limit_ms=NEW.time_limit_ms
			AND a.token_limit=NEW.token_limit AND a.cost_micros=NEW.cost_micros AND a.admitted_at=NEW.admitted_at)
		OR (NEW.disposition='completed' AND NOT EXISTS(SELECT 1 FROM workboard_candidates c
			WHERE c.board_id=NEW.board_id AND c.card_id=NEW.card_id AND c.attempt_id=NEW.attempt_id
			AND c.id=NEW.candidate_id AND c.digest=NEW.candidate_digest
			AND c.criteria_digest=NEW.criteria_digest AND c.policy_digest=NEW.policy_digest))
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement binding mismatch'); END;
	CREATE TRIGGER workboard_auxiliary_review_admission_immutable_update BEFORE UPDATE ON workboard_auxiliary_review_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review admission is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_admission_immutable_delete BEFORE DELETE ON workboard_auxiliary_review_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review admission is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_settlement_immutable_update BEFORE UPDATE ON workboard_auxiliary_review_settlements
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_settlement_immutable_delete BEFORE DELETE ON workboard_auxiliary_review_settlements
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement is immutable'); END;
	PRAGMA user_version=43;`); err != nil {
		return err
	}
	return validateWorkboardAuxiliaryReviewSchema(ctx, conn)
}

func validateWorkboardAuxiliaryReviewSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardExecutionBudgetSchema(ctx, conn); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "workboard_auxiliary_review_admissions",
		"admission_id:TEXT:0:1,operation_id:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,candidate_id:TEXT:1:0,candidate_digest:TEXT:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,reviewer_id:TEXT:1:0,model_id:TEXT:1:0,provider_id:TEXT:1:0,config_id:TEXT:1:0,time_limit_ms:INTEGER:1:0,token_limit:INTEGER:1:0,cost_micros:INTEGER:1:0,admitted_at:INTEGER:1:0,reservation_digest:TEXT:1:0,admission_digest:TEXT:1:0,body:BLOB:1:0") ||
		!browserTableShape(ctx, conn, "workboard_auxiliary_review_settlements",
			"settlement_id:TEXT:0:1,admission_id:TEXT:1:0,admission_digest:TEXT:1:0,reservation_digest:TEXT:1:0,operation_id:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,candidate_id:TEXT:1:0,candidate_digest:TEXT:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,reviewer_id:TEXT:1:0,model_id:TEXT:1:0,provider_id:TEXT:1:0,config_id:TEXT:1:0,time_limit_ms:INTEGER:1:0,token_limit:INTEGER:1:0,cost_micros:INTEGER:1:0,admitted_at:INTEGER:1:0,disposition:TEXT:1:0,charged_time_ms:INTEGER:1:0,charged_tokens:INTEGER:1:0,charged_cost_micros:INTEGER:1:0,time_charge_mode:TEXT:1:0,token_charge_mode:TEXT:1:0,cost_charge_mode:TEXT:1:0,settled_at:INTEGER:1:0,settlement_digest:TEXT:1:0,body:BLOB:1:0") {
		return errors.New("invalid workboard auxiliary review table shape")
	}
	for name, rules := range map[string][]string{
		"workboard_auxiliary_review_admissions":  {"operation_idtextnotnullunique", "time_limit_msintegernotnullcheck(time_limit_msbetween100and300000)", "unique(board_id,card_id,attempt_id,claim_id,candidate_id)", "foreignkey(board_id,card_id,attempt_id,claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)"},
		"workboard_auxiliary_review_settlements": {"admission_idtextnotnulluniquereferences", "time_limit_msintegernotnullcheck(time_limit_msbetween100and300000)", "dispositiontextnotnullcheck(dispositionin('completed','failed','canceled'))", "check(time_charge_mode!='conservative'orcharged_time_ms=time_limit_ms)", "check(token_charge_mode!='conservative'orcharged_tokens=token_limit)", "check(cost_charge_mode!='conservative'orcharged_cost_micros=cost_micros)"},
	} {
		if !browserTableRules(ctx, conn, name, rules) {
			return errors.New("invalid " + name + " table rules")
		}
	}
	for name, rule := range map[string]string{
		"workboard_auxiliary_review_admissions_board":  "onworkboard_auxiliary_review_admissions(board_id,card_id,admitted_at,admission_id)",
		"workboard_auxiliary_review_settlements_board": "onworkboard_auxiliary_review_settlements(board_id,card_id,settled_at,settlement_id)",
	} {
		if !workboardObjectRules(ctx, conn, "index", name, []string{rule}) {
			return errors.New("invalid " + name)
		}
	}
	for name, rules := range map[string][]string{
		"workboard_auxiliary_review_admission_binding":           {"beforeinsertonworkboard_auxiliary_review_admissions", "a.criteria_digest=new.criteria_digest", "c.state='active'", "admissionbindingmismatch"},
		"workboard_auxiliary_review_settlement_binding":          {"beforeinsertonworkboard_auxiliary_review_settlements", "a.admission_digest=new.admission_digest", "new.disposition='completed'", "settlementbindingmismatch"},
		"workboard_auxiliary_review_admission_immutable_update":  {"beforeupdateonworkboard_auxiliary_review_admissions", "admissionisimmutable"},
		"workboard_auxiliary_review_admission_immutable_delete":  {"beforedeleteonworkboard_auxiliary_review_admissions", "admissionisimmutable"},
		"workboard_auxiliary_review_settlement_immutable_update": {"beforeupdateonworkboard_auxiliary_review_settlements", "settlementisimmutable"},
		"workboard_auxiliary_review_settlement_immutable_delete": {"beforedeleteonworkboard_auxiliary_review_settlements", "settlementisimmutable"},
	} {
		if !workboardObjectRules(ctx, conn, "trigger", name, rules) {
			return errors.New("invalid " + name)
		}
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
