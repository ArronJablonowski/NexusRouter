package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

const prepareSummaryVersion = 1

// PrepareSummaryRequest is the versioned semantic input to one idempotent
// summary preparation. The caller key is supplied separately and is never
// persisted in plaintext.
type PrepareSummaryRequest struct {
	Version int     `json:"version"`
	TaskID  string  `json:"task_id"`
	ModelID string  `json:"model_id"`
	Keep    int     `json:"keep"`
	MaxCost float64 `json:"max_cost"`
}

type preparedSummaryAdmission struct {
	history       sessions.Snapshot
	input         sessions.Snapshot
	model         config.Model
	provider      config.Provider
	selection     sessions.CompactionRequest
	engine        runtime.ContextEngineIdentity
	tiers         runtime.ContextTierPlan
	config        json.RawMessage
	policy        json.RawMessage
	secrets       []string
	key           string
	local         bool
	operationID   string
	requestID     string
	attemptID     string
	requestDigest string
	maxCost       float64
}

type summaryPreparationConfigSnapshot struct {
	Version  int    `json:"version"`
	Mode     string `json:"mode"`
	Hardware struct {
		AutoProfile         bool    `json:"auto_profile"`
		MaxRAM              float64 `json:"max_ram_usage_pct"`
		MaxVRAM             float64 `json:"max_vram_usage_pct"`
		Concurrent          string  `json:"max_concurrent_local_models"`
		LocalPressurePolicy string  `json:"local_pressure_policy"`
		LocalQueueTimeout   string  `json:"local_queue_timeout"`
	} `json:"hardware"`
	Model struct {
		ID            string  `json:"id"`
		Provider      string  `json:"provider"`
		Model         string  `json:"model"`
		Locality      string  `json:"locality"`
		GPUDevice     string  `json:"gpu_device,omitempty"`
		ContextTokens int     `json:"context_tokens"`
		EstimatedCost float64 `json:"estimated_cost"`
		RAMBytes      uint64  `json:"ram_bytes"`
		VRAMBytes     uint64  `json:"vram_bytes"`
	} `json:"model"`
	Provider struct {
		ID              string `json:"id"`
		Kind            string `json:"kind"`
		RequestTimeout  string `json:"request_timeout,omitempty"`
		APIKeyEnv       string `json:"api_key_env,omitempty"`
		ManageResidency bool   `json:"manage_residency"`
	} `json:"provider"`
}

// PrepareSummary durably starts exactly one caller-keyed summary operation
// before provider construction. Repeating the same semantic request returns
// its current durable projection without dispatch; changing it conflicts.
// A successful draft remains in the started plan phase until a separate
// trusted review authorizes plan preparation.
func (s *Service) PrepareSummary(ctx context.Context, idempotencyKey string, request PrepareSummaryRequest) (sessions.ContextCompactionOperationState, error) {
	bad := func() (sessions.ContextCompactionOperationState, error) {
		return sessions.ContextCompactionOperationState{}, ErrAdmission
	}
	if s == nil || ctx == nil || ctx.Err() != nil || request.Version != prepareSummaryVersion ||
		!validSummaryPreparationKey(idempotencyKey) || request.TaskID == "" || len(request.TaskID) > 128 ||
		request.ModelID == "" || len(request.ModelID) > 128 || request.Keep < 1 || request.Keep > 100000 ||
		request.MaxCost < 0 || math.IsNaN(request.MaxCost) || math.IsInf(request.MaxCost, 0) {
		return bad()
	}
	admission, err := s.prepareSummaryAdmission(ctx, idempotencyKey, request)
	if err != nil {
		return bad()
	}
	owner, err := processguard.Current(ctx)
	if err != nil || owner.Validate() != nil {
		return bad()
	}
	start := sessions.ContextCompactionPlanStart{
		OperationID: admission.operationID, RequestID: admission.requestID, TaskID: request.TaskID,
		SourceSequence: admission.history.Sequence, SourceDigest: admission.requestDigest, AttemptID: admission.attemptID,
		Model: admission.model.Model, Provider: admission.model.Provider, Keep: admission.selection.Keep,
		EstimatedCost: *admission.model.EstimatedCost, ConfigSnapshot: admission.config, PolicySnapshot: admission.policy,
		Engine: admission.engine, Tiers: admission.tiers, ProcessID: owner.ID, StartedAt: s.routingNow(),
	}
	start, err = sealSummaryPreparationStart(start, admission.history, admission.selection)
	if err != nil {
		return bad()
	}
	write, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer write.Close()
	if existing, readErr := write.ContextCompactionPlan(ctx, admission.operationID); readErr == nil {
		if existing.Start.RequestDigest != start.RequestDigest {
			return sessions.ContextCompactionOperationState{}, telemetry.ErrConflict
		}
		return existing, nil
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return bad()
	}
	state, created, err := write.BeginContextCompactionPlan(ctx, start)
	if err != nil {
		// A concurrent exact begin may have committed with a different wall-clock
		// start. Re-read and compare only its semantic request digest.
		if errors.Is(err, telemetry.ErrConflict) {
			existing, readErr := write.ContextCompactionPlan(ctx, admission.operationID)
			if readErr == nil && existing.Start.RequestDigest == start.RequestDigest {
				return existing, nil
			}
		}
		return sessions.ContextCompactionOperationState{}, err
	}
	if !created {
		return state, nil
	}
	return s.executePreparedSummary(ctx, write, admission, state)
}

func sealSummaryPreparationStart(start sessions.ContextCompactionPlanStart, history sessions.Snapshot, selection sessions.CompactionRequest) (sessions.ContextCompactionPlanStart, error) {
	_, checkpoint, err := sessions.PrepareContinuation(history, selection)
	if err != nil {
		return sessions.ContextCompactionPlanStart{}, err
	}
	start.SourceDigest = checkpoint.SourceDigest
	return sessions.SealContextCompactionPlanStart(start)
}

func (s *Service) prepareSummaryAdmission(ctx context.Context, key string, request PrepareSummaryRequest) (preparedSummaryAdmission, error) {
	var out preparedSummaryAdmission
	read, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return out, err
	}
	defer read.Close()
	history, err := sessions.Replay(ctx, read, request.TaskID)
	if err != nil {
		return out, err
	}
	var model config.Model
	for _, candidate := range s.settings.Models {
		if candidate.ID == request.ModelID {
			model = candidate
			break
		}
	}
	if model.ID == "" || model.ContextTokens < 1 || model.EstimatedCost == nil || *model.EstimatedCost > request.MaxCost || model.Locality == "local" && model.RAMBytes == 0 {
		return out, ErrAdmission
	}
	local := model.Locality == "local"
	if (s.settings.Mode == "local_only" && !local) || (s.settings.Mode == "cloud_only" && local) || (history.Privacy != "cloud_allowed" && !local) {
		return out, ErrAdmission
	}
	var provider config.Provider
	for _, candidate := range s.settings.Providers {
		if candidate.ID == model.Provider {
			provider = candidate
			break
		}
	}
	secrets := memorySecrets(s.settings, s.secret)
	providerKey := ""
	if s.secret != nil && provider.APIKeyEnv != "" {
		providerKey = s.secret(provider.APIKeyEnv)
	}
	if provider.ID == "" || !selectionValueClean([]string{request.TaskID, history.SessionID, request.ModelID, model.ID, model.Model, model.Provider, provider.ID}, secrets) {
		return out, ErrAdmission
	}
	engineInput := history
	engineInput.Messages, err = redactSummaryMessages(history.Messages, secrets)
	if err != nil {
		return out, ErrAdmission
	}
	selection, engine, err := contextengine.SelectCompactionDescribed(ctx, s.contextEngine, engineInput,
		sessions.CompactionRequest{Keep: request.Keep, Summary: sessions.Summary{Decisions: []string{"pending draft"}}})
	if err != nil {
		return out, ErrAdmission
	}
	_, checkpoint, err := sessions.PrepareContinuation(history, selection)
	if err != nil {
		return out, ErrAdmission
	}
	input := history
	if provider.Kind == "codex_app_server" {
		input.Messages, err = nativeSummaryMessages(history.Messages, secrets)
	} else {
		input.Messages, err = redactSummaryMessages(history.Messages, secrets)
	}
	if err != nil {
		return out, ErrAdmission
	}
	// Freeze only authority that participated in admission. Provider connection
	// details and filesystem paths are deliberately excluded from durable state.
	snapshot := summaryPreparationConfigSnapshot{Version: prepareSummaryVersion, Mode: s.settings.Mode}
	snapshot.Hardware.AutoProfile = s.settings.Hardware.AutoProfile
	snapshot.Hardware.MaxRAM = s.settings.Hardware.MaxRAM
	snapshot.Hardware.MaxVRAM = s.settings.Hardware.MaxVRAM
	snapshot.Hardware.Concurrent = s.settings.Hardware.Concurrent
	snapshot.Hardware.LocalPressurePolicy = s.settings.Hardware.LocalPressurePolicy
	snapshot.Hardware.LocalQueueTimeout = s.settings.Hardware.LocalQueueTimeout
	snapshot.Model.ID, snapshot.Model.Provider, snapshot.Model.Model = model.ID, model.Provider, model.Model
	snapshot.Model.Locality, snapshot.Model.GPUDevice = model.Locality, model.GPUDevice
	snapshot.Model.ContextTokens, snapshot.Model.EstimatedCost = model.ContextTokens, *model.EstimatedCost
	snapshot.Model.RAMBytes, snapshot.Model.VRAMBytes = model.RAMBytes, model.VRAMBytes
	snapshot.Provider.ID, snapshot.Provider.Kind = provider.ID, provider.Kind
	snapshot.Provider.RequestTimeout, snapshot.Provider.APIKeyEnv = provider.RequestTimeout, provider.APIKeyEnv
	snapshot.Provider.ManageResidency = provider.ManageResidency
	configSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return out, ErrAdmission
	}
	credentialEnv := make([]string, 0, len(s.settings.Providers))
	for _, configured := range s.settings.Providers {
		if configured.APIKeyEnv != "" {
			credentialEnv = append(credentialEnv, configured.APIKeyEnv)
		}
	}
	metricsEnv, traceEnv := "", ""
	if s.settings.Telemetry.MetricsExport != nil {
		metricsEnv = s.settings.Telemetry.MetricsExport.APIKeyEnv
	}
	if s.settings.Telemetry.TraceExport != nil {
		traceEnv = s.settings.Telemetry.TraceExport.APIKeyEnv
	}
	// Bind every configured redaction source by name, never by resolved value.
	// Changing the policy under a reused caller key must conflict even when the
	// selected provider and the source task are otherwise unchanged.
	policySnapshot, err := json.Marshal(struct {
		Version              int      `json:"version"`
		Privacy              string   `json:"privacy"`
		RequestedKeep        int      `json:"requested_keep"`
		MaxCost              float64  `json:"max_cost"`
		LocalOnly            bool     `json:"local_only"`
		Egress               string   `json:"egress"`
		CredentialEnv        []string `json:"credential_env"`
		RedactEnv            []string `json:"redact_env"`
		MetricsCredentialEnv string   `json:"metrics_credential_env,omitempty"`
		TraceCredentialEnv   string   `json:"trace_credential_env,omitempty"`
	}{prepareSummaryVersion, history.Privacy, request.Keep, request.MaxCost, s.settings.Mode == "local_only", s.settings.Security.Egress,
		credentialEnv, append([]string(nil), s.settings.Security.RedactEnv...), metricsEnv, traceEnv})
	if err != nil || !selectionValueClean([]string{string(configSnapshot), string(policySnapshot)}, secrets) {
		return out, ErrAdmission
	}
	stable := summaryPreparationDigest(configSnapshot)
	projectBody, err := json.Marshal(struct {
		Version        int    `json:"version"`
		SourceSequence int64  `json:"source_sequence"`
		SessionID      string `json:"session_id"`
		Privacy        string `json:"privacy"`
	}{prepareSummaryVersion, history.Sequence, history.SessionID, history.Privacy})
	if err != nil {
		return out, ErrAdmission
	}
	volatileBody, err := json.Marshal(struct {
		Version, Keep        int
		TaskID, SourceDigest string
		MaxCost              float64
	}{prepareSummaryVersion, selection.Keep, request.TaskID, checkpoint.SourceDigest, request.MaxCost})
	if err != nil {
		return out, ErrAdmission
	}
	tiers, err := runtime.NewContextTierPlan(stable, summaryPreparationDigest(projectBody), summaryPreparationDigest(volatileBody),
		[]runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierVolatile})
	if err != nil {
		return out, ErrAdmission
	}
	out = preparedSummaryAdmission{history: history, input: input, model: model, provider: provider, selection: selection,
		engine: engine, tiers: tiers, config: configSnapshot, policy: policySnapshot, secrets: secrets,
		key: providerKey, local: local, operationID: summaryPreparationID("operation", key),
		requestID: summaryPreparationID("request", key), attemptID: summaryPreparationID("attempt", key), requestDigest: checkpoint.SourceDigest,
		maxCost: request.MaxCost}
	return out, nil
}

func (s *Service) executePreparedSummary(ctx context.Context, write *telemetry.Store, admission preparedSummaryAdmission, state sessions.ContextCompactionOperationState) (sessions.ContextCompactionOperationState, error) {
	attempt := sessions.SummaryAttempt{Version: 1, ID: state.Start.AttemptID, TaskID: state.Start.TaskID, SourceSequence: state.Start.SourceSequence,
		SourceDigest: state.Start.SourceDigest, Model: state.Start.Model, Provider: state.Start.Provider, Status: "started", Keep: state.Start.Keep,
		EstimatedCost: state.Start.EstimatedCost, StartedAt: state.Start.StartedAt}
	fail := func(code string) (sessions.ContextCompactionOperationState, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		attempt.Status, attempt.Code, attempt.Draft, attempt.FinishedAt = "failed", code, nil, s.routingNow()
		if code == "canceled" {
			attempt.Usage, attempt.Elapsed = nil, 0
		}
		if err := write.FinishSummary(cleanup, attempt); err != nil {
			return state, errors.New("cannot persist summary terminal state")
		}
		fact, err := sessions.SealContextCompactionLifecycleFact(sessions.ContextCompactionLifecycleFact{
			ID: summaryPreparationID("failed-fact", state.Start.OperationID+"\x00"+code), OperationID: state.Start.OperationID,
			Sequence: 2, PreviousID: state.Facts[0].ID, Kind: sessions.ContextCompactionFailed, Code: code, CreatedAt: attempt.FinishedAt,
		})
		if err != nil {
			return state, err
		}
		return write.FailContextCompactionPlan(cleanup, fact)
	}
	if admission.provider.APIKeyEnv != "" && admission.key == "" {
		return fail("summary_failed")
	}
	if admission.local {
		release, err := s.reserveExplicit(ctx, admission.model)
		if err != nil {
			return fail("summary_failed")
		}
		defer release()
	}
	adapter, closeProvider, err := s.openAuxiliaryProvider(ctx, admission.provider, admission.model, admission.history.Privacy, admission.key)
	if err != nil {
		return fail("summary_failed")
	}
	defer closeProvider()
	if native, ok := adapter.(*codexAuxiliaryProvider); ok {
		native.beforeStream = func() error {
			secrets := append(admission.secrets, memorySecrets(s.settings, s.secret)...)
			clean, err := nativeSummaryMessages(admission.history.Messages, secrets)
			if err != nil || !reflect.DeepEqual(clean, admission.input.Messages) {
				return ErrAdmission
			}
			return nil
		}
	}
	summarizer := sessions.Summarizer{ContextEstimator: s.contextEstimator, Provider: adapter, Model: admission.model.Model,
		ContextTokens: admission.model.ContextTokens, Timeout: time.Minute, EstimatedCost: *admission.model.EstimatedCost,
		MaxCost: admission.maxCost, StructuredOutput: admission.provider.Kind == "codex_app_server"}
	draft, err := summarizer.Draft(ctx, admission.input, admission.selection.Keep)
	if err != nil {
		if ctx.Err() != nil {
			return fail("canceled")
		}
		if draft.Usage != nil {
			usage := *draft.Usage
			attempt.Usage, attempt.Elapsed = &usage, draft.Elapsed
		}
		return fail("summary_failed")
	}
	secrets := append(admission.secrets, memorySecrets(s.settings, s.secret)...)
	draft.Request.Summary = redactSummary(draft.Request.Summary, secrets)
	_, checkpoint, err := sessions.PrepareContinuation(admission.history, draft.Request)
	if err != nil {
		return fail("summary_failed")
	}
	draft.Checkpoint, draft.SourceTaskID, draft.SourceSequence, draft.SourceDigest = checkpoint, attempt.TaskID, attempt.SourceSequence, checkpoint.SourceDigest
	attempt.Status, attempt.Draft, attempt.FinishedAt = "drafted", &draft, s.routingNow()
	attempt.Usage, attempt.Elapsed = nil, 0
	if !selectionValueClean(attempt, secrets) {
		return fail("summary_failed")
	}
	if err := write.CompleteSummary(ctx, attempt); err != nil {
		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		committed, readErr := write.SummaryAttempt(inspect, attempt.ID)
		cancel()
		if readErr != nil || !reflect.DeepEqual(committed, attempt) {
			return fail("persistence_failed")
		}
	}
	inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return write.ContextCompactionPlan(inspect, state.Start.OperationID)
}

func validSummaryPreparationKey(key string) bool {
	return len(key) >= 16 && len(key) <= 128 && !strings.ContainsFunc(key, func(r rune) bool { return r < 33 || r > 126 })
}

func summaryPreparationID(kind, value string) string {
	digest := sha256.Sum256([]byte("darwin.context-compaction." + kind + "\x00" + value))
	return "cc_" + hex.EncodeToString(digest[:])
}

func summaryPreparationDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
