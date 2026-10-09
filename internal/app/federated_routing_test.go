package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

type federationFixture struct {
	candidate FederatedCandidate
	opens     int
	fail      bool
	request   providers.Request
}

func (f *federationFixture) Candidates(_ context.Context, r routing.Request) ([]FederatedCandidate, error) {
	c := f.candidate
	key := routing.Key{Model: c.Model.Model, Provider: c.Model.Provider, Domain: r.Domain, Profile: r.Profile}
	c.Observations = routing.ObservationSet{}
	for i := 0; i < 20; i++ {
		id := string(rune('a' + i))
		c.Observations.Fitness = append(c.Observations.Fitness, routing.FitnessObservation{ID: id, BaseID: id, Key: key, Quality: 1, Reliability: true, Time: time.Now().Add(-time.Minute)})
	}
	return []FederatedCandidate{c}, nil
}
func (f *federationFixture) Open(_ context.Context, _ FederatedCandidate, _ string, _ routing.Request) (providers.Provider, error) {
	f.opens++
	return f, nil
}
func (f *federationFixture) Models(context.Context) ([]string, error) {
	return []string{"remote-coder"}, nil
}
func (f *federationFixture) Stream(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	f.request = r
	if f.fail {
		return &providers.Failure{Code: "unavailable"}
	}
	if err := emit(providers.Chunk{Text: "remote answer"}); err != nil {
		return err
	}
	return emit(providers.Chunk{Done: true, FinishReason: "stop"})
}
func strongerFederation() *federationFixture {
	cost := 0.0
	return &federationFixture{candidate: FederatedCandidate{Instance: "spark", Hostname: "spark-host", DestinationModel: "coder", Model: config.Model{ID: "paired-coder", Provider: "paired-coder", Model: "remote-coder", Locality: "local", ContextTokens: 32768, Capabilities: []string{"chat", "code"}, EstimatedCost: &cost}, Candidate: routing.Candidate{Model: "remote-coder", Provider: "paired-coder", Local: true, ContextTokens: 32768, Capabilities: []string{"chat", "code"}, Healthy: true, PolicyAllowed: true, CapacityAvailable: true}}}
}
func TestUnifiedAutomaticStrongerRemoteAndManualPin(t *testing.T) {
	s, cfg := autoFixture(t)
	f := strongerFederation()
	s.ConfigureFederatedRouting(f)
	input := []providers.Message{{Role: "user", Content: "previous question"}, {Role: "assistant", Content: "previous answer"}, {Role: "user", Content: "current question"}}
	result, err := s.Run(context.Background(), Request{ModelID: "auto", Messages: input, Domain: "code", Profile: "default"})
	if err != nil || result.Text != "remote answer" || f.opens != 1 || len(f.request.Messages) != 3 {
		t.Fatal(result, err, f.opens, f.request)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), result.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	routed, turned := false, false
	for _, event := range events {
		if event.Kind == runtime.RouteSelected {
			routed = event.Data.ProviderID == "paired-coder" && event.Data.Route != nil && event.Data.Route.Primary.Provider == "paired-coder"
		}
		if event.Kind == runtime.TurnStarted {
			turned = routed
		}
	}
	if !routed || !turned {
		t.Fatal("remote execution lost durable route boundary", events)
	}
	pinned, err := s.Run(context.Background(), Request{ModelID: "a", Prompt: "manual"})
	if err != nil || pinned.Text != "a" || f.opens != 1 {
		t.Fatal("manual selection replaced", pinned, err, f.opens)
	}
}
func TestUnifiedRemoteFailureDoesNotFallback(t *testing.T) {
	s, _ := autoFixture(t)
	f := strongerFederation()
	f.fail = true
	s.ConfigureFederatedRouting(f)
	_, err := s.Run(context.Background(), Request{ModelID: "auto", Prompt: "question"})
	if err == nil || f.opens != 1 {
		t.Fatal("remote failure hidden or retried", err, f.opens)
	}
}
func TestUnifiedRemoteCannotBorrowLocalToolAuthority(t *testing.T) {
	s, _ := autoFixture(t)
	f := strongerFederation()
	s.ConfigureFederatedRouting(f)
	cfg := s.settings
	cfg.Tools.Enabled = true
	got, err := s.federatedCandidates(context.Background(), cfg, Request{}, providers.Request{}, 8192)
	if err != nil || len(got) != 0 || f.opens != 0 {
		t.Fatal(got, err)
	}
	cfg.Tools.Enabled = false
	got, err = s.federatedCandidates(context.Background(), cfg, Request{RemoteExecution: &runtime.RemoteExecution{Mode: "direct", Depth: 1}}, providers.Request{}, 8192)
	if err != nil || len(got) != 0 {
		t.Fatal("remote recursively routed", got, err)
	}
}
func TestUnifiedGridAndDispatchUseSameRemoteEvidence(t *testing.T) {
	s, _ := autoFixture(t)
	f := strongerFederation()
	s.ConfigureFederatedRouting(f)
	if _, err := s.Run(context.Background(), Request{ModelID: "a", Prompt: "seed"}); err != nil {
		t.Fatal(err)
	}
	cost := 0.0
	ram := uint64(100)
	tokens := int64(8192)
	models := []contract.ModelInspection{{ID: "a", Provider: "local", Model: "a", Locality: "local", Configured: true, Enabled: true, Usable: true, Health: "healthy", Capabilities: []string{"code"}, ContextTokens: &tokens, EstimatedCost: &cost, RAMBytes: &ram}}
	rows := s.browserRankingsUnified(context.Background(), &models, true)
	if len(rows) == 0 || rows[0].Key != "coding" || len(rows[0].Models) == 0 || rows[0].Models[0].ModelID != "paired-coder" {
		t.Fatal(rows)
	}
	found := false
	for _, m := range models {
		if m.ID == "paired-coder" {
			found = m.RemoteInstance == "spark" && m.Hostname == "spark-host" && m.Validate() == nil
		}
	}
	if !found {
		t.Fatal("remote provenance missing", models)
	}
}

type rankedFederationFixture struct {
	calls       []string
	failCount   int
	discoveries int
}

func (f *rankedFederationFixture) Candidates(_ context.Context, r routing.Request) ([]FederatedCandidate, error) {
	f.discoveries++
	out := []FederatedCandidate{}
	for i, id := range []string{"first", "second", "third", "fourth"} {
		c := strongerFederation().candidate
		c.DestinationModel = id
		c.Model.ID = "paired-" + id
		c.Model.Provider = c.Model.ID
		c.Model.Model = id
		c.Candidate.Model = id
		c.Candidate.Provider = c.Model.Provider
		c.Candidate.FailureDomain = "same-host"
		key := routing.Key{Model: id, Provider: c.Model.Provider, Domain: r.Domain, Profile: r.Profile}
		for n := 0; n < 20; n++ {
			obs := id + string(rune('a'+n))
			c.Observations.Fitness = append(c.Observations.Fitness, routing.FitnessObservation{ID: obs, BaseID: obs, Key: key, Quality: 1 - float64(i)*.1, Reliability: true, Time: time.Now().Add(-time.Minute)})
		}
		out = append(out, c)
	}
	return out, nil
}
func (f *rankedFederationFixture) Open(_ context.Context, c FederatedCandidate, _ string, _ routing.Request) (providers.Provider, error) {
	f.calls = append(f.calls, c.Model.Model)
	return rankedFederationProvider{f, c.Model.Model}, nil
}

type rankedFederationProvider struct {
	fixture *rankedFederationFixture
	id      string
}

func (p rankedFederationProvider) Models(context.Context) ([]string, error) {
	return []string{p.id}, nil
}
func (p rankedFederationProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	if len(p.fixture.calls) <= p.fixture.failCount {
		return &providers.Failure{Code: "unavailable", Retryable: true}
	}
	if err := emit(providers.Chunk{Text: p.id}); err != nil {
		return err
	}
	return emit(providers.Chunk{Done: true, FinishReason: "stop"})
}
func TestUnifiedConfirmedFailureUsesOriginalTopThree(t *testing.T) {
	for _, failures := range []int{1, 3} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.Runtime.FallbackMaxAttempts = 5
			f := &rankedFederationFixture{failCount: failures}
			s.ConfigureFederatedRouting(f)
			result, err := s.Run(context.Background(), Request{ModelID: "auto", Prompt: "question"})
			if failures == 1 {
				if err != nil || result.Text != "second" || fmt.Sprint(f.calls) != "[first second]" || len(result.PreviousTaskIDs) != 1 {
					t.Fatal(result, err, f.calls)
				}
			} else if !errors.Is(err, ErrRecoveryExhausted) || fmt.Sprint(f.calls) != "[first second third]" || len(result.PreviousTaskIDs) != 2 {
				t.Fatal(result, err, f.calls)
			}
			if f.discoveries != 1 {
				t.Fatal("fallback expanded or reranked original pool", f.discoveries)
			}
		})
	}
}

func TestTopThreeModelsDoNotCountAlternateHarnessTwice(t *testing.T) {
	remotes := map[string]FederatedCandidate{
		"first":             {Instance: "spark", DestinationModel: "coder", Model: config.Model{ID: "first"}},
		"alternate-harness": {Instance: "spark", DestinationModel: "coder", Model: config.Model{ID: "alternate-harness"}},
		"second":            {Instance: "spark", DestinationModel: "other", Model: config.Model{ID: "second"}},
		"third":             {Instance: "mini", DestinationModel: "third", Model: config.Model{ID: "third"}},
		"fourth":            {Instance: "mini", DestinationModel: "fourth", Model: config.Model{ID: "fourth"}},
	}
	selected := routing.Selection{Primary: routing.Ranked{Provider: "first", Model: "coder"}, Ranked: []routing.Ranked{{Provider: "first", Model: "coder"}, {Provider: "alternate-harness", Model: "coder"}, {Provider: "second", Model: "other"}, {Provider: "third", Model: "third"}, {Provider: "fourth", Model: "fourth"}}}
	targets := topModelTargets(selected, remotes, nil)
	if len(targets) != 2 || targets[0].ID != "second" || targets[1].ID != "third" {
		t.Fatal("top three counted registrations or included fourth model", targets)
	}
}
