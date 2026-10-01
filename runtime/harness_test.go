package runtime_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func nativeRequest(calls *atomic.Int32) runtime.HarnessRequest {
	id := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "provider", Model: "model", ModelRevision: "weights", ConfigSHA256: strings.Repeat("a", 64)}
	return runtime.HarnessRequest{TaskID: "native-task", SessionID: "native-session", Attribution: runtime.HarnessAttribution{Identity: id, Task: harness.TaskClass{Domain: "writing", Profile: "rubric-v1", Difficulty: "easy"}}, ContextTokens: 8192, MaxOutputBytes: 4096, Execute: func(context.Context) (runtime.HarnessOutput, error) {
		calls.Add(1)
		return runtime.HarnessOutput{Actual: id, Text: "secret answer"}, nil
	}}
}
func TestHarnessDurableOwnershipAndCompletion(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()
	var calls atomic.Int32
	r := nativeRequest(&calls)
	r.OutputView = func(s string) string { return strings.ReplaceAll(s, "secret", "[REDACTED]") }
	outcome, text, err := runtime.RunHarness(ctx, s, r)
	if err != nil || text != "[REDACTED] answer" || outcome.Status != "completed" {
		t.Fatal(outcome, text, err)
	}
	events, err := s.Read(ctx, r.TaskID, 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	if events[0].Data.Harness == nil || events[1].Data.HarnessOutcome == nil || *events[1].Data.HarnessOutcome != outcome || events[1].Data.Text != text {
		t.Fatal("lost committed provenance")
	}
	for _, e := range events {
		if e.Kind == runtime.EvaluationRecorded || e.Data.Accepted != nil {
			t.Fatal("success fabricated evaluation")
		}
	}
	if _, _, err := runtime.RunHarness(ctx, s, r); !errors.Is(err, runtime.ErrPersistence) || calls.Load() != 1 {
		t.Fatal("duplicate task executed", calls.Load(), err)
	}
	clone, err := events[0].Clone()
	if err != nil {
		t.Fatal(err)
	}
	clone.Data.Harness.Identity.Model = "changed"
	if events[0].Data.Harness.Identity.Model != "model" {
		t.Fatal("clone shares attribution")
	}
	corrupt := events[1]
	corrupt.Data.Text = "changed"
	if corrupt.Validate() == nil {
		t.Fatal("changed output retained binding")
	}
}
func TestHarnessConcurrentOwnership(t *testing.T) {
	s, _ := store(t)
	var calls atomic.Int32
	r := nativeRequest(&calls)
	var wg sync.WaitGroup
	var succeeded atomic.Int32
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := runtime.RunHarness(context.Background(), s, r); err == nil {
				succeeded.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || succeeded.Load() != 1 {
		t.Fatal("duplicate native dispatch", calls.Load(), succeeded.Load())
	}
}
func TestHarnessFailureAndCancellationHaveNoAcceptedOutput(t *testing.T) {
	for _, mode := range []string{"failure", "panic", "wrong_model", "cancel", "cancel_view"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := store(t)
			var calls atomic.Int32
			r := nativeRequest(&calls)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel_view" {
				r.OutputView = func(text string) string { cancel(); return text }
			}
			original := r.Execute
			r.Execute = func(c context.Context) (runtime.HarnessOutput, error) {
				out, _ := original(c)
				switch mode {
				case "failure":
					return out, errors.New("private error")
				case "panic":
					panic("private panic")
				case "wrong_model":
					out.Actual.Model = "wrong"
				case "cancel":
					cancel()
				}
				return out, nil
			}
			outcome, text, err := runtime.RunHarness(ctx, s, r)
			if err == nil || text != "" || outcome.OutputSHA256 != "" {
				t.Fatal("failed execution returned accepted output", outcome, text, err)
			}
			events, e := s.Read(context.Background(), r.TaskID, 0, 10)
			if e != nil || len(events) != 2 {
				t.Fatal(events, e)
			}
			if events[1].Data.Text != "" || events[1].Data.Accepted != nil || events[1].Kind == runtime.TaskCompleted {
				t.Fatal("failure accepted")
			}
		})
	}
}
func TestHarnessJournalFailureNeverReturnsCompletion(t *testing.T) {
	for _, failAt := range []int64{0, 1} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			s, _ := store(t)
			var calls atomic.Int32
			r := nativeRequest(&calls)
			j := journal(func(c context.Context, seq int64, e runtime.Event) error {
				if seq == failAt {
					return errors.New("disk failure")
				}
				return s.Append(c, seq, e)
			})
			outcome, text, err := runtime.RunHarness(context.Background(), j, r)
			if !errors.Is(err, runtime.ErrPersistence) || text != "" || outcome.ID != "" || calls.Load() != int32(failAt) {
				t.Fatal("ambiguous durability returned result", outcome, text, err, calls.Load())
			}
		})
	}
}

type harnessReader func(context.Context, string, int64, int) ([]runtime.Event, error)

func (r harnessReader) Read(c context.Context, id string, after int64, limit int) ([]runtime.Event, error) {
	return r(c, id, after, limit)
}
func TestHarnessReconciliationRejectsIncompleteOrChangedJournal(t *testing.T) {
	s, _ := store(t)
	var calls atomic.Int32
	r := nativeRequest(&calls)
	ctx := context.Background()
	execution, _, err := runtime.RunHarness(ctx, s, r)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.Read(ctx, r.TaskID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	now := time.Now().UTC()
	for range 2 {
		recorded, e := runtime.RecordHarnessOutcome(ctx, s, ledger, r.TaskID, now)
		if e != nil || recorded != execution {
			t.Fatal(recorded, e)
		}
	}
	for _, mode := range []string{"partial", "identity", "class", "sequence", "session", "output", "failed"} {
		t.Run(mode, func(t *testing.T) {
			first, e := events[0].Clone()
			if e != nil {
				t.Fatal(e)
			}
			last, e := events[1].Clone()
			if e != nil {
				t.Fatal(e)
			}
			input := []runtime.Event{first, last}
			switch mode {
			case "partial":
				input = input[:1]
			case "identity":
				input[1].Data.HarnessOutcome.Actual.HarnessVersion = "different"
			case "class":
				input[1].Data.HarnessOutcome.Task.Difficulty = "hard"
			case "sequence":
				input[1].Sequence = 3
			case "session":
				input[1].SessionID = "other"
			case "output":
				input[1].Data.Text = "altered"
			case "failed":
				input[1].Kind = runtime.TaskFailed
				input[1].Data.HarnessOutcome = nil
				input[1].Data.Text = ""
			}
			reader := harnessReader(func(context.Context, string, int64, int) ([]runtime.Event, error) { return input, nil })
			if _, e := runtime.RecordHarnessOutcome(ctx, reader, ledger, r.TaskID, now); e == nil {
				t.Fatal("unbound journal accepted")
			}
		})
	}
}

func TestHarnessUsageDurableWithoutQualityAcceptance(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel", "absent", "zero", "invalid", "wrong_identity"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := store(t)
			var calls atomic.Int32
			r := nativeRequest(&calls)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := r.Execute
			r.Execute = func(c context.Context) (runtime.HarnessOutput, error) {
				out, _ := original(c)
				out.Usage = &providers.Usage{InputTokens: 17, OutputTokens: 4}
				switch mode {
				case "failure":
					return out, errors.New("failed after measured completion")
				case "cancel":
					cancel()
				case "absent":
					out.Usage = nil
				case "zero":
					out.Usage = &providers.Usage{}
				case "invalid":
					out.Usage.InputTokens = -1
				case "wrong_identity":
					out.Actual.Model = "other"
				}
				return out, nil
			}
			_, text, err := runtime.RunHarness(ctx, s, r)
			failed := mode == "failure" || mode == "cancel" || mode == "invalid" || mode == "wrong_identity"
			if (err != nil) != failed {
				t.Fatal(err)
			}
			events, e := s.Read(context.Background(), r.TaskID, 0, 10)
			if e != nil || len(events) != 2 {
				t.Fatal(e)
			}
			terminal := events[1]
			if failed && (text != "" || terminal.Data.HarnessOutcome != nil) {
				t.Fatal("failure promoted to success")
			}
			want := mode != "absent" && mode != "invalid" && mode != "wrong_identity"
			if (terminal.Data.Usage != nil) != want {
				t.Fatal("usage presence changed", mode)
			}
			if want {
				in, out := int64(17), int64(4)
				if mode == "zero" {
					in, out = 0, 0
				}
				if terminal.Data.Usage.InputTokens != in || terminal.Data.Usage.OutputTokens != out {
					t.Fatal("lost measurement")
				}
			}
		})
	}
}
