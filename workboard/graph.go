package workboard

type Node struct {
	ID           string
	BoardID      string
	ParentID     string
	Dependencies []string
}

type Graph struct {
	BoardID        string
	GraphRevision  int64
	LayoutRevision int64
	Nodes          []Node
}

type GraphLimits struct {
	Cards         int
	Dependencies  int
	ReverseFanout int
	Depth         int
	Visits        int
}

func DefaultGraphLimits() GraphLimits {
	return GraphLimits{
		Cards: MaxCardsPerBoard, Dependencies: MaxDependencies,
		ReverseFanout: MaxReverseFanout, Depth: MaxGraphDepth, Visits: MaxGraphVisits,
	}
}

type GraphResult struct {
	GraphRevision   int64
	LayoutRevision  int64
	Nodes           int
	DependencyEdges int
	ParentEdges     int
	Visits          int
	Depth           int
}

// ValidateGraph validates a complete board graph at an explicit graph CAS
// revision. LayoutRevision is carried through but is never used as a graph
// fence.
func ValidateGraph(graph Graph, expectedGraphRevision int64) (GraphResult, error) {
	return ValidateGraphWithLimits(graph, expectedGraphRevision, DefaultGraphLimits())
}

func ValidateGraphWithLimits(graph Graph, expected int64, limits GraphLimits) (GraphResult, error) {
	result := GraphResult{GraphRevision: graph.GraphRevision, LayoutRevision: graph.LayoutRevision, Nodes: len(graph.Nodes)}
	if !validID(graph.BoardID) || graph.GraphRevision < 1 || graph.LayoutRevision < 1 || invalidLimits(limits) {
		return GraphResult{}, fail(CodeInvalid, "graph")
	}
	if expected != graph.GraphRevision {
		return GraphResult{}, fail(CodeStaleRevision, "graph_revision")
	}
	if len(graph.Nodes) > limits.Cards {
		return GraphResult{}, fail(CodeLimitExceeded, "cards")
	}
	byID := make(map[string]Node, len(graph.Nodes))
	reverse := make(map[string]int, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if !validID(node.ID) || !validID(node.BoardID) {
			return GraphResult{}, fail(CodeInvalid, "node")
		}
		if node.BoardID != graph.BoardID {
			return GraphResult{}, fail(CodeCrossBoard, "node")
		}
		if _, exists := byID[node.ID]; exists {
			return GraphResult{}, fail(CodeInvalid, "duplicate_node")
		}
		if len(node.Dependencies) > limits.Dependencies {
			return GraphResult{}, fail(CodeLimitExceeded, "dependencies")
		}
		byID[node.ID] = node
	}
	for _, node := range graph.Nodes {
		if node.ParentID != "" {
			if node.ParentID == node.ID {
				return GraphResult{}, fail(CodeCycle, "parent")
			}
			parent, ok := byID[node.ParentID]
			if !ok {
				return GraphResult{}, fail(CodeMissingNode, "parent")
			}
			if parent.BoardID != node.BoardID {
				return GraphResult{}, fail(CodeCrossBoard, "parent")
			}
			result.ParentEdges++
		}
		seen := make(map[string]struct{}, len(node.Dependencies))
		for _, dependencyID := range node.Dependencies {
			if dependencyID == node.ID {
				return GraphResult{}, fail(CodeCycle, "dependency")
			}
			if _, duplicate := seen[dependencyID]; duplicate {
				return GraphResult{}, fail(CodeInvalid, "duplicate_dependency")
			}
			seen[dependencyID] = struct{}{}
			dependency, ok := byID[dependencyID]
			if !ok {
				return GraphResult{}, fail(CodeMissingNode, "dependency")
			}
			if dependency.BoardID != node.BoardID {
				return GraphResult{}, fail(CodeCrossBoard, "dependency")
			}
			reverse[dependencyID]++
			if reverse[dependencyID] > limits.ReverseFanout {
				return GraphResult{}, fail(CodeLimitExceeded, "reverse_fanout")
			}
			result.DependencyEdges++
		}
	}
	visits := 0
	for _, parents := range []bool{true, false} {
		if err := walkGraph(graph.Nodes, byID, parents, limits, &visits, &result); err != nil {
			return GraphResult{}, err
		}
	}
	result.Visits = visits
	return result, nil
}

func invalidLimits(l GraphLimits) bool {
	return l.Cards < 1 || l.Cards > MaxCardsPerBoard || l.Dependencies < 1 || l.Dependencies > MaxDependencies ||
		l.ReverseFanout < 1 || l.ReverseFanout > MaxReverseFanout || l.Depth < 1 || l.Depth > MaxGraphDepth ||
		l.Visits < 1 || l.Visits > MaxGraphVisits
}

func walkGraph(nodes []Node, byID map[string]Node, parents bool, limits GraphLimits, visits *int, result *GraphResult) error {
	state := make(map[string]uint8, len(nodes))
	depths := make(map[string]int, len(nodes))
	var visit func(string) (int, error)
	visit = func(id string) (int, error) {
		if state[id] == 1 {
			return 0, fail(CodeCycle, "graph")
		}
		if state[id] == 2 {
			return depths[id], nil
		}
		(*visits)++
		if *visits > limits.Visits {
			return 0, fail(CodeVisitsExhausted, "graph")
		}
		state[id] = 1
		node := byID[id]
		maxChildDepth := 0
		if parents && node.ParentID != "" {
			childDepth, err := visit(node.ParentID)
			if err != nil {
				return 0, err
			}
			maxChildDepth = childDepth
		}
		if !parents {
			for _, dependency := range node.Dependencies {
				childDepth, err := visit(dependency)
				if err != nil {
					return 0, err
				}
				if childDepth > maxChildDepth {
					maxChildDepth = childDepth
				}
			}
		}
		depth := maxChildDepth + 1
		if depth > limits.Depth {
			return 0, fail(CodeDepthExhausted, "graph")
		}
		state[id], depths[id] = 2, depth
		if depth > result.Depth {
			result.Depth = depth
		}
		return depth, nil
	}
	for _, node := range nodes {
		if _, err := visit(node.ID); err != nil {
			return err
		}
	}
	return nil
}
