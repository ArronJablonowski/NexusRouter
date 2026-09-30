package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

type summaryProvider func(context.Context, providers.Request, func(providers.Chunk) error) error

func (p summaryProvider) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	return p(ctx, r, emit)
}
func (p summaryProvider) Models(context.Context) ([]string, error) {
	return []string{"summary-model"}, nil
}

const summaryResponse = `{"version":1,"summary":{"decisions":["Use the requested design"],"pending_work":["Run the tests"],"failures":[],"artifacts":[]}}`

func summarySource() Snapshot {
	return Snapshot{TaskID: "source", SessionID: "session", State: "completed", Sequence: 12, Messages: []providers.Message{{Role: "user", Content: "Build this"}, {Role: "assistant", Content: "Implemented the design"}, {Role: "user", Content: "Keep going"}, {Role: "assistant", Content: "Tests remain"}}}
}
func summarySettings(p providers.Provider) Summarizer {
	return Summarizer{Provider: p, Model: "summary-model", ContextTokens: 1000000, Timeout: time.Second, EstimatedCost: .01, MaxCost: .1}
}

func TestSummarizerDraftProvenanceAndIsolation(t *testing.T) {
	source := summarySource()
	encoded, _ := json.Marshal(source.Messages)
	hash := sha256.Sum256(encoded)
	calls := 0
	usage := &providers.Usage{InputTokens: 10, OutputTokens: 4}
	s := summarySettings(summaryProvider(func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing bounded deadline")
		}
		if r.Model != "summary-model" || len(r.Tools) != 0 || len(r.Messages) < 2 {
			t.Fatal("unexpected dispatch", r)
		}
		if !strings.Contains(strings.ToLower(r.Messages[0].Content), "untrusted") {
			t.Fatal("missing untrusted input boundary")
		}
		if !json.Valid([]byte(r.Messages[len(r.Messages)-1].Content)) {
			t.Fatal("source not JSON envelope")
		}
		source.Messages[0].Content = "caller changed source during inference"
		if err := emit(providers.Chunk{Text: summaryResponse}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "stop", Usage: usage})
	}))
	draft, err := s.Draft(context.Background(), source, 2)
	if err != nil || calls != 1 || draft.Request.Keep != 2 || draft.SourceTaskID != source.TaskID || draft.SourceSequence != source.Sequence || draft.SourceDigest != hex.EncodeToString(hash[:]) || draft.Model != "summary-model" || draft.Usage == nil || draft.Usage.InputTokens != 10 || draft.Elapsed < 0 {
		t.Fatal(draft, calls, err)
	}
	if ValidateCompactionRequest(&draft.Request) != nil {
		t.Fatal("invalid generated request")
	}
	if draft.Checkpoint == nil || draft.Checkpoint.SourceDigest != draft.SourceDigest || draft.Checkpoint.BeforeContextTokens < 1 || draft.Checkpoint.AfterContextTokens < 1 {
		t.Fatal("missing proposed context estimate or provenance", draft.Checkpoint)
	}
	draft.Request.Summary.Decisions[0] = "changed draft"
	if draft.Checkpoint.Summary.Decisions[0] != "Use the requested design" {
		t.Fatal("proposal aliases inspection checkpoint")
	}
	usage.InputTokens = 999
	if draft.Usage.InputTokens != 10 {
		t.Fatal("draft aliases provider usage")
	}
}

func TestSummarizerAdmissionBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"incomplete", "pending", "uncertain", "interrupted", "noop", "model", "context", "timeout", "long_timeout", "negativecost", "infinitecost", "nancost", "budget", "oversize", "provider"} {
		t.Run(mode, func(t *testing.T) {
			source := summarySource()
			keep := 2
			s := summarySettings(summaryProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
				t.Fatal("invalid request dispatched")
				return nil
			}))
			switch mode {
			case "incomplete":
				source.State = "running"
			case "pending":
				source.Pending = map[string]Pending{"call": {}}
			case "uncertain":
				source.UncertainEffects = true
			case "interrupted":
				source.InterruptedTurn = true
			case "noop":
				keep = len(source.Messages)
			case "model":
				s.Model = ""
			case "context":
				s.ContextTokens = 1
			case "timeout":
				s.Timeout = 0
			case "long_timeout":
				s.Timeout = time.Minute + 1
			case "negativecost":
				s.EstimatedCost = -1
			case "infinitecost":
				s.MaxCost = math.Inf(1)
			case "nancost":
				s.EstimatedCost = math.NaN()
			case "budget":
				s.MaxCost = 0
			case "oversize":
				source.Messages[0].Content = strings.Repeat("x", 1<<20)
			case "provider":
				s.Provider = nil
			}
			if _, err := s.Draft(context.Background(), source, keep); !errors.Is(err, ErrHistory) {
				t.Fatal("admission failure not safe", err)
			}
		})
	}
}

func TestSummarizerRejectsMalformedResponses(t *testing.T) {
	for _, body := range []string{"", `null`, `[]`, summaryResponse + `{}`, "```json\n" + summaryResponse + "\n```",
		strings.Replace(summaryResponse, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(summaryResponse, `"version":1`, `"version":2`, 1),
		strings.Replace(summaryResponse, `"version":1`, `"Version":1`, 1),
		strings.TrimSuffix(summaryResponse, "}") + `,"tools":[]}`,
		strings.Replace(summaryResponse, `"decisions":`, `"decisions":[],"decisions":`, 1),
		strings.Replace(summaryResponse, `"failures":[]`, `"failures":null`, 1),
		strings.Replace(summaryResponse, `["Use the requested design"]`, `[null]`, 1),
		strings.Replace(summaryResponse, `["Use the requested design"]`, `[" "]`, 1),
		`{"version":1,"summary":{"decisions":[],"pending_work":[],"failures":[],"artifacts":[]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := summarySettings(summaryProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				return emit(providers.Chunk{Text: body, Done: true, FinishReason: "stop"})
			}))
			if _, err := s.Draft(context.Background(), summarySource(), 2); !errors.Is(err, ErrHistory) {
				t.Fatal("malformed output accepted", err)
			}
		})
	}
}

func TestSummarizerLatchesIgnoredCallbackErrors(t *testing.T) {
	for _, mode := range []string{"postdone", "tool", "oversize", "duplicateusage", "negativeusage", "nonstop", "missingdone", "providererror"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			s := summarySettings(summaryProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				_ = emit(providers.Chunk{Text: summaryResponse})
				switch mode {
				case "postdone":
					_ = emit(providers.Chunk{Done: true, FinishReason: "stop"})
					_ = emit(providers.Chunk{Text: "late"})
					return nil
				case "tool":
					_ = emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "execute"}})
				case "oversize":
					_ = emit(providers.Chunk{Text: strings.Repeat("x", 64<<10)})
				case "duplicateusage":
					_ = emit(providers.Chunk{Usage: &providers.Usage{InputTokens: 1}})
					_ = emit(providers.Chunk{Usage: &providers.Usage{InputTokens: 1}})
				case "negativeusage":
					_ = emit(providers.Chunk{Usage: &providers.Usage{InputTokens: -1}})
				case "nonstop":
					_ = emit(providers.Chunk{Done: true, FinishReason: "length"})
					return nil
				case "missingdone":
					return nil
				case "providererror":
					return errors.New("private provider detail")
				}
				_ = emit(providers.Chunk{Done: true, FinishReason: "stop"})
				return nil
			}))
			if _, err := s.Draft(context.Background(), summarySource(), 2); !errors.Is(err, ErrHistory) || calls != 1 {
				t.Fatal("ignored callback failure or retried", calls, err)
			}
		})
	}
}

func TestSummarizerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := summarySettings(summaryProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Fatal("canceled request dispatched")
		return nil
	}))
	if _, err := s.Draft(ctx, summarySource(), 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.Provider = summaryProvider(func(ctx context.Context, _ providers.Request, _ func(providers.Chunk) error) error {
		<-ctx.Done()
		return ctx.Err()
	})
	s.Timeout = time.Millisecond
	if _, err := s.Draft(context.Background(), summarySource(), 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline ignored")
	}
}
