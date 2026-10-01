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
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

func TestDaemonRemoteReviewQueueFailureSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := buildWorkboardDaemonBinary(t, ctx, dir)
	var inference atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			inference.Add(1)
			w.WriteHeader(500)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
	}))
	defer provider.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	s := config.Defaults()
	s.Daemon.Listen = address
	s.Telemetry.Database = filepath.Join(dir, "tasks.db")
	s.Skills.Root = filepath.Join(dir, "skills")
	s.Skills.Scope = "project"
	s.Evaluation.Judge = true
	zero := 0.0
	s.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
	s.Models = []config.Model{{ID: "reviewer", Provider: "fixture", Model: "fixture", Locality: "local", Capabilities: []string{"general"}, ContextTokens: 32768, EstimatedCost: &zero, RAMBytes: 1024}}
	s.WebUI.Enabled = true
	s.WebUI.RemoteTaskControls = true
	s.WebUI.RemoteTrustFile = filepath.Join(dir, "peers.json")
	s.WebUI.RemoteClient = &config.WebUIRemoteClient{CertificateFile: filepath.Join(dir, "cert"), KeyFile: filepath.Join(dir, "key"), CAFile: filepath.Join(dir, "ca")}
	s.WebUI.RemoteDispatchDirectory = filepath.Join(dir, "routes")
	s.WebUI.RemoteAutomaticEvidenceDirectory = filepath.Join(dir, "evidence")
	queue := filepath.Join(dir, "queue")
	s.WebUI.RemoteReview = &config.WebUIRemoteReview{Model: "reviewer", MaxCost: &zero, QueueDirectory: queue, Wait: "1h"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(queue, 0700); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(queue, "broken.job.json")
	if err := os.WriteFile(corrupt, []byte("private-invalid-job"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configuration, data, 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("remote-review-process-fixture-", 2)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for attempt := range 3 {
		if attempt == 2 {
			if err := os.Remove(corrupt); err != nil {
				t.Fatal(err)
			}
		} // explicit repair of test-only corrupt state
		process := startOwnedDaemon(t, ctx, binary, configuration, token, filepath.Join(dir, "owners"))
		func() {
			defer killAndJoinOwnedDaemon(process)
			want := "supervisor_error"
			if attempt == 2 {
				want = "supervisor_ok"
			}
			deadline := time.Now().Add(12 * time.Second)
			found := false
			for time.Now().Before(deadline) {
				select {
				case <-process.done:
					t.Fatal("daemon exited", process.waitErr, process.output.String())
				default:
				}
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/health", nil)
				request.Header.Set("Authorization", "Bearer "+token)
				response, err := client.Do(request)
				if err == nil {
					var report health.Report
					err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&report)
					response.Body.Close()
					if err == nil && report.Validate() == nil {
						for _, check := range report.Checks {
							if check.Component == "remote_review" && check.Code == want {
								found = true
								if attempt < 2 && report.Ready {
									t.Fatal("failed worker reported ready")
								}
							}
						}
					}
				}
				if found {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if !found {
				t.Fatal("missing daemon review health", want, process.output.String())
			}
			if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case <-process.done:
			case <-time.After(5 * time.Second):
				t.Fatal("daemon did not join worker on shutdown")
			}
			if process.waitErr != nil {
				t.Fatal(process.waitErr, process.output.String())
			}
			for _, secret := range []string{token, "private-invalid-job"} {
				if strings.Contains(process.output.String(), secret) {
					t.Fatal("private diagnostic leaked")
				}
			}
		}()
	}
	if inference.Load() != 0 {
		t.Fatal("queue recovery dispatched inference", inference.Load())
	}
}
