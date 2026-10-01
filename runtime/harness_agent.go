package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// HarnessAgentProtocol is an explicit multi-turn native journal format. Existing
// two-event native readers must reject it until they implement its replay rules.
const HarnessAgentProtocol = "native-tools-v1"

// HarnessAgentRequest is trusted host configuration. Execute must report actual
// provider dispatches through BeginTurn/CompleteTurn, use Invoke for tools, and
// join its child and bridge before returning. The host owns context admission,
// pinned gateway verification, resource leases and redacting journal wrappers.
// Opt-in SDK adapters must reuse those host controls rather than granting
// independent authority to the child harness.
type HarnessAgentRequest struct {
	Request  HarnessRequest
	MaxTurns int
	Tools    ToolExecutor
	Execute  func(context.Context, *HarnessAgentSession) (HarnessOutput, error)
}

// HarnessTurnOutput is supplied only after the pinned gateway has verified the
// actual complete upstream response. Native child messages cannot attest it.
type HarnessTurnOutput struct {
	Actual harness.Identity
	Text   string
	Calls  []providers.ToolCall
	Usage  *providers.Usage
}

// HarnessAgentSession serializes journal writes and effects. Its methods are
// host-only; do not expose registration or turn mutation over the child bridge.
// There is deliberately no resume/retry method for uncertain native effects.
type HarnessAgentSession struct {
	ctx                          context.Context
	mu                           sync.Mutex
	journal                      Journal
	request                      HarnessRequest
	tools                        ToolExecutor
	seq                          int64
	turns, maxTurns, used        int
	turn, attempt                string
	active, final, ended, closed bool
	fault                        error
	seen                         map[string]bool
	pending                      []providers.ToolCall
	finalText                    string
}

func RunHarnessAgent(ctx context.Context, j Journal, r HarnessAgentRequest) (harness.Execution, string, error) {
	q := r.Request
	if ctx == nil || ctx.Err() != nil || j == nil || r.Execute == nil || r.Tools == nil || r.MaxTurns < 1 || r.MaxTurns > 64 || q.Execute != nil || q.Attribution.Protocol != "" || q.TaskID == "" || q.SessionID == "" || q.Attribution.Identity.Validate() != nil || q.Attribution.Task.Validate() != nil || q.ContextTokens < 1 || q.MaxOutputBytes < 1 || q.MaxOutputBytes > 4<<20 {
		return harness.Execution{}, "", ErrInvalidRun
	}
	if _, ok := r.Tools.(ScopedToolExecutor); !ok {
		return harness.Execution{}, "", ErrInvalidRun
	}
	q.Attribution.Protocol = HarnessAgentProtocol
	s := &HarnessAgentSession{ctx: ctx, journal: j, request: q, tools: r.Tools, maxTurns: r.MaxTurns, seen: map[string]bool{}}
	a := q.Attribution
	if err := s.append(ctx, TaskStarted, Data{Harness: &a, Messages: q.Messages, Privacy: q.Privacy, SubmissionID: q.SubmissionID, Domain: a.Task.Domain, Profile: a.Task.Profile, ProviderID: a.Identity.Provider, ModelID: a.Identity.Model, ConfigID: a.Identity.ConfigSHA256, ContextTokens: q.ContextTokens}); err != nil {
		return harness.Execution{}, "", err
	}
	output, runErr := invokeHarness(ctx, func(c context.Context) (HarnessOutput, error) { return r.Execute(c, s) })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.fault != nil {
		runErr = s.fault
	}
	// Ambiguous persistence must not be followed by another append or a fabricated
	// terminal, even when the adapter swallows the callback failure.
	if errors.Is(runErr, ErrPersistence) {
		return harness.Execution{}, "", runErr
	}
	if ctx.Err() != nil {
		runErr = ctx.Err()
	}
	if runErr == nil && (!s.final || s.active || len(s.pending) != 0 || output.Actual != a.Identity || output.Text != s.finalText || output.Usage != nil) {
		runErr = ErrProtocol
	}
	text := ""
	if runErr == nil {
		text, runErr = validationView(q.OutputView, output.Text, q.MaxOutputBytes)
		if runErr == nil && (strings.TrimSpace(text) == "" || !utf8.ValidString(text)) {
			runErr = ErrEmptyOutput
		}
	}
	if ctx.Err() != nil {
		runErr = ctx.Err()
	}
	kind, code := TaskCompleted, ""
	var outcome *harness.Execution
	if runErr == nil {
		o := harness.Execution{Version: harness.Version, ID: q.TaskID, Actual: a.Identity, Task: a.Task, Status: "completed", OutputSHA256: harnessOutputDigest(text), CompletedAt: time.Now().UTC()}
		outcome = &o
	} else {
		kind, code = TaskFailed, "harness_failed"
		text = ""
		if ctx.Err() != nil {
			kind, code = TaskCanceled, "harness_canceled"
		}
	}
	s.turn, s.attempt = "", ""
	terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.append(terminal, kind, Data{HarnessOutcome: outcome, Text: text, Code: code}); err != nil {
		return harness.Execution{}, "", errors.Join(runErr, err)
	}
	if runErr != nil {
		return harness.Execution{}, "", runErr
	}
	return *outcome, text, nil
}
func (s *HarnessAgentSession) append(ctx context.Context, kind Kind, data Data) error {
	now := time.Now().UTC()
	if data.HarnessOutcome != nil {
		now = data.HarnessOutcome.CompletedAt
	}
	e := Event{Version: 1, ID: rand.Text(), TaskID: s.request.TaskID, SessionID: s.request.SessionID, CorrelationID: s.request.TaskID, Sequence: s.seq + 1, Time: now, Kind: kind, TurnID: s.turn, AttemptID: s.attempt, Data: data}
	if e.Validate() != nil {
		s.fault = ErrProtocol
		return s.fault
	}
	owned, err := e.Clone()
	if err != nil {
		s.fault = ErrProtocol
		return s.fault
	}
	if invokeJournalAppend(ctx, s.journal, s.seq, owned) != nil {
		s.fault = ErrPersistence
		return s.fault
	}
	s.seq++
	return nil
}
func (s *HarnessAgentSession) reject(err error) error {
	if s.fault == nil {
		s.fault = err
	}
	return s.fault
}

// BeginTurn must commit before upstream dispatch. Turn/attempt IDs are generated
// by the host, never adopted from child output. No retries or compaction occur.
func (s *HarnessAgentSession) BeginTurn(ctx context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil || s.closed || s.fault != nil || s.active || s.final || len(s.pending) != 0 || s.turns >= s.maxTurns {
		return "", "", s.reject(ErrProtocol)
	}
	s.turn, s.attempt = rand.Text(), rand.Text()
	if err := s.append(ctx, TurnStarted, Data{ProviderID: s.request.Attribution.Identity.Provider, ModelID: s.request.Attribution.Identity.Model}); err != nil {
		return "", "", err
	}
	s.active = true
	s.turns++
	return s.turn, s.attempt, nil
}

// CompleteTurn commits the verified full response before returning the only
// ToolExecution identities eligible for registration in the native tool bridge.
func (s *HarnessAgentSession) CompleteTurn(ctx context.Context, turn, attempt string, o HarnessTurnOutput) ([]ToolExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil || s.closed || s.fault != nil || !s.active || turn != s.turn || attempt != s.attempt || o.Actual != s.request.Attribution.Identity || !utf8.ValidString(o.Text) || len(o.Calls) > 128 || len(s.seen)+len(o.Calls) > 128 || (s.ended && len(o.Calls) > 0) {
		return nil, s.reject(ErrProtocol)
	}
	calls := make([]providers.ToolCall, len(o.Calls))
	batch := map[string]bool{}
	bytesUsed := len(o.Text)
	for i, c := range o.Calls {
		if c.ID == "" || len(c.ID) > 256 || c.Name == "" || len(c.Name) > 64 || s.seen[c.ID] || batch[c.ID] || len(c.Arguments) > 64<<10 {
			return nil, s.reject(ErrProtocol)
		}
		canonical, err := canonicalToolArguments(c.Arguments)
		if !utf8.Valid(c.Arguments) || err != nil {
			return nil, s.reject(ErrProtocol)
		}
		c.Arguments = canonical
		batch[c.ID] = true
		c.Arguments = append(json.RawMessage(nil), c.Arguments...)
		calls[i] = c
		bytesUsed += len(c.Arguments)
	}
	if s.used+bytesUsed > s.request.MaxOutputBytes {
		return nil, s.reject(ErrLimit)
	}
	if o.Usage != nil && (o.Usage.InputTokens < 0 || o.Usage.OutputTokens < 0 || o.Usage.InputTokens > 1<<40 || o.Usage.OutputTokens > 1<<40) {
		return nil, s.reject(ErrProtocol)
	}
	reason := "tool_calls"
	if len(calls) == 0 {
		reason = "stop"
		if strings.TrimSpace(o.Text) == "" {
			return nil, s.reject(ErrEmptyOutput)
		}
	}
	displayText, viewErr := validationView(s.request.OutputView, o.Text, s.request.MaxOutputBytes)
	if viewErr != nil || !utf8.ValidString(displayText) {
		return nil, s.reject(ErrInvalidOutput)
	}
	if len(displayText) > len(o.Text) {
		bytesUsed += len(displayText) - len(o.Text)
	}
	if s.used+bytesUsed > s.request.MaxOutputBytes {
		return nil, s.reject(ErrLimit)
	}
	if err := s.append(ctx, TurnCompleted, Data{Text: displayText, ToolCalls: calls, Usage: o.Usage, FinishReason: reason}); err != nil {
		return nil, err
	}
	s.used += bytesUsed
	s.active = false
	s.pending = calls
	if len(calls) == 0 {
		s.final = true
		s.finalText = o.Text
	}
	result := make([]ToolExecution, len(calls))
	for i, c := range calls {
		s.seen[c.ID] = true
		c.Arguments = append(json.RawMessage(nil), c.Arguments...)
		result[i] = ToolExecution{TaskID: s.request.TaskID, SessionID: s.request.SessionID, TurnID: s.turn, AttemptID: s.attempt, Call: c}
	}
	return result, nil
}

// Invoke is suitable for a native tool bridge callback. It verifies the exact
// committed proposal, persists ToolStarted before authority/effects, and persists
// ToolCompleted before releasing redacted content. It never repeats a call.
func (s *HarnessAgentSession) Invoke(ctx context.Context, x ToolExecution) (ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil || s.closed || s.fault != nil || s.active || s.ended || len(s.pending) == 0 || x.TaskID != s.request.TaskID || x.SessionID != s.request.SessionID || x.TurnID != s.turn || x.AttemptID != s.attempt {
		return ToolResult{}, s.reject(ErrProtocol)
	}
	call := s.pending[0]
	if x.Call.ID != call.ID || x.Call.Name != call.Name || !bytes.Equal(x.Call.Arguments, call.Arguments) {
		return ToolResult{}, s.reject(ErrProtocol)
	}
	behavior, err := declaredToolBehavior(s.tools, call.Name)
	if err != nil || !behavior.Valid() {
		return ToolResult{}, s.reject(ErrTool)
	}
	if err = s.append(ctx, ToolStarted, Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: behavior, Effect: UncertainEffect}); err != nil {
		return ToolResult{}, err
	}
	callCtx, stopCall := context.WithCancel(s.ctx)
	stopRequest := context.AfterFunc(ctx, stopCall)
	defer func() { stopRequest(); stopCall() }()
	result, toolErr := ToolResult{Effect: NoEffect}, callCtx.Err()
	if toolErr == nil {
		result, toolErr = invokeTool(callCtx, s.tools, x)
	}
	if errors.Is(toolErr, ErrToolArguments) && result.Effect == NoEffect && callCtx.Err() == nil {
		result = ToolResult{Effect: NoEffect, Failed: true, Recoverable: true, Content: `{"error":"invalid_tool_arguments","message":"No tool ran. Correct the arguments using the provided tool schema; do not repeat the same invalid call."}`}
		toolErr = nil
	}
	if result.Effect != NoEffect && result.Effect != ConfirmedEffect && result.Effect != UncertainEffect {
		result.Effect = UncertainEffect
		toolErr = ErrTool
	}
	if result.Recoverable && (!result.Failed || result.Effect != NoEffect) || behavior == BehaviorReadOnly && result.Effect == ConfirmedEffect {
		toolErr = ErrTool
	}
	if !utf8.ValidString(result.Content) || s.used+len(result.Content) > s.request.MaxOutputBytes {
		result.Content = ""
		toolErr = ErrLimit
	}
	// The delivery view and journal wrapper must use the same redaction policy.
	view, viewErr := validationView(s.request.OutputView, result.Content, s.request.MaxOutputBytes)
	if viewErr != nil || !utf8.ValidString(view) {
		result.Content = ""
		toolErr = ErrInvalidOutput
	} else {
		result.Content = view
	}
	if s.used+len(result.Content) > s.request.MaxOutputBytes {
		result.Content = ""
		toolErr = ErrLimit
	}
	s.used += len(result.Content)
	code := ""
	if toolErr != nil || result.Failed {
		code = "tool_failed"
	}
	if toolErr == nil && result.Failed && result.Recoverable {
		code = "tool_failed_recoverable"
	}
	if result.EndToolUse && toolErr == nil && !result.Failed && result.Effect != UncertainEffect {
		code = "tool_use_ended"
	}
	terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.append(terminal, ToolCompleted, Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: behavior, Effect: result.Effect, Text: result.Content, Code: code}); err != nil {
		return ToolResult{}, err
	}
	s.pending = s.pending[1:]
	if callCtx.Err() != nil {
		return ToolResult{}, s.reject(callCtx.Err())
	}
	if toolErr != nil || (result.Failed && !result.Recoverable) || result.Effect == UncertainEffect {
		return ToolResult{}, s.reject(ErrTool)
	}
	if result.EndToolUse && !result.Failed {
		s.ended = true
		if len(s.pending) != 0 {
			return ToolResult{}, s.reject(ErrTool)
		}
	}
	return result, nil
}
