package remote

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWatchEvaluationSSHNative(t *testing.T) {
	if os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
		t.Skip("requires isolated native sshd qualification")
	}
	ssh := nativeSSHServer(t)
	f, routes, key, task, v, _, b := reviewFixture(t)
	f.serverPeer.Transport = "ssh"
	f.serverPeer.SSH = &ssh
	writeRegistry(t, f.clientTrust, f.serverPeer)
	watched := &watchBackend{outcomeBackend: b, transition: true}
	f.server.backend = watched
	evaluator := &remoteEvaluatorFixture{}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	root := filepath.Join(t.TempDir(), "evidence")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for range 2 {
		result, err := f.client.WatchRecordedEvaluation(ctx, routes, root, key, task, policy, 10*time.Second, time.Second)
		if err != nil || !result.ReviewApplied {
			t.Fatal(result, err)
		}
	}
	if rank := remoteRank(t, root, v); evaluator.calls.Load() != 1 || b.submits.Load() != 1 || rank.AdvisorySamples != 1 {
		t.Fatal(rank, evaluator.calls.Load(), b.submits.Load())
	}
	known, err := os.ReadFile(ssh.KnownHostsFile)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := os.ReadFile(ssh.IdentityFile + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	// The HTTPS listener remains reachable throughout. Any successful poll after
	// this mismatch would expose a direct transport fallback.
	if err = os.WriteFile(ssh.KnownHostsFile, []byte("[127.0.0.1]:"+strconv.Itoa(ssh.Port)+" "+string(bytes.TrimSpace(wrong))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := watched.polls.Load()
	blockedRoot := filepath.Join(t.TempDir(), "blocked")
	if _, err = f.client.WatchRecordedEvaluation(ctx, routes, blockedRoot, key, task, policy, 5*time.Second, time.Second); err == nil {
		t.Fatal("wrong SSH host key accepted")
	}
	if watched.polls.Load() != before || evaluator.calls.Load() != 1 {
		t.Fatal("SSH failure fell back or invoked reviewer")
	}
	if _, err = os.Stat(blockedRoot); !os.IsNotExist(err) {
		t.Fatal("SSH failure created evidence", err)
	}
	if err = os.WriteFile(ssh.KnownHostsFile, known, 0600); err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, f.serverTrust)
	if _, err = f.client.WatchRecordedEvaluation(ctx, routes, blockedRoot, key, task, policy, 5*time.Second, time.Second); err == nil {
		t.Fatal("SSH bypassed NexusRouter revocation")
	}
	if evaluator.calls.Load() != 1 || b.submits.Load() != 1 {
		t.Fatal("revocation invoked work")
	}
}
