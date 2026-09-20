package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

const browserInspectionTimeout = 5 * time.Second

// BrowserModels projects configured metadata and optional current health facts.
// Invalid health input does not make configuration disappear; it leaves each
// model explicitly unknown.
func (s *Service) BrowserModels(ctx context.Context, report health.Report) (contract.ModelInspectionPage, error) {
	zero := contract.ModelInspectionPage{Version: 1, Availability: contract.Unavailable, LocalProviders: []contract.LocalProviderInspection{}, Models: []contract.ModelInspection{}}
	if s == nil || ctx == nil || ctx.Err() != nil {
		return zero, ErrAdmission
	}
	catalog, err := s.ConfiguredModelCatalog(ctx)
	if err != nil || len(catalog.Models) > contract.MaxInspectionModels {
		return zero, nil
	}
	type healthFact struct{ status, code string }
	modelHealth, providerHealth := map[string]healthFact{}, map[string]healthFact{}
	healthValid := report.Validate() == nil
	if healthValid {
		for _, check := range report.Checks {
			if check.Component == "model" {
				modelHealth[check.ID] = healthFact{check.Status, check.Code}
			} else if check.Component == "provider" {
				providerHealth[check.ID] = healthFact{check.Status, check.Code}
			}
		}
	}
	refreshed := time.Now().UTC()
	refreshInterval, intervalErr := time.ParseDuration(s.settings.WebUI.ModelInventoryRefreshInterval)
	if intervalErr != nil || refreshInterval < 5*time.Second || refreshInterval > 5*time.Minute || refreshInterval%time.Millisecond != 0 {
		return zero, ErrInspection
	}
	total := uint64(0)
	out := contract.ModelInspectionPage{Version: 1, Availability: contract.Available, ConfigID: catalog.ConfigID, RefreshedAt: &refreshed,
		LocalTotalBytes: &total, LocalTotalKind: "logical_deduplicated", LocalTotalCoverage: "complete", RefreshIntervalMS: refreshInterval.Milliseconds(),
		LocalProviders: []contract.LocalProviderInspection{}, Models: make([]contract.ModelInspection, len(catalog.Models)), LocalConcurrency: s.settings.Hardware.Concurrent,
		LocalPressurePolicy: s.settings.Hardware.LocalPressurePolicy, LocalRAMLimitPct: s.settings.Hardware.MaxRAM, LocalVRAMLimitPct: s.settings.Hardware.MaxVRAM}
	out.CommanderID, out.CommanderSource = browserCommander(s.settings.WebUI.DefaultModel, catalog.Models)
	out.CommanderFallbackID = s.settings.WebUI.CommanderFallbackModel
	out.SpecialistsAllowCloud = s.settings.WebUI.SpecialistsAllowCloud
	for _, provider := range s.settings.Providers {
		out.ManagedResidency = out.ManagedResidency || provider.ManageResidency
	}
	configured := map[string]int{}
	for i, model := range catalog.Models {
		fact, observed := modelHealth[model.ID]
		if fact.status != "healthy" && fact.status != "degraded" && fact.status != "unavailable" && fact.status != "disabled" {
			fact.status = "unknown"
		}
		enabled := (s.settings.Mode != "local_only" || model.Locality == "local") && (s.settings.Mode != "cloud_only" || model.Locality == "cloud")
		out.Models[i] = browserModel(model, fact.status)
		if observed {
			checked := report.CheckedAt
			out.Models[i].HealthCheckedAt = &checked
		}
		out.Models[i].Configured, out.Models[i].Enabled = true, enabled
		out.Models[i].Usable, out.Models[i].StatusCode = enabled && fact.status == "healthy", fact.code
		configured[model.Provider+"\x00"+model.Model] = i
	}
	inventories := s.localModelInventory(ctx)
	counted := map[string]bool{}
	for _, configuredProvider := range s.settings.Providers {
		provider := configuredProvider.ID
		inventory, exists := inventories[provider]
		if !exists {
			continue
		}
		providerStatus := contract.LocalProviderInspection{Provider: provider, Status: "available", StatusCode: "available", CheckedAt: inventory.checkedAt}
		if inventory.err != nil {
			providerStatus.Status, providerStatus.StatusCode = "unavailable", "discovery_failed"
			out.LocalProviders = append(out.LocalProviders, providerStatus)
			out.LocalTotalCoverage = "partial"
			continue
		}
		out.LocalProviders = append(out.LocalProviders, providerStatus)
		for _, installed := range inventory.models {
			key := provider + "\x00" + installed.Name
			index, exists := configured[key]
			if !exists {
				if len(out.Models) == contract.MaxInspectionModels {
					return zero, ErrInspection
				}
				digest := sha256.Sum256([]byte(key))
				fact, healthObserved := providerHealth[provider]
				status := fact.status
				if status != "healthy" && status != "degraded" && status != "unavailable" && status != "disabled" {
					status = "unknown"
				}
				out.Models = append(out.Models, contract.ModelInspection{ID: "inventory_" + hex.EncodeToString(digest[:8]), Provider: provider,
					Model: installed.Name, Locality: "local", Installed: true, Capabilities: []string{}, Health: status, StatusCode: fact.code})
				index = len(out.Models) - 1
				if healthObserved {
					checked := report.CheckedAt
					out.Models[index].HealthCheckedAt = &checked
				}
			} else {
				out.Models[index].Installed = true
			}
			item := &out.Models[index]
			if installed.SizeBytes > 0 {
				size := installed.SizeBytes
				item.SizeBytes = &size
				physicalKey := provider + "\x00" + installed.Digest
				if installed.Digest == "" {
					physicalKey = key
				}
				if !counted[physicalKey] {
					if ^uint64(0)-total < size {
						return zero, ErrInspection
					}
					total += size
					counted[physicalKey] = true
				}
			} else {
				out.LocalUnknownSizeCount++
				out.LocalTotalCoverage = "partial"
			}
			item.Digest, item.Family, item.ParameterSize, item.Quantization = installed.Digest, installed.Family, installed.ParameterSize, installed.Quantization
			if !installed.ModifiedAt.IsZero() {
				modified := installed.ModifiedAt.UTC()
				item.ModifiedAt = &modified
			}
		}
	}
	if out.Validate() != nil || !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
		return zero, ErrInspection
	}
	return out, nil
}

func browserCommander(configured string, models []routing.ConfiguredModel) (string, string) {
	if configured != "" {
		return configured, "configured"
	}
	for _, model := range models {
		for _, capability := range model.Capabilities {
			if capability == "orchestration" {
				return model.ID, "inferred"
			}
		}
	}
	for _, model := range models {
		id := strings.ToLower(model.ID)
		if strings.Contains(id, "coordinator") || strings.Contains(id, "commander") || strings.Contains(id, "brain") {
			return model.ID, "inferred"
		}
	}
	return "", ""
}

func browserModel(model routing.ConfiguredModel, modelHealth string) contract.ModelInspection {
	var contextTokens *int64
	if model.ContextTokens > 0 {
		value := int64(model.ContextTokens)
		contextTokens = &value
	}
	var ram, vram *uint64
	if model.RAMBytes > 0 {
		value := model.RAMBytes
		ram = &value
	}
	if model.VRAMBytes > 0 {
		value := model.VRAMBytes
		vram = &value
	}
	return contract.ModelInspection{ID: model.ID, Provider: model.Provider, Model: model.Model, ReasoningEffort: model.ReasoningEffort, Locality: model.Locality,
		Capabilities: append([]string(nil), model.Capabilities...), ContextTokens: contextTokens, EstimatedCost: cloneFloat(model.EstimatedCost),
		RAMBytes: ram, VRAMBytes: vram, FailureDomain: model.FailureDomain, Health: modelHealth}
}

// BrowserRoute returns unavailable for a valid explicit-route task. Unknown or
// corrupt task history remains an inspection error rather than looking absent.
func (s *Service) BrowserRoute(ctx context.Context, task string) (contract.RouteInspection, error) {
	zero := contract.RouteInspection{Version: 1, TaskID: task, Availability: contract.Unavailable, Candidates: []contract.RouteCandidateInspection{}}
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) {
		return zero, ErrAdmission
	}
	if _, err := InspectTask(ctx, s.settings.Telemetry.Database, task); err != nil {
		return zero, err
	}
	routeExpected, err := browserRouteExpected(ctx, s.settings.Telemetry.Database, task)
	if err != nil {
		return zero, err
	}
	if !routeExpected {
		return zero, nil
	}
	explanation, err := InspectRouteExplanation(ctx, s.settings.Telemetry.Database, task)
	if err != nil {
		return zero, ErrInspection
	}
	if len(explanation.Selection.Ranked)+len(explanation.Selection.Excluded) > contract.MaxRouteCandidates {
		return zero, nil
	}
	usage, err := browserUsage(*explanation.Usage)
	if err != nil {
		return zero, ErrInspection
	}
	out := contract.RouteInspection{Version: 1, TaskID: task, Availability: contract.Available, RouteID: explanation.RouteID,
		Domain: explanation.Domain, Profile: explanation.Profile, Explored: explanation.Selection.Explored,
		Candidates: browserRouteCandidates(explanation), Usage: &usage}
	if out.Validate() != nil || !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
		return zero, ErrInspection
	}
	return out, nil
}

func browserRouteExpected(ctx context.Context, path, task string) (bool, error) {
	readCtx, cancel := context.WithTimeout(ctx, browserInspectionTimeout)
	defer cancel()
	db, err := telemetry.OpenReadOnly(readCtx, path)
	if err != nil {
		return false, ErrInspection
	}
	defer db.Close()
	page, err := db.ReadEventPage(readCtx, task, 0, 2)
	if err != nil || len(page.Events) == 0 || page.Events[0].Kind != runtime.TaskStarted {
		return false, ErrInspection
	}
	return len(page.Events) > 1 && page.Events[1].Kind == runtime.RouteSelected, nil
}

func browserRouteCandidates(explanation sessions.RouteExplanation) []contract.RouteCandidateInspection {
	out := make([]contract.RouteCandidateInspection, 0, len(explanation.Selection.Ranked)+len(explanation.Selection.Excluded))
	fallback := map[[2]string]bool{}
	for _, item := range explanation.Selection.Fallbacks {
		fallback[[2]string{item.Provider, item.Model}] = true
	}
	for _, item := range explanation.Selection.Ranked {
		disposition := "eligible"
		if item.Provider == explanation.Selection.Primary.Provider && item.Model == explanation.Selection.Primary.Model {
			disposition = "selected"
		} else if fallback[[2]string{item.Provider, item.Model}] {
			disposition = "fallback"
		}
		score, confidence, samples := item.Score, item.Confidence, int64(item.Samples)
		out = append(out, contract.RouteCandidateInspection{Model: item.Model, Provider: item.Provider, FailureDomain: item.FailureDomain,
			Disposition: disposition, Score: &score, Confidence: &confidence, Samples: &samples, ConstraintCodes: []string{}})
	}
	domains := map[[2]string]string{}
	for _, candidate := range explanation.Candidates {
		domains[[2]string{candidate.Provider, candidate.Model}] = candidate.FailureDomain
	}
	for _, item := range explanation.Selection.Excluded {
		out = append(out, contract.RouteCandidateInspection{Model: item.Model, Provider: item.Provider,
			FailureDomain: domains[[2]string{item.Provider, item.Model}], Disposition: "excluded", ConstraintCodes: append([]string(nil), item.Reasons...)})
	}
	return out
}

func (s *Service) BrowserTaskUsage(ctx context.Context, task string) (contract.TaskUsageInspection, error) {
	zero := contract.TaskUsageInspection{Version: 1, TaskID: task, Availability: contract.Unavailable}
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) {
		return zero, ErrAdmission
	}
	if _, err := InspectTask(ctx, s.settings.Telemetry.Database, task); err != nil {
		return zero, err
	}
	totals, err := s.InspectTaskUsage(ctx, task)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	usage, err := browserUsage(totals)
	if err != nil {
		return zero, err
	}
	out := contract.TaskUsageInspection{Version: 1, TaskID: task, Availability: contract.Available, Usage: &usage}
	if out.Validate() != nil || !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
		return zero, ErrInspection
	}
	return out, nil
}

func browserUsage(t accounting.Totals) (contract.UsageInspection, error) {
	if t.Validate() != nil {
		return contract.UsageInspection{}, ErrInspection
	}
	convert := func(value accounting.Total) contract.UsageTotalInspection {
		return contract.UsageTotalInspection{Records: value.Records, KnownUsageRecords: value.KnownUsageRecords,
			UnknownUsageRecords: value.UnknownUsageRecords, InputTokens: cloneInt64(value.InputTokens), OutputTokens: cloneInt64(value.OutputTokens),
			KnownCostRecords: value.KnownCostRecords, UnknownCostRecords: value.UnknownCostRecords, NormalizedCost: cloneFloat(value.NormalizedCost)}
	}
	out := contract.UsageInspection{Coverage: string(t.Coverage), UnaccountedRouted: t.UnaccountedRoutedOperations,
		Primary: convert(t.Primary), Fallback: convert(t.Fallback), Classifier: convert(t.Classifier), Summarizer: convert(t.Summarizer),
		OrchestratorAudit: convert(t.OrchestratorAudit), OptionalJudge: convert(t.Judge), Routed: convert(t.Routed),
		Auxiliary: convert(t.Auxiliary), Overall: convert(t.Overall), CalculatedAt: t.CalculatedAt}
	return out, out.Validate()
}

func (s *Service) BrowserTools(ctx context.Context, task, after string, limit int) (contract.ToolInspectionPage, error) {
	zero := contract.ToolInspectionPage{Version: 1, TaskID: task, Tools: []contract.ToolInspection{}}
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) || limit < 1 || limit > contract.MaxInspectionItems {
		return zero, ErrAdmission
	}
	offset, err := browserOffset(after)
	if err != nil {
		return zero, ErrAdmission
	}
	readCtx, cancel := context.WithTimeout(ctx, browserInspectionTimeout)
	defer cancel()
	db, err := telemetry.OpenReadOnly(readCtx, s.settings.Telemetry.Database)
	if err != nil {
		return zero, ErrInspection
	}
	defer db.Close()
	tools, err := readBrowserTools(readCtx, db, task)
	if err != nil || offset > len(tools) {
		return zero, ErrInspection
	}
	end := offset + limit
	if end > len(tools) {
		end = len(tools)
	}
	out := contract.ToolInspectionPage{Version: 1, TaskID: task, Tools: append([]contract.ToolInspection(nil), tools[offset:end]...)}
	if end < len(tools) {
		out.NextCursor = strconv.Itoa(end)
	}
	if out.Validate() != nil || !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
		return zero, ErrInspection
	}
	return out, nil
}

func readBrowserTools(ctx context.Context, db *telemetry.Store, task string) ([]contract.ToolInspection, error) {
	items := []contract.ToolInspection{}
	positions := map[string]int{}
	permissions, err := browserToolPermissions(ctx, db, task)
	if err != nil {
		return nil, err
	}
	after := int64(0)
	for after < sessions.MaxTaskEvents {
		page, err := db.ReadEventPage(ctx, task, after, 100)
		if err != nil {
			return nil, err
		}
		for _, event := range page.Events {
			switch event.Kind {
			case runtime.ToolStarted:
				if _, exists := positions[event.Data.ToolCallID]; exists {
					return nil, ErrInspection
				}
				permission := permissions[event.Data.ToolCallID]
				if permission == "" {
					permission = "unknown"
				}
				positions[event.Data.ToolCallID] = len(items)
				items = append(items, contract.ToolInspection{CallID: event.Data.ToolCallID, Name: event.Data.ToolName,
					Behavior: browserToolBehavior(event.Data.ToolBehavior), Permission: permission, State: "pending", Effect: "uncertain",
					StartedAt: event.Time.UTC(), StartSequence: event.Sequence})
			case runtime.ToolCompleted:
				position, exists := positions[event.Data.ToolCallID]
				if !exists || items[position].CompletedAt != nil || items[position].Name != event.Data.ToolName || items[position].Behavior != browserToolBehavior(event.Data.ToolBehavior) {
					return nil, ErrInspection
				}
				completed, sequence := event.Time.UTC(), event.Sequence
				items[position].CompletedAt, items[position].CompletionSequence, items[position].Effect = &completed, &sequence, string(event.Data.Effect)
				if event.Data.Code == "" {
					items[position].State = "completed"
				} else {
					items[position].State, items[position].Code = "failed", event.Data.Code
				}
			}
		}
		after = page.NextSequence
		if !page.HasMore {
			break
		}
	}
	if after >= sessions.MaxTaskEvents {
		page, err := db.ReadEventPage(ctx, task, after, 1)
		if err != nil || page.HasMore {
			return nil, ErrInspection
		}
	}
	for _, item := range items {
		if item.Validate() != nil {
			return nil, ErrInspection
		}
	}
	return items, nil
}

func browserToolPermissions(ctx context.Context, db *telemetry.Store, task string) (map[string]string, error) {
	out := map[string]string{}
	after := ""
	for pageNumber := 0; pageNumber < sessions.MaxTaskEvents/100; pageNumber++ {
		page, err := db.ListApprovals(ctx, approvals.ListOptions{TaskID: task, AfterCallID: after, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, record := range page.Records {
			permission := record.State
			if permission == approvals.Consumed {
				permission = approvals.Approved
			}
			out[record.Request.ToolCallID] = permission
		}
		if page.NextAfterCallID == "" {
			return out, nil
		}
		after = page.NextAfterCallID
	}
	return nil, ErrInspection
}

func browserToolBehavior(value runtime.ToolBehavior) string {
	if value == runtime.BehaviorReadOnly || value == runtime.BehaviorIdempotentWrite || value == runtime.BehaviorNonIdempotentWrite {
		return string(value)
	}
	return "unknown"
}

func (s *Service) BrowserAudits(ctx context.Context, task, after string, limit int) (contract.AuditInspectionPage, error) {
	zero := contract.AuditInspectionPage{Version: 1, TaskID: task, Audits: []contract.AuditInspection{}}
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) || len(after) > 128 || limit < 1 || limit > contract.MaxInspectionItems {
		return zero, ErrAdmission
	}
	readCtx, cancel := context.WithTimeout(ctx, browserInspectionTimeout)
	defer cancel()
	db, err := telemetry.OpenReadOnly(readCtx, s.settings.Telemetry.Database)
	if err != nil {
		return zero, ErrInspection
	}
	defer db.Close()
	if _, err = db.TaskSnapshot(readCtx, task); err != nil {
		return zero, ErrInspection
	}
	attempts, err := db.ReviewAttempts(readCtx, task, after, limit)
	if err != nil {
		return zero, ErrInspection
	}
	out := contract.AuditInspectionPage{Version: 1, TaskID: task, Audits: make([]contract.AuditInspection, 0, len(attempts))}
	for _, attempt := range attempts {
		item, err := s.browserAudit(readCtx, db, attempt)
		if err != nil {
			return zero, err
		}
		out.Audits = append(out.Audits, item)
	}
	if len(attempts) == limit {
		more, moreErr := db.ReviewAttempts(readCtx, task, attempts[len(attempts)-1].ID, 1)
		if moreErr != nil {
			return zero, ErrInspection
		}
		if len(more) != 0 {
			out.NextCursor = attempts[len(attempts)-1].ID
		}
	}
	if out.Validate() != nil {
		return zero, ErrInspection
	}
	return out, nil
}

func (s *Service) browserAudit(ctx context.Context, db *telemetry.Store, attempt evaluation.ReviewAttempt) (contract.AuditInspection, error) {
	record := auditForAttempt(ctx, db, attempt)
	if attempt.ReviewerID == "" {
		if record == nil || record.Validate() != nil || record.TaskID != attempt.TaskID || record.AttemptID != attempt.AttemptID || record.EvaluatorModel != attempt.EvaluatorModel || record.EvaluatorProvider != attempt.EvaluatorProvider {
			return contract.AuditInspection{}, ErrInspection
		}
		return s.browserAutomaticAudit(attempt, *record)
	}
	status, err := publicAuditStatus(attempt, record, memorySecrets(s.settings, s.secret))
	if err != nil {
		return contract.AuditInspection{}, ErrInspection
	}
	out := contract.AuditInspection{ID: status.ID, Status: status.Status, ReviewerID: status.ReviewerID,
		EvaluatorModel: status.EvaluatorModel, EvaluatorProvider: status.EvaluatorProvider, RubricVersion: status.RubricVersion,
		Domain: status.Domain, Findings: make([]contract.AuditFindingInspection, len(status.Findings)),
		EvidencePrecedence: make([]string, len(status.EvidencePrecedence)), StartedAt: status.StartedAt.UTC(), FinishedAt: cloneTime(status.FinishedAt)}
	for i, finding := range status.Findings {
		out.Findings[i] = contract.AuditFindingInspection{Summary: finding.Summary, EvidenceRefs: append([]string(nil), finding.EvidenceRefs...)}
	}
	for i, source := range status.EvidencePrecedence {
		out.EvidencePrecedence[i] = string(source)
	}
	if status.Usage != nil {
		total := contract.UsageTotalInspection{Records: 1, KnownUsageRecords: 1, InputTokens: cloneInt64(&status.Usage.InputTokens),
			OutputTokens: cloneInt64(&status.Usage.OutputTokens), UnknownCostRecords: 1}
		out.Usage = &total
	}
	if out.Validate() != nil {
		return contract.AuditInspection{}, ErrInspection
	}
	return out, nil
}

func (s *Service) browserAutomaticAudit(attempt evaluation.ReviewAttempt, record evaluation.AuditRecord) (contract.AuditInspection, error) {
	status := map[string]string{"accept": "completed", "reject": "rejected", "abstain": "abstained"}[record.Audit.Verdict]
	started, finished := attempt.StartedAt.UTC(), attempt.FinishedAt.UTC()
	out := contract.AuditInspection{ID: attempt.ID, Status: status, ReviewerID: record.Audit.EvaluatorID,
		EvaluatorModel: record.EvaluatorModel, EvaluatorProvider: record.EvaluatorProvider, RubricVersion: record.Audit.RubricVersion,
		Domain: record.Audit.Domain, Findings: make([]contract.AuditFindingInspection, len(record.Audit.Findings)),
		EvidencePrecedence: []string{"deterministic", "tool_result", "user_feedback", "llm_judge"}, StartedAt: started, FinishedAt: &finished}
	secrets := memorySecrets(s.settings, s.secret)
	for i, finding := range record.Audit.Findings {
		out.Findings[i] = contract.AuditFindingInspection{Summary: redact(finding.Summary, secrets), EvidenceRefs: append([]string(nil), finding.EvidenceRefs...)}
	}
	if record.Usage != nil {
		total := contract.UsageTotalInspection{Records: 1, KnownUsageRecords: 1, InputTokens: cloneInt64(&record.Usage.InputTokens),
			OutputTokens: cloneInt64(&record.Usage.OutputTokens), UnknownCostRecords: 1}
		out.Usage = &total
	}
	if out.Validate() != nil || !selectionValueClean(out, secrets) {
		return contract.AuditInspection{}, ErrInspection
	}
	return out, nil
}

func BrowserHealth(report health.Report, sourceErr error) contract.HealthInspection {
	zero := contract.HealthInspection{Version: 1, Availability: contract.Unavailable, Status: "unavailable", Checks: []contract.HealthCheckInspection{}}
	if sourceErr != nil || report.Validate() != nil || len(report.Checks) > contract.MaxHealthChecks {
		return zero
	}
	ready, checked := report.Ready, report.CheckedAt.UTC()
	out := contract.HealthInspection{Version: 1, Availability: contract.Available, Status: report.Status, Ready: &ready, CheckedAt: &checked, Checks: make([]contract.HealthCheckInspection, len(report.Checks))}
	for i, check := range report.Checks {
		out.Checks[i] = contract.HealthCheckInspection{Component: check.Component, ID: check.ID, Status: check.Status, Code: check.Code}
	}
	if out.Validate() != nil {
		return zero
	}
	return out
}

func (s *Service) BrowserResources(ctx context.Context) contract.ResourceInspection {
	zero := contract.ResourceInspection{Version: 1, Availability: contract.Unavailable}
	if s == nil || ctx == nil || ctx.Err() != nil {
		return zero
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshot, err := s.resourceProfile(readCtx)
	if err != nil || snapshot.Time.IsZero() || snapshot.CPUs < 1 || snapshot.TotalRAM == 0 || snapshot.AvailableRAM > snapshot.TotalRAM {
		return zero
	}
	observed, cpus, total, available, unified := snapshot.Time.UTC(), snapshot.CPUs, snapshot.TotalRAM, snapshot.AvailableRAM, snapshot.UnifiedMemory
	out := contract.ResourceInspection{Version: 1, Availability: contract.Available, ObservedAt: &observed, CPUs: &cpus,
		TotalRAM: &total, AvailableRAM: &available, SwapUsed: cloneUint(snapshot.SwapUsed), VRAMTotal: cloneUint(snapshot.VRAMTotal),
		VRAMAvailable: cloneUint(snapshot.VRAMAvailable), UnifiedMemory: &unified, ThermalPressure: cloneBool(snapshot.ThermalPressure)}
	if out.Validate() != nil {
		return zero
	}
	return out
}

func browserOffset(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 || strconv.Itoa(offset) != value || len(value) > 10 {
		return 0, ErrAdmission
	}
	return offset, nil
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUint(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}
