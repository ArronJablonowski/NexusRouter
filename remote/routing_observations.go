package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"time"
)

// RecordedRoutingObservations reads the commander's ledger, never host-reported
// scores. Certificate rotation cannot borrow the preceding caller's evidence.
func (c *Client) RecordedRoutingObservations(ctx context.Context, root, caller, destination string, identity harness.Identity, task harness.TaskClass, key routing.Key) (routing.ObservationSet, error) {
	cert, _, err := c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 || certificateDigest(cert.Certificate[0]) != caller {
		return routing.ObservationSet{}, ErrConflict
	}
	snapshot, err := readDestinationEvidence(ctx, root, destination, caller, time.Now().UTC())
	if err != nil {
		return routing.ObservationSet{}, err
	}
	return snapshot.RoutingObservations(identity, task, key), nil
}
