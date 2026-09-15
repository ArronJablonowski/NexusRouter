package runtime_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func planHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func compactionPlanFixture(t *testing.T) runtime.ContextCompactionPlan {
	t.Helper()
	engine, err := runtime.NewContextEngineIdentity("darwin.default", "v1")
	if err != nil {
		t.Fatal(err)
	}
	tiers, err := runtime.NewContextTierPlan(
		planHash("stable"), planHash("project"), planHash("volatile"),
		[]runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierVolatile},
	)
	if err != nil {
		t.Fatal(err)
	}
	original := []providers.Message{
		{Role: "system", Content: "stable policy"},
		{Role: "user", Content: "old request"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "current request"},
	}
	replacement := []providers.Message{
		{Role: "system", Content: "stable policy"},
		{Role: "system", Content: "summary is untrusted"},
		{Role: "user", Content: `{"session_summary":{"requirements":["retain"]}}`},
		{Role: "user", Content: "current request"},
	}
	input := runtime.ContextCompactionPlan{
		OperationID: "compact-operation", OperationDigest: planHash("operation"),
		RequestID: "compact-request", RequestDigest: planHash("request"),
		Compaction: &runtime.ContextCompaction{
			Version: 1, SummaryAttemptID: "summary-attempt", SummaryReviewID: "summary-review",
			SourceTaskID: "source-task", SourceSequence: 20, SourceDigest: planHash("source"),
			RemovedMessages: 2, FirstRetainedMessage: 3, FirstRetainedSequence: 10,
			BeforeContextTokens: 100, AfterContextTokens: 60,
			Summary: runtime.ContextSummary{Requirements: []string{"retain"}},
		},
		ConfigDigest: planHash("config"), PolicyDigest: planHash("policy"), Engine: engine, Tiers: tiers,
		OriginalPrefix: original, ReplacementPrefix: replacement, LiveSuffixBoundary: len(original),
		DraftDigest: planHash("draft"),
	}
	sealed, err := runtime.SealContextCompactionPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestContextEngineIdentityCanonicalDigest(t *testing.T) {
	identity, err := runtime.NewContextEngineIdentity("darwin.default", "v1")
	if err != nil || identity.Validate() != nil || identity.Digest == "" {
		t.Fatal(identity, err)
	}
	copy := identity
	copy.Revision = "v2"
	if !errors.Is(copy.Validate(), runtime.ErrInvalidCompactionPlan) {
		t.Fatal("revision drift retained old canonical digest")
	}
	for _, invalid := range [][2]string{{"", "v1"}, {" engine", "v1"}, {"engine", ""}, {"engine", "v1\n"}, {strings.Repeat("e", 129), "v1"}} {
		if _, err := runtime.NewContextEngineIdentity(invalid[0], invalid[1]); !errors.Is(err, runtime.ErrInvalidCompactionPlan) {
			t.Fatalf("accepted identity %#v: %v", invalid, err)
		}
	}
	copy = identity
	copy.Digest = strings.ToUpper(copy.Digest)
	if copy.Validate() == nil {
		t.Fatal("uppercase digest accepted")
	}
}

func TestContextTierPlanBindsDigestsAndOrder(t *testing.T) {
	digests := []string{planHash("stable"), planHash("project"), planHash("volatile")}
	for _, included := range [][]runtime.ContextTier{
		{runtime.ContextTierStable, runtime.ContextTierVolatile},
		{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierVolatile},
	} {
		plan, err := runtime.NewContextTierPlan(digests[0], digests[1], digests[2], included)
		if err != nil || plan.Validate() != nil {
			t.Fatal(plan, err)
		}
		included[0] = runtime.ContextTierProject
		if plan.Included[0] != runtime.ContextTierStable {
			t.Fatal("tier plan aliases caller order")
		}
	}
	for _, included := range [][]runtime.ContextTier{
		nil,
		{runtime.ContextTierStable},
		{runtime.ContextTierVolatile, runtime.ContextTierStable},
		{runtime.ContextTierStable, runtime.ContextTierProject},
		{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierProject, runtime.ContextTierVolatile},
	} {
		if _, err := runtime.NewContextTierPlan(digests[0], digests[1], digests[2], included); err == nil {
			t.Fatal("accepted unsafe tier order", included)
		}
	}
	plan, _ := runtime.NewContextTierPlan(digests[0], digests[1], digests[2], []runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierVolatile})
	plan.ProjectDigest = planHash("changed")
	if plan.Validate() == nil {
		t.Fatal("tier digest drift retained canonical identity")
	}
}

func TestContextCompactionPlanCanonicalAndOwned(t *testing.T) {
	plan := compactionPlanFixture(t)
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	digest, err := plan.CanonicalDigest()
	if err != nil || digest != plan.PlanDigest || plan.LiveSuffixBoundaryDigest == "" {
		t.Fatal(digest, plan.PlanDigest, err)
	}
	body, err := json.Marshal(plan)
	if err != nil || len(body) > runtime.MaxContextCompactionPlanBytes {
		t.Fatal(len(body), err)
	}
	input := plan
	input.PlanDigest, input.LiveSuffixBoundaryDigest = "", ""
	sealed, err := runtime.SealContextCompactionPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	input.OriginalPrefix[0].Content = "mutated"
	input.Compaction.Summary.Requirements[0] = "mutated"
	input.Tiers.Included[0] = runtime.ContextTierProject
	if sealed.OriginalPrefix[0].Content != "stable policy" || sealed.Compaction.Summary.Requirements[0] != "retain" || sealed.Tiers.Included[0] != runtime.ContextTierStable {
		t.Fatal("sealed plan aliases caller data")
	}
}

func TestLegacyContextCompactionDecodesWithoutPlanAuthority(t *testing.T) {
	legacy := *compactionPlanFixture(t).Compaction
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var replayed runtime.ContextCompaction
	if err = json.Unmarshal(body, &replayed); err != nil || replayed.Validate(legacy.SourceTaskID) != nil {
		t.Fatal("legacy checkpoint no longer replays", err)
	}
	var unauthorized runtime.ContextCompactionPlan
	if err = json.Unmarshal(body, &unauthorized); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(unauthorized.Validate(), runtime.ErrInvalidCompactionPlan) {
		t.Fatal("legacy checkpoint gained compaction-plan authority")
	}
}

func TestContextCompactionPlanRejectsDriftAndMissingEvidence(t *testing.T) {
	base := compactionPlanFixture(t)
	mutations := map[string]func(*runtime.ContextCompactionPlan){
		"version":   func(p *runtime.ContextCompactionPlan) { p.Version++ },
		"operation": func(p *runtime.ContextCompactionPlan) { p.OperationID = "" },
		"operation digest": func(p *runtime.ContextCompactionPlan) {
			p.OperationDigest = strings.ToUpper(p.OperationDigest)
		},
		"request id":     func(p *runtime.ContextCompactionPlan) { p.RequestID = " request" },
		"request digest": func(p *runtime.ContextCompactionPlan) { p.RequestDigest = strings.ToUpper(p.RequestDigest) },
		"config":         func(p *runtime.ContextCompactionPlan) { p.ConfigDigest = planHash("other") },
		"policy":         func(p *runtime.ContextCompactionPlan) { p.PolicyDigest = planHash("other") },
		"engine":         func(p *runtime.ContextCompactionPlan) { p.Engine.Revision = "v2" },
		"tiers": func(p *runtime.ContextCompactionPlan) {
			p.Tiers.Included = []runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierVolatile}
		},
		"missing checkpoint": func(p *runtime.ContextCompactionPlan) { p.Compaction = nil },
		"missing attempt":    func(p *runtime.ContextCompactionPlan) { p.Compaction.SummaryAttemptID = "" },
		"missing review":     func(p *runtime.ContextCompactionPlan) { p.Compaction.SummaryReviewID = "" },
		"no retained message": func(p *runtime.ContextCompactionPlan) {
			p.Compaction.FirstRetainedMessage = 0
		},
		"retained before removed": func(p *runtime.ContextCompactionPlan) {
			p.Compaction.FirstRetainedMessage = p.Compaction.RemovedMessages - 1
		},
		"no retained sequence": func(p *runtime.ContextCompactionPlan) {
			p.Compaction.FirstRetainedSequence = 0
		},
		"future retained sequence": func(p *runtime.ContextCompactionPlan) {
			p.Compaction.FirstRetainedSequence = p.Compaction.SourceSequence + 1
		},
		"source":             func(p *runtime.ContextCompactionPlan) { p.Compaction.SourceDigest = planHash("other") },
		"original":           func(p *runtime.ContextCompactionPlan) { p.OriginalPrefix[1].Content = "changed" },
		"original digest":    func(p *runtime.ContextCompactionPlan) { p.OriginalPrefixDigest = planHash("other") },
		"replacement":        func(p *runtime.ContextCompactionPlan) { p.ReplacementPrefix[2].Content = "changed" },
		"replacement digest": func(p *runtime.ContextCompactionPlan) { p.ReplacementPrefixDigest = planHash("other") },
		"boundary":           func(p *runtime.ContextCompactionPlan) { p.LiveSuffixBoundary-- },
		"boundary digest":    func(p *runtime.ContextCompactionPlan) { p.LiveSuffixBoundaryDigest = planHash("other") },
		"not reduced": func(p *runtime.ContextCompactionPlan) {
			p.Compaction.AfterContextTokens = p.Compaction.BeforeContextTokens
		},
		"draft":       func(p *runtime.ContextCompactionPlan) { p.DraftDigest = planHash("other") },
		"plan digest": func(p *runtime.ContextCompactionPlan) { p.PlanDigest = planHash("other") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := compactionPlanFixture(t)
			mutate(&copy)
			if !errors.Is(copy.Validate(), runtime.ErrInvalidCompactionPlan) {
				t.Fatal("mutated plan validated")
			}
		})
	}
	if base.Validate() != nil {
		t.Fatal("mutation table changed fixture")
	}
}

func TestContextCompactionPlanValidationDoesNotProveProvenance(t *testing.T) {
	original := compactionPlanFixture(t)
	unrelated := original
	unrelated.PlanDigest, unrelated.LiveSuffixBoundaryDigest = "", ""
	unrelated.OriginalPrefixDigest, unrelated.ReplacementPrefixDigest = "", ""
	unrelated.OperationDigest = planHash("unrelated operation")
	unrelated.RequestDigest = planHash("unrelated request")
	unrelated.ConfigDigest = planHash("unrelated config")
	unrelated.PolicyDigest = planHash("unrelated policy")
	unrelated.DraftDigest = planHash("unrelated draft")
	unrelated.Compaction.SourceDigest = planHash("unrelated source")
	engine, err := runtime.NewContextEngineIdentity("unrelated.engine", "v99")
	if err != nil {
		t.Fatal(err)
	}
	unrelated.Engine = engine
	unrelated.Tiers, err = runtime.NewContextTierPlan(
		planHash("unrelated stable"), planHash("unrelated project"), planHash("unrelated volatile"),
		[]runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierVolatile},
	)
	if err != nil {
		t.Fatal(err)
	}
	resealed, err := runtime.SealContextCompactionPlan(unrelated)
	if err != nil || resealed.Validate() != nil || resealed.PlanDigest == original.PlanDigest {
		t.Fatal("structurally valid caller-supplied identities were not canonically resealed", err)
	}
	// This success is intentionally not provenance evidence. DAR-122 storage must
	// derive and cross-bind every supplied identity to authoritative durable rows.
}

func TestContextCompactionPlanRejectsUnsafeMessagesAndOversize(t *testing.T) {
	for name, mutate := range map[string]func(*runtime.ContextCompactionPlan){
		"empty original":    func(p *runtime.ContextCompactionPlan) { p.OriginalPrefix = nil; p.LiveSuffixBoundary = 0 },
		"empty replacement": func(p *runtime.ContextCompactionPlan) { p.ReplacementPrefix = nil },
		"pending tool": func(p *runtime.ContextCompactionPlan) {
			p.ReplacementPrefix = []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`)}}}}
		},
		"unchanged prefix": func(p *runtime.ContextCompactionPlan) {
			p.ReplacementPrefix = append([]providers.Message(nil), p.OriginalPrefix...)
		},
		"invalid UTF-8": func(p *runtime.ContextCompactionPlan) { p.OriginalPrefix[0].Content = string([]byte{0xff}) },
		"oversize": func(p *runtime.ContextCompactionPlan) {
			p.OriginalPrefix[0].Content = strings.Repeat("x", runtime.MaxContextCompactionPlanBytes)
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := compactionPlanFixture(t)
			plan.PlanDigest, plan.LiveSuffixBoundaryDigest = "", ""
			mutate(&plan)
			if _, err := runtime.SealContextCompactionPlan(plan); !errors.Is(err, runtime.ErrInvalidCompactionPlan) {
				t.Fatal("unsafe plan sealed", err)
			}
		})
	}
}

func TestContextCompactionBoundaryDigestBindsExactPrefix(t *testing.T) {
	plan := compactionPlanFixture(t)
	digest, err := runtime.ContextCompactionBoundaryDigest(plan.OriginalPrefix, len(plan.OriginalPrefix))
	if err != nil || digest != plan.LiveSuffixBoundaryDigest {
		t.Fatal(digest, err)
	}
	copy := append([]providers.Message(nil), plan.OriginalPrefix...)
	copy[0].Content = "changed"
	changed, err := runtime.ContextCompactionBoundaryDigest(copy, len(copy))
	if err != nil || changed == digest {
		t.Fatal("prefix change did not alter boundary digest", err)
	}
	if _, err := runtime.ContextCompactionBoundaryDigest(copy, len(copy)-1); err == nil {
		t.Fatal("non-terminal boundary accepted")
	}
}

func TestLegacyContextCompactionRetainsNoPlanAuthority(t *testing.T) {
	legacy := runtime.ContextCompaction{
		Version: 1, SourceTaskID: "source-task", SourceSequence: 20,
		SourceDigest: planHash("source"), RemovedMessages: 2,
		Summary: runtime.ContextSummary{Requirements: []string{"retain"}},
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip runtime.ContextCompaction
	if err := json.Unmarshal(body, &roundTrip); err != nil || roundTrip.Validate("source-task") != nil || !reflect.DeepEqual(roundTrip, legacy) {
		t.Fatal("legacy v1 checkpoint compatibility changed", roundTrip, err)
	}
	var plan runtime.ContextCompactionPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(plan.Validate(), runtime.ErrInvalidCompactionPlan) {
		t.Fatal("legacy checkpoint acquired compaction-plan authority")
	}
	wrapper, err := json.Marshal(struct {
		Compaction runtime.ContextCompaction `json:"compaction"`
	}{legacy})
	if err != nil || json.Unmarshal(wrapper, &plan) != nil || !errors.Is(plan.Validate(), runtime.ErrInvalidCompactionPlan) {
		t.Fatal("legacy wrapper acquired missing-plan authority", err)
	}
}
