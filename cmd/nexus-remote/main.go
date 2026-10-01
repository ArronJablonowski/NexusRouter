// nexus-remote is an opt-in mTLS endpoint and client. It never opens the ordinary
// local daemon API to the network or accepts identity from forwarded headers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: nexus-remote serve|info|harness-identity|harness-capacity|harness-readiness|route-binding|reconcile|review|tasks|dispatch|status|cancel|events|validate-trust|replace-trust [flags]")
	}
	operation := args[0]
	flags := flag.NewFlagSet("nexus-remote", flag.ContinueOnError)
	expected := flags.String("expected", "", "current validated registry digest, or absent for initial pairing")
	trust := flags.String("trust", "", "owner-private paired-peer JSON registry")
	cert := flags.String("cert", "", "local PEM certificate")
	key := flags.String("key", "", "owner-private PEM key")
	ca := flags.String("ca", "", "trusted CA PEM")
	instance := flags.String("instance", "", "local instance ID (serve) or destination ID (client)")
	listen := flags.String("listen", "127.0.0.1:8443", "explicit IP:port listener")
	journal := flags.String("journal", "", "absolute private remote journal directory")
	configFile := flags.String("config", "", "dedicated runtime configuration file (serve)")
	request := flags.String("request", "", "persisted caller request ID, 16–64 letters/digits/_/-")
	task := flags.String("task", "", "owned task ID (events)")
	afterRequest := flags.String("after-request", "", "last caller request ID from previous tasks page")
	routes := flags.String("routes", "", "private caller route-binding directory (dispatch/route-binding/reconcile/review)")
	evidence := flags.String("evidence", "", "private destination-separated outcome evidence root (reconcile/review)")
	reviewFile := flags.String("review", "", "absolute owner-private saved outcome review JSON (review)")
	modelID := flags.String("model", "", "configured model ID (harness-identity)")
	harnessID := flags.String("harness", "", "configured harness registration (harness-identity)")
	contextTokens := flags.Int("context", 0, "requested context tokens (harness-identity)")
	after := flags.Int64("after", 0, "committed event cursor")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if operation == "route-binding" && flags.NArg() == 0 {
		store, e := remote.OpenRouteStore(*routes)
		if e != nil {
			return e
		}
		binding, e := store.Lookup(*request)
		if e != nil {
			return e
		}
		return json.NewEncoder(output).Encode(binding)
	}
	if flags.NArg() != 0 || *trust == "" {
		return remote.ErrInvalid
	}
	registry := remote.TrustFile(*trust)
	if operation == "replace-trust" {
		data, e := io.ReadAll(io.LimitReader(input, remote.MaxBody+1))
		if e != nil || len(data) > remote.MaxBody {
			return remote.ErrInvalid
		}
		var next remote.Registry
		if json.Unmarshal(data, &next) != nil {
			return remote.ErrInvalid
		}
		if e = registry.Replace(next, *expected); e != nil {
			return e
		}
		_, e = fmt.Fprintln(output, next.Digest())
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
		return serve(ctx, *instance, *listen, *journal, *configFile, registry, credentials)
	}
	client := remote.Client{Trust: registry, Credentials: credentials}
	var result any
	switch operation {
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
func serve(ctx context.Context, instance, address, journalDir, configFile string, trust remote.TrustFile, credentials remote.Credentials) error {
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
	service.ConfigureHarnessEvidence(ledger)
	journal, err := remote.OpenJournal(journalDir, instance)
	if err != nil {
		return err
	}
	defer journal.Close()
	backend := &remote.SDKBackend{Client: client, Identify: service.NativeHarnessIdentity, PlanHarness: service.NativeHarnessCapacity, CheckHarness: service.NativeHarnessReadiness, Observe: modelObserver(cfg, os.Getenv, resources.Profile)}
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
	dispatcher, err := app.StartDispatcher(ctx, service)
	if err != nil {
		return err
	}
	defer dispatcher.Close()
	backend.Available = func(context.Context) bool { return dispatcher.Health().Status == "healthy" }
	done := make(chan error, 1)
	go func() { done <- httpServer.ServeTLS(listener, "", "") }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
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
