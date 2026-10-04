// Package remotecli provides the opt-in remote host and client shared by CLI entry points.
package remotecli

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

const Usage = "Usage: nexus remote collect-logs|logs-status|logs-read|service-template|discover|auto-dispatch-review-job|enqueue-review|enqueue-auto-review|run-review-jobs|review-job-status|dispatch-evaluate|auto-dispatch-evaluate|watch-evaluate|auto-watch-evaluate|peers|pair|revoke|evaluate|auto-evaluate|audit|audit-archive|audit-prune|serve|info|catalogue|candidates|rank|auto-dispatch|auto-status|auto-cancel|auto-output|auto-reconcile|auto-review|auto-review-state|automatic-choice|harness-identity|harness-capacity|harness-readiness|recorded-status|route-binding|reconcile|review|tasks|dispatch|status|cancel|events|validate-trust|replace-trust [flags]"

// Run executes explicit remote operations using only the supplied configuration.
func Run(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(output, Usage)
		return err
	}
	operation := args[0]
	if operation == "collect-logs" || operation == "logs-status" || operation == "logs-read" {
		return loggingOperation(ctx, operation, args[1:], output, errorOutput)
	}
	if operation == "service-template" {
		return serviceTemplate(args[1:], output, errorOutput)
	}
	if operation == "discover" {
		return discoverUnpaired(ctx, args[1:], output, errorOutput)
	}
	flags := flag.NewFlagSet("nexus remote", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	expected := flags.String("expected", "", "current registry digest (absent for pairing), or archive SHA-256 for audit-prune")
	trust := flags.String("trust", "", "owner-private paired-peer JSON registry")
	cert := flags.String("cert", "", "local PEM certificate")
	key := flags.String("key", "", "owner-private PEM key")
	ca := flags.String("ca", "", "trusted CA PEM")
	instance := flags.String("instance", "", "local instance ID (serve) or destination ID (client)")
	advertiseInterface := flags.String("advertise-interface", "", "opt-in private IPv4 DNS-SD interface (serve only)")
	advertiseName := flags.String("advertise-name", "", "explicit TLS certificate DNS name (serve only)")
	advertiseSSH := flags.Int("advertise-ssh-port", 0, "optional SSH port hint (serve only)")
	listen := flags.String("listen", "127.0.0.1:8443", "explicit IP:port listener")
	journal := flags.String("journal", "", "absolute private remote journal directory")
	configFile := flags.String("config", "", "dedicated runtime configuration file (serve)")
	request := flags.String("request", "", "persisted caller request ID, 16–64 letters/digits/_/-")
	task := flags.String("task", "", "owned task ID (events)")
	afterRequest := flags.String("after-request", "", "last caller request ID from previous tasks page")
	routes := flags.String("routes", "", "private caller route-binding directory (dispatch/recorded-status/route-binding/reconcile/review)")
	evidence := flags.String("evidence", "", "private destination-separated outcome evidence root (reconcile/review)")
	reviewQueue := flags.String("review-queue", "", "private persistent original-requirements store for review jobs")
	reviewDeadline := flags.String("review-deadline", "", "immutable absolute RFC3339 review deadline, at most 24h ahead")
	reviewFile := flags.String("review", "", "absolute owner-private saved outcome review JSON (review)")
	reviewerID := flags.String("reviewer", "", "configured evaluator model ID (evaluate/auto-evaluate)")
	reviewWait := flags.Duration("review-wait", 0, "explicit total wait/review deadline for watch-evaluate (maximum 24h)")
	reviewMaxCost := flags.Float64("review-max-cost", -1, "explicit evaluator cost ceiling")
	modelID := flags.String("model", "", "configured model ID (harness-identity)")
	harnessID := flags.String("harness", "", "configured harness registration (harness-identity)")
	contextTokens := flags.Int("context", 0, "requested context tokens (harness-identity)")
	after := flags.Int64("after", 0, "committed event or audit cursor")
	archiveFile := flags.String("archive", "", "absolute private audit archive path")
	through := flags.Int64("through", 0, "audit high-water sequence returned by first page")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *advertiseInterface != "" || *advertiseName != "" || *advertiseSSH != 0 {
		if operation != "serve" || *advertiseInterface == "" || *advertiseName == "" || *advertiseSSH < 0 || *advertiseSSH > 65535 {
			return remote.ErrInvalid
		}
	}
	if operation == "audit-archive" || operation == "audit-prune" {
		if flags.NArg() != 0 || *after != 0 {
			return remote.ErrInvalid
		}
		var result any
		var err error
		if operation == "audit-archive" {
			if *expected != "" {
				return remote.ErrInvalid
			}
			result, err = remote.ArchiveAudit(ctx, *journal, *instance, *archiveFile, *through)
		} else {
			if *through != 0 {
				return remote.ErrInvalid
			}
			result, err = remote.PruneAudit(ctx, *journal, *instance, *archiveFile, *expected)
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(result)
	}
	if operation == "audit" {
		if flags.NArg() != 0 {
			return remote.ErrInvalid
		}
		page, err := remote.ReadAuditPage(ctx, *journal, *instance, *after, *through)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(page)
	}
	if (operation == "route-binding" || operation == "automatic-choice") && flags.NArg() == 0 {
		store, e := remote.OpenRouteStore(*routes)
		if e != nil {
			return e
		}
		if operation == "automatic-choice" {
			choice, e := store.AutomaticChoice(*request)
			if e != nil {
				return e
			}
			return json.NewEncoder(output).Encode(choice)
		}
		binding, e := store.Lookup(*request)
		if e != nil {
			return e
		}
		return json.NewEncoder(output).Encode(binding)
	}
	if operation == "review-job-status" {
		if flags.NArg() != 0 {
			return remote.ErrInvalid
		}
		q, e := remote.OpenExistingReviewQueue(*reviewQueue)
		if e != nil {
			return e
		}
		state, e := q.Status(*request)
		if e != nil {
			return e
		}
		return json.NewEncoder(output).Encode(state)
	}
	if flags.NArg() != 0 || *trust == "" {
		return remote.ErrInvalid
	}
	registry := remote.TrustFile(*trust)
	if operation == "peers" || operation == "pair" || operation == "revoke" {
		return membershipOperation(registry, operation, *instance, *expected, input, output)
	}
	if operation == "replace-trust" {
		var next remote.Registry
		if e := readTrustInput(input, &next); e != nil {
			return e
		}
		if e := registry.Replace(next, *expected); e != nil {
			return e
		}
		_, e := fmt.Fprintln(output, next.Digest())
		return e
	}
	current, err := registry.Read()
	if err != nil {
		return err
	}
	if operation == "validate-trust" {
		_, err := fmt.Fprintln(output, current.Digest())
		return err
	}
	credentials := remote.Credentials{CertificateFile: *cert, KeyFile: *key, CAFile: *ca}
	if operation == "serve" {
		return serve(ctx, *instance, *listen, *journal, *configFile, registry, credentials, *advertiseInterface, *advertiseName, *advertiseSSH)
	}
	client := remote.Client{UsageFile: string(registry) + ".usage.db", Trust: registry, Credentials: credentials}
	if operation == "enqueue-review" || operation == "enqueue-auto-review" || operation == "run-review-jobs" || operation == "auto-dispatch-review-job" {
		if *instance != "" || *modelID != "" || *harnessID != "" || *contextTokens != 0 {
			return remote.ErrInvalid
		}
		var result any
		var e error
		if operation == "run-review-jobs" {
			if *reviewDeadline != "" || *request != "" {
				return remote.ErrInvalid
			}
			result, e = runReviewQueueOperation(ctx, &client, *reviewQueue, *routes, *evidence, *configFile, *reviewerID, *reviewMaxCost, *reviewWait)
		} else {
			if *reviewWait != 0 {
				return remote.ErrInvalid
			}
			deadline, de := time.Parse(time.RFC3339Nano, *reviewDeadline)
			if de != nil {
				return remote.ErrInvalid
			}
			if operation == "auto-dispatch-review-job" {
				result, e = dispatchQueuedReviewOperation(ctx, &client, *reviewQueue, *routes, *evidence, *request, *configFile, *reviewerID, *reviewMaxCost, deadline, input)
			} else {
				result, e = enqueueReviewOperation(ctx, &client, operation == "enqueue-auto-review", *reviewQueue, *routes, *evidence, *request, *configFile, *reviewerID, *reviewMaxCost, deadline, input)
			}
		}
		return writeReviewQueueResult(output, operation, result, e)
	}
	if operation == "dispatch-evaluate" || operation == "auto-dispatch-evaluate" {
		if *modelID != "" || *harnessID != "" || *contextTokens != 0 {
			return remote.ErrInvalid
		}
		result, e := dispatchReviewOperation(ctx, &client, operation == "auto-dispatch-evaluate", *instance, *routes, *evidence, *request, *configFile, *reviewerID, *reviewMaxCost, *reviewWait, input)
		if err := json.NewEncoder(output).Encode(result); err != nil {
			return err
		}
		return e
	}
	if operation == "evaluate" || operation == "auto-evaluate" || operation == "watch-evaluate" || operation == "auto-watch-evaluate" {
		watching := operation == "watch-evaluate" || operation == "auto-watch-evaluate"
		if watching && *reviewWait <= 0 || !watching && *reviewWait != 0 {
			return remote.ErrInvalid
		}
		if operation == "watch-evaluate" {
			operation = "evaluate"
		}
		if operation == "auto-watch-evaluate" {
			operation = "auto-evaluate"
		}
		if *instance != "" || *modelID != "" || *harnessID != "" || *contextTokens != 0 {
			return remote.ErrInvalid
		}
		result, e := evaluateOperationWithWait(ctx, &client, operation, *routes, *evidence, *request, *configFile, *reviewerID, *reviewMaxCost, input, *reviewWait)
		if result.Version != 0 {
			if err := json.NewEncoder(output).Encode(result); err != nil {
				return err
			}
		}
		return e
	}
	var result any
	switch operation {
	case "auto-status", "auto-cancel", "auto-output", "auto-reconcile", "auto-review", "auto-review-state":
		if *instance != "" || *modelID != "" || *harnessID != "" || *contextTokens != 0 {
			return remote.ErrInvalid
		}
		result, err = automaticResultOperation(ctx, &client, operation, *routes, *evidence, *request, *reviewFile, input)

	case "candidates", "rank", "auto-dispatch":
		if *instance != "" || *modelID != "" || *harnessID != "" || *contextTokens != 0 {
			return remote.ErrInvalid
		}
		result, err = automaticOperation(ctx, &client, operation, *routes, *evidence, *request, input)
	case "catalogue":
		result, err = client.Catalogue(ctx, *instance)
	case "harness-readiness":
		result, err = client.HarnessReadiness(ctx, *instance, remote.HarnessIdentityRequest{ModelID: *modelID, HarnessID: *harnessID, ContextTokens: *contextTokens})
	case "harness-capacity":
		result, err = client.HarnessCapacity(ctx, *instance, remote.HarnessIdentityRequest{ModelID: *modelID, HarnessID: *harnessID, ContextTokens: *contextTokens})
	case "harness-identity":
		result, err = client.HarnessIdentity(ctx, *instance, remote.HarnessIdentityRequest{ModelID: *modelID, HarnessID: *harnessID, ContextTokens: *contextTokens})
	case "info":
		result, err = client.Info(ctx, *instance)
	case "tasks":
		result, err = client.Tasks(ctx, *instance, *afterRequest)
	case "recorded-status":
		if *instance != "" || *modelID != "" || *harnessID != "" || *contextTokens != 0 {
			return remote.ErrInvalid
		}
		store, e := remote.OpenExistingRouteStore(*routes)
		if e != nil {
			return e
		}
		result, err = client.InspectRecorded(ctx, store, *request)
	case "status":
		result, err = client.Status(ctx, *instance, *request)
	case "cancel":
		result, err = client.Cancel(ctx, *instance, *request)
	case "events":
		result, err = client.Events(ctx, *instance, *request, *task, *after)
	case "dispatch", "reconcile", "review":
		data, e := io.ReadAll(io.LimitReader(input, remote.MaxBody+1))
		if e != nil || len(data) > remote.MaxBody {
			return remote.ErrInvalid
		}
		var t remote.Task
		if json.Unmarshal(data, &t) != nil || t.Validate() != nil {
			return remote.ErrInvalid
		}
		if operation == "reconcile" || operation == "review" {
			store, e := remote.OpenRouteStore(*routes)
			if e != nil {
				return e
			}
			binding, e := store.Lookup(*request)
			if e != nil {
				return e
			}
			if *instance != "" && *instance != binding.Destination {
				return remote.ErrConflict
			}
			if operation == "review" {
				evaluation, e := readOutcomeReview(*reviewFile)
				if e != nil {
					return e
				}
				if e = client.ReviewRecordedOutcome(ctx, store, *evidence, *request, t, evaluation); e != nil {
					return e
				}
				result = evaluation
				break
			}
			verified, e := client.RecordedOutcome(ctx, store, *request, t)
			if e != nil {
				return e
			}
			if e = verified.Record(ctx, *evidence, time.Now().UTC()); e != nil {
				return e
			}
			receipt := verified.Receipt()
			digest, e := receipt.Digest()
			if e != nil {
				return e
			}
			executionDigest, e := receipt.Execution.Digest()
			if e != nil {
				return e
			}
			result = struct {
				ExecutionSHA256 string                `json:"execution_sha256"`
				ReceiptSHA256   string                `json:"receipt_sha256"`
				Receipt         remote.OutcomeReceipt `json:"receipt"`
			}{executionDigest, digest, receipt}
			break
		}
		if *routes != "" {
			store, e := remote.OpenRouteStore(*routes)
			if e != nil {
				return e
			}
			result, err = client.DispatchRecorded(ctx, store, *instance, *request, t)
		} else {
			result, err = client.Dispatch(ctx, *instance, *request, t)
		}
	default:
		return remote.ErrInvalid
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
func serve(ctx context.Context, instance, address, journalDir, configFile string, trust remote.TrustFile, credentials remote.Credentials, advertiseInterface, advertiseName string, advertiseSSH int) error {
	host, port, err := net.SplitHostPort(address)
	number, e := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || e != nil || number < 1 || number > 65535 || configFile == "" {
		return remote.ErrInvalid
	}
	// Both clients read this explicit file; a changed configuration is fenced by
	// the existing durable queue digest, never silently dispatched by another host.
	cfg, err := config.Load(config.Options{ProjectFile: configFile})
	if err != nil {
		return err
	}
	if advertiseInterface == "" && advertiseName == "" && advertiseSSH == 0 && cfg.RemoteAdvertisement.Enabled {
		advertiseInterface, advertiseName, advertiseSSH = cfg.RemoteAdvertisement.Interface, cfg.RemoteAdvertisement.Name, cfg.RemoteAdvertisement.SSHPort
	}
	var ledger *harness.EvidenceStore
	if len(cfg.NativeHarnesses) > 0 && cfg.NativeHarnessEvidenceDir == "" {
		return remote.ErrInvalid
	}
	if cfg.NativeHarnessEvidenceDir != "" {
		ledger, err = harness.OpenEvidenceStore(cfg.NativeHarnessEvidenceDir)
		if err != nil {
			return err
		}
		defer ledger.Close()
	}
	client, err := sdk.New(sdk.ConfigOptions{ProjectFile: configFile, LookupSecret: os.Getenv, HarnessEvidence: ledger})
	if err != nil {
		return err
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		return err
	}
	// Remote execution must contend with other local daemons, not just the
	// requests handled by this server. Install before starting any dispatcher.
	closeResources, err := app.InstallHostResourceCoordinator(ctx, service, instance)
	if err != nil {
		return err
	}
	defer closeResources()
	service.ConfigureHarnessEvidence(ledger)
	journal, err := remote.OpenJournal(journalDir, instance)
	if err != nil {
		return err
	}
	defer journal.Close()
	backend := &remote.SDKBackend{JobHistory: service.ChatHistory, RunnerModelID: cfg.VLLM.ModelID, ControlRunner: vllmController(cfg, service), Routing: service.RemoteRoutingInspection, ReadStatus: service.SubmissionStatus, Client: client, LogEvents: service.RemoteCommittedLogs, Usage: service.RemoteTaskUsage, Identify: service.NativeHarnessIdentity, PlanHarness: service.NativeHarnessCapacity, CheckHarness: service.NativeHarnessReadiness, Observe: modelObserver(cfg, os.Getenv, resources.Profile)}
	for _, m := range cfg.Models {
		backend.Models = append(backend.Models, remote.Model{EstimatedCost: m.EstimatedCost, ID: m.ID, Provider: m.Provider, Model: m.Model, Harness: "nexus-native", Capabilities: m.Capabilities, ContextTokens: m.ContextTokens, Local: m.Locality == "local"})
	}
	for _, h := range cfg.NativeHarnesses {
		backend.Harnesses = append(backend.Harnesses, remote.Harness{ID: h.ID, ModelID: h.ModelID, Kind: h.Kind, ModelRevision: h.ModelRevision, NativeTools: h.NativeTools})
	}
	server, err := remote.NewServer(instance, trust, journal, backend)
	if err != nil {
		return err
	}
	httpServer, err := server.HTTPServer(address, credentials)
	if err != nil {
		return err
	}
	// Bind before starting a dispatcher so invalid TLS/listener settings cannot
	// accidentally start queued inference.
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	var advertiser *remote.DiscoveryAdvertiser
	if advertiseInterface != "" {
		leaf, e := x509.ParseCertificate(httpServer.TLSConfig.Certificates[0].Certificate[0])
		if e != nil {
			return remote.ErrInvalid
		}
		advertiser, err = remote.OpenDiscoveryAdvertiser(advertiseInterface, remote.DiscoveryAdvertisement{Instance: instance, Address: address, ServerName: advertiseName, SSHPort: advertiseSSH, Certificate: leaf})
		if err != nil {
			return err
		}
		defer advertiser.Close()
	}
	dispatcher, err := app.StartDispatcher(ctx, service)
	if err != nil {
		return err
	}
	defer dispatcher.Close()
	backend.Available = func(context.Context) bool { return dispatcher.Health().Status == "healthy" }
	done := make(chan error, 1)
	go func() { done <- httpServer.ServeTLS(listener, "", "") }()
	var advertiseDone chan error
	if advertiser != nil {
		advertiseCtx, cancel := context.WithCancel(ctx)
		advertiseDone = make(chan error, 1)
		joined := make(chan struct{})
		go func() { defer close(joined); advertiseDone <- advertiser.Run(advertiseCtx) }()
		defer func() { cancel(); advertiser.Close(); <-joined }()
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-advertiseDone:
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := httpServer.Shutdown(shutdown); e != nil {
			_ = httpServer.Close()
		}
		<-done
		if ctx.Err() != nil {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
		}
		<-done
		return nil
	}
}
