//go:build darwin || linux

package cli

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"go.yaml.in/yaml/v3"
)

const codexChatSteeringMarker = "DARWIN_STEERING_REVIEW_42_CONFIRMED"

func TestChatCodexSteeringOwnedChild(t *testing.T) {
	if os.Getenv("DARWIN_CHAT_CODEX_STEERING_CHILD") != "1" {
		t.Skip("owned child only")
	}
	path := os.Getenv("DARWIN_CHAT_CODEX_STEERING_CONFIG")
	if !filepath.IsAbs(path) {
		os.Exit(90)
	}
	os.Exit(RunWithInput([]string{"chat", "--config", path, "--model", "coordinator"}, os.Stdin, os.Stdout, os.Stderr, "live-fixture"))
}

// Explicit opt-in spends signed-in Codex usage and runs the installed Ollama
// worker. It never opens the smoke configuration's operational database.
func TestChatCodexLiveSteering(t *testing.T) {
	if os.Getenv("DARWIN_CHAT_CODEX_LIVE_STEERING") != "1" {
		t.Skip("set DARWIN_CHAT_CODEX_LIVE_STEERING=1 for owned real-provider qualification")
	}
	cfg, err := config.Load(config.Options{ProjectFile: "../../examples/sol-codex-local-smoke.yaml"})
	if err != nil {
		t.Fatal("smoke configuration unavailable")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("Codex executable unavailable")
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal("Codex executable path unavailable")
	}
	dir := t.TempDir()
	database := filepath.Join(dir, "chat.db")
	cfg.Telemetry.Database = database
	for i := range cfg.Providers {
		if cfg.Providers[i].Kind == "codex_app_server" {
			cfg.Providers[i].Executable = executable
		}
	}
	if !codexChatSteeringAuthority(cfg) {
		t.Fatal("unexpected smoke authority")
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal("cannot encode owned configuration")
	}
	path := filepath.Join(dir, "chat.yaml")
	if os.WriteFile(path, body, 0600) != nil {
		t.Fatal("cannot write owned configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal("input pipe unavailable")
	}
	defer input.Close()
	defer inputWriter.Close()
	output, outputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal("output pipe unavailable")
	}
	defer output.Close()
	defer outputWriter.Close()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChatCodexSteeringOwnedChild$")
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "DARWIN_PROCESS_OWNER_DIR="+filepath.Join(dir, "owners"), "DARWIN_CHAT_CODEX_STEERING_CHILD=1", "DARWIN_CHAT_CODEX_STEERING_CONFIG="+path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, outputWriter, io.Discard
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if cmd.Start() != nil {
		t.Fatal("owned CLI child could not start")
	}
	_ = input.Close()
	_ = outputWriter.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	joined := false
	defer func() {
		cancel()
		_ = output.Close()
		_ = inputWriter.Close()
		if !joined {
			<-done
		}
	}()
	const prompt = "Delegate exactly once to the configured local worker: ask for only a complete small Go source file, package answer, with func Answer() int { return 42 }. Set the delegation validation to go_source. Then review the actual worker result. Do not use any other tool."
	if _, err := io.WriteString(inputWriter, prompt+"\n"); err != nil {
		t.Fatal("could not send task")
	}
	result := make(chan codexChatSteeringRender, 1)
	go func() {
		result <- readCodexChatSteering(output, func() error {
			_, err := io.WriteString(inputWriter, "/steer Review the returned Go source. If it is valid and returns 42, output only "+codexChatSteeringMarker+". Do not call any more tools.\n")
			_ = inputWriter.Close()
			return err
		})
	}()
	var rendered codexChatSteeringRender
	select {
	case rendered = <-result:
	case <-ctx.Done():
		_ = output.Close()
		<-result
		t.Fatal("owned live CLI timed out")
	}
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal("owned live CLI failed; output withheld")
		}
	case <-ctx.Done():
		t.Fatal("owned live CLI did not join")
	}
	if rendered.err != nil || !rendered.sent || rendered.toolStarts != 1 || rendered.toolCompletions != 1 || rendered.taskCompletions != 1 || rendered.queued != 1 || rendered.applied != 1 || !rendered.marker {
		t.Fatal("live rendering qualification failed; output withheld")
	}
	qualifyCodexChatSteeringDatabase(t, database)
	t.Logf("live chat metadata: tool_starts=%d tool_results=%d guidance_queued=%d guidance_applied=%d task_completions=%d durable_tasks=3", rendered.toolStarts, rendered.toolCompletions, rendered.queued, rendered.applied, rendered.taskCompletions)
}

func codexChatSteeringAuthority(cfg config.Settings) bool {
	if cfg.Evaluation.AutoReviewModel != "" || cfg.Evaluation.AutoReviewMaxCost != 0 {
		return false
	}
	if cfg.Validate() != nil || cfg.Mode != "hybrid" || cfg.Tools.Enabled || cfg.Tools.CreateEnabled || cfg.Tools.ReplaceEnabled || cfg.Skills.Enabled || cfg.Skills.AutoDraft || cfg.Skills.AutoActivate || cfg.Skills.Learning.Enabled || cfg.Memory.Enabled || cfg.Evaluation.Judge || cfg.Telemetry.OTEL || cfg.Telemetry.MetricsExport != nil && cfg.Telemetry.MetricsExport.Enabled || cfg.Workers.DelegateMaxCalls != 1 || cfg.Workers.DelegateMaxCost != 0 || cfg.Workers.DelegateReadTools || cfg.Workers.DelegateModel != "local-worker" || cfg.Runtime.MaxTurns != 3 || len(cfg.Providers) != 2 || len(cfg.Models) != 2 {
		return false
	}
	providers := map[string]config.Provider{}
	for _, p := range cfg.Providers {
		if p.APIKeyEnv != "" {
			return false
		}
		providers[p.ID] = p
	}
	cloud, local := providers["codex-coordinator"], providers["ollama-worker"]
	if len(providers) != 2 || cloud.Kind != "codex_app_server" || !filepath.IsAbs(cloud.Executable) || cloud.Endpoint != "" || local.Kind != "ollama" || local.Endpoint != "http://127.0.0.1:11434" {
		return false
	}
	models := map[string]config.Model{}
	for _, m := range cfg.Models {
		models[m.ID] = m
	}
	c, l := models["coordinator"], models["local-worker"]
	return len(models) == 2 && c.Model == "gpt-5.6-sol" && c.Provider == cloud.ID && c.Locality == "cloud" && c.ContextTokens == 16384 && c.EstimatedCost != nil && *c.EstimatedCost == .1 && l.Model == "gemma4:12b-it-q4_K_M" && l.Provider == local.ID && l.Locality == "local" && l.ContextTokens == 4096 && l.EstimatedCost != nil && *l.EstimatedCost == 0
}

func TestCodexChatSteeringAuthority(t *testing.T) {
	for _, mutate := range []func(*config.Settings){nil, func(c *config.Settings) { c.Models[0].Model = "other" }, func(c *config.Settings) { c.Providers[1].Endpoint = "https://remote.invalid" }, func(c *config.Settings) { c.Providers[0].APIKeyEnv = "TOKEN" }, func(c *config.Settings) { c.Evaluation.Judge = true }, func(c *config.Settings) { c.Workers.DelegateReadTools = true }, func(c *config.Settings) { c.Runtime.MaxTurns = 8 }, func(c *config.Settings) { c.Tools.CreateEnabled = true }} {
		cfg, err := config.Load(config.Options{ProjectFile: "../../examples/sol-codex-local-smoke.yaml"})
		if err != nil {
			t.Fatal("sample unavailable")
		}
		if mutate != nil {
			mutate(&cfg)
		}
		if codexChatSteeringAuthority(cfg) != (mutate == nil) {
			t.Fatal("smoke authority mismatch")
		}
	}
}

type codexChatSteeringRender struct {
	sent, marker                                 bool
	toolStarts, toolCompletions, taskCompletions int
	queued, applied                              int
	err                                          error
}

func readCodexChatSteering(input io.Reader, send func() error) codexChatSteeringRender {
	var result codexChatSteeringRender
	reader := bufio.NewReader(io.LimitReader(input, (1<<20)+1))
	total := 0
	for {
		line, err := reader.ReadString('\n')
		total += len(line)
		if total > 1<<20 {
			result.err = errors.New("render limit")
			return result
		}
		switch strings.TrimSuffix(line, "\n") {
		case "[tool delegate started]":
			result.toolStarts++
			if !result.sent {
				result.sent = true
				if send() != nil {
					result.err = errors.New("steering input failed")
					return result
				}
			}
		case "[tool delegate completed]":
			result.toolCompletions++
		case "[task completed]":
			result.taskCompletions++
		case "[guidance applied]":
			result.applied++
		}
		if strings.HasPrefix(line, "Guidance queued: ") {
			result.queued++
		}
		result.marker = result.marker || strings.Contains(line, codexChatSteeringMarker)
		if err != nil {
			if err != io.EOF {
				result.err = errors.New("render failed")
			}
			return result
		}
	}
}

func qualifyCodexChatSteeringDatabase(t *testing.T, path string) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal("owned journal unavailable")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT body FROM events ORDER BY rowid`)
	if err != nil {
		t.Fatal("owned events unavailable")
	}
	var root string
	started, completed := map[string]bool{}, map[string]bool{}
	var steering, toolStart, toolEnd, workers int
	var lastToolEnd, steeringOrder int
	index := 0
	histories := map[string][]runtime.Event{}
	for rows.Next() {
		index++
		var body []byte
		var event runtime.Event
		if rows.Scan(&body) != nil || json.Unmarshal(body, &event) != nil || event.Validate() != nil {
			_ = rows.Close()
			t.Fatal("invalid durable event")
		}
		histories[event.TaskID] = append(histories[event.TaskID], event)
		switch event.Kind {
		case runtime.TaskStarted:
			started[event.TaskID] = true
			if event.Data.ParentTaskID == "" {
				if root != "" {
					_ = rows.Close()
					t.Fatal("multiple root tasks")
				}
				root = event.TaskID
			}
		case runtime.TaskCompleted:
			completed[event.TaskID] = true
		case runtime.ToolStarted:
			if event.Data.ToolName == "delegate" {
				toolStart++
			}
		case runtime.ToolCompleted:
			if event.Data.ToolName == "delegate" {
				toolEnd++
				lastToolEnd = index
			}
		case runtime.SteeringApplied:
			steering++
			steeringOrder = index
			if event.TaskID != root {
				_ = rows.Close()
				t.Fatal("guidance applied to wrong task")
			}
		case runtime.WorkerCompleted:
			workers++
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil || len(started) != 3 || len(completed) != 3 || root == "" || !completed[root] || steering != 1 || toolStart != 1 || toolEnd != 1 || workers != 1 || steeringOrder <= lastToolEnd {
		t.Fatal("durable delegation/steering qualification failed")
	}
	for id := range started {
		if !completed[id] {
			t.Fatal("unfinished worker or execution")
		}
	}
	qualifyCodexChatSteeringLinks(t, ctx, db, path, root, histories)
}

func TestCodexChatSteeringRenderBoundary(t *testing.T) {
	calls := 0
	r := readCodexChatSteering(strings.NewReader("prefix [tool delegate started]\n[tool delegate started]\n[tool delegate started]\nGuidance queued: test\n[tool delegate completed]\n[guidance applied]\n"+codexChatSteeringMarker+"\n[task completed]\n"), func() error { calls++; return nil })
	if r.err != nil || calls != 1 || r.toolStarts != 2 || r.toolCompletions != 1 || r.taskCompletions != 1 || r.queued != 1 || r.applied != 1 || !r.marker {
		t.Fatal("render parser boundary")
	}
	if r := readCodexChatSteering(strings.NewReader(strings.Repeat("x", (1<<20)+1)), func() error { return nil }); r.err == nil {
		t.Fatal("unbounded output accepted")
	}
}

func qualifyCodexChatSteeringLinks(t *testing.T, ctx context.Context, db *sql.DB, path, root string, histories map[string][]runtime.Event) {
	t.Helper()
	var count int
	if db.QueryRowContext(ctx, `SELECT count(*) FROM task_heads WHERE state='completed'`).Scan(&count) != nil || count != 3 {
		t.Fatal("owned task count")
	}
	if db.QueryRowContext(ctx, `SELECT count(*) FROM task_heads`).Scan(&count) != nil || count != 3 {
		t.Fatal("extra owned tasks")
	}
	var start, end, applied runtime.Event
	parentModel, nextTurn := false, false
	var parentAnswer string
	for _, e := range histories[root] {
		if e.Kind == runtime.TurnCompleted && applied.ID != "" {
			parentAnswer = e.Data.Text
		}
		if e.Kind == runtime.TurnStarted {
			if e.Data.ModelID != "gpt-5.6-sol" || e.Data.ProviderID != "codex-coordinator" {
				t.Fatal("wrong coordinator identity")
			}
			parentModel = true
			if applied.ID != "" && e.Sequence > applied.Sequence {
				nextTurn = true
			}
		}
		if e.Kind == runtime.ToolStarted {
			start = e
		}
		if e.Kind == runtime.ToolCompleted {
			end = e
		}
		if e.Kind == runtime.SteeringApplied {
			applied = e
		}
	}
	if !parentModel || !nextTurn || strings.TrimSpace(parentAnswer) != codexChatSteeringMarker || start.ID == "" || end.Data.ToolCallID != start.Data.ToolCallID || end.TurnID != start.TurnID || end.AttemptID != start.AttemptID || start.Data.ToolBehavior != runtime.BehaviorReadOnly || end.Data.Effect != runtime.NoEffect || end.Data.Code != "" || end.Sequence >= applied.Sequence {
		t.Fatal("parent correspondence")
	}
	var result struct {
		Work      string `json:"work_task_id"`
		Execution string `json:"execution_task_id"`
		Output    string `json:"untrusted_output"`
	}
	if json.Unmarshal([]byte(end.Data.Text), &result) != nil || result.Work == "" || result.Execution == "" || result.Work == result.Execution || !evaluation.GoSourceValid(result.Output) {
		t.Fatal("invalid delegation result")
	}
	work, execution := histories[result.Work], histories[result.Execution]
	if len(work) < 3 || len(execution) < 3 || work[0].Kind != runtime.TaskStarted || execution[0].Kind != runtime.TaskStarted || work[0].Data.ParentTaskID != root || work[0].SessionID != histories[root][0].SessionID || execution[0].Data.ParentTaskID != result.Work || execution[0].Data.Validation != "go_source" {
		t.Fatal("child graph mismatch")
	}
	origin := work[0].Data.DelegationOrigin
	if origin == nil || origin.TurnID != start.TurnID || origin.AttemptID != start.AttemptID || origin.ToolCallID != start.Data.ToolCallID || origin.ToolName != "delegate" || origin.BatchIndex != nil {
		t.Fatal("origin mismatch")
	}
	accepted, workerDone := false, false
	for _, e := range work {
		if e.Kind == runtime.EvaluationRecorded && e.Data.Code == "worker_validator" && e.Data.Accepted != nil && *e.Data.Accepted {
			accepted = true
		}
		if e.Kind == runtime.WorkerCompleted {
			if !accepted || e.Data.Text != result.Output {
				t.Fatal("worker output mismatch")
			}
			workerDone = true
		}
	}
	local, syntax, nonempty := false, false, false
	var executionText string
	var executionTurn, executionFinal runtime.Event
	for _, e := range execution {
		if e.Kind == runtime.TurnCompleted {
			executionText = e.Data.Text
			executionFinal = e
		}
		if e.Kind == runtime.TurnStarted {
			if e.Data.ModelID != "gemma4:12b-it-q4_K_M" || e.Data.ProviderID != "ollama-worker" {
				t.Fatal("wrong local model")
			}
			local = true
			executionTurn = e
			syntax, nonempty = false, false
		}
		if e.Kind == runtime.EvaluationRecorded && e.Data.Code == "deterministic.go_syntax.v1" && e.Data.Accepted != nil && *e.Data.Accepted {
			if e.TurnID != executionTurn.TurnID || e.AttemptID != executionTurn.AttemptID || e.Data.ModelID != executionTurn.Data.ModelID || e.Data.ProviderID != executionTurn.Data.ProviderID || e.Sequence <= executionFinal.Sequence {
				t.Fatal("syntax evidence binding")
			}
			syntax = true
		}
		if e.Kind == runtime.EvaluationRecorded && e.Data.Code == "deterministic.nonempty_text.v1" && e.Data.Accepted != nil && *e.Data.Accepted {
			if e.TurnID != executionTurn.TurnID || e.AttemptID != executionTurn.AttemptID || e.Data.ModelID != executionTurn.Data.ModelID || e.Data.ProviderID != executionTurn.Data.ProviderID || e.Sequence <= executionFinal.Sequence {
				t.Fatal("nonempty evidence binding")
			}
			nonempty = true
		}
	}
	last := execution[len(execution)-1]
	if !accepted || !workerDone || !local || !syntax || !nonempty || work[len(work)-1].Kind != runtime.TaskCompleted || last.Kind != runtime.TaskCompleted || executionText != result.Output || executionFinal.TurnID != executionTurn.TurnID || executionFinal.AttemptID != executionTurn.AttemptID || len(executionFinal.Data.ToolCalls) != 0 {
		t.Fatal("incomplete accepted child output")
	}
	store, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal("steering store unavailable")
	}
	defer store.Close()
	guidance, err := store.ListSteering(ctx, root)
	if err != nil || len(guidance) != 1 || guidance[0].ID != applied.Data.SteeringID || guidance[0].CreatedAt.Before(start.Time) || guidance[0].CreatedAt.After(end.Time) || guidance[0].AppliedSequence == nil || *guidance[0].AppliedSequence != applied.Sequence {
		t.Fatal("guidance not durably queued before worker result")
	}
}
