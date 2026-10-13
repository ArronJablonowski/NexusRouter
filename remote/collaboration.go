package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"net/http"
	"strconv"
)

// Collaboration export uses the existing explicit instance-wide logs grant.
// Ordinary inspect permission cannot read another caller's model messages.
func (c *Client) Collaboration(ctx context.Context, destination, stream string, after int64) (agentchat.Batch, error) {
	var b agentchat.Batch
	if after < 0 || len(stream) > 32 {
		return b, ErrInvalid
	}
	err := c.call(ctx, destination, "logs", "GET", "/v1/remote/collaboration", nil, map[string]string{"X-Nexus-Collaboration-Stream": stream, "X-Nexus-Collaboration-After": strconv.FormatInt(after, 10)}, &b)
	if err == nil && (b.Validate() != nil || b.After != after || (stream != "" && stream != b.Stream)) {
		err = ErrConflict
	}
	return b, err
}
func (s *Server) collaborationPage(ctx context.Context, r *http.Request) (agentchat.Batch, error) {
	after, e := strconv.ParseInt(r.Header.Get("X-Nexus-Collaboration-After"), 10, 64)
	stream := r.Header.Get("X-Nexus-Collaboration-Stream")
	if e != nil || after < 0 || len(stream) > 32 {
		return agentchat.Batch{}, ErrInvalid
	}
	backend, ok := s.backend.(interface {
		Collaboration(context.Context, string, int64) (agentchat.Batch, error)
	})
	if !ok {
		return agentchat.Batch{}, ErrUnavailable
	}
	b, e := backend.Collaboration(ctx, stream, after)
	if e == nil && b.Validate() != nil {
		e = ErrUnavailable
	}
	return b, e
}
