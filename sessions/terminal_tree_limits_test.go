package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func treeLimitRenumber(history []runtime.Event) {
	for i := range history {
		history[i].Sequence = int64(i + 1)
		history[i].ID = fmt.Sprintf("%s-event-%d", history[i].TaskID, i)
	}
}

func TestTerminalTreeHistoryCountLimit(t *testing.T) {
	base := terminalTreeFixture(t)
	h := [][]runtime.Event{base[0]}
	for i := 0; i < 33; i++ {
		work := append([]runtime.Event(nil), base[1]...)
		child := append([]runtime.Event(nil), base[2]...)
		workID, childID := fmt.Sprintf("work%d", i), fmt.Sprintf("child%d", i)
		for j := range work {
			work[j].TaskID = workID
			work[j].CorrelationID = workID
		}
		for j := range child {
			child[j].TaskID = childID
			child[j].CorrelationID = childID
			child[j].SessionID = childID
		}
		child[0].Data.ParentTaskID = workID
		treeLimitRenumber(work)
		treeLimitRenumber(child)
		h = append(h, work, child)
	}
	if _, err := ProjectTerminalTree(h[:65]); err != nil {
		t.Fatal("valid 65-node tree", err)
	}
	out, err := ProjectTerminalTree(h)
	if !errors.Is(err, ErrHistory) || out.Result != nil {
		t.Fatal(out, err)
	}
}

func TestTerminalTreeAggregateEventLimit(t *testing.T) {
	h := terminalTreeFixture(t)
	count := 0
	for _, history := range h {
		count += len(history)
	}
	heartbeat := h[1][1]
	heartbeat.Kind = runtime.WorkerHeartbeat
	work := append([]runtime.Event(nil), h[1][:2]...)
	for i := 0; i < 10000-count; i++ {
		work = append(work, heartbeat)
	}
	work = append(work, h[1][2:]...)
	treeLimitRenumber(work)
	h[1] = work
	if _, err := ProjectTerminalTree(h); err != nil {
		t.Fatal("exact event limit", err)
	}
	work = append([]runtime.Event{work[0], work[1], heartbeat}, work[2:]...)
	treeLimitRenumber(work)
	h[1] = work
	if _, _, err := projectWorkerTerminal(work); err != nil {
		t.Fatal("individual worker must remain valid", err)
	}
	out, err := ProjectTerminalTree(h)
	if !errors.Is(err, ErrHistory) || out.Result != nil {
		t.Fatal(out, err)
	}
}

func TestTerminalTreeAggregateEncodedByteLimit(t *testing.T) {
	h := terminalTreeFixture(t)
	// Each inference history has valid partial-output events totaling less than
	// 8 MiB. Their combined canonical histories exceed the tree budget.
	for _, index := range []int{0, 2} {
		var expanded []runtime.Event
		for _, e := range h[index] {
			if e.Kind == runtime.TurnCompleted {
				delta := e
				delta.Kind = runtime.ModelDelta
				delta.Data = runtime.Data{Text: strings.Repeat("x", 48<<10)}
				for i := 0; i < 100; i++ {
					expanded = append(expanded, delta)
				}
			}
			expanded = append(expanded, e)
		}
		treeLimitRenumber(expanded)
		h[index] = expanded
		if _, err := ProjectTerminalSubmission(expanded); err != nil {
			t.Fatal("individual history", err)
		}
	}
	total := 0
	for _, history := range h {
		size := 0
		for _, e := range history {
			body, err := e.Encode()
			if err != nil {
				t.Fatal(err)
			}
			size += len(body)
		}
		if size >= 8<<20 {
			t.Fatal("individual overflow")
		}
		total += size
	}
	if total <= 8<<20 {
		t.Fatal("fixture too small", total)
	}
	out, err := ProjectTerminalTree(h)
	if !errors.Is(err, ErrHistory) || out.Result != nil {
		t.Fatal(out, err)
	}
}

func TestTerminalTreeInputAndResultOwnership(t *testing.T) {
	h := terminalTreeFixture(t)
	var first []runtime.Event
	for _, e := range terminalHistory(t, "failed") {
		if e.Kind != runtime.TaskStarted && e.Kind != runtime.TurnStarted && e.Kind != runtime.TaskFailed {
			continue
		}
		e.TaskID = "first"
		e.SessionID = "first"
		e.CorrelationID = "first"
		if e.Kind == runtime.TaskFailed {
			e.Data.Code = "provider_retryable_no_output"
		}
		first = append(first, e)
	}
	treeLimitRenumber(first)
	h[0][0].Data.RetryOfTaskID = "first"
	h = append([][]runtime.Event{first}, h...)
	before, _ := json.Marshal(h)
	out, err := ProjectTerminalTree(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Result.PreviousTaskIDs) != 1 {
		t.Fatal(out)
	}
	out.Result.PreviousTaskIDs[0] = "modified"
	if out.Result.Usage != nil {
		out.Result.Usage.InputTokens = 999
	}
	after, _ := json.Marshal(h)
	if string(before) != string(after) {
		t.Fatal("mutated input")
	}
	again, err := ProjectTerminalTree(h)
	if err != nil || again.Result.PreviousTaskIDs[0] != "first" {
		t.Fatal(again, err)
	}
}
