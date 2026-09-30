package webui

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestInspectionFixturesValidate(t *testing.T) {
	var fixture struct {
		Models               ModelInspectionPage `json:"models"`
		Route                RouteInspection     `json:"route"`
		TaskUsage            TaskUsageInspection `json:"task_usage"`
		TaskUsageUnavailable TaskUsageInspection `json:"task_usage_unavailable"`
		Tools                ToolInspectionPage  `json:"tools"`
		Audits               AuditInspectionPage `json:"audits"`
		Health               HealthInspection    `json:"health"`
		Resources            ResourceInspection  `json:"resources"`
		ResourcesUnavailable ResourceInspection  `json:"resources_unavailable"`
	}
	decodeFixture(t, "inspections.json", &fixture)
	for name, validate := range map[string]func() error{
		"models": fixture.Models.Validate, "route": fixture.Route.Validate,
		"task usage": fixture.TaskUsage.Validate, "task usage unavailable": fixture.TaskUsageUnavailable.Validate,
		"tools": fixture.Tools.Validate, "audits": fixture.Audits.Validate,
		"health": fixture.Health.Validate, "resources": fixture.Resources.Validate,
		"resources unavailable": fixture.ResourcesUnavailable.Validate,
	} {
		if err := validate(); err != nil {
			t.Fatalf("%s fixture: %v", name, err)
		}
	}
}

func TestInspectionSchemaFixturesAndGoldenFailures(t *testing.T) {
	body, err := os.ReadFile("schema/v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err = json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://nexusrouter.local/schema/webui/v1"
	if err = compiler.AddResource(location, document); err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	decodeFixture(t, "inspections.json", &fixtures)
	definitions := map[string]string{
		"models": "model_inspection_page", "route": "route_inspection",
		"task_usage": "task_usage_inspection", "task_usage_unavailable": "task_usage_inspection",
		"tools": "tool_inspection_page", "audits": "audit_inspection_page",
		"health": "health_inspection", "resources": "resource_inspection",
		"resources_unavailable": "resource_inspection",
	}
	for name, definition := range definitions {
		validateSchemaValue(t, compiler, location+"#/$defs/"+definition, fixtures[name], true)
	}
	for _, name := range []string{"models", "route", "task_usage", "task_usage_unavailable", "tools", "audits", "health", "resources"} {
		validateSchemaValue(t, compiler, location, fixtures[name], true)
	}
	var negative []struct {
		Name       string          `json:"name"`
		Definition string          `json:"definition"`
		Payload    json.RawMessage `json:"payload"`
	}
	decodeFixture(t, "inspection-errors.json", &negative)
	for _, item := range negative {
		t.Run(item.Name, func(t *testing.T) {
			validateSchemaValue(t, compiler, location+"#/$defs/"+item.Definition, item.Payload, false)
		})
	}
}

func TestInspectionGoValidationRejectsAmbiguity(t *testing.T) {
	var raw map[string]json.RawMessage
	decodeFixture(t, "inspections.json", &raw)
	var fixture struct {
		Models    ModelInspectionPage
		Route     RouteInspection
		TaskUsage TaskUsageInspection
		Tools     ToolInspectionPage
		Audits    AuditInspectionPage
		Health    HealthInspection
		Resources ResourceInspection
	}
	for name, target := range map[string]any{"models": &fixture.Models, "route": &fixture.Route, "task_usage": &fixture.TaskUsage, "tools": &fixture.Tools, "audits": &fixture.Audits, "health": &fixture.Health, "resources": &fixture.Resources} {
		if err := json.Unmarshal(raw[name], target); err != nil {
			t.Fatal(name, err)
		}
	}

	duplicateModel := fixture.Models
	duplicateModel.Models = append(duplicateModel.Models, duplicateModel.Models[0])
	noSelected := fixture.Route
	noSelected.Candidates = append([]RouteCandidateInspection(nil), fixture.Route.Candidates...)
	noSelected.Candidates[0].Disposition = "eligible"
	unnormalizedScore := fixture.Route
	unnormalizedScore.Candidates = append([]RouteCandidateInspection(nil), fixture.Route.Candidates...)
	score := -0.1
	unnormalizedScore.Candidates[0].Score = &score
	optimisticUnavailable := fixture.Resources
	optimisticUnavailable.Availability = Unavailable
	optimisticUsage := fixture.TaskUsage
	optimisticUsage.Availability = Unavailable
	pendingCompleted := fixture.Tools
	pendingCompleted.Tools = append([]ToolInspection(nil), fixture.Tools.Tools...)
	pendingCompleted.Tools[0].State = "pending"
	duplicateAudit := fixture.Audits
	duplicateAudit.Audits = append(duplicateAudit.Audits, duplicateAudit.Audits[0])
	wrongPrecedence := fixture.Audits
	wrongPrecedence.Audits = append([]AuditInspection(nil), fixture.Audits.Audits...)
	wrongPrecedence.Audits[0].EvidencePrecedence = []string{"user_feedback", "deterministic", "tool_result", "llm_judge"}
	inconsistentUsage := fixture.Route
	cost := .003
	inconsistentUsage.Usage.Overall.NormalizedCost = &cost
	optimisticHealth := fixture.Health
	optimisticHealth.Status = "healthy"

	for name, validate := range map[string]func() error{
		"duplicate model":               duplicateModel.Validate,
		"route without selection":       noSelected.Validate,
		"unnormalized route score":      unnormalizedScore.Validate,
		"unavailable with values":       optimisticUnavailable.Validate,
		"unavailable usage with values": optimisticUsage.Validate,
		"split pending tool":            pendingCompleted.Validate,
		"duplicate audit":               duplicateAudit.Validate,
		"reordered audit evidence":      wrongPrecedence.Validate,
		"inconsistent usage totals":     inconsistentUsage.Validate,
		"optimistic health":             optimisticHealth.Validate,
	} {
		if !errors.Is(validate(), ErrContract) {
			t.Fatal("ambiguous inspection accepted:", name)
		}
	}
}

func TestInspectionExpandedBoundsMatchSchema(t *testing.T) {
	compiler, location := compileInspectionSchema(t)
	now, total := time.Now().UTC(), uint64(0)
	models := ModelInspectionPage{Version: 1, Availability: Available, ConfigID: strings.Repeat("a", 64), RefreshedAt: &now,
		LocalTotalBytes: &total, LocalTotalKind: "logical_deduplicated", LocalTotalCoverage: "complete", RefreshIntervalMS: 10000,
		LocalProviders: []LocalProviderInspection{}, Models: []ModelInspection{}, LocalConcurrency: "1", LocalPressurePolicy: "reject",
		LocalRAMLimitPct: 75, LocalVRAMLimitPct: 75, Fitness: []ModelFitnessInspection{}}
	for index := 0; index < 101; index++ {
		models.Models = append(models.Models, ModelInspection{ID: "model_" + strconv.Itoa(index), Provider: "provider", Model: "model", Locality: "local", Capabilities: []string{}, Health: "unknown"})
	}
	if models.Validate() != nil {
		t.Fatal("101-model Go contract rejected")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/model_inspection_page", marshalInspection(t, models), true)
	models.LocalConcurrency = "01"
	if !errors.Is(models.Validate(), ErrContract) {
		t.Fatal("noncanonical local concurrency accepted")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/model_inspection_page", marshalInspection(t, models), false)
	models.LocalConcurrency = "1"
	for len(models.Models) <= MaxInspectionModels {
		index := len(models.Models)
		models.Models = append(models.Models, ModelInspection{ID: "model_" + strconv.Itoa(index), Provider: "provider", Model: "model", Locality: "local", Capabilities: []string{}, Health: "unknown"})
	}
	if !errors.Is(models.Validate(), ErrContract) {
		t.Fatal("model maximum not enforced")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/model_inspection_page", marshalInspection(t, models), false)

	zero := UsageTotalInspection{}
	usage := UsageInspection{Coverage: "complete", Primary: zero, Fallback: zero, Classifier: zero, Summarizer: zero, OrchestratorAudit: zero, OptionalJudge: zero, Routed: zero, Auxiliary: zero, Overall: zero, CalculatedAt: time.Unix(100, 0).UTC()}
	score, confidence, samples := .5, .5, int64(1)
	route := RouteInspection{Version: 1, TaskID: "task", Availability: Available, RouteID: "route", Domain: "code", Profile: "default", Candidates: []RouteCandidateInspection{{Model: "selected", Provider: "provider", Disposition: "selected", Score: &score, Confidence: &confidence, Samples: &samples, ConstraintCodes: []string{}}}, Usage: &usage}
	for len(route.Candidates) < 101 {
		index := len(route.Candidates)
		route.Candidates = append(route.Candidates, RouteCandidateInspection{Model: "excluded_" + strconv.Itoa(index), Provider: "provider", Disposition: "excluded", ConstraintCodes: []string{"health"}})
	}
	if route.Validate() != nil {
		t.Fatal("101-candidate Go contract rejected")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/route_inspection", marshalInspection(t, route), true)
	for len(route.Candidates) <= MaxRouteCandidates {
		index := len(route.Candidates)
		route.Candidates = append(route.Candidates, RouteCandidateInspection{Model: "excluded_" + strconv.Itoa(index), Provider: "provider", Disposition: "excluded", ConstraintCodes: []string{"health"}})
	}
	if !errors.Is(route.Validate(), ErrContract) {
		t.Fatal("route candidate maximum not enforced")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/route_inspection", marshalInspection(t, route), false)

	checks := []HealthCheckInspection{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"}}
	for len(checks) < 101 {
		checks = append(checks, HealthCheckInspection{Component: "model", ID: "model_" + strconv.Itoa(len(checks)), Status: "healthy", Code: "available"})
	}
	checked, ready := time.Unix(100, 0).UTC(), true
	healthInspection := HealthInspection{Version: 1, Availability: Available, Status: "healthy", Ready: &ready, CheckedAt: &checked, Checks: checks}
	if healthInspection.Validate() != nil {
		t.Fatal("101-check Go contract rejected")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/health_inspection", marshalInspection(t, healthInspection), true)
	for len(healthInspection.Checks) <= MaxHealthChecks {
		healthInspection.Checks = append(healthInspection.Checks, HealthCheckInspection{Component: "model", ID: "model_" + strconv.Itoa(len(healthInspection.Checks)), Status: "healthy", Code: "available"})
	}
	if !errors.Is(healthInspection.Validate(), ErrContract) {
		t.Fatal("health check maximum not enforced")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/health_inspection", marshalInspection(t, healthInspection), false)
}

func TestUsageAggregationPreservesUnknownMeasurements(t *testing.T) {
	input, output, cost := int64(10), int64(5), .1
	known := UsageTotalInspection{Records: 1, KnownUsageRecords: 1, InputTokens: &input, OutputTokens: &output, KnownCostRecords: 1, NormalizedCost: &cost}
	unknown := UsageTotalInspection{Records: 1, UnknownUsageRecords: 1, UnknownCostRecords: 1}
	mixed := UsageTotalInspection{Records: 2, KnownUsageRecords: 1, UnknownUsageRecords: 1, KnownCostRecords: 1, UnknownCostRecords: 1}
	empty := UsageTotalInspection{}
	usage := UsageInspection{Coverage: "complete", Primary: known, Fallback: unknown, Classifier: empty, Summarizer: empty,
		OrchestratorAudit: empty, OptionalJudge: empty, Routed: mixed, Auxiliary: empty, Overall: mixed, CalculatedAt: time.Unix(100, 0).UTC()}
	if err := usage.Validate(); err != nil {
		t.Fatal("pointer-aware unknown aggregate rejected", err)
	}
	optimistic := usage
	zero := int64(0)
	optimistic.Routed.InputTokens, optimistic.Routed.OutputTokens = &zero, &zero
	if !errors.Is(optimistic.Validate(), ErrContract) {
		t.Fatal("unknown routed usage accepted optimistic zero")
	}
}

func compileInspectionSchema(t *testing.T) (*jsonschema.Compiler, string) {
	t.Helper()
	body, err := os.ReadFile("schema/v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if json.Unmarshal(body, &document) != nil {
		t.Fatal("invalid schema")
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://nexusrouter.local/schema/webui/v1"
	if err = compiler.AddResource(location, document); err != nil {
		t.Fatal(err)
	}
	return compiler, location
}

func marshalInspection(t *testing.T, value any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestInspectionOperationsAreSessionReads(t *testing.T) {
	want := map[string]string{
		"model.list":       "/app/api/v1/models",
		"route.inspect":    "/app/api/v1/tasks/{task}/route",
		"usage.inspect":    "/app/api/v1/tasks/{task}/usage",
		"tool.inspect":     "/app/api/v1/tasks/{task}/tools",
		"audit.inspect":    "/app/api/v1/tasks/{task}/audits",
		"health.inspect":   "/app/api/v1/health",
		"resource.inspect": "/app/api/v1/resources",
	}
	for _, operation := range Operations() {
		path, ok := want[operation.Operation]
		if !ok {
			continue
		}
		if operation.Method != "GET" || operation.BrowserPath != path || operation.Security != SessionRead || operation.Mutation {
			t.Fatalf("unsafe inspection operation: %+v", operation)
		}
		delete(want, operation.Operation)
	}
	if len(want) != 0 {
		t.Fatal("missing inspection operations:", want)
	}
}
