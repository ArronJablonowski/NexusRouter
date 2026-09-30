package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/memory"
)

// Buffer the complete bounded snapshot before committing success headers. A
// partial network write cannot be retracted; never retry it or append an error
// document to the factual payload. Native HTTP connections support deadlines;
// an embedded writer that does not must supply its own bounded I/O contract.
func writeMemorySnapshot(ctx context.Context, w http.ResponseWriter, out any) {
	committed := false
	defer func() {
		if recover() != nil && !committed {
			failure(w, 503, "memory_unavailable")
		}
	}()
	snapshot, ok := out.(memory.ExportSnapshot)
	if !ok || ctx.Err() != nil {
		failure(w, 503, "memory_unavailable")
		return
	}
	body, err := json.Marshal(snapshot)
	if err != nil || len(body) > memory.ExportMaxBytes || ctx.Err() != nil {
		failure(w, 503, "memory_unavailable")
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		failure(w, 503, "memory_unavailable")
		return
	}
	defer controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
	// A panicking writer may already have emitted bytes. Do not let the outer
	// handler's panic boundary append a second JSON document to this response.
	committed = true
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(body, '\n'))
}
