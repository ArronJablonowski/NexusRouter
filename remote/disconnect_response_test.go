package remote

import (
	"net"
	"net/http"
)

// Simulate a severed destination connection after the handler has committed its
// mutation, just as it attempts to send response headers. This is a real TLS
// socket close (not a fabricated backend error), also carried over native SSH.
type disconnectResponse struct {
	http.ResponseWriter
	closed bool
}

func (w *disconnectResponse) WriteHeader(int) {
	if w.closed {
		return
	}
	w.closed = true
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		conn, _, err := h.Hijack()
		if err == nil {
			conn.Close()
		}
	}
}
func (w *disconnectResponse) Write([]byte) (int, error) { w.WriteHeader(200); return 0, net.ErrClosed }
