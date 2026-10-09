package remote

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// OpenExistingRouteStore opens evidence for inspection without creating a
// directory or syncing/writing records. Missing or unsafe evidence fails closed.
func OpenExistingRouteStore(directory string) (*RouteStore, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	store := &RouteStore{directory: directory}
	if err := store.check(); err != nil {
		return nil, err
	}
	return store, nil
}

type RecordedRequestStatus struct {
	Version     int                `json:"version"`
	RequestID   string             `json:"request_id"`
	Destination string             `json:"destination"`
	Status      submissions.Status `json:"status"`
}

// InspectRecorded reads status at the original bound destination with the
// original caller certificate. It never discovers, repairs, rebinds, submits,
// cancels, reviews or needs a prompt. This is status inspection, not verification
// that a supplied task or automatic request matches its original intent.
func (c *Client) InspectRecorded(ctx context.Context, routes *RouteStore, key string) (RecordedRequestStatus, error) {
	var out RecordedRequestStatus
	if c == nil || ctx == nil || ctx.Err() != nil || routes == nil || !requestID(key) {
		return out, ErrInvalid
	}
	binding, err := routes.Lookup(key)
	if err != nil {
		return out, err
	}
	var status submissions.Status
	err = c.callPinned(ctx, binding.Destination, "inspect", "GET", "/v1/remote/tasks/"+key, nil, nil, &status, binding.CallerFingerprint)
	if err != nil {
		return out, err
	}
	if status.Version != 1 || !name(status.ID) || len(status.TaskIDs) > 128 || !slices.Contains([]string{"queued", "running", "succeeded", "failed", "canceled"}, status.State) {
		return out, ErrUnavailable
	}
	return RecordedRequestStatus{Version: Version, RequestID: key, Destination: binding.Destination, Status: status}, nil
}

// CancelRecorded cancels only the original destination with the original caller
// certificate. A rotated credential or changed binding never gains retry authority.
func (c *Client) CancelRecorded(ctx context.Context, routes *RouteStore, key string) (submissions.Status, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || routes == nil || !requestID(key) {
		return submissions.Status{}, ErrInvalid
	}
	binding, err := routes.Lookup(key)
	if err != nil {
		return submissions.Status{}, err
	}
	var status submissions.Status
	err = c.callPinned(ctx, binding.Destination, "cancel", "POST", "/v1/remote/tasks/"+key+"/cancel", nil, nil, &status, binding.CallerFingerprint)
	if err == nil && (status.Version != 1 || !name(status.ID) || !slices.Contains([]string{"queued", "running", "succeeded", "failed", "canceled"}, status.State)) {
		return submissions.Status{}, ErrUnavailable
	}
	return status, err
}
