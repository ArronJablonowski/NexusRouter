package remote

import (
	"sync"
	"time"
)

// RequestLimits bounds each authenticated peer's requests per operation per
// minute. Cancellation has its own allowance. Nil policy uses bounded defaults.
type RequestLimits struct {
	Info     int `json:"info"`
	Dispatch int `json:"dispatch"`
	Inspect  int `json:"inspect"`
	Cancel   int `json:"cancel"`
}

func (l RequestLimits) valid() bool {
	for _, n := range []int{l.Info, l.Dispatch, l.Inspect, l.Cancel} {
		if n < 1 || n > 10000 {
			return false
		}
	}
	return true
}
func (l RequestLimits) count(op string) int {
	switch op {
	case "runner":
		return 6
	case "logs":
		return l.Info // separate bucket, same conservative configured allowance
	case "info":
		return l.Info
	case "dispatch":
		return l.Dispatch
	case "inspect":
		return l.Inspect
	case "cancel":
		return l.Cancel
	}
	return 0
}

type requestWindow struct {
	start    time.Time
	count    int
	reported bool
}
type requestLimiter struct {
	mu    sync.Mutex
	peers map[string]map[string]requestWindow
}

// Windows begin with the first request and use monotonic elapsed time. Policy
// changes retain consumed counts; peer removal discards its counters. Only
// registry identities allocate buckets, bounded by registry and operation limits.
func (l *requestLimiter) allow(reg Registry, peer Peer, op string, now time.Time) (bool, bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.peers == nil {
		l.peers = make(map[string]map[string]requestWindow)
	}
	current := make(map[string]bool, len(reg.Peers))
	for _, p := range reg.Peers {
		current[p.ID] = true
	}
	for id := range l.peers {
		if !current[id] {
			delete(l.peers, id)
		}
	}
	if !current[peer.ID] {
		return false, false, 60
	}
	policy := RequestLimits{Info: 60, Dispatch: 60, Inspect: 600, Cancel: 120}
	if peer.RequestLimits != nil {
		policy = *peer.RequestLimits
	}
	limit := policy.count(op)
	if limit < 1 {
		return false, false, 60
	}
	if l.peers[peer.ID] == nil {
		l.peers[peer.ID] = make(map[string]requestWindow)
	}
	w := l.peers[peer.ID][op]
	if w.start.IsZero() || now.Sub(w.start) >= time.Minute {
		w = requestWindow{start: now}
	}
	allowed := w.count < limit
	first := false
	if allowed {
		w.count++
	} else if !w.reported {
		w.reported = true
		first = true
	}
	l.peers[peer.ID][op] = w
	remaining := time.Minute - now.Sub(w.start)
	if remaining > time.Minute {
		remaining = time.Minute
	}
	retry := int((remaining + time.Second - 1) / time.Second)
	if retry < 1 {
		retry = 1
	}
	return allowed, first, retry
}
