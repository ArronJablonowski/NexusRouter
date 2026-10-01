package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

// Exercise the shipped entry point in separate processes so a retry cannot
// accidentally rely on an in-memory evaluator cache.
func TestRemoteEvaluationCLIReconcilesAcrossProcesses(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nexus")
	build := exec.Command("go", "build", "-o", binary, "../cmd/nexus")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	var sshTransport *SSH
	if os.Getenv("NEXUS_REMOTE_EVALUATION_SSH") == "1" {
		native := nativeSSHServer(t)
		sshTransport = &native
	}
	useTransport := func(f *fixture) {
		if sshTransport != nil {
			f.serverPeer.Transport = "ssh"
			f.serverPeer.SSH = sshTransport
			writeRegistry(t, f.clientTrust, f.serverPeer)
		}
	}
	f, routes, key, task, v, _, backend := reviewFixture(t)
	key = "combined-cli-review-01"
	useTransport(f)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		audit := evaluation.Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: evaluation.ReviewRubricVersion, Domain: task.Domain, Verdict: "accept", Confidence: .7, Findings: []evaluation.AuditFinding{{Summary: "fixture candidate meets request", EvidenceRefs: []string{"candidate", "requirements"}}}}
		body, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", string(body))
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Tools.Enabled = false
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "local.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "reviewer", Model: "judge", Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, RAMBytes: 100, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "runtime.yaml")
	if err = os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	args := []string{"remote", "evaluate", "--trust", f.clientTrust, "--cert", f.client.Credentials.CertificateFile, "--key", f.client.Credentials.KeyFile, "--ca", f.client.Credentials.CAFile, "--routes", routes.directory, "--evidence", root, "--request", key, "--config", configPath, "--reviewer", "reviewer", "--review-max-cost", "0"}
	input, _ := json.Marshal(task)
	run := func(args []string, input []byte, wantSuccess bool) RemoteEvaluationStatus {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := exec.CommandContext(ctx, binary, args...)
		command.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		cancel()
		if (err == nil) != wantSuccess {
			t.Fatalf("invoke: %v stdout=%s stderr=%s", err, output, stderr.String())
		}
		if !wantSuccess {
			return RemoteEvaluationStatus{}
		}
		var result RemoteEvaluationStatus
		if strings.Contains(args[1], "dispatch-evaluate") {
			var flow struct {
				Phase      string                  `json:"phase"`
				Evaluation *RemoteEvaluationStatus `json:"evaluation"`
			}
			if e := json.Unmarshal(output, &flow); e != nil || flow.Phase != "finished" || flow.Evaluation == nil {
				t.Fatal(string(output), e)
			}
			result = *flow.Evaluation
			output, _ = json.Marshal(result)
		}
		if err = json.Unmarshal(output, &result); err != nil || result.Status != "completed" || !result.ReviewApplied {
			t.Fatal(string(output), err)
		}
		return result
	}
	flowArgs := append(append([]string{}, args...), "--instance", "node-a", "--review-wait", "1m")
	flowArgs[1] = "dispatch-evaluate"
	badArgs := append([]string{}, flowArgs...)
	for i := range badArgs {
		if badArgs[i] == "--reviewer" {
			badArgs[i+1] = "missing"
		}
	}
	run(badArgs, input, false)
	if backend.submits.Load() != 1 || calls.Load() != 0 {
		t.Fatal("reviewer preflight dispatched work")
	}
	publicRoot := filepath.Join(t.TempDir(), "public-evidence")
	if err := os.Mkdir(publicRoot, 0755); err != nil {
		t.Fatal(err)
	}
	invalidStoreArgs := append([]string{}, flowArgs...)
	for i := range invalidStoreArgs {
		if invalidStoreArgs[i] == "--evidence" {
			invalidStoreArgs[i+1] = publicRoot
		}
	}
	run(invalidStoreArgs, input, false)
	if backend.submits.Load() != 1 || calls.Load() != 0 {
		t.Fatal("invalid evidence directory dispatched work", backend.submits.Load(), calls.Load())
	}
	for range 2 {
		run(flowArgs, input, true)
	}
	for range 2 {
		run(args, input, true)
	}
	watchArgs := append(append([]string{}, args...), "--review-wait", "1m")
	watchArgs[1] = "watch-evaluate"
	run(watchArgs, input, true)
	if rank := remoteRank(t, root, v); calls.Load() != 1 || backend.submits.Load() != 2 || rank.AdvisorySamples != 1 || rank.ConfirmedSamples != 0 {
		t.Fatal(calls.Load(), backend.submits.Load(), rank)
	}
	// Resolve the immutable automatic choice through the same main CLI, without
	// invoking discovery or changing the original destination on another process.
	// A fresh destination fixture is required: the synthetic backend deliberately
	// reuses one canonical task/execution, which must not acquire another review
	// head merely because a different request key points to it.
	f, routes, _, task, _, _, backend = reviewFixture(t)
	useTransport(f)
	f.server.backend = cliDiscoveryBackend{rankingBackend{backend, *task.ExpectedHarnessIdentity}, task}
	args = []string{"remote", "auto-evaluate", "--trust", f.clientTrust, "--cert", f.client.Credentials.CertificateFile, "--key", f.client.Credentials.KeyFile, "--ca", f.client.Credentials.CAFile, "--routes", routes.directory, "--evidence", root, "--request", key, "--config", configPath, "--reviewer", "reviewer", "--review-max-cost", "0"}
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: int64(task.ContextTokens)}}
	autoKey := "automatic-cli-eval-01"
	args[1] = "auto-evaluate"
	for i := range args {
		if args[i] == key {
			args[i] = autoKey
		}
	}
	input, _ = json.Marshal(request)
	flowArgs = append(append([]string{}, args...), "--review-wait", "1m")
	flowArgs[1] = "auto-dispatch-evaluate"
	for range 2 {
		run(flowArgs, input, true)
	}
	automatic, err := f.client.AutomaticOutcome(context.Background(), routes, autoKey, request)
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		run(args, input, true)
	}
	watchArgs = append(append([]string{}, args...), "--review-wait", "1m")
	watchArgs[1] = "auto-watch-evaluate"
	run(watchArgs, input, true)
	if rank := remoteRank(t, root, automatic); calls.Load() != 2 || backend.submits.Load() != 2 || rank.AdvisorySamples != 1 || rank.ConfirmedSamples != 0 {
		t.Fatal(calls.Load(), backend.submits.Load(), rank)
	}
	request.Prompt = "changed intent"
	input, _ = json.Marshal(request)
	run(args, input, false)
	if calls.Load() != 2 || backend.submits.Load() != 2 {
		t.Fatal("changed request invoked work", calls.Load(), backend.submits.Load())
	}
}

// Supply actual identity/readiness/capacity through the production discovery
// protocol while retaining immutable synthetic canonical execution evidence.
type cliDiscoveryBackend struct {
	rankingBackend
	task Task
}

func (b cliDiscoveryBackend) Catalogue(context.Context, []string, bool, []string) (Info, error) {
	zero := 0.0
	return Info{Version: 1, Available: true, Models: []Model{{ID: b.task.ModelID, Provider: b.identity.Provider, Model: b.identity.Model, Local: true, ContextTokens: 32768, EstimatedCost: &zero}}, Harnesses: []Harness{{ID: b.task.HarnessID, ModelID: b.task.ModelID, Kind: b.identity.Harness, ModelRevision: b.identity.ModelRevision}}}, nil
}
