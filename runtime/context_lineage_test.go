package runtime_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestContextLineageOrderedExtensionAndSourceBinding(t *testing.T) {
	call := providers.ToolCall{ID: "retired-call", Name: "read", Arguments: []byte(`{}`)}
	messages := []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: "done"}}
	first := compactionFixture()
	lineage, err := runtime.ExtendContextLineage(nil, "task-b", 4, first, messages)
	if err != nil || lineage.Validate() != nil || !reflect.DeepEqual(lineage.ToolCallIDs, []string{"retired-call"}) {
		t.Fatal(lineage, err)
	}
	digest, err := runtime.ContextSourceStateDigest(messages, lineage)
	if err != nil || len(digest) != 64 {
		t.Fatal(digest, err)
	}
	second := *compactionFixture()
	second.Version, second.SourceTaskID, second.SourceStateDigest = 2, "ordinary-hop-c", digest
	second.SourceToolCallIDs = []string{"retired-call"}
	next, err := runtime.ExtendContextLineage(lineage, "task-d", 1, &second, messages)
	if err != nil || len(next.Epochs) != 2 || next.Epochs[1].Compaction.SourceTaskID != "ordinary-hop-c" {
		t.Fatal(next, err)
	}
	if next.Digest == lineage.Digest {
		t.Fatal("extension did not change identity")
	}
	second.SourceToolCallIDs[0] = "mutated"
	lineage.ToolCallIDs[0] = "mutated"
	if next.ToolCallIDs[0] != "retired-call" || next.Epochs[1].Compaction.SourceToolCallIDs[0] != "retired-call" {
		t.Fatal("lineage aliases caller-owned input")
	}
}

func TestContextLineageRejectsCorruption(t *testing.T) {
	lineage, err := runtime.ExtendContextLineage(nil, "task", 2, compactionFixture(), []providers.Message{{Role: "user", Content: "question"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*runtime.ContextLineage){
		"digest": func(l *runtime.ContextLineage) { l.Digest = strings.Repeat("0", 64) },
		"epoch order": func(l *runtime.ContextLineage) {
			l.Epochs = append(l.Epochs, l.Epochs[0])
		},
		"duplicate id": func(l *runtime.ContextLineage) { l.ToolCallIDs = []string{"x", "x"} },
		"unsorted id":  func(l *runtime.ContextLineage) { l.ToolCallIDs = []string{"z", "a"} },
	} {
		t.Run(name, func(t *testing.T) {
			copy := *lineage
			copy.Epochs = append([]runtime.ContextCompactionEpoch(nil), lineage.Epochs...)
			copy.ToolCallIDs = append([]string(nil), lineage.ToolCallIDs...)
			mutate(&copy)
			if copy.Validate() == nil {
				t.Fatal("corrupt lineage accepted")
			}
		})
	}
}

func TestContextSourceStateDigestBindsLineageTombstones(t *testing.T) {
	messages := []providers.Message{{Role: "user", Content: "same visible context"}}
	left, _ := runtime.ExtendContextLineage(nil, "task", 2, compactionFixture(), messages)
	right, _ := runtime.ExtendContextLineage(nil, "task", 2, compactionFixture(), []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "hidden", Name: "read", Arguments: []byte(`{}`)}}},
		{Role: "tool", ToolCallID: "hidden", Content: "done"},
	})
	a, _ := runtime.ContextSourceStateDigest(messages, left)
	b, _ := runtime.ContextSourceStateDigest(messages, right)
	if a == b {
		t.Fatal("retired identity omitted from source state")
	}
}

func TestContextLineageToolIdentityUnionProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(73))
	for iteration := 0; iteration < 100; iteration++ {
		count := 1 + rng.Intn(40)
		calls := make([]providers.ToolCall, count)
		messages := make([]providers.Message, 0, count*2)
		for i := range calls {
			calls[i] = providers.ToolCall{ID: "call-" + strings.Repeat("x", i+1), Name: "read", Arguments: []byte(`{}`)}
		}
		rng.Shuffle(len(calls), func(i, j int) { calls[i], calls[j] = calls[j], calls[i] })
		for _, call := range calls {
			messages = append(messages, providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, providers.Message{Role: "tool", ToolCallID: call.ID, Content: "done"})
		}
		lineage, err := runtime.ExtendContextLineage(nil, "task", 2, compactionFixture(), messages)
		if err != nil || len(lineage.ToolCallIDs) != count {
			t.Fatalf("iteration %d: ids=%v err=%v", iteration, lineage, err)
		}
		for i := 1; i < len(lineage.ToolCallIDs); i++ {
			if lineage.ToolCallIDs[i-1] >= lineage.ToolCallIDs[i] {
				t.Fatalf("iteration %d: noncanonical identity set", iteration)
			}
		}
	}
}

func TestContextLineageGeneratedMultiEpochProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(123))
	for iteration := 0; iteration < 100; iteration++ {
		count := 2 + rng.Intn(9)
		first := compactionFixture()
		lineage, err := runtime.ExtendContextLineage(nil, "task-0", 1, first, []providers.Message{{Role: "user", Content: "initial"}})
		if err != nil {
			t.Fatal(iteration, err)
		}
		for epoch := 1; epoch < count; epoch++ {
			checkpoint := *compactionFixture()
			checkpoint.Version = 2
			checkpoint.SourceTaskID = "ordinary-source-" + strings.Repeat("x", epoch)
			checkpoint.SourceStateDigest = strings.Repeat("a", 64)
			checkpoint.SourceToolCallIDs = append([]string(nil), lineage.ToolCallIDs...)
			call := providers.ToolCall{ID: "epoch-call-" + strings.Repeat("x", epoch), Name: "read", Arguments: []byte(`{}`)}
			messages := []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: "done"}}
			lineage, err = runtime.ExtendContextLineage(lineage, "task-"+strings.Repeat("x", epoch), 1, &checkpoint, messages)
			if err != nil || lineage.Validate() != nil || len(lineage.Epochs) != epoch+1 {
				t.Fatalf("iteration=%d epoch=%d lineage=%v err=%v", iteration, epoch, lineage, err)
			}
		}
		if len(lineage.Epochs) != count {
			t.Fatal("generated lineage truncated")
		}
	}
}
