package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"time"
)

// Usage and UsageCount are the token-only receipt contract exposed by remote hosts.
type Usage = usagestats.RemoteUsage
type UsageCount = usagestats.Count

func (c *Client) recordUsagePage(ctx context.Context, destination string, page TaskPage) error {
	if !id(page.Caller) {
		return ErrUnavailable
	}
	caller := page.Caller
	var e error
	for _, task := range page.Tasks {
		u := task.Usage
		if task.Usage == nil {
			e = usagestats.SaveMissingRemoteUsage(ctx, c.UsageFile, destination, caller, task.RequestID, task.State)
			if e != nil {
				return e
			}
			continue
		}
		if e = usagestats.SaveRemoteUsage(ctx, c.UsageFile, destination, caller, task.RequestID, task.State, *u); e != nil {
			return e
		}
	}
	return nil
}

// SyncUsage reconciles only this authenticated caller's owned requests. It
// never dispatches, retries inference, cancels work or changes quality feedback.
func (c *Client) SyncUsage(ctx context.Context) error {
	registry, e := c.Trust.Read()
	if e != nil {
		return e
	}
	var failures []error
	for _, p := range registry.Peers {
		if !p.permits("inspect") {
			continue
		}
		after := ""
		for {
			page, e := c.Tasks(ctx, p.ID, after)
			if e != nil {
				failures = append(failures, e)
				break
			}
			for _, t := range page.Tasks {
				if t.Usage == nil {
					failures = append(failures, ErrUnavailable)
				}
			}
			if !page.HasMore {
				break
			}
			after = page.Next
		}
	}
	return errors.Join(failures...)
}
func (c *Client) RunUsageSync(ctx context.Context) {
	for {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Minute)
		e := c.SyncUsage(attempt)
		cancel()
		if ctx.Err() != nil {
			return
		}
		mark, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = usagestats.MarkRemoteSync(mark, c.UsageFile, e != nil)
		cancel()
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
