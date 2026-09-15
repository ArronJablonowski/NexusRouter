package workboard

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"
)

func decompositionPolicy(t *testing.T, depth, children int, configByte string) DecompositionPolicy {
	t.Helper()
	policy, err := NewDecompositionPolicy(DecompositionLimits{Version: 1, MaxDepth: depth, MaxChildren: children}, strings.Repeat(configByte, 64))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func decompositionGraph(nodes ...Node) Graph {
	return Graph{BoardID: "board-a", GraphRevision: 3, LayoutRevision: 1, Nodes: nodes}
}

func TestDecompositionAdmissionDepthAndDirectChildBoundaries(t *testing.T) {
	policy := decompositionPolicy(t, MaxGraphDepth, MaxChildrenPerParent, "a")
	chain := make([]Node, MaxGraphDepth-1)
	for i := range chain {
		chain[i] = Node{ID: "node-" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), BoardID: "board-a", Dependencies: []string{}}
		if i > 0 {
			chain[i].ParentID = chain[i-1].ID
		}
	}
	decision, err := AdmitDecomposition(decompositionGraph(chain...), 3, "", chain[len(chain)-1].ID, policy)
	if err != nil || decision.Depth != MaxGraphDepth {
		t.Fatalf("boundary decision=%+v error=%v", decision, err)
	}
	tooShallow := decompositionPolicy(t, MaxGraphDepth-1, MaxChildrenPerParent, "a")
	if _, err = AdmitDecomposition(decompositionGraph(chain...), 3, "", chain[len(chain)-1].ID, tooShallow); !errors.Is(err, &Violation{Code: CodeDepthExhausted}) {
		t.Fatalf("depth error=%v", err)
	}

	nodes := []Node{{ID: "parent", BoardID: "board-a", Dependencies: []string{}}}
	for i := 0; i < MaxChildrenPerParent-1; i++ {
		nodes = append(nodes, Node{ID: "child-" + strings.Repeat("x", i/10) + string(rune('a'+i%10)), BoardID: "board-a", ParentID: "parent", Dependencies: []string{}})
	}
	decision, err = AdmitDecomposition(decompositionGraph(nodes...), 3, "", "parent", policy)
	if err != nil || decision.DirectChildren != MaxChildrenPerParent {
		t.Fatalf("fanout boundary=%+v error=%v", decision, err)
	}
	nodes = append(nodes, Node{ID: "last-child", BoardID: "board-a", ParentID: "parent", Dependencies: []string{}})
	if _, err = AdmitDecomposition(decompositionGraph(nodes...), 3, "", "parent", policy); !errors.Is(err, &Violation{Code: CodeLimitExceeded}) {
		t.Fatalf("fanout error=%v", err)
	}
}

func TestDecompositionIgnoresDependencyDepthAndExcludesMovedCard(t *testing.T) {
	graph := decompositionGraph(
		Node{ID: "dependency", BoardID: "board-a", Dependencies: []string{}},
		Node{ID: "parent", BoardID: "board-a", Dependencies: []string{"dependency"}},
		Node{ID: "child", BoardID: "board-a", ParentID: "parent", Dependencies: []string{}},
	)
	rootOnly := decompositionPolicy(t, 1, 1, "b")
	if _, err := AdmitDecomposition(graph, 3, "", "", rootOnly); err != nil {
		t.Fatalf("dependency depth affected root admission: %v", err)
	}
	twoLevels := decompositionPolicy(t, 2, 1, "b")
	decision, err := AdmitDecomposition(graph, 3, "child", "parent", twoLevels)
	if err != nil || decision.DirectChildren != 1 {
		t.Fatalf("same-parent move double counted: %+v error=%v", decision, err)
	}
}

func TestDecompositionReparentIncludesDescendantDepth(t *testing.T) {
	graph := decompositionGraph(
		Node{ID: "new-parent", BoardID: "board-a", Dependencies: []string{}},
		Node{ID: "moved", BoardID: "board-a", Dependencies: []string{}},
		Node{ID: "descendant", BoardID: "board-a", ParentID: "moved", Dependencies: []string{}},
	)
	policy := decompositionPolicy(t, 2, 4, "1")
	if _, err := AdmitDecomposition(graph, 3, "moved", "new-parent", policy); !errors.Is(err, &Violation{Code: CodeDepthExhausted}) {
		t.Fatalf("reparented subtree exceeded depth without rejection: %v", err)
	}
	policy = decompositionPolicy(t, 3, 4, "1")
	decision, err := AdmitDecomposition(graph, 3, "moved", "new-parent", policy)
	if err != nil || decision.Depth != 2 {
		t.Fatalf("valid reparent decision=%+v error=%v", decision, err)
	}
}

func TestDecompositionPolicyDigestAndRestriction(t *testing.T) {
	base := decompositionPolicy(t, 8, 6, "c")
	equal, err := base.Restrict(base.Limits)
	if err != nil || equal != base {
		t.Fatalf("equal restriction=%+v error=%v", equal, err)
	}
	tight, err := base.Restrict(DecompositionLimits{Version: 1, MaxDepth: 4, MaxChildren: 3})
	if err != nil || tight.ConfigDigest != base.ConfigDigest || tight.PolicyDigest == base.PolicyDigest {
		t.Fatalf("tight restriction=%+v error=%v", tight, err)
	}
	for _, limits := range []DecompositionLimits{
		{Version: 1, MaxDepth: 9, MaxChildren: 6},
		{Version: 1, MaxDepth: 8, MaxChildren: 7},
	} {
		if _, err = base.Restrict(limits); !errors.Is(err, &Violation{Code: CodeInvalid}) {
			t.Fatalf("loosening accepted: %+v", limits)
		}
	}
	changedConfig := decompositionPolicy(t, 8, 6, "d")
	if changedConfig.PolicyDigest == base.PolicyDigest {
		t.Fatal("policy digest did not bind config digest")
	}
}

func TestDecompositionPolicyInheritanceIsEqualOrStricter(t *testing.T) {
	parentPolicy := decompositionPolicy(t, 3, 2, "2")
	decision, err := AdmitDecomposition(decompositionGraph(), 3, "", "", parentPolicy)
	if err != nil {
		t.Fatal(err)
	}
	parent := DecompositionAdmission{Version: 1, AdmissionID: "parent-admission", OperationID: "parent-operation-1", RequestDigest: strings.Repeat("3", 64),
		DecisionDigest: decision.Digest, BoardID: "board-a", CardID: "parent", Actor: Actor{ID: "model-a", Type: "model"}, Limits: parentPolicy.Limits,
		ConfigDigest: parentPolicy.ConfigDigest, PolicyDigest: parentPolicy.PolicyDigest, Depth: 1, DirectChildren: 0,
		AdmittedAt: time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)}
	parent.AdmissionDigest, _ = parent.CanonicalDigest()
	host := decompositionPolicy(t, 8, 8, "4")
	inherited, err := host.Inherit(parent, nil)
	if err != nil || inherited.Limits.MaxDepth != 3 || inherited.Limits.MaxChildren != 2 || inherited.ConfigDigest != host.ConfigDigest {
		t.Fatalf("inherited=%+v error=%v", inherited, err)
	}
	tighter := DecompositionLimits{Version: 1, MaxDepth: 2, MaxChildren: 1}
	inherited, err = host.Inherit(parent, &tighter)
	if err != nil || inherited.Limits != tighter {
		t.Fatalf("tighter=%+v error=%v", inherited, err)
	}
	looser := DecompositionLimits{Version: 1, MaxDepth: 4, MaxChildren: 2}
	if _, err = host.Inherit(parent, &looser); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("looser child inheritance accepted: %v", err)
	}
}

func TestDecompositionAdmissionRecordAndBoardEventReplayShape(t *testing.T) {
	policy := decompositionPolicy(t, 4, 3, "e")
	decision, err := AdmitDecomposition(decompositionGraph(), 3, "", "", policy)
	if err != nil {
		t.Fatal(err)
	}
	admission := DecompositionAdmission{Version: 1, AdmissionID: "admission-1", OperationID: "operation-key-0001", RequestDigest: strings.Repeat("1", 64),
		DecisionDigest: decision.Digest, BoardID: "board-a", CardID: "card-a", Actor: Actor{ID: "model-a", Type: "model"}, Limits: policy.Limits,
		ConfigDigest: policy.ConfigDigest, PolicyDigest: policy.PolicyDigest, Depth: decision.Depth, DirectChildren: decision.DirectChildren,
		AdmittedAt: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	admission.AdmissionDigest, err = admission.CanonicalDigest()
	if err != nil || admission.Validate() != nil {
		t.Fatalf("admission=%+v error=%v validation=%v", admission, err, admission.Validate())
	}
	body, _ := json.Marshal(admission)
	var replayed DecompositionAdmission
	if json.Unmarshal(body, &replayed) != nil || !reflect.DeepEqual(replayed, admission) || replayed.Validate() != nil {
		t.Fatalf("replayed admission=%+v", replayed)
	}
	event := BoardEvent{Version: SchemaVersion, ID: "event-a", BoardID: "board-a", Sequence: 1, OperationID: admission.OperationID,
		Kind: CardCreateAction, ActorID: "model-a", ActorType: "model", CardID: "card-a", CreatedAt: admission.AdmittedAt}
	if err = event.BindDecompositionAdmission(admission); err != nil || event.Validate() != nil || !event.HasDecompositionAdmission() {
		t.Fatalf("event=%+v bind=%v validation=%v", event, err, event.Validate())
	}
	event.DecompositionAdmissionDigest = ""
	if event.Validate() == nil {
		t.Fatal("partial admission reference accepted")
	}
}

func TestConfiguredCardServiceBindsOnlyModelHierarchyMutations(t *testing.T) {
	policy := decompositionPolicy(t, 4, 3, "f")
	newService := func(actor Actor, store *fakeCardStore) *CardService {
		service, err := NewCardServiceWithDecomposition(store, boardAuthorityStub{authority: Authority{CreationScope: "session", Actor: actor}}, policy)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	intent := NewCard{Title: "Child", Priority: "normal", Labels: []string{}, Dependencies: []string{}, Budget: WorkBudget{AttemptLimit: 1}, Criteria: []AcceptanceCriterion{serviceCriterion()}}
	created := serviceCard("created", Backlog)
	for _, tc := range []struct {
		actor Actor
		bound bool
	}{{Actor{ID: "model-a", Type: "model"}, true}, {Actor{ID: "operator-a", Type: "operator"}, false}} {
		store := &fakeCardStore{graph: decompositionGraph(), applyResult: serviceMutationResult(created)}
		_, err := newService(tc.actor, store).CreateCard(context.Background(), CreateCardRequest{BoardID: "board-a", Card: intent,
			IdempotencyKey: serviceKey, ExpectedBoardRevision: 1, ExpectedGraphRevision: 3})
		if err != nil || len(store.mutations) != 1 || (store.mutations[0].Decomposition != nil) != tc.bound {
			t.Fatalf("actor=%+v mutation=%+v error=%v", tc.actor, store.mutations, err)
		}
	}
	legacy := &fakeCardStore{graph: decompositionGraph(), applyResult: serviceMutationResult(created)}
	if _, err := serviceFor(t, legacy).CreateCard(context.Background(), CreateCardRequest{BoardID: "board-a", Card: intent,
		IdempotencyKey: serviceKey, ExpectedBoardRevision: 1, ExpectedGraphRevision: 3}); err != nil || legacy.mutations[0].Decomposition != nil {
		t.Fatalf("legacy operator create=%+v error=%v", legacy.mutations, err)
	}
}

type decompositionReplayStore struct {
	fakeCardStore
	replayed CardMutation
}

func (s *decompositionReplayStore) ReplayCardMutation(_ context.Context, mutation CardMutation) (CardMutationResult, bool, error) {
	s.replayed = mutation
	return CardMutationResult{}, false, nil
}

func TestConfiguredCardServiceReplayBindsOriginalPolicyDigest(t *testing.T) {
	policy := decompositionPolicy(t, 4, 3, "6")
	created := serviceCard("created", Backlog)
	store := &decompositionReplayStore{fakeCardStore: fakeCardStore{applyResult: serviceMutationResult(created)}}
	service, err := NewCardServiceWithDecomposition(store, boardAuthorityStub{authority: Authority{CreationScope: "session", Actor: Actor{ID: "model-a", Type: "model"}}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	restriction := DecompositionLimits{Version: 1, MaxDepth: 3, MaxChildren: 2}
	request := CreateCardRequest{BoardID: "board-a", Card: NewCard{Title: "Child", Priority: "normal", Labels: []string{}, Dependencies: []string{},
		Budget: WorkBudget{AttemptLimit: 1}, Criteria: []AcceptanceCriterion{serviceCriterion()}}, Decomposition: &restriction,
		IdempotencyKey: serviceKey, ExpectedBoardRevision: 1, ExpectedGraphRevision: 3}
	if _, err = service.CreateCard(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(store.mutations) != 1 || store.replayed.RequestDigest == "" || store.mutations[0].RequestDigest != store.replayed.RequestDigest ||
		store.mutations[0].Decomposition == nil || store.mutations[0].Decomposition.Limits != restriction {
		t.Fatalf("replay=%+v applied=%+v", store.replayed, store.mutations)
	}
	drifted := store.replayed
	drifted.Decomposition = copyDecompositionPolicy(drifted.Decomposition)
	drifted.Decomposition.ConfigDigest = strings.Repeat("7", 64)
	drifted.Decomposition.PolicyDigest, _ = decompositionPolicyDigest(*drifted.Decomposition)
	driftDigest, _ := mutationDigest(drifted)
	if driftDigest == store.replayed.RequestDigest {
		t.Fatal("configuration drift did not require a fresh operation digest")
	}
}

func TestDecompositionBoundaryProperty(t *testing.T) {
	property := func(rawDepth, rawChildren uint8) bool {
		depth := int(rawDepth%15) + 2
		children := int(rawChildren%15) + 1
		nodes := make([]Node, depth-1, depth-1+children)
		for i := range nodes {
			nodes[i] = Node{ID: "depth-" + string(rune('a'+i)), BoardID: "board-a", Dependencies: []string{}}
			if i > 0 {
				nodes[i].ParentID = nodes[i-1].ID
			}
		}
		accept := decompositionPolicy(t, depth, MaxChildrenPerParent, "9")
		if _, err := AdmitDecomposition(decompositionGraph(nodes...), 3, "", nodes[len(nodes)-1].ID, accept); err != nil {
			return false
		}
		reject := decompositionPolicy(t, depth-1, MaxChildrenPerParent, "9")
		if _, err := AdmitDecomposition(decompositionGraph(nodes...), 3, "", nodes[len(nodes)-1].ID, reject); !errors.Is(err, &Violation{Code: CodeDepthExhausted}) {
			return false
		}
		fanout := []Node{{ID: "parent", BoardID: "board-a", Dependencies: []string{}}}
		for i := 0; i < children; i++ {
			fanout = append(fanout, Node{ID: "fan-" + string(rune('a'+i)), BoardID: "board-a", ParentID: "parent", Dependencies: []string{}})
		}
		fanPolicy := decompositionPolicy(t, MaxGraphDepth, children, "8")
		_, err := AdmitDecomposition(decompositionGraph(fanout...), 3, "", "parent", fanPolicy)
		return errors.Is(err, &Violation{Code: CodeLimitExceeded})
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}
