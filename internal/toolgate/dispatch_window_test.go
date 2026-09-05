package toolgate

import (
	"context"
	"testing"
	"time"
)

func TestDispatchWindowRejectsObservableExpiry(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name            string
		approval, lease time.Time
		cancel          bool
		want            bool
	}{
		{"live", now.Add(time.Second), now.Add(time.Second), false, true},
		{"approval_boundary", now, now.Add(time.Second), false, false},
		{"approval_expired", now.Add(-time.Nanosecond), now.Add(time.Second), false, false},
		{"lease_boundary", now.Add(time.Second), now, false, false},
		{"lease_expired", now.Add(time.Second), now.Add(-time.Nanosecond), false, false},
		{"canceled", now.Add(time.Second), now.Add(time.Second), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if got := dispatchWindow(ctx, now, tc.approval, tc.lease); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}
