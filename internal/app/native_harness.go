package app

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/responsecontract"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

var ErrHarnessUnsupported = errors.New("native harness request or configured capability is unsupported")

// NativeHarnessPrices is shared across registered native adapters.
// The alias preserves compatibility with earlier Pi registrations.
type NativeHarnessPrices = pi.Prices

// NativeHarness registers one operator-pinned model/harness pair. ModelRevision
// and OverheadRAMBytes must be verified host metadata, not model self-reports.
// Registration grants no task authority; ordinary admission still applies.
type NativeHarness struct {
	// HermesSourceDir binds Hermes source; RuntimeSHA256 attests Hermes or OpenHands dependencies.
	HermesSourceDir, RuntimeSHA256                                 string
	ID, ModelID, Kind, Executable, ExecutableSHA256, ModelRevision string
	MaxOutputTokens                                                int
	OverheadRAMBytes                                               uint64
	Prices                                                         *NativeHarnessPrices
}

// ConfigureNativeHarnesses is constructor-only; call before exposing Service.
func (s *Service) ConfigureNativeHarnesses(registrations []NativeHarness, ledger *harness.EvidenceStore) error {
	if s == nil || len(registrations) > 256 {
		return ErrAdmission
	}
	entries := make(map[string]NativeHarness, len(registrations))
	digest, err := settingsConfigID(s.settings)
	if err != nil {
		return err
	}
	for _, entry := range registrations {
		if entry.Prices == nil {
			return ErrAdmission
		}
		prices := *entry.Prices
		entry.Prices = &prices
		if !reservationLabel(entry.ID, 128) || entry.ID == "auto" || (entry.Kind != "pi" && entry.Kind != "openclaw" && entry.Kind != "hermes" && entry.Kind != "goose" && entry.Kind != "openhands") || entry.OverheadRAMBytes == 0 {
			return ErrAdmission
		}
		if _, exists := entries[entry.ID]; exists {
			return ErrAdmission
		}
		var model config.Model
		var provider config.Provider
		for _, m := range s.settings.Models {
			if m.ID == entry.ModelID {
				model = m
			}
		}
		for _, p := range s.settings.Providers {
			if p.ID == model.Provider {
				provider = p
			}
		}
		if model.ID == "" || (provider.Kind != "openai_compatible" && provider.Kind != "ollama") {
			return ErrHarnessUnsupported
		}
		c, e := nativeConfig(entry, provider, model, model.WorkingContextTokens(), digest, "", deniedNativeTransport{}, nil)
		if e != nil {
			return e
		}
		if _, e := c.Identity(); e != nil {
			return ErrAdmission
		}
		entries[entry.ID] = entry
	}
	s.nativeHarnesses = entries
	s.harnessEvidence = ledger
	return nil
}

type deniedNativeTransport struct{}

func (deniedNativeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, ErrAdmission
}

func (s *Service) bindNativeHarness(r Request) (Request, error) {
	if r.HarnessID == "" {
		return r, nil
	}
	if r.submissionID != "" || r.runtimeHostAdmission != nil || r.delegatedParent != "" || r.ContinueTaskID != "" || r.Compaction != nil || r.SummaryAttemptID != "" || r.Validation != "" || s.settings.Tools.Enabled || s.settings.Tools.WorkboardReadEnabled || s.settings.Tools.WorkboardWriteEnabled || len(s.toolExtension.Names()) > 0 || s.settings.Workers.DelegateModel != "" {
		return Request{}, ErrHarnessUnsupported
	}
	if r.HarnessID == "auto" {
		if s.harnessEvidence == nil || (r.ModelID != "" && r.ModelID != "auto") || r.ContextTokens < 8192 {
			return Request{}, ErrHarnessUnsupported
		}
		return r, nil
	}
	entry, ok := s.nativeHarnesses[r.HarnessID]
	if !ok || r.ModelID != entry.ModelID || r.ModelID == "auto" {
		return Request{}, ErrHarnessUnsupported
	}
	r.nativeHarness = &entry
	return r, nil
}

func nativePiConfig(entry NativeHarness, p config.Provider, m config.Model, contextTokens int, policyDigest, key string, tr http.RoundTripper, messages []providers.Message) pi.Config {
	endpoint := strings.TrimRight(p.ResolvedEndpoint(), "/")

	return pi.Config{UpstreamProtocol: p.Kind, Messages: messages, Executable: entry.Executable, ExecutableSHA256: entry.ExecutableSHA256, ModelRevision: entry.ModelRevision, Provider: p.ID, Model: m.Model, BaseURL: endpoint, APIKey: key, ContextTokens: contextTokens, MaxOutputTokens: entry.MaxOutputTokens, Prices: entry.Prices, Timeout: httpProviderTimeout(p), Transport: tr, TransportPolicySHA256: policyDigest,
		// The caller already owns the ordinary model + native-process reservation.
		Admit: func(context.Context) (func(), error) { return func() {}, nil }}
}

func nativeReservationModel(model config.Model, entry *NativeHarness, tokens int) (config.Model, error) {
	sized, err := contextReservationModel(model, tokens)
	if err != nil {
		return config.Model{}, err
	}
	if model.Locality != "local" {
		sized.RAMBytes = 0
		sized.VRAMBytes = 0
		sized.GPUDevice = ""
	}
	if sized.RAMBytes > math.MaxUint64-entry.OverheadRAMBytes {
		return config.Model{}, ErrAdmission
	}
	sized.RAMBytes += entry.OverheadRAMBytes
	sized.Locality = "local"
	// Size model context before adding fixed harness overhead; never scale that
	// process overhead down with a smaller token allocation.
	sized.DefaultContextTokens = tokens
	sized.ContextTokens = tokens
	return sized, nil
}

func runNativeAdmitted(ctx context.Context, s config.Settings, r Request, p config.Provider, m config.Model, key string, messages []providers.Message, j runtime.Journal, result Result, sessionID string, secrets []string) (Result, error) {
	for _, capability := range r.Capabilities {
		switch capability {
		case "completion", "chat", "text", "writing", "translation", "creative", "coding", "code":
		default:
			return result, ErrHarnessUnsupported
		}
	}
	if r.nativeHarness == nil || r.nativeHarness.ModelID != m.ID {
		return result, ErrHarnessUnsupported
	}
	digest, err := settingsConfigID(s)
	if err != nil {
		return result, ErrAdmission
	}
	tr, err := policy.NewTransportWithHeaderTimeout(s.Mode == "local_only" || m.Locality == "local", []string{p.ResolvedEndpoint()}, httpProviderTimeout(p))
	if err != nil {
		return result, ErrAdmission
	}
	defer tr.CloseIdleConnections()
	tokens := r.ContextTokens
	if tokens == 0 {
		tokens = m.WorkingContextTokens()
	}
	c, err := nativeConfig(*r.nativeHarness, p, m, tokens, digest, key, tr, messages)
	if err != nil {
		return result, err
	}
	identity, err := c.Identity()
	if err != nil {
		return result, ErrAdmission
	}
	profile := r.Profile
	if profile == "" {
		profile = "default"
	}
	task := harness.TaskClass{Domain: r.Domain, Profile: profile, Difficulty: "unknown"}
	privacy := "cloud_allowed"
	if m.Locality == "local" {
		privacy = "local_only"
	}
	var measured *providers.Usage
	outcome, text, err := runtime.RunHarness(ctx, j, runtime.HarnessRequest{TaskID: result.TaskID, SessionID: sessionID, Attribution: runtime.HarnessAttribution{Identity: identity, Task: task, Selection: r.nativeSelection}, ContextTokens: tokens, MaxOutputBytes: 1 << 20, Messages: messages, Privacy: privacy, OutputView: func(text string) string { return redact(text, secrets) }, Execute: func(run context.Context) (runtime.HarnessOutput, error) {
		estimate, e := providers.EstimateWith(run, r.contextEstimator, providers.Request{Model: m.Model, Messages: messages, ContextTokens: int64(tokens), MaxOutputTokens: int64(c.MaxOutputTokens)})
		if e != nil || estimate+c.MaxOutputTokens > tokens {
			return runtime.HarnessOutput{}, runtime.ErrContextOverflow
		}
		native, e := c.Run(run, "Execute the host-supplied task context.")
		if native.Usage != nil {
			copy := *native.Usage
			measured = &copy
		}
		instructions := responseInstructions(r)
		if e == nil && redact(instructions, secrets) == instructions && len(responsecontract.Infer(instructions).Validate(redact(native.Text, secrets))) > 0 {
			e = runtime.ErrInvalidOutput
		}
		return native, e
	}})
	result.Text = text
	result.HarnessSelection = r.nativeSelection
	result.HarnessOutcome = nil
	if err == nil {
		result.HarnessOutcome = &outcome
		result.Usage = measured
		result.Turns = 1
		result.FinishReason = "stop"
	}
	return result, err
}
