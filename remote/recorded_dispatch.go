package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func certificateDigest(der []byte) string { s := sha256.Sum256(der); return hex.EncodeToString(s[:]) }

// DispatchRecorded persists destination, caller certificate and exact task digest
// before sending. It never reroutes, deletes a binding or retries automatically.
// On uncertain delivery use Lookup and the same destination/key/task/credential.
func (c *Client) DispatchRecorded(ctx context.Context, store *RouteStore, destination, key string, task Task) (submissions.Status, error) {
	var out submissions.Status
	if c == nil || store == nil || ctx == nil || ctx.Err() != nil || !requestID(key) || task.Validate() != nil {
		return out, ErrInvalid
	}
	if task.ExpectedHarnessIdentity != nil {
		snapshot := *task.ExpectedHarnessIdentity
		task.ExpectedHarnessIdentity = &snapshot
	}
	reg, err := c.Trust.Read()
	if err != nil {
		return out, err
	}
	peer, err := reg.peer(destination)
	if err != nil || !peer.permits("dispatch") || !peer.permitsTask(task) {
		return out, ErrDenied
	}
	cert, _, err := c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 {
		return out, ErrDenied
	}
	pin := certificateDigest(cert.Certificate[0])
	binding := RouteBinding{Version, key, destination, pin, hash(task)}
	if err = store.Bind(binding); err != nil {
		return out, err
	}
	err = c.callPinned(ctx, destination, "dispatch", "POST", "/v1/remote/tasks/"+key, &task, nil, &out, pin)
	return out, err
}
