package workboard

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

func graph(nodes ...Node) Graph {
	return Graph{BoardID: "board-a", GraphRevision: 4, LayoutRevision: 91, Nodes: nodes}
}

func node(id string, dependencies ...string) Node {
	return Node{ID: id, BoardID: "board-a", Dependencies: dependencies}
}

func TestGraphValidationAndIndependentRevisionFence(t *testing.T) {
	g := graph(node("a"), node("b", "a"), Node{ID: "c", BoardID: "board-a", ParentID: "b"})
	result, err := ValidateGraph(g, 4)
	if err != nil || result.GraphRevision != 4 || result.LayoutRevision != 91 || result.DependencyEdges != 1 || result.ParentEdges != 1 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if _, err = ValidateGraph(g, g.LayoutRevision); !errors.Is(err, &Violation{Code: CodeStaleRevision}) {
		t.Fatalf("layout revision incorrectly acted as graph fence: %v", err)
	}
}

func TestGraphRejectsInvalidReferencesAndCycles(t *testing.T) {
	tests := []struct {
		name string
		g    Graph
		code ErrorCode
	}{
		{"missing dependency", graph(node("a", "missing")), CodeMissingNode},
		{"missing parent", graph(Node{ID: "a", BoardID: "board-a", ParentID: "missing"}), CodeMissingNode},
		{"cross board", graph(Node{ID: "a", BoardID: "board-b"}), CodeCrossBoard},
		{"dependency self", graph(node("a", "a")), CodeCycle},
		{"dependency cycle", graph(node("a", "b"), node("b", "a")), CodeCycle},
		{"parent cycle", graph(Node{ID: "a", BoardID: "board-a", ParentID: "b"}, Node{ID: "b", BoardID: "board-a", ParentID: "a"}), CodeCycle},
		{"duplicate dependency", graph(node("a"), node("b", "a", "a")), CodeInvalid},
		{"duplicate node", graph(node("a"), node("a")), CodeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateGraph(test.g, 4)
			if !errors.Is(err, &Violation{Code: test.code}) {
				t.Fatalf("error=%v, want %s", err, test.code)
			}
		})
	}
}

func TestGraphBoundsFailClosed(t *testing.T) {
	base := GraphLimits{Cards: 8, Dependencies: 4, ReverseFanout: 2, Depth: 8, Visits: 8}
	tests := []struct {
		name   string
		g      Graph
		limits GraphLimits
		code   ErrorCode
	}{
		{"reverse fanout", graph(node("a"), node("b", "a"), node("c", "a"), node("d", "a")), base, CodeLimitExceeded},
		{"depth", graph(node("a"), node("b", "a"), node("c", "b")), GraphLimits{Cards: 3, Dependencies: 3, ReverseFanout: 3, Depth: 2, Visits: 6}, CodeDepthExhausted},
		{"visits", graph(node("a"), node("b"), node("c")), GraphLimits{Cards: 3, Dependencies: 3, ReverseFanout: 3, Depth: 3, Visits: 2}, CodeVisitsExhausted},
		{"combined parent and dependency visits", graph(node("a"), node("b")), GraphLimits{Cards: 2, Dependencies: 2, ReverseFanout: 2, Depth: 2, Visits: 3}, CodeVisitsExhausted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateGraphWithLimits(test.g, 4, test.limits)
			if !errors.Is(err, &Violation{Code: test.code}) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGraphReportsCombinedVisitBudget(t *testing.T) {
	g := graph(node("a"), node("b", "a"))
	result, err := ValidateGraphWithLimits(g, 4, GraphLimits{Cards: 2, Dependencies: 2, ReverseFanout: 2, Depth: 2, Visits: 4})
	if err != nil || result.Visits != 4 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestRandomForwardGraphsRemainAcyclic(t *testing.T) {
	rng := rand.New(rand.NewSource(81))
	for iteration := 0; iteration < 100; iteration++ {
		nodes := make([]Node, 64)
		for i := range nodes {
			nodes[i] = node(fmt.Sprintf("node-%d", i))
			if i > 0 {
				for count := rng.Intn(4); count > 0; count-- {
					candidate := fmt.Sprintf("node-%d", rng.Intn(i))
					duplicate := false
					for _, existing := range nodes[i].Dependencies {
						duplicate = duplicate || existing == candidate
					}
					if !duplicate {
						nodes[i].Dependencies = append(nodes[i].Dependencies, candidate)
					}
				}
			}
		}
		if _, err := ValidateGraph(graph(nodes...), 4); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
	}
}

func TestGraphValidationIsConcurrencySafe(t *testing.T) {
	g := graph(node("a"), node("b", "a"), node("c", "b"))
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := ValidateGraph(g, 4); errorsSeen <- err }()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
}
