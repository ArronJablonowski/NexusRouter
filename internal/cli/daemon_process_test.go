package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestDaemonLifecycleAcrossCLIProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "darwin")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/darwin").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	configuration := filepath.Join(dir, "daemon.yaml")
	exports := make(chan struct{}, 8)
	var collectorFail atomic.Bool
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/v1/metrics" || !bytes.Contains(body, []byte("resourceMetrics")) {
			t.Error("invalid daemon export")
		}
		select {
		case exports <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		if collectorFail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"private-collector-detail"}`)
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer collector.Close()
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("daemon:\n  listen: %q\ntelemetry:\n  database: %q\n  metrics_export:\n    enabled: true\n    endpoint: %q\n    interval: 1s\n", address, filepath.Join(dir, "tasks.db"), collector.URL+"/v1/metrics")), 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("lifecycle-fixture-", 3)
	run := func(action string) (daemon.Status, error) {
		cmd := exec.CommandContext(ctx, binary, "daemon", action, "--config", configuration)
		cmd.Env = append(os.Environ(), "DARWIN_API_TOKEN="+token)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		if strings.Contains(out.String()+diagnostic.String(), token) {
			t.Error("token leaked")
		}
		var status daemon.Status
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &status)
		}
		return status, err
	}
	first, err := run("start")
	if err != nil || first.Validate() != nil || first.State != "ready" {
		t.Fatal("start failed", err, first)
	}
	liveID := first.InstanceID
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		cfg := config.Defaults()
		cfg.Daemon.Listen = address
		client, err := daemonClient(cfg, token)
		if err == nil {
			defer client.Close()
			_, _ = client.Stop(cleanup, liveID)
		}
	}()
	select {
	case <-exports:
	case <-ctx.Done():
		t.Fatal("owned daemon did not export configured metrics")
	}
	status, err := run("status")
	if err != nil || status.InstanceID != first.InstanceID {
		t.Fatal("status identity", status, err)
	}
	// Exercise the real serve.go service binding in the already-running child.
	// This is inspection only: the database must remain without task execution.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/resources/attention", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	qualifyDaemonMetricsFailure(t, ctx, client, address, token, &collectorFail, run)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("attention endpoint request failed", err)
	}
	var attention workers.LeaseAttentionPage
	decodeErr := json.NewDecoder(response.Body).Decode(&attention)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || attention.Validate() != nil || attention.Version != 1 || attention.StorageSchema != 30 || !attention.Available || attention.Items == nil || len(attention.Items) != 0 || attention.HasMore || attention.NextCursor != "" {
		t.Fatal("real daemon attention inspection failed", response.StatusCode, decodeErr, closeErr)
	}
	// The production serve wiring must bind audit inspection to the application
	// service. An unknown operation is therefore a sanitized 404, not the 503
	// returned when the hook is absent, and it performs no model dispatch.
	auditURL := "http://" + address + "/v1/tasks/missing-task/audits/" + strings.Repeat("a", 64)
	auditRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, auditURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	auditRequest.Header.Set("Authorization", "Bearer "+token)
	auditResponse, err := client.Do(auditRequest)
	if err != nil {
		t.Fatal("audit inspection request failed", err)
	}
	auditBody, auditReadErr := io.ReadAll(io.LimitReader(auditResponse.Body, 8193))
	auditCloseErr := auditResponse.Body.Close()
	if auditResponse.StatusCode != http.StatusNotFound || auditReadErr != nil || auditCloseErr != nil || !bytes.Contains(auditBody, []byte(`"error":"audit_unavailable"`)) || bytes.Contains(auditBody, []byte(token)) {
		t.Fatal("production audit inspection route not wired safely", auditResponse.StatusCode, auditReadErr, auditCloseErr, string(auditBody))
	}
	usageRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/tasks/missing-task/usage", nil)
	if err != nil {
		t.Fatal(err)
	}
	usageRequest.Header.Set("Authorization", "Bearer "+token)
	usageResponse, err := client.Do(usageRequest)
	if err != nil {
		t.Fatal("usage inspection request failed", err)
	}
	usageBody, usageReadErr := io.ReadAll(io.LimitReader(usageResponse.Body, 8193))
	usageCloseErr := usageResponse.Body.Close()
	if usageResponse.StatusCode != http.StatusNotFound || usageReadErr != nil || usageCloseErr != nil || !bytes.Contains(usageBody, []byte(`"error":"usage_unavailable"`)) || bytes.Contains(usageBody, []byte(token)) {
		t.Fatal("production usage inspection route not wired safely", usageResponse.StatusCode, usageReadErr, usageCloseErr, string(usageBody))
	}
	databaseURL := url.URL{Scheme: "file", Path: filepath.Join(dir, "tasks.db"), RawQuery: "mode=ro"}
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	var taskCount int
	queryErr := database.QueryRowContext(ctx, `SELECT count(*) FROM task_heads`).Scan(&taskCount)
	closeErr = database.Close()
	if queryErr != nil || closeErr != nil || taskCount != 0 {
		t.Fatal("attention inspection created execution", queryErr, closeErr, taskCount)
	}
	// A missing history returns 404, not a missing service hook or an invented
	// empty result. Then seed metadata only in this owned daemon fixture DB.
	historyURL := "http://" + address + "/v1/resources/attention/daemon-history/history"
	readHistory := func() (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, historyURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal("history endpoint request failed", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, 8193))
		if closeErr := res.Body.Close(); readErr != nil || closeErr != nil || len(body) > 8192 {
			t.Fatal("history response unreadable", readErr, closeErr)
		}
		return res, body
	}
	res, body := readHistory()
	if res.StatusCode != http.StatusNotFound {
		t.Fatal("missing history route not wired", res.StatusCode, string(body))
	}
	databaseURL.RawQuery = "mode=rw"
	fixtureDB, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureDB.Close()
	if _, err := fixtureDB.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	fixtureTx, err := fixtureDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureTx.Rollback()
	now := time.Now().UTC()
	observation := workers.LeaseAttention{Version: 1, ID: "daemon-history", TaskID: "history-fixture", State: "resolved", Reason: "lease_released", FirstObserved: now, UpdatedAt: now, LeaseExpires: now.Add(-time.Minute)}
	observationBody, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('history-fixture','history-fixture',1,'failed')`, nil},
		{`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES('private-history-token','history-fixture','private-owner','private-scope',0,?,1)`, []any{observation.LeaseExpires.UnixNano()}},
		{`INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES('daemon-history','private-history-token','history-fixture','resolved',?)`, []any{observationBody}},
		{`INSERT INTO lease_attention_history(attention_id,sequence,kind,body) VALUES('daemon-history',1,'baseline',?)`, []any{observationBody}},
	} {
		if _, err := fixtureTx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixtureTx.Commit(); err != nil {
		t.Fatal(err)
	}
	res, body = readHistory()
	var history workers.LeaseAttentionHistoryPage
	if res.StatusCode != http.StatusOK || json.Unmarshal(body, &history) != nil || history.Version != 1 || history.StorageSchema != 30 || !history.Available || history.AttentionID != "daemon-history" || len(history.Items) != 1 || history.Items[0].Sequence != 1 || history.Items[0].Kind != "baseline" || history.Items[0].Observation != observation {
		t.Fatal("positive history route not wired", res.StatusCode, string(body))
	}
	for _, private := range []string{token, "private-history-token", "private-owner", "private-scope"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatal("history exposed private metadata")
		}
	}
	var eventCount int
	if err := fixtureDB.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatal("history inspection executed task", err, eventCount)
	}
	if _, err := run("start"); err == nil {
		t.Fatal("duplicate start accepted")
	}
	status, err = run("status")
	if err != nil || status.InstanceID != first.InstanceID {
		t.Fatal("duplicate disturbed live daemon", status, err)
	}
	stopped, err := run("stop")
	if err != nil || stopped.InstanceID != first.InstanceID || stopped.State != "stopping" {
		t.Fatal("stop failed", stopped, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("stopped listener still open")
		}
		time.Sleep(20 * time.Millisecond)
	}
	second, err := run("start")
	if err == nil {
		liveID = second.InstanceID
	}
	if err != nil || second.InstanceID == first.InstanceID || second.State != "ready" {
		t.Fatal("restart failed", second, err)
	}
}
