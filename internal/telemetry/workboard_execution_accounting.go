package telemetry

import (
	"context"
	"database/sql"
	"math"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type validatedExecutionAccount struct {
	admission  workboard.ExecutionAdmissionRecord
	settlement *workboard.ExecutionSettlementRecord
}

// validatedExecutionAccounts is the fail-closed source for WIP and the current
// card's aggregate resource accounting. Canonical bodies remain authoritative,
// and the append-only guards are rechecked without replaying unrelated settled
// task journals on every admission.
func validatedExecutionAccounts(ctx context.Context, tx *sql.Tx, boardID, cardID string) ([]validatedExecutionAccount, error) {
	if err := validateExecutionImmutabilityGuards(ctx, tx); err != nil {
		return nil, err
	}
	// Historical settled work on other cards cannot affect this card's budget
	// or current WIP. Limit canonical replay to globally active admissions plus
	// this card's bounded attempt history so admission cost does not grow with
	// every task NexusRouter has ever completed.
	rows, err := tx.QueryContext(ctx, `SELECT a.task_id FROM workboard_execution_admissions a
		WHERE (a.board_id=? AND a.card_id=?) OR NOT EXISTS(
			SELECT 1 FROM workboard_execution_settlements s WHERE s.admission_id=a.admission_id)
		ORDER BY a.admitted_at,a.task_id`, boardID, cardID)
	if err != nil {
		return nil, err
	}
	tasks := []string{}
	for rows.Next() {
		var task string
		if rows.Scan(&task) != nil {
			rows.Close()
			return nil, ErrWorkboardCorrupt
		}
		tasks = append(tasks, task)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	accounts := make([]validatedExecutionAccount, 0, len(tasks))
	cardSettlementCount := 0
	for _, task := range tasks {
		indexed, admission, found, readErr := readExecutionAdmission(ctx, tx, task)
		if readErr != nil || !found || indexed != admission {
			if readErr != nil {
				return nil, readErr
			}
			return nil, ErrWorkboardCorrupt
		}
		account := validatedExecutionAccount{admission: admission}
		settlementIndexed, settlement, settled, readErr := readExecutionSettlement(ctx, tx, admission.AdmissionID)
		if readErr != nil {
			return nil, readErr
		}
		if settled {
			if settlementIndexed != settlement || validateStoredExecutionSettlement(ctx, tx, admission, settlement) != nil {
				return nil, ErrWorkboardCorrupt
			}
			copy := settlement
			account.settlement = &copy
			if admission.BoardID == boardID && admission.CardID == cardID {
				cardSettlementCount++
			}
		}
		accounts = append(accounts, account)
	}
	var storedCardSettlements int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workboard_execution_settlements WHERE board_id=? AND card_id=?`, boardID, cardID).Scan(&storedCardSettlements); err != nil {
		return nil, err
	}
	if storedCardSettlements != cardSettlementCount {
		return nil, ErrWorkboardCorrupt
	}
	return accounts, nil
}

func validateExecutionImmutabilityGuards(ctx context.Context, tx *sql.Tx) error {
	return validateCanonicalTriggerDefinitions(ctx, tx, canonicalExecutionGuardDefinitions())
}

func validateCanonicalTriggerDefinitions(ctx context.Context, tx *sql.Tx, expected map[string]string) error {
	if len(expected) == 0 {
		return ErrWorkboardCorrupt
	}
	names := make([]string, 0, len(expected))
	args := make([]any, 0, len(expected))
	for name := range expected {
		names = append(names, "?")
		args = append(args, name)
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND name IN(`+strings.Join(names, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, definition string
		if rows.Scan(&name, &definition) != nil {
			return ErrWorkboardCorrupt
		}
		definitionExpected, ok := expected[name]
		normalized := normalizeTriggerDefinition(definition)
		if !ok || normalized != definitionExpected {
			return ErrWorkboardCorrupt
		}
		seen++
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	if seen != len(expected) {
		return ErrWorkboardCorrupt
	}
	return nil
}

func normalizeTriggerDefinition(definition string) string {
	return strings.ToLower(strings.Join(strings.Fields(definition), ""))
}

func canonicalExecutionGuardDefinitions() map[string]string {
	raw := map[string]string{
		"workboard_execution_admission_no_active": `CREATE TRIGGER workboard_execution_admission_no_active BEFORE INSERT ON workboard_execution_admissions
			WHEN EXISTS(SELECT 1 FROM workboard_execution_admissions a
				WHERE a.board_id=NEW.board_id AND a.card_id=NEW.card_id
				AND NOT EXISTS(SELECT 1 FROM workboard_execution_settlements s WHERE s.admission_id=a.admission_id))
			BEGIN SELECT RAISE(ABORT,'workboard card already has an active execution admission'); END`,
		"workboard_execution_admission_binding": `CREATE TRIGGER workboard_execution_admission_binding BEFORE INSERT ON workboard_execution_admissions
			WHEN NOT EXISTS(SELECT 1 FROM workboard_task_start_claims c
				WHERE c.task_id=NEW.task_id AND c.session_id=NEW.session_id AND c.event_id=NEW.event_id AND c.event_digest=NEW.event_digest
				AND c.board_id=NEW.board_id AND c.card_id=NEW.card_id AND c.attempt_id=NEW.attempt_id AND c.claim_id=NEW.claim_id
				AND c.worker_id=NEW.worker_id AND c.operation_id=NEW.operation_id AND c.request_digest=NEW.request_digest
				AND c.expected_card_revision=NEW.card_revision AND c.policy_digest=NEW.policy_digest)
			OR NOT EXISTS(SELECT 1 FROM events e JOIN event_log l ON l.event_id=e.id
				WHERE e.id=NEW.event_id AND e.task_id=NEW.task_id AND e.sequence=1 AND l.task_id=NEW.task_id AND l.task_sequence=1
				AND l.body_digest=NEW.event_digest
				AND json_extract(e.body,'$.session_id')=NEW.session_id AND json_extract(e.body,'$.worker_id')=NEW.worker_id
				AND json_extract(e.body,'$.kind')='task.started' AND json_extract(e.body,'$.data.model_id')=NEW.model_id
				AND json_extract(e.body,'$.data.provider_id')=NEW.provider_id AND json_extract(e.body,'$.data.config_id')=NEW.config_id)
			BEGIN SELECT RAISE(ABORT,'workboard execution admission binding mismatch'); END`,
		"workboard_execution_settlement_binding": `CREATE TRIGGER workboard_execution_settlement_binding BEFORE INSERT ON workboard_execution_settlements
			WHEN NOT EXISTS(SELECT 1 FROM workboard_execution_admissions a
				WHERE a.admission_id=NEW.admission_id AND a.admission_digest=NEW.admission_digest AND a.task_id=NEW.task_id
				AND a.session_id=NEW.session_id AND a.board_id=NEW.board_id AND a.card_id=NEW.card_id AND a.attempt_id=NEW.attempt_id
				AND a.claim_id=NEW.claim_id AND a.worker_id=NEW.worker_id AND a.operation_id=NEW.operation_id
				AND a.model_id=NEW.model_id AND a.provider_id=NEW.provider_id AND a.config_id=NEW.config_id)
			OR NOT EXISTS(SELECT 1 FROM events e JOIN event_log l ON l.event_id=e.id
				WHERE e.id=NEW.terminal_event_id AND e.task_id=NEW.task_id AND e.sequence=NEW.terminal_sequence
				AND l.task_id=NEW.task_id AND l.task_sequence=NEW.terminal_sequence AND l.body_digest=NEW.terminal_event_digest
				AND json_extract(e.body,'$.session_id')=NEW.session_id
				AND json_extract(e.body,'$.kind')=NEW.terminal_kind)
			OR NOT EXISTS(SELECT 1 FROM workboard_attempts a JOIN workboard_claims c
				ON c.board_id=a.board_id AND c.card_id=a.card_id AND c.attempt_id=a.id
				WHERE a.board_id=NEW.board_id AND a.card_id=NEW.card_id AND a.id=NEW.attempt_id AND a.state!='running' AND a.ended_at IS NOT NULL
				AND c.id=NEW.claim_id AND c.state='released' AND c.released_at IS NOT NULL)
			BEGIN SELECT RAISE(ABORT,'workboard execution settlement binding mismatch'); END`,
		"workboard_execution_admission_immutable_update": `CREATE TRIGGER workboard_execution_admission_immutable_update BEFORE UPDATE ON workboard_execution_admissions
			BEGIN SELECT RAISE(ABORT,'workboard execution admission is immutable'); END`,
		"workboard_execution_admission_immutable_delete": `CREATE TRIGGER workboard_execution_admission_immutable_delete BEFORE DELETE ON workboard_execution_admissions
			BEGIN SELECT RAISE(ABORT,'workboard execution admission is immutable'); END`,
		"workboard_execution_settlement_immutable_update": `CREATE TRIGGER workboard_execution_settlement_immutable_update BEFORE UPDATE ON workboard_execution_settlements
			BEGIN SELECT RAISE(ABORT,'workboard execution settlement is immutable'); END`,
		"workboard_execution_settlement_immutable_delete": `CREATE TRIGGER workboard_execution_settlement_immutable_delete BEFORE DELETE ON workboard_execution_settlements
			BEGIN SELECT RAISE(ABORT,'workboard execution settlement is immutable'); END`,
	}
	for name, definition := range raw {
		raw[name] = normalizeTriggerDefinition(definition)
	}
	return raw
}

func addExecutionCharge(total *executionCharges, timeMS, tokens, costMicros int64) error {
	if timeMS < 0 || tokens < 0 || costMicros < 0 || timeMS > math.MaxInt64-total.timeMS ||
		tokens > math.MaxInt64-total.tokens || costMicros > math.MaxInt64-total.costMicros {
		return ErrWorkboardCorrupt
	}
	total.timeMS += timeMS
	total.tokens += tokens
	total.costMicros += costMicros
	return nil
}
