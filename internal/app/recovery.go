package app

import (
	"context"
	"time"

	"darwinrouter/submissions"
)

func (d *Dispatcher) reconcile(ctx context.Context, configDigest string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	after := ""
	for ctx.Err() == nil {
		d.supervisorHeartbeat(-1)
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		next, err := d.recoverPage(query, configDigest, after)
		cancel()
		d.supervisorHeartbeat(-1)
		if err != nil {
			if ctx.Err() == nil {
				d.recordError()
			}
		} else {
			after = next
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// One bounded page per tick avoids monopolizing the writer even when a large
// legacy running population is ineligible for recovery.
func (d *Dispatcher) recoverPage(ctx context.Context, configDigest, after string) (string, error) {
	page, err := d.db.ListSubmissions(ctx, submissions.ListOptions{State: "running", After: after, Limit: 100})
	if err != nil {
		return after, err
	}
	for _, item := range page.Items {
		if item.ConfigDigest != configDigest || !item.LeaseExpired {
			continue
		}
		var err error
		if len(item.TaskIDs) == 0 {
			_, err = d.db.RecoverUndispatched(ctx, item.ID, configDigest, time.Now().UTC())
		} else {
			_, err = d.db.RecoverTerminalSubmission(ctx, item.ID, configDigest, time.Now().UTC())
		}
		if err != nil {
			return after, err
		}
	}
	if page.HasMore {
		return page.NextCursor, nil
	}
	return "", nil
}
