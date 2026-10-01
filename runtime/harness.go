package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// HarnessAttribution binds the host-admitted route to one native harness run.
// This is execution provenance, never an evaluation or permission grant.
type HarnessAttribution struct {
	Selection *harness.Selection `json:",omitempty"`
	Identity  harness.Identity
	Task      harness.TaskClass
}
type HarnessOutput struct {
	Actual harness.Identity
	Text   string
}
type HarnessRequest struct {
	Messages []providers.Message
	Privacy  string
	// OutputView must match the host journal redaction policy. Hashes bind the
	// delivered/evaluated text, not private raw harness output.
	OutputView                      func(string) string
	TaskID, SessionID, SubmissionID string
	Attribution                     HarnessAttribution
	ContextTokens, MaxOutputBytes   int
	// Execute is trusted host code. It must enforce shared resource admission,
	// credentials, privacy, tool authority and its deadline; join on cancellation;
	// and return actual verified provenance. It must never silently retry.
	Execute func(context.Context) (HarnessOutput, error)
}

// RunHarness uses the ordinary journal and expected-sequence ownership boundary
// for a fresh task. Supply the same redacting, submission-fenced journal used by
// the normal runtime host. It neither opens a second task store nor pretends that
// an agent harness is a single provider turn. Native tool execution is the
// adapter's responsibility and requires a separately authorized host bridge.
// No success/quality feedback is synthesized here. A trusted ingestor can replay
// the committed terminal outcome into the evidence ledger without new inference.
func RunHarness(ctx context.Context, j Journal, r HarnessRequest) (harness.Execution, string, error) {
	if ctx == nil || j == nil || r.Execute == nil || r.TaskID == "" || r.SessionID == "" || r.Attribution.Identity.Validate() != nil || r.Attribution.Task.Validate() != nil || r.ContextTokens < 1 || r.MaxOutputBytes < 1 || r.MaxOutputBytes > 4<<20 {
		return harness.Execution{}, "", ErrInvalidRun
	}
	if err := ctx.Err(); err != nil {
		return harness.Execution{}, "", err
	}
	seq := int64(0)
	appendEvent := func(c context.Context, kind Kind, data Data) (Event, error) {
		e := Event{Version: 1, ID: rand.Text(), TaskID: r.TaskID, SessionID: r.SessionID, CorrelationID: r.TaskID, Sequence: seq + 1, Time: time.Now().UTC(), Kind: kind, Data: data}
		if err := e.Validate(); err != nil {
			return Event{}, ErrInvalidRun
		}
		owned, cloneErr := e.Clone()
		if cloneErr != nil {
			return Event{}, ErrInvalidRun
		}
		if err := invokeJournalAppend(c, j, seq, owned); err != nil {
			return Event{}, ErrPersistence
		}
		seq++
		return e, nil
	}
	attribution := r.Attribution
	if _, err := appendEvent(ctx, TaskStarted, Data{Messages: r.Messages, Privacy: r.Privacy, Harness: &attribution, SubmissionID: r.SubmissionID, Domain: attribution.Task.Domain, Profile: attribution.Task.Profile, ProviderID: attribution.Identity.Provider, ModelID: attribution.Identity.Model, ContextTokens: r.ContextTokens, ConfigID: attribution.Identity.ConfigSHA256}); err != nil {
		return harness.Execution{}, "", err
	}
	output, runErr := invokeHarness(ctx, r.Execute)
	if ctx.Err() != nil {
		runErr = ctx.Err()
	}
	if runErr == nil && (output.Actual != attribution.Identity || !utf8.ValidString(output.Text) || strings.TrimSpace(output.Text) == "" || len(output.Text) > r.MaxOutputBytes) {
		runErr = ErrProtocol
	}
	if runErr == nil {
		output.Text, runErr = validationView(r.OutputView, output.Text, r.MaxOutputBytes)
		if runErr == nil && strings.TrimSpace(output.Text) == "" {
			runErr = ErrEmptyOutput
		}
	}
	if ctx.Err() != nil {
		runErr = ctx.Err()
	}
	status, kind, code := "completed", TaskCompleted, ""
	if runErr != nil {
		status, kind, code = "infrastructure_failed", TaskFailed, "harness_failed"
		if ctx.Err() != nil {
			status, kind, code = "canceled", TaskCanceled, "harness_canceled"
		}
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	outcome := harness.Execution{Version: harness.Version, ID: r.TaskID, Actual: attribution.Identity, Task: attribution.Task, Status: status, CompletedAt: time.Now().UTC()}
	text := ""
	if runErr == nil {
		text = output.Text
		sum := sha256.Sum256([]byte(text))
		outcome.OutputSHA256 = hex.EncodeToString(sum[:])
	}
	// Outcome and task terminal are one journal append. There is no window where
	// accepted output can escape before its terminal provenance commits.
	event := Event{Version: 1, ID: rand.Text(), TaskID: r.TaskID, SessionID: r.SessionID, CorrelationID: r.TaskID, Sequence: seq + 1, Time: outcome.CompletedAt, Kind: kind, Data: Data{HarnessOutcome: &outcome, Text: text, Code: code}}
	// A failed adapter may never have established actual execution identity.
	// Preserve the failed task journal, but do not invent an Actual identity for
	// evidence ingestion. Only completed, verified results receive an outcome.
	if runErr != nil {
		event.Data.HarnessOutcome = nil
	}
	if event.Validate() != nil {
		return harness.Execution{}, "", ErrInvalidRun
	}
	owned, cloneErr := event.Clone()
	if cloneErr != nil {
		return harness.Execution{}, "", ErrInvalidRun
	}
	if err := invokeJournalAppend(finalCtx, j, seq, owned); err != nil {
		return harness.Execution{}, "", errors.Join(runErr, ErrPersistence)
	}
	if runErr != nil {
		return harness.Execution{}, "", runErr
	}
	return outcome, text, nil
}
func invokeHarness(ctx context.Context, execute func(context.Context) (HarnessOutput, error)) (output HarnessOutput, err error) {
	defer func() {
		if recover() != nil {
			output = HarnessOutput{}
			err = ErrProvider
		}
	}()
	return execute(ctx)
}

func harnessOutputDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
