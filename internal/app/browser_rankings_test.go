package app

import (
	"context"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"math"
	"testing"
	"time"
)

func TestBrowserRankingUsesDispatchWeightsCurrentHeadsAndExactScope(t *testing.T) {
	for _, scope := range []struct{ key, domain, profile, capability string }{
		{"cli", "commandline", "benchmark", "tools"},
		{"ocr", "ocr", "ocr-progressive-v1", "vision"},
	} {
		t.Run(scope.key, func(t *testing.T) {
			testBrowserEvidenceRanking(t, scope.key, scope.domain, scope.profile, scope.capability)
		})
	}
}

func testBrowserEvidenceRanking(t *testing.T, card, domain, profile, capability string) {
	s, cfg := autoFixture(t)
	ctx := context.Background()
	var winnerTask string
	for i := range s.settings.Models {
		s.settings.Models[i].ContextTokens = 32768
		s.settings.Models[i].Capabilities = []string{capability}
	}
	for _, seed := range []struct {
		id, domain, profile string
		pass                bool
	}{{"a", domain, profile, false}, {"z", domain, profile, true}, {"a", domain, "default", true}} {
		result, err := s.Run(ctx, Request{ModelID: seed.id, Domain: seed.domain, Profile: seed.profile, Prompt: "seed"})
		if err != nil {
			t.Fatal(err)
		}
		if seed.id == "z" {
			winnerTask = result.TaskID
		}
		if err := RecordFeedback(ctx, cfg.Telemetry.Database, result.TaskID, seed.pass, 0); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	cost := 0.0
	ram := uint64(100)
	tokens := int64(32768)
	models := []contract.ModelInspection{}
	for _, id := range []string{"a", "z"} {
		models = append(models, contract.ModelInspection{ID: id, Model: id, Provider: "local", Configured: true, Usable: true, ContextTokens: &tokens, EstimatedCost: &cost, RAMBytes: &ram, Capabilities: []string{capability}, Locality: "local"})
	}
	rows := s.browserRankings(ctx, models)
	var cli contract.SpecialistRankingInspection
	for _, row := range rows {
		if row.Key == card {
			cli = row
		}
	}
	if cli.Domain != domain || cli.Profile != profile || len(cli.Models) != 2 || cli.Models[0].ModelID != "z" {
		t.Fatal(cli)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, decay := configuredRoutingPolicy(s.settings)
	p.Exploration = 0
	evidence := map[routing.Key]routing.Evidence{}
	candidates := []routing.Candidate{}
	for _, m := range models {
		key := routing.Key{Model: m.Model, Provider: m.Provider, Domain: domain, Profile: profile}
		set, err := db.ObservationSet(ctx, key, s.settings.Evaluation.Judge)
		if err != nil {
			t.Fatal(err)
		}
		e, err := routing.AggregateEvidence(key, set, now, p, decay)
		if err != nil {
			t.Fatal(err)
		}
		v, err := db.OutputValidity(ctx, key, "")
		if err != nil {
			t.Fatal(err)
		}
		if v.Samples > 0 {
			e.Validity = v
		}
		evidence[key] = e
		candidates = append(candidates, routing.Candidate{Model: m.Model, Provider: m.Provider, Local: true, Capabilities: m.Capabilities, ContextTokens: 32768, Healthy: true, PolicyAllowed: true, CapacityAvailable: true})
	}
	expected, err := routing.Select(routing.Request{Mode: "local_only", Domain: domain, Profile: profile, LocalRequired: true, ContextTokens: 32768}, p, candidates, evidence, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, rank := range expected.Ranked {
		if cli.Models[i].ModelID != rank.Model || math.Abs(cli.Models[i].Score-rank.Score) > 1e-12 || cli.Models[i].Samples != 1 {
			t.Fatal("preview drift", cli.Models, expected.Ranked)
		}
	}
	models[1].ContextSelectionStatus = "blocked"
	for _, row := range s.browserRankings(ctx, models) {
		if row.Key == card && (len(row.Models) != 1 || row.Models[0].ModelID != "a") {
			t.Fatal("unsafe candidate shown", row)
		}
	}
	models[1].ContextSelectionStatus = ""
	history, err := FeedbackHistory(ctx, cfg.Telemetry.Database, winnerTask)
	if err != nil || len(history) == 0 {
		t.Fatal(history, err)
	}
	if err := WithdrawFeedback(ctx, cfg.Telemetry.Database, winnerTask, history[len(history)-1].ID); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().UTC() }
	for _, row := range s.browserRankings(ctx, models) {
		if row.Key == card {
			for _, rank := range row.Models {
				if rank.ModelID == "z" {
					t.Fatal("withdrawn-only evidence ranked as benchmark evidence", row)
				}
			}
		}
	}

}
