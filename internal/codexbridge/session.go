package codexbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// Wire is an already-admitted, task-owned connection. Close MUST unblock IO.
// Session does not launch Codex or establish sandbox/capability isolation.
type Wire interface {
	Read() (codexrpc.Envelope, error)
	Write(codexrpc.Envelope) error
	Close() error
}

type Options struct{ Model, CWD, ReasoningEffort string }

// Session adapts one Codex turn into NexusRouter model/tool segments. A verified
// item/tool/call is a paused segment boundary, NOT successful task completion.
// Only turn/completed can finish the final segment. A caller must defer Close
// across the entire runtime loop, including while a proposed tool is executing.
// Initial history is imported as typed items into a new ephemeral thread, not
// flattened into a user prompt or resumed from a foreign Codex thread ID.
// Durable steering is admitted only between segments, never during Stream.
// Compaction of an active exchange remains unsupported.
type Session struct {
	w                          Wire
	options                    Options
	mu                         sync.Mutex
	closed                     atomic.Bool
	closeDone                  chan struct{}
	stopWatch                  func() bool
	thread, turn               string
	started, finished, emitted bool
	pending                    *Pending
	pendingID                  json.RawMessage
	pendingCall                string
	queue                      []codexrpc.Envelope
	frames, wireBytes          int
	items                      map[string]*itemState
	usageTotal, usageReported  providers.Usage
	usageUpdated               bool
	prepared                   bool
	launchFeatures             []string
	launchRequestID            int
	allowDisabledStatus        bool
	completedRequest           *providers.Request
	controlSequence, steered   int
	turnIDs                    map[string]bool
}

func NewSession(ctx context.Context, w Wire, options Options) (*Session, error) {
	if ctx == nil || ctx.Err() != nil || w == nil || (reflect.ValueOf(w).Kind() == reflect.Pointer && reflect.ValueOf(w).IsNil()) || options.Model == "" || len(options.Model) > 128 ||
		!utf8.ValidString(options.Model) || !filepath.IsAbs(options.CWD) || !utf8.ValidString(options.CWD) || !validReasoningEffort(options.ReasoningEffort) {
		return nil, failure(false)
	}
	s := &Session{w: w, options: options, items: make(map[string]*itemState), closeDone: make(chan struct{})}
	// Do not call Close here: that would access stopWatch during assignment.
	s.stopWatch = context.AfterFunc(ctx, func() {
		if s.closed.CompareAndSwap(false, true) {
			defer close(s.closeDone)
			_ = s.w.Close()
		}
	})
	return s, nil
}

func failure(partial bool) error { return &providers.Failure{Code: "codex_protocol", Partial: partial} }

func (s *Session) Close() error {
	s.stopWatch()
	if s.closed.CompareAndSwap(false, true) {
		defer close(s.closeDone)
		return s.w.Close()
	}
	<-s.closeDone
	return nil
}

func (s *Session) Models(ctx context.Context) ([]string, error) {
	if ctx == nil || ctx.Err() != nil || s.closed.Load() {
		return nil, failure(false)
	}
	return []string{s.options.Model}, nil
}

func (s *Session) Stream(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) (err error) {
	if !s.mu.TryLock() {
		return failure(false)
	}
	defer s.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = failure(s.emitted)
		}
		if err != nil {
			_ = s.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || emit == nil || s.closed.Load() ||
		!validRequest(req) || providers.ValidateMessages(req.Messages) != nil || req.Model != s.options.Model {
		return failure(s.emitted)
	}
	// Own the admitted history/catalog before a callback can mutate it.
	body, cloneErr := json.Marshal(req)
	if cloneErr != nil || len(body) > maxExchangeBytes {
		return failure(s.emitted)
	}
	var owned providers.Request
	if json.Unmarshal(body, &owned) != nil {
		return failure(s.emitted)
	}
	if req.JSONSchema == nil {
		owned.JSONSchema = nil
	}
	req = owned
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	if !s.started {
		if err = s.begin(req); err != nil {
			return err
		}
		s.started = true
	} else if s.finished {
		if err = s.continueCompleted(ctx, req); err != nil {
			return err
		}
	} else {
		if s.pending == nil {
			return failure(s.emitted)
		}
		response, guidance, resumeErr := s.pending.ResumeSteered(req)
		if resumeErr != nil {
			return failure(s.emitted)
		}
		if len(guidance) > 0 {
			if err = s.steer(ctx, guidance); err != nil {
				return err
			}
		}
		if ctx.Err() != nil || s.closed.Load() {
			return failure(s.emitted)
		}
		if err = s.w.Write(codexrpc.Envelope{ID: bytes.Clone(s.pendingID), Result: response}); err != nil {
			return failure(s.emitted)
		}
		s.items[s.pendingCall].responded = true
		s.items[s.pendingCall].responseSuccess = !req.Messages[len(req.Messages)-len(guidance)-1].ToolFailed
		s.pending, s.pendingID, s.pendingCall = nil, nil, ""
	}
	return s.segment(ctx, req, emit)
}

func marshal(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (s *Session) begin(req providers.Request) error {
	history, prompt, err := initialHistory(req.Messages)
	if err != nil {
		return failure(false)
	}
	names := map[string]bool{}
	definitions := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		if !namePattern.MatchString(tool.Name) || names[tool.Name] || !json.Valid(tool.Parameters) {
			return failure(false)
		}
		names[tool.Name] = true
		definitions = append(definitions, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "inputSchema": tool.Parameters})
	}
	if err := s.prepare(); err != nil {
		return err
	}
	dynamic := []map[string]any{}
	if len(definitions) > 0 {
		dynamic = append(dynamic, map[string]any{"type": "namespace", "name": "darwin", "description": "NexusRouter-authorized tools", "tools": definitions})
	}
	result, err := s.call("2", "thread/start", map[string]any{
		"model": req.Model, "cwd": s.options.CWD, "ephemeral": true, "allowProviderModelFallback": false,
		"approvalPolicy": "never", "sandbox": "read-only", "environments": []any{}, "dynamicTools": dynamic,
	})
	if err != nil {
		return err
	}
	var thread struct {
		Model  string `json:"model"`
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if decodePayload(result, &thread) != nil || !validOpaque(thread.Thread.ID) || thread.Model != req.Model {
		return failure(false)
	}
	s.thread = thread.Thread.ID
	// Validate thread-start notices before transmitting the user's input.
	for _, queued := range s.queue {
		var notice struct {
			ThreadID string `json:"threadId"`
			Thread   struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if decodePayload(queued.Params, &notice) != nil || (queued.Method == "thread/started" && notice.Thread.ID != s.thread) || (queued.Method == "thread/status/changed" && notice.ThreadID != s.thread) {
			return failure(false)
		}
	}
	if len(history) > 0 {
		result, err = s.call("4", "thread/inject_items", map[string]any{"threadId": s.thread, "items": history})
		if err != nil {
			return err
		}
		var ack map[string]json.RawMessage
		if decodePayload(result, &ack) != nil || len(ack) != 0 {
			return failure(false)
		}
	}
	params := map[string]any{"threadId": s.thread, "model": req.Model, "environments": []any{}, "input": []map[string]any{{"type": "text", "text": prompt, "text_elements": []any{}}}}
	if s.options.ReasoningEffort != "" {
		params["effort"] = s.options.ReasoningEffort
	}
	if req.JSONSchema != nil {
		if !json.Valid(req.JSONSchema) {
			return failure(false)
		}
		params["outputSchema"] = req.JSONSchema
	}
	result, err = s.call("3", "turn/start", params)
	if err != nil {
		return err
	}
	var turn struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if decodePayload(result, &turn) != nil || !validOpaque(turn.Turn.ID) || turn.Turn.Status != "inProgress" {
		return failure(false)
	}
	s.turn = turn.Turn.ID
	s.turnIDs = map[string]bool{s.turn: true}
	return nil
}

func validOpaque(v string) bool { return v != "" && len(v) <= 256 && utf8.ValidString(v) }

func validReasoningEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func (s *Session) receive() (codexrpc.Envelope, error) {
	e, err := s.w.Read()
	if err != nil {
		return codexrpc.Envelope{}, failure(s.emitted)
	}
	if _, err = e.Kind(); err != nil {
		return codexrpc.Envelope{}, failure(s.emitted)
	}
	b, err := json.Marshal(e)
	if err != nil || len(b) > codexrpc.DefaultMaxFrame || s.frames >= 4096 || len(b) > 16<<20-s.wireBytes {
		return codexrpc.Envelope{}, failure(s.emitted)
	}
	s.frames++
	s.wireBytes += len(b)
	return e, nil
}

func (s *Session) call(id, method string, params any) (json.RawMessage, error) {
	request := codexrpc.Envelope{ID: json.RawMessage(id), Method: method, Params: marshal(params)}
	// Validate the complete frame before any wire can observe it, including
	// custom host wires. Guidance is never silently split or truncated.
	if codexrpc.NewEncoder(io.Discard, codexrpc.DefaultMaxFrame).Write(request) != nil || s.closed.Load() || s.w.Write(request) != nil {
		return nil, failure(s.emitted)
	}
	notices := 0
	for {
		e, err := s.receive()
		if err != nil {
			return nil, err
		}
		kind, _ := e.Kind()
		if s.options.Model == "health-discovery" && (method == "account/read" || method == "model/list") && kind == codexrpc.Notification && e.Method == "account/updated" {
			notices++
			if notices > 16 || !validHealthAccountNotice(e.Params) {
				return nil, failure(false)
			}
			continue
		}

		if s.allowDisabledStatus && kind == codexrpc.Notification && e.Method == "remoteControl/status/changed" && disabledRemoteControl(e.Params) {
			continue
		}
		// Checked launch deprecations may precede the thread/start response.
		// They are informational only, bounded, and never establish a thread.
		if method == "thread/start" && kind == codexrpc.Notification && e.Method == "deprecationNotice" && s.compatibilityNotice(e) {
			notices++
			if notices > 16 {
				return nil, failure(s.emitted)
			}
			continue
		}
		if method == "turn/steer" && kind == codexrpc.Notification {
			if !s.steeringNotice(e) {
				return nil, failure(s.emitted)
			}
			continue
		}
		if method == "thread/inject_items" && kind == codexrpc.Notification && s.compatibilityNotice(e) {
			continue // Same checked informational notices as normal streaming.
		}
		if method == "thread/inject_items" && kind == codexrpc.Notification && (e.Method == "thread/status/changed" || e.Method == "thread/started") {
			var notice struct {
				ThreadID string `json:"threadId"`
				Thread   struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if decodePayload(e.Params, &notice) != nil || (e.Method == "thread/status/changed" && notice.ThreadID != s.thread) || (e.Method == "thread/started" && notice.Thread.ID != s.thread) {
				return nil, failure(false)
			}
			// The CLI can deliver thread/started after its start response. Bind
			// this delayed metadata to the new thread; never dispatch from it.
			continue
		}
		if kind == codexrpc.Response || kind == codexrpc.ErrorResponse {
			if !bytes.Equal(e.ID, []byte(id)) || kind == codexrpc.ErrorResponse {
				return nil, failure(s.emitted)
			}
			return e.Result, nil
		}
		if kind == codexrpc.Request && (method != "turn/start" || e.Method != "item/tool/call") {
			return nil, failure(s.emitted)
		}
		if (method != "thread/start" && method != "turn/start") || (method == "thread/start" && (kind != codexrpc.Notification || (e.Method != "thread/started" && e.Method != "thread/status/changed"))) {
			return nil, failure(s.emitted)
		}
		if len(s.queue) >= 64 {
			return nil, failure(s.emitted)
		}
		s.queue = append(s.queue, e)
	}
}

func (s *Session) next() (codexrpc.Envelope, error) {
	if len(s.queue) == 0 {
		return s.receive()
	}
	e := s.queue[0]
	s.queue[0] = codexrpc.Envelope{}
	s.queue = s.queue[1:]
	return e, nil
}
