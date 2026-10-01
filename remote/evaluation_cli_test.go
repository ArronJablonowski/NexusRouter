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
	f, routes, key, task, v, _, backend := reviewFixture(t)
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
		if err = json.Unmarshal(output, &result); err != nil || result.Status != "completed" || !result.ReviewApplied {
			t.Fatal(string(output), err)
		}
		return result
	}
	for range 2 {
		run(args, input, true)
	}
	if rank := remoteRank(t, root, v); calls.Load() != 1 || backend.submits.Load() != 1 || rank.AdvisorySamples != 1 || rank.ConfirmedSamples != 0 {
		t.Fatal(calls.Load(), backend.submits.Load(), rank)
	}
	// Resolve the immutable automatic choice through the same main CLI, without
	// invoking discovery or changing the original destination on another process.
	// A fresh destination fixture is required: the synthetic backend deliberately
	// reuses one canonical task/execution, which must not acquire another review
	// head merely because a different request key points to it.
	f, routes, _, task, _, _, backend = reviewFixture(t)
	f.server.backend = rankingBackend{backend, *task.ExpectedHarnessIdentity}
	args = []string{"remote", "auto-evaluate", "--trust", f.clientTrust, "--cert", f.client.Credentials.CertificateFile, "--key", f.client.Credentials.KeyFile, "--ca", f.client.Credentials.CAFile, "--routes", routes.directory, "--evidence", root, "--request", key, "--config", configPath, "--reviewer", "reviewer", "--review-max-cost", "0"}
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: int64(task.ContextTokens)}}
	autoKey := "automatic-cli-eval-01"
	candidate := DestinationCandidate{Destination: "node-a", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: harness.Candidate{Identity: *task.ExpectedHarnessIdentity, Local: true, Available: true, Authorized: true, Compatible: true, CredentialAvailable: true, CapacityAvailable: true, ContextTokens: 8192}}
	if _, _, err = f.client.DispatchAutomatic(context.Background(), routes, root, autoKey, request, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0); err != nil {
		t.Fatal(err)
	}
	automatic, err := f.client.AutomaticOutcome(context.Background(), routes, autoKey, request)
	if err != nil {
		t.Fatal(err)
	}
	args[1] = "auto-evaluate"
	for i := range args {
		if args[i] == key {
			args[i] = autoKey
		}
	}
	input, _ = json.Marshal(request)
	for range 2 {
		run(args, input, true)
	}
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
