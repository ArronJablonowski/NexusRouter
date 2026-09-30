package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/ArronJablonowski/NexusRouter/memory"
)

const memoryBodyLimit = 128 << 10

type memoryCommand struct {
	Version          int         `json:"version"`
	ID               string      `json:"id"`
	AfterID          string      `json:"after_id"`
	Contains         string      `json:"contains"`
	Limit            int         `json:"limit"`
	IncludeExpired   bool        `json:"include_expired"`
	Fact             memory.Fact `json:"fact"`
	ExpectedRevision int64       `json:"expected_revision"`
}

func memoryManagementRoute(path string) bool {
	switch path {
	case "/v1/memory/get", "/v1/memory/query", "/v1/memory/put", "/v1/memory/delete", "/v1/memory/export":
		return true
	}
	return false
}

func (h *Handler) serveMemoryManagement(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		failure(w, 405, "method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength < 0 || len(r.TransferEncoding) != 0 {
		failure(w, 400, "invalid_request")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return
	}
	if r.Body == nil || r.ContentLength > memoryBodyLimit {
		failure(w, 413, "invalid_request")
		return
	}
	select {
	case h.memorySlots <- struct{}{}:
		defer func() { <-h.memorySlots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "memory_unavailable")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
	defer http.NewResponseController(w).SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, memoryBodyLimit))
	command, decodeErr := decodeMemoryCommand(r.URL.Path, body)
	if err != nil || decodeErr != nil {
		failure(w, 400, "invalid_request")
		return
	}
	if ctx.Err() != nil {
		failure(w, 503, "memory_unavailable")
		return
	}
	out, err := h.callMemoryCommand(ctx, r.URL.Path, command)
	// A lost acknowledgement after a mutation is not proof it failed. The
	// caller must inspect the current revision, never blindly replay a write.
	if ctx.Err() != nil {
		failure(w, 503, "memory_unavailable")
		return
	}
	if errors.Is(err, memory.ErrConflict) {
		failure(w, 409, "memory_conflict")
		return
	}
	if err != nil {
		failure(w, 503, "memory_unavailable")
		return
	}
	if r.URL.Path == "/v1/memory/export" {
		writeMemorySnapshot(ctx, w, out)
		return
	}
	writeJSON(w, 200, out)
}

func decodeMemoryCommand(path string, body []byte) (memoryCommand, error) {
	var command memoryCommand
	keys := []string{"version"}
	switch path {
	case "/v1/memory/export":
		// Export cannot override the configured scope or select a partial page.
	case "/v1/memory/get":
		keys = append(keys, "id")
	case "/v1/memory/query":
		keys = append(keys, "after_id", "contains", "limit", "include_expired")
	case "/v1/memory/put":
		keys = append(keys, "fact", "expected_revision")
	case "/v1/memory/delete":
		keys = append(keys, "id", "expected_revision")
	default:
		return command, memory.ErrInput
	}
	if !memoryJSONUnicodeValid(body) {
		return command, memory.ErrInput
	}
	fields, err := chatObject(body, keys...)
	if err != nil || len(fields) != len(keys) || hasNullMemoryFields(fields) {
		return command, memory.ErrInput
	}
	if path == "/v1/memory/put" {
		fact, err := chatObject(fields["fact"], "version", "id", "scope", "revision", "content", "provenance", "confidence", "privacy", "created", "updated", "last_use", "expires")
		if err != nil || hasNullMemoryFields(fact) {
			return command, memory.ErrInput
		}
		for _, key := range []string{"version", "id", "scope", "revision", "content", "provenance", "confidence", "privacy", "created", "updated"} {
			if _, ok := fact[key]; !ok {
				return command, memory.ErrInput
			}
		}
	}
	if json.Unmarshal(body, &command) != nil || command.Version != 1 {
		return memoryCommand{}, memory.ErrInput
	}
	switch path {
	case "/v1/memory/get", "/v1/memory/delete":
		if !memory.ValidKey(command.ID) || (path == "/v1/memory/delete" && command.ExpectedRevision < 1) {
			return memoryCommand{}, memory.ErrInput
		}
	case "/v1/memory/query":
		q := memory.Query{Scope: "operator", AfterID: command.AfterID, Contains: command.Contains, Limit: command.Limit, Now: time.Now().UTC()}
		if q.Validate() != nil || command.Limit > 100 {
			return memoryCommand{}, memory.ErrInput
		}
	case "/v1/memory/put":
		if command.Fact.Validate() != nil || command.ExpectedRevision < 0 || command.Fact.Revision-1 != command.ExpectedRevision {
			return memoryCommand{}, memory.ErrInput
		}
	}
	return command, nil
}

func hasNullMemoryFields(fields map[string]json.RawMessage) bool {
	for _, value := range fields {
		if string(value) == "null" {
			return true
		}
	}
	return false
}

func (h *Handler) callMemoryCommand(ctx context.Context, path string, c memoryCommand) (out any, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, memory.ErrInput
		}
	}()
	switch path {
	case "/v1/memory/export":
		if h.services.ExportMemory == nil {
			return nil, memory.ErrInput
		}
		snapshot, err := h.services.ExportMemory(ctx)
		if err != nil {
			// Export is read-only: backend conflicts are not operator CAS errors.
			return nil, memory.ErrInput
		}
		if ctx.Err() != nil || snapshot.Validate() != nil {
			return nil, memory.ErrInput
		}
		return snapshot, nil
	case "/v1/memory/get":
		if h.services.Memory == nil {
			return nil, memory.ErrInput
		}
		fact, err := h.services.Memory(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		if fact.Validate() != nil || fact.ID != c.ID {
			return nil, memory.ErrInput
		}
		return fact, nil
	case "/v1/memory/query":
		if h.services.Memories == nil {
			return nil, memory.ErrInput
		}
		facts, err := h.services.Memories(ctx, c.AfterID, c.Contains, c.Limit, c.IncludeExpired)
		if err != nil {
			return nil, err
		}
		if len(facts) > c.Limit {
			return nil, memory.ErrInput
		}
		last, scope := c.AfterID, ""
		budget := (8 << 20) - 64 // Include the page envelope and separators.
		for _, fact := range facts {
			if fact.Validate() != nil || fact.ID <= last || (scope != "" && fact.Scope != scope) {
				return nil, memory.ErrInput
			}
			encoded, err := json.Marshal(fact)
			budget -= len(encoded) + 1
			if err != nil || budget < 0 || ctx.Err() != nil {
				return nil, memory.ErrInput
			}
			last, scope = fact.ID, fact.Scope
		}
		if facts == nil {
			facts = []memory.Fact{}
		}
		return struct {
			Version int           `json:"version"`
			Facts   []memory.Fact `json:"facts"`
		}{1, facts}, nil
	case "/v1/memory/put":
		if h.services.PutMemory == nil {
			return nil, memory.ErrInput
		}
		if err := h.services.PutMemory(ctx, c.Fact, c.ExpectedRevision); err != nil {
			return nil, err
		}
		return map[string]any{"version": 1, "id": c.Fact.ID, "revision": c.Fact.Revision}, nil
	case "/v1/memory/delete":
		if h.services.DeleteMemory == nil {
			return nil, memory.ErrInput
		}
		if err := h.services.DeleteMemory(ctx, c.ID, c.ExpectedRevision); err != nil {
			return nil, err
		}
		return map[string]any{"version": 1, "id": c.ID, "deleted": true}, nil
	}
	return nil, memory.ErrInput
}
