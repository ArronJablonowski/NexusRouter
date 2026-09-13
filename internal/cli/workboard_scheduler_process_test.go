//go:build darwin || linux

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"go.yaml.in/yaml/v3"
)

type workboardDaemonProvider struct {
	workerCalls   atomic.Int32
	reviewerCalls atomic.Int32
	blockWorker   atomic.Bool
	workerStarted chan struct{}
	disconnected  chan struct{}
}

func newWorkboardDaemonProvider() *workboardDaemonProvider {
	return &workboardDaemonProvider{workerStarted: make(chan struct{}, 4), disconnected: make(chan struct{}, 4)}
}

func (p *workboardDaemonProvider) worker(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/tags" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[{"name":"worker-native"}]}`)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || !validWorkboardDaemonProviderRequest(r) {
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	p.workerCalls.Add(1)
	select {
	case p.workerStarted <- struct{}{}:
	default:
	}
	if p.blockWorker.Load() {
		<-r.Context().Done()
		select {
		case p.disconnected <- struct{}{}:
		default:
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message":           map[string]string{"role": "assistant", "content": "qualified daemon candidate"},
		"done":              true,
		"done_reason":       "stop",
		"prompt_eval_count": 10,
		"eval_count":        5,
	})
}

func (p *workboardDaemonProvider) reviewer(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/tags" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[{"name":"reviewer-native"}]}`)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || !validWorkboardDaemonProviderRequest(r) {
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	p.reviewerCalls.Add(1)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message": map[string]string{"role": "assistant", "content": workboardDaemonAudit()},
		"done":    true, "done_reason": "stop", "prompt_eval_count": 8, "eval_count": 4,
	})
}

func validWorkboardDaemonProviderRequest(r *http.Request) bool {
	var request struct {
		Messages []providers.Message `json:"messages"`
		Options  struct {
			MaxOutputTokens int64 `json:"num_predict"`
		} `json:"options"`
	}
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request) == nil &&
		len(request.Messages) > 0 && request.Options.MaxOutputTokens > 0
}

func workboardDaemonAudit() string {
	body, _ := json.Marshal(map[string]any{
		"version": 1, "evaluator_id": "reviewer", "rubric_version": "darwin-review-v2",
		"domain": "general", "verdict": "accept", "confidence": .9,
		"findings": []any{map[string]any{
			"summary":       "The candidate satisfies the configured criterion.",
			"evidence_refs": []string{"criterion_00", "source_binding"},
		}},
	})
	return string(body)
}

// TestEnabledWorkboardSchedulerDaemonExecutesAndJoinsOnSIGTERM qualifies the
// stock executable rather than an in-process composition. It proves that an
// enabled scheduler participates in readiness, uses independent loopback
// worker and reviewer identities, and joins an in-flight provider call before
// the daemon closes its database and exits.
func TestEnabledWorkboardSchedulerDaemonExecutesAndJoinsOnSIGTERM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	binary := buildWorkboardDaemonBinary(t, ctx, dir)
	fixture := newWorkboardDaemonProvider()
	workerServer := httptest.NewServer(http.HandlerFunc(fixture.worker))
	defer workerServer.Close()
	reviewerServer := httptest.NewServer(http.HandlerFunc(fixture.reviewer))
	defer reviewerServer.Close()

	cfg, configuration := workboardDaemonConfiguration(t, dir, workerServer.URL, reviewerServer.URL)
	boardID, firstCard := seedWorkboardDaemonCard(t, cfg.Telemetry.Database, "success")
	token := strings.Repeat("workboard-daemon-token-", 2)
	client := workboardDaemonClient(t)
	process := startOwnedDaemon(t, ctx, binary, configuration, token, filepath.Join(dir, "owners"))
	defer killAndJoinOwnedDaemon(process)
	waitDaemonReady(t, ctx, process, client, cfg.Daemon.Listen, token)
	report := readWorkboardDaemonHealth(t, ctx, client, cfg.Daemon.Listen, token)
	assertWorkboardSchedulerHealth(t, report, "healthy", "supervisor_ok")
	waitForWorkboardWorkerCall(t, ctx, fixture, 1)
	waitForWorkboardReviewerCall(t, ctx, fixture, 1, process)
	waitWorkboardCardState(t, ctx, cfg.Telemetry.Database, boardID, firstCard, workboard.Review, fixture, process)
	if fixture.workerCalls.Load() != 1 || fixture.reviewerCalls.Load() != 1 {
		t.Fatal("configured worker/reviewer did not execute exactly once", fixture.workerCalls.Load(), fixture.reviewerCalls.Load())
	}

	fixture.blockWorker.Store(true)
	secondBoard, secondCard := seedWorkboardDaemonCard(t, cfg.Telemetry.Database, "shutdown")
	select {
	case <-fixture.workerStarted:
		// Drain the successful card's buffered notification first.
	default:
	}
	waitForWorkboardWorkerCall(t, ctx, fixture, 2)
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.disconnected:
	case <-ctx.Done():
		t.Fatal("SIGTERM did not cancel the in-flight Workboard provider")
	}
	select {
	case <-process.done:
	case <-ctx.Done():
		t.Fatal("SIGTERM did not join the Workboard scheduler")
	}
	if process.waitErr != nil || strings.Contains(process.output.String(), token) ||
		strings.Contains(process.output.String(), "requires inspection") ||
		strings.Contains(process.output.String(), "daemon stopped with an error") {
		t.Fatal("daemon did not stop cleanly", process.waitErr, process.output.String())
	}
	assertWorkboardDatabaseReadable(t, cfg.Telemetry.Database, boardID, firstCard)
	assertWorkboardDatabaseReadable(t, cfg.Telemetry.Database, secondBoard, secondCard)
}

// TestEnabledWorkboardSchedulerDaemonAfterSIGKILLDoesNotRedispatch qualifies
// the process-loss boundary. A killed daemon leaves an uncertain, eventually
// expired claim; a fresh daemon must derive it as existing WIP and must not
// manufacture retry authority from age, lease expiry, or a healthy supervisor.
func TestEnabledWorkboardSchedulerDaemonAfterSIGKILLDoesNotRedispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	binary := buildWorkboardDaemonBinary(t, ctx, dir)
	fixture := newWorkboardDaemonProvider()
	fixture.blockWorker.Store(true)
	workerServer := httptest.NewServer(http.HandlerFunc(fixture.worker))
	defer workerServer.Close()
	reviewerServer := httptest.NewServer(http.HandlerFunc(fixture.reviewer))
	defer reviewerServer.Close()
	cfg, configuration := workboardDaemonConfiguration(t, dir, workerServer.URL, reviewerServer.URL)
	boardID, cardID := seedWorkboardDaemonCard(t, cfg.Telemetry.Database, "crash")
	token := strings.Repeat("workboard-crash-token-", 2)
	owners := filepath.Join(dir, "owners")
	client := workboardDaemonClient(t)

	first := startOwnedDaemon(t, ctx, binary, configuration, token, owners)
	defer killAndJoinOwnedDaemon(first)
	waitWorkboardDaemonServing(t, ctx, first, client, cfg.Daemon.Listen, token)
	waitForWorkboardWorkerCall(t, ctx, fixture, 1)
	if err := first.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-first.done
	if first.waitErr == nil || first.command.ProcessState == nil {
		t.Fatal("daemon survived SIGKILL")
	}
	status, ok := first.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("daemon did not terminate by SIGKILL")
	}
	select {
	case <-fixture.disconnected:
	case <-ctx.Done():
		t.Fatal("crashed daemon retained its provider connection")
	}
	time.Sleep(200 * time.Millisecond) // Exceed the configured 100ms claim lease.

	second := startOwnedDaemon(t, ctx, binary, configuration, token, owners)
	defer killAndJoinOwnedDaemon(second)
	waitDaemonReady(t, ctx, second, client, cfg.Daemon.Listen, token)
	time.Sleep(600 * time.Millisecond) // More than two configured schedule passes.
	if fixture.workerCalls.Load() != 1 || fixture.reviewerCalls.Load() != 0 {
		t.Fatal("restart redispatched uncertain Workboard work", fixture.workerCalls.Load(), fixture.reviewerCalls.Load())
	}
	report := readWorkboardDaemonHealth(t, ctx, client, cfg.Daemon.Listen, token)
	assertWorkboardSchedulerHealth(t, report, "healthy", "supervisor_ok")
	store, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	card, cardErr := store.GetCard(ctx, boardID, cardID)
	closeErr := store.Close()
	if cardErr != nil || closeErr != nil || card.State != workboard.InProgress || card.CurrentClaimID == "" {
		t.Fatal("restart changed uncertain claim without recovery proof", card, cardErr, closeErr)
	}
	if err = second.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-second.done
	if second.waitErr != nil {
		t.Fatal("restarted daemon did not stop cleanly", second.waitErr, second.output.String())
	}
}

func buildWorkboardDaemonBinary(t *testing.T, ctx context.Context, dir string) string {
	t.Helper()
	if binary := os.Getenv("DARWIN_TEST_DAEMON_BINARY"); binary != "" {
		if !filepath.IsAbs(binary) {
			t.Fatal("test daemon binary must be absolute")
		}
		return binary
	}
	binary := filepath.Join(dir, "darwin")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/darwin").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	return binary
}

func workboardDaemonConfiguration(t *testing.T, dir, workerEndpoint, reviewerEndpoint string) (config.Settings, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cfg := config.Defaults()
	zero := 0.0
	cfg.Mode, cfg.Daemon.Listen = "local_only", address
	cfg.Memory.Enabled, cfg.Skills.Enabled, cfg.Tools.Enabled = false, false, false
	cfg.Runtime.MaxTurns = 1
	cfg.Workers.Max, cfg.Workers.Heartbeat, cfg.Workers.Lease = 1, "20ms", "100ms"
	cfg.Telemetry.Database = filepath.Join(dir, "workboard-daemon.db")
	cfg.Providers = []config.Provider{
		{ID: "worker-provider", Kind: "ollama", Endpoint: workerEndpoint},
		{ID: "reviewer-provider", Kind: "ollama", Endpoint: reviewerEndpoint},
	}
	cfg.Models = []config.Model{
		{ID: "worker", Provider: "worker-provider", Model: "worker-native", Locality: "local", ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 1, Capabilities: []string{"chat"}},
		{ID: "reviewer", Provider: "reviewer-provider", Model: "reviewer-native", Locality: "local", ContextTokens: 32_768, EstimatedCost: &zero, RAMBytes: 1, Capabilities: []string{"audit"}},
	}
	cfg.Evaluation.Judge = true
	cfg.Workboard.Scheduler.Enabled = true
	cfg.Workboard.Scheduler.Interval = "250ms"
	cfg.Workboard.Scheduler.MaxActiveClaims = 1
	cfg.Workboard.Scheduler.CardScanLimit = 100
	cfg.Workboard.Scheduler.WorkerModel = "worker"
	cfg.Workboard.Scheduler.AcceptanceJudge = config.WorkboardAcceptanceJudge{
		Enabled: true, ReviewerModel: "reviewer", MaxCost: .1,
		MaxInputTokens: 20_000, MaxOutputTokens: 200, Timeout: "2s",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func seedWorkboardDaemonCard(t *testing.T, database, suffix string) (string, string) {
	t.Helper()
	ctx := context.Background()
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authority := workboardDaemonAuthority{}
	boards, err := workboard.NewBoardService(store, authority, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := workboard.NewCardService(store, authority)
	if err != nil {
		t.Fatal(err)
	}
	title := "Daemon board " + suffix
	board, err := boards.Create(ctx, workboard.CreateBoardRequest{Version: 1,
		IdempotencyKey: "daemon-board-" + suffix, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	cardTitle := "Daemon card " + suffix
	description := "Produce a concise implementation result for " + suffix + "."
	budget := workboard.WorkBudget{AttemptLimit: 2, TimeLimitMS: 30_000, TokenLimit: 50_000, CostMicros: 1_000_000}
	criteria := []workboard.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "deterministic.meaningful_text.v1", Description: "The configured qualification passes", Required: true}}
	card, err := cards.CreateCard(ctx, workboard.CreateCardRequest{BoardID: board.BoardID,
		IdempotencyKey: "daemon-card-" + suffix, ExpectedBoardRevision: board.BoardRevision, ExpectedGraphRevision: 1,
		Card: workboard.NewCard{Title: cardTitle, Description: description, Priority: "normal", Labels: []string{},
			Dependencies: []string{}, Budget: budget, Criteria: criteria}})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := store.LoadGraph(ctx, board.BoardID)
	if err != nil || graph.BoardID != board.BoardID || graph.GraphRevision < 1 || graph.LayoutRevision < 1 {
		t.Fatal("card creation omitted graph revisions", card, graph, err)
	}
	ready, err := cards.MoveCard(ctx, workboard.MoveCardRequest{BoardID: board.BoardID, CardID: card.ID,
		IdempotencyKey: "daemon-ready-" + suffix, TargetState: workboard.Ready,
		ExpectedBoardRevision: card.Receipt.BoardRevision, ExpectedLayoutRevision: graph.LayoutRevision,
		ExpectedCardRevision: card.Revision})
	if err != nil || ready.Receipt.CardRevision == nil {
		t.Fatal("cannot prepare daemon Workboard card", err, ready)
	}
	return board.BoardID, card.ID
}

type workboardDaemonAuthority struct{}

func (workboardDaemonAuthority) WorkboardAuthority(context.Context) (workboard.Authority, error) {
	return workboard.Authority{CreationScope: "daemon-process-test", Actor: workboard.Actor{ID: "test-operator", Type: "operator"}}, nil
}

func workboardDaemonClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func readWorkboardDaemonHealth(t *testing.T, ctx context.Context, client *http.Client, address, token string) health.Report {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var report health.Report
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&report) != nil || report.Validate() != nil || !report.Ready {
		t.Fatal("invalid scheduler-enabled health report", response.StatusCode, report)
	}
	return report
}

func waitWorkboardDaemonServing(t *testing.T, ctx context.Context, process *ownedDaemonProcess, client *http.Client, address, token string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-process.done:
			t.Fatal("daemon exited before serving", process.waitErr, process.output.String())
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/daemon/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := client.Do(request)
		if requestErr == nil {
			var status daemon.Status
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&status)
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && closeErr == nil && status.Validate() == nil {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("daemon did not begin serving", process.output.String())
}

func assertWorkboardSchedulerHealth(t *testing.T, report health.Report, status, code string) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Component == "workboard_scheduler" {
			if check.Status != status || check.Code != code {
				t.Fatal("unexpected Workboard scheduler health", check)
			}
			return
		}
	}
	t.Fatal("Workboard scheduler missing from health report", report)
}

func waitForWorkboardWorkerCall(t *testing.T, ctx context.Context, fixture *workboardDaemonProvider, count int32) {
	t.Helper()
	for fixture.workerCalls.Load() < count {
		select {
		case <-fixture.workerStarted:
		case <-ctx.Done():
			t.Fatal("Workboard worker was not dispatched", count, fixture.workerCalls.Load())
		}
	}
}

func waitForWorkboardReviewerCall(t *testing.T, ctx context.Context, fixture *workboardDaemonProvider, count int32,
	process *ownedDaemonProcess,
) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for fixture.reviewerCalls.Load() < count {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("Workboard reviewer was not dispatched", count, fixture.reviewerCalls.Load(), process.output.String())
		case <-ctx.Done():
			t.Fatal("Workboard reviewer wait canceled", ctx.Err(), process.output.String())
		}
	}
}

func waitWorkboardCardState(t *testing.T, ctx context.Context, database, boardID, cardID string, state workboard.State,
	fixture *workboardDaemonProvider, process *ownedDaemonProcess,
) {
	t.Helper()
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deadline := time.Now().Add(10 * time.Second)
	var last workboard.Card
	var lastErr error
	for time.Now().Before(deadline) {
		card, cardErr := store.GetCard(ctx, boardID, cardID)
		last, lastErr = card, cardErr
		if cardErr == nil && card.State == state {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	lifecycle, _ := store.ReadCardLifecycleSnapshots(context.Background(), boardID, []string{cardID})
	tasks, _ := store.ListTasks(context.Background(), sessions.TaskListOptions{Limit: 10})
	t.Fatal("Workboard card did not reach state", state, "last", last, "error", lastErr,
		"worker_calls", fixture.workerCalls.Load(), "reviewer_calls", fixture.reviewerCalls.Load(), "lifecycle", lifecycle,
		"tasks", tasks, "daemon", process.output.String())
}

func assertWorkboardDatabaseReadable(t *testing.T, database, boardID string, cardIDs ...string) {
	t.Helper()
	ctx := context.Background()
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal("database unavailable after scheduler shutdown", err)
	}
	defer store.Close()
	for _, cardID := range cardIDs {
		card, err := store.GetCard(ctx, boardID, cardID)
		if err != nil || card.Validate() != nil {
			t.Fatal("card unavailable after scheduler shutdown", cardID, err)
		}
	}
}
