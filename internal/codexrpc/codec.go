// Package codexrpc implements bounded Codex app-server JSON-line envelopes.
// It does not dispatch methods, execute tools, or manage subprocesses. Callers
// must treat protocol/read/write failures as terminal for the current stream.
package codexrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sync"
	"unicode/utf8"
)

var (
	ErrFrame = errors.New("codex RPC invalid frame")
	ErrLimit = errors.New("codex RPC frame limit exceeded")
	ErrRead  = errors.New("codex RPC read failed")
	ErrWrite = errors.New("codex RPC write failed")
)

const DefaultMaxFrame = 1 << 20

type Kind uint8

const (
	Request Kind = iota + 1
	Notification
	Response
	ErrorResponse
)

// Envelope retains exact JSON ID bytes, including escaped string IDs and large
// integer IDs. Payloads may contain sensitive content; do not log envelopes.
// A nil Result means absent; json.RawMessage("null") is a valid result.
type Envelope struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RemoteError    `json:"error,omitempty"`
}

// RemoteError is protocol data, not a Go error: callers must not surface its
// message or data without their normal redaction policy.
type RemoteError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

var integer = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func validID(id json.RawMessage) bool {
	if integer.Match(id) {
		return true
	}
	var value string
	return len(id) > 0 && id[0] == '"' && json.Unmarshal(id, &value) == nil
}

// Kind validates the entire envelope before classifying it.
func (e Envelope) Kind() (Kind, error) {
	if e.JSONRPC != "" && e.JSONRPC != "2.0" {
		return 0, ErrFrame
	}
	if e.ID != nil && !validID(e.ID) {
		return 0, ErrFrame
	}
	if e.Params != nil && (!json.Valid(e.Params) || len(e.Params) == 0 || (e.Params[0] != '{' && e.Params[0] != '[')) {
		return 0, ErrFrame
	}
	if e.Method != "" {
		if e.Result != nil || e.Error != nil {
			return 0, ErrFrame
		}
		if e.ID == nil {
			return Notification, nil
		}
		return Request, nil
	}
	if e.ID == nil || e.Params != nil || (e.Result == nil) == (e.Error == nil) {
		return 0, ErrFrame
	}
	if e.Error != nil {
		if e.Error.Data != nil && !json.Valid(e.Error.Data) {
			return 0, ErrFrame
		}
		return ErrorResponse, nil
	}
	if !json.Valid(e.Result) {
		return 0, ErrFrame
	}
	return Response, nil
}

// object rejects duplicate and unknown members rather than accepting the last
// value as encoding/json would normally do.
func object(raw []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, ErrFrame
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		t, err = d.Token()
		key, ok := t.(string)
		if err != nil || !ok || !allowed[key] || fields[key] != nil {
			return nil, ErrFrame
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrFrame
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, ErrFrame
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrFrame
	}
	return fields, nil
}

func decode(raw []byte) (Envelope, error) {
	var e Envelope
	if !utf8.Valid(raw) {
		return e, ErrFrame
	}
	f, err := object(raw, map[string]bool{"jsonrpc": true, "id": true, "method": true, "params": true, "result": true, "error": true})
	if err != nil {
		return e, err
	}
	for key, dst := range map[string]*string{"jsonrpc": &e.JSONRPC, "method": &e.Method} {
		if v, ok := f[key]; ok {
			if len(v) == 0 || v[0] != '"' || json.Unmarshal(v, dst) != nil || *dst == "" {
				return Envelope{}, ErrFrame
			}
		}
	}
	e.ID, e.Params, e.Result = f["id"], f["params"], f["result"]
	if v, ok := f["error"]; ok {
		fields, err := object(v, map[string]bool{"code": true, "message": true, "data": true})
		if err != nil || !integer.Match(fields["code"]) || len(fields["message"]) == 0 || fields["message"][0] != '"' {
			return Envelope{}, ErrFrame
		}
		e.Error = new(RemoteError)
		if json.Unmarshal(fields["code"], &e.Error.Code) != nil || json.Unmarshal(fields["message"], &e.Error.Message) != nil {
			return Envelope{}, ErrFrame
		}
		e.Error.Data = fields["data"]
	}
	if _, err := e.Kind(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// Decode parses one envelope without a line terminator, with the default frame
// size limit. Stream users should use Decoder for configurable framing limits.
func Decode(raw []byte) (Envelope, error) {
	if len(raw) > DefaultMaxFrame {
		return Envelope{}, ErrLimit
	}
	return decode(raw)
}

type Decoder struct {
	reader *bufio.Reader
	max    int
}

func NewDecoder(r io.Reader, maxFrame int) *Decoder {
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrame
	}
	return &Decoder{reader: bufio.NewReaderSize(r, 4096), max: maxFrame}
}

// Read requires a newline terminator. EOF is returned only between frames.
// Decoder is single-reader; cancellation is provided by closing the transport.
func (d *Decoder) Read() (Envelope, error) {
	var frame []byte
	for {
		part, err := d.reader.ReadSlice('\n')
		terminated := len(part) > 0 && part[len(part)-1] == '\n'
		if terminated {
			part = part[:len(part)-1]
		}
		if len(part) > d.max-len(frame) {
			return Envelope{}, ErrLimit
		}
		frame = append(frame, part...)
		if terminated {
			return decode(frame)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(frame) == 0 {
			return Envelope{}, io.EOF
		}
		return Envelope{}, ErrRead
	}
}

type Encoder struct {
	writer io.Writer
	max    int
	mu     sync.Mutex
}

func NewEncoder(w io.Writer, maxFrame int) *Encoder {
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrame
	}
	return &Encoder{writer: w, max: maxFrame}
}

// Write serializes full frames. Callers must not concurrently mutate e or its
// payload slices. Writer failures never include transport or payload details.
func (w *Encoder) Write(e Envelope) error {
	// Reject oversized caller payloads before validation or marshaling can scan
	// or duplicate them. JSON escaping may expand within this bounded input;
	// the exact encoded limit is checked below as well.
	remaining := w.max
	parts := []int{len(e.JSONRPC), len(e.ID), len(e.Method), len(e.Params), len(e.Result)}
	if e.Error != nil {
		parts = append(parts, len(e.Error.Message), len(e.Error.Data))
	}
	for _, size := range parts {
		if size > remaining {
			return ErrLimit
		}
		remaining -= size
	}
	if _, err := e.Kind(); err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(e) != nil {
		return ErrFrame
	}
	raw := buffer.Bytes()
	raw = raw[:len(raw)-1]
	if len(raw) > w.max {
		return ErrLimit
	}
	raw = append(raw, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(raw) > 0 {
		n, err := w.writer.Write(raw)
		if err != nil || n <= 0 || n > len(raw) {
			return ErrWrite
		}
		raw = raw[n:]
	}
	return nil
}
