package hostresources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const SchemaVersion = 1

func migrateV1(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `CREATE TABLE owner_processes(
		id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 8192));
	CREATE TABLE coordinator_policy(singleton INTEGER PRIMARY KEY CHECK(singleton=1),max_concurrent INTEGER NOT NULL CHECK(max_concurrent BETWEEN 1 AND 64),ram_percent_bits INTEGER NOT NULL,vram_percent_bits INTEGER NOT NULL,max_age_ns INTEGER NOT NULL CHECK(max_age_ns>0),adaptive INTEGER NOT NULL CHECK(adaptive IN(0,1)));
	CREATE TABLE coordinator_clock(singleton INTEGER PRIMARY KEY CHECK(singleton=1),last_seen_ns INTEGER NOT NULL CHECK(last_seen_ns>=0));
	INSERT INTO coordinator_clock VALUES(1,0);
	CREATE TABLE reservations(
		id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),request_digest TEXT NOT NULL,
		token TEXT NOT NULL UNIQUE CHECK(length(CAST(token AS BLOB)) BETWEEN 16 AND 128),revision INTEGER NOT NULL CHECK(revision>0),
		state TEXT NOT NULL CHECK(state IN('active','released','recovered')),process_id TEXT NOT NULL REFERENCES owner_processes(id),daemon_id TEXT NOT NULL,
		host_scope TEXT NOT NULL,task_id TEXT NOT NULL,session_id TEXT NOT NULL,provider_id TEXT NOT NULL,model_id TEXT NOT NULL,profile TEXT NOT NULL,
		device TEXT NOT NULL,ram_bytes INTEGER NOT NULL CHECK(ram_bytes>0),vram_bytes INTEGER NOT NULL CHECK(vram_bytes>=0),context_tokens INTEGER NOT NULL CHECK(context_tokens>0),config_digest TEXT NOT NULL,
		acquired_at_ns INTEGER NOT NULL CHECK(acquired_at_ns>=0),last_renewed_at_ns INTEGER NOT NULL CHECK(last_renewed_at_ns>=acquired_at_ns),
		expires_at_ns INTEGER NOT NULL CHECK(expires_at_ns>last_renewed_at_ns),terminal_at_ns INTEGER,request BLOB NOT NULL CHECK(length(request) BETWEEN 1 AND 65536),
		CHECK((state='active' AND terminal_at_ns IS NULL) OR (state!='active' AND terminal_at_ns IS NOT NULL)));
	CREATE INDEX reservations_capacity ON reservations(host_scope,state,expires_at_ns,id);
	CREATE INDEX reservations_provider ON reservations(host_scope,provider_id,state,expires_at_ns,id);
	CREATE INDEX reservations_device ON reservations(host_scope,device,state,expires_at_ns,id);
	CREATE TABLE reservation_events(
		reservation_id TEXT NOT NULL REFERENCES reservations(id),revision INTEGER NOT NULL CHECK(revision>0),kind TEXT NOT NULL CHECK(kind IN('acquired','renewed','released','recovered')),
		previous_digest TEXT,event_digest TEXT NOT NULL UNIQUE,observed_at_ns INTEGER NOT NULL CHECK(observed_at_ns>=0),process_id TEXT NOT NULL REFERENCES owner_processes(id),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),PRIMARY KEY(reservation_id,revision));
	CREATE INDEX reservation_events_time ON reservation_events(observed_at_ns,reservation_id,revision);
	CREATE TABLE reservation_recoveries(
		reservation_id TEXT PRIMARY KEY REFERENCES reservations(id),recovery_id TEXT NOT NULL UNIQUE,previous_process_id TEXT NOT NULL REFERENCES owner_processes(id),
		recovering_process_id TEXT NOT NULL REFERENCES owner_processes(id),event_digest TEXT NOT NULL UNIQUE REFERENCES reservation_events(event_digest),
		recovered_at_ns INTEGER NOT NULL CHECK(recovered_at_ns>=0),body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),CHECK(previous_process_id!=recovering_process_id));
	CREATE TRIGGER reservation_event_immutable_update BEFORE UPDATE ON reservation_events BEGIN SELECT RAISE(ABORT,'reservation event immutable'); END;
	CREATE TRIGGER reservation_event_immutable_delete BEFORE DELETE ON reservation_events BEGIN SELECT RAISE(ABORT,'reservation event immutable'); END;
	CREATE TRIGGER reservation_recovery_immutable_update BEFORE UPDATE ON reservation_recoveries BEGIN SELECT RAISE(ABORT,'reservation recovery immutable'); END;
	CREATE TRIGGER reservation_recovery_immutable_delete BEFORE DELETE ON reservation_recoveries BEGIN SELECT RAISE(ABORT,'reservation recovery immutable'); END;
	PRAGMA user_version=1;`)
	return err
}

func validateSchema(ctx context.Context, conn *sql.Conn) error {
	want := map[string]string{
		"owner_processes": "id:TEXT:0:1,body:BLOB:1:0", "coordinator_policy": "singleton:INTEGER:0:1,max_concurrent:INTEGER:1:0,ram_percent_bits:INTEGER:1:0,vram_percent_bits:INTEGER:1:0,max_age_ns:INTEGER:1:0,adaptive:INTEGER:1:0", "coordinator_clock": "singleton:INTEGER:0:1,last_seen_ns:INTEGER:1:0",
		"reservations":           "id:TEXT:0:1,request_digest:TEXT:1:0,token:TEXT:1:0,revision:INTEGER:1:0,state:TEXT:1:0,process_id:TEXT:1:0,daemon_id:TEXT:1:0,host_scope:TEXT:1:0,task_id:TEXT:1:0,session_id:TEXT:1:0,provider_id:TEXT:1:0,model_id:TEXT:1:0,profile:TEXT:1:0,device:TEXT:1:0,ram_bytes:INTEGER:1:0,vram_bytes:INTEGER:1:0,context_tokens:INTEGER:1:0,config_digest:TEXT:1:0,acquired_at_ns:INTEGER:1:0,last_renewed_at_ns:INTEGER:1:0,expires_at_ns:INTEGER:1:0,terminal_at_ns:INTEGER:0:0,request:BLOB:1:0",
		"reservation_events":     "reservation_id:TEXT:1:1,revision:INTEGER:1:2,kind:TEXT:1:0,previous_digest:TEXT:0:0,event_digest:TEXT:1:0,observed_at_ns:INTEGER:1:0,process_id:TEXT:1:0,body:BLOB:1:0",
		"reservation_recoveries": "reservation_id:TEXT:0:1,recovery_id:TEXT:1:0,previous_process_id:TEXT:1:0,recovering_process_id:TEXT:1:0,event_digest:TEXT:1:0,recovered_at_ns:INTEGER:1:0,body:BLOB:1:0",
	}
	for name, shape := range want {
		got, err := tableShape(ctx, conn, name)
		if err != nil || got != shape {
			return fmt.Errorf("invalid resource coordinator table %s", name)
		}
	}
	// Column shape alone is not authority: a same-version database with any
	// uniqueness, ownership, lifecycle, size, or time bound removed would admit
	// states the coordinator never writes. Match the complete canonical table
	// definitions before trusting existing rows.
	tableDDL := map[string]string{
		"owner_processes": `CREATE TABLE owner_processes(
		id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 8192))`,
		"coordinator_policy": `CREATE TABLE coordinator_policy(singleton INTEGER PRIMARY KEY CHECK(singleton=1),max_concurrent INTEGER NOT NULL CHECK(max_concurrent BETWEEN 1 AND 64),ram_percent_bits INTEGER NOT NULL,vram_percent_bits INTEGER NOT NULL,max_age_ns INTEGER NOT NULL CHECK(max_age_ns>0),adaptive INTEGER NOT NULL CHECK(adaptive IN(0,1)))`,
		"coordinator_clock":  `CREATE TABLE coordinator_clock(singleton INTEGER PRIMARY KEY CHECK(singleton=1),last_seen_ns INTEGER NOT NULL CHECK(last_seen_ns>=0))`,
		"reservations": `CREATE TABLE reservations(
		id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),request_digest TEXT NOT NULL,
		token TEXT NOT NULL UNIQUE CHECK(length(CAST(token AS BLOB)) BETWEEN 16 AND 128),revision INTEGER NOT NULL CHECK(revision>0),
		state TEXT NOT NULL CHECK(state IN('active','released','recovered')),process_id TEXT NOT NULL REFERENCES owner_processes(id),daemon_id TEXT NOT NULL,
		host_scope TEXT NOT NULL,task_id TEXT NOT NULL,session_id TEXT NOT NULL,provider_id TEXT NOT NULL,model_id TEXT NOT NULL,profile TEXT NOT NULL,
		device TEXT NOT NULL,ram_bytes INTEGER NOT NULL CHECK(ram_bytes>0),vram_bytes INTEGER NOT NULL CHECK(vram_bytes>=0),context_tokens INTEGER NOT NULL CHECK(context_tokens>0),config_digest TEXT NOT NULL,
		acquired_at_ns INTEGER NOT NULL CHECK(acquired_at_ns>=0),last_renewed_at_ns INTEGER NOT NULL CHECK(last_renewed_at_ns>=acquired_at_ns),
		expires_at_ns INTEGER NOT NULL CHECK(expires_at_ns>last_renewed_at_ns),terminal_at_ns INTEGER,request BLOB NOT NULL CHECK(length(request) BETWEEN 1 AND 65536),
		CHECK((state='active' AND terminal_at_ns IS NULL) OR (state!='active' AND terminal_at_ns IS NOT NULL)))`,
		"reservation_events": `CREATE TABLE reservation_events(
		reservation_id TEXT NOT NULL REFERENCES reservations(id),revision INTEGER NOT NULL CHECK(revision>0),kind TEXT NOT NULL CHECK(kind IN('acquired','renewed','released','recovered')),
		previous_digest TEXT,event_digest TEXT NOT NULL UNIQUE,observed_at_ns INTEGER NOT NULL CHECK(observed_at_ns>=0),process_id TEXT NOT NULL REFERENCES owner_processes(id),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),PRIMARY KEY(reservation_id,revision))`,
		"reservation_recoveries": `CREATE TABLE reservation_recoveries(
		reservation_id TEXT PRIMARY KEY REFERENCES reservations(id),recovery_id TEXT NOT NULL UNIQUE,previous_process_id TEXT NOT NULL REFERENCES owner_processes(id),
		recovering_process_id TEXT NOT NULL REFERENCES owner_processes(id),event_digest TEXT NOT NULL UNIQUE REFERENCES reservation_events(event_digest),
		recovered_at_ns INTEGER NOT NULL CHECK(recovered_at_ns>=0),body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),CHECK(previous_process_id!=recovering_process_id))`,
	}
	for name, expected := range tableDDL {
		var sqlText string
		if err := conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&sqlText); err != nil {
			return fmt.Errorf("invalid resource coordinator table %s", name)
		}
		if normalizeDDL(sqlText) != normalizeDDL(expected) {
			return fmt.Errorf("invalid resource coordinator table %s", name)
		}
	}
	objects := map[string]string{
		"reservations_capacity":                 "CREATE INDEX reservations_capacity ON reservations(host_scope,state,expires_at_ns,id)",
		"reservations_provider":                 "CREATE INDEX reservations_provider ON reservations(host_scope,provider_id,state,expires_at_ns,id)",
		"reservations_device":                   "CREATE INDEX reservations_device ON reservations(host_scope,device,state,expires_at_ns,id)",
		"reservation_events_time":               "CREATE INDEX reservation_events_time ON reservation_events(observed_at_ns,reservation_id,revision)",
		"reservation_event_immutable_update":    "CREATE TRIGGER reservation_event_immutable_update BEFORE UPDATE ON reservation_events BEGIN SELECT RAISE(ABORT,'reservation event immutable'); END",
		"reservation_event_immutable_delete":    "CREATE TRIGGER reservation_event_immutable_delete BEFORE DELETE ON reservation_events BEGIN SELECT RAISE(ABORT,'reservation event immutable'); END",
		"reservation_recovery_immutable_update": "CREATE TRIGGER reservation_recovery_immutable_update BEFORE UPDATE ON reservation_recoveries BEGIN SELECT RAISE(ABORT,'reservation recovery immutable'); END",
		"reservation_recovery_immutable_delete": "CREATE TRIGGER reservation_recovery_immutable_delete BEFORE DELETE ON reservation_recoveries BEGIN SELECT RAISE(ABORT,'reservation recovery immutable'); END",
	}
	for name, expected := range objects {
		var sqlText string
		if err := conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name=?", name).Scan(&sqlText); err != nil || normalizeDDL(sqlText) != normalizeDDL(expected) {
			return fmt.Errorf("invalid resource coordinator object %s", name)
		}
	}
	var clocks int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM coordinator_clock WHERE singleton=1 AND last_seen_ns>=0").Scan(&clocks); err != nil || clocks != 1 {
		return errors.New("invalid resource coordinator clock")
	}
	var policies int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM coordinator_policy").Scan(&policies); err != nil || policies > 1 {
		return errors.New("invalid resource coordinator policy")
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		return errors.New("invalid resource coordinator foreign keys")
	}
	return nil
}

func normalizeDDL(value string) string {
	return strings.Map(func(r rune) rune {
		if r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

func tableShape(ctx context.Context, conn *sql.Conn, table string) (string, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	parts := []string{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%d:%d", name, kind, notnull, pk))
	}
	return strings.Join(parts, ","), rows.Err()
}
