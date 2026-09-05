package app

import (
	"time"

	"darwinrouter/health"
)

func (d *Dispatcher) supervisorTime() time.Time {
	if d.healthNow != nil {
		return d.healthNow()
	}
	return time.Now()
}
func (d *Dispatcher) supervisorStarted(id int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.supervisorTime()
	if id == -1 {
		d.reconcilerStarted = true
		d.reconcilerAlive = true
		d.reconcilerBeat = now
		return
	}
	if d.workerAlive == nil {
		d.workerAlive = map[int]bool{}
		d.workerBeats = map[int]time.Time{}
	}
	d.startedWorkers++
	d.workerAlive[id] = true
	d.workerBeats[id] = now
}
func (d *Dispatcher) supervisorStopped(id int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == -1 {
		d.reconcilerAlive = false
	} else {
		d.workerAlive[id] = false
	}
}
func (d *Dispatcher) supervisorHeartbeat(id int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == -1 {
		if d.reconcilerAlive {
			d.reconcilerBeat = d.supervisorTime()
		}
	} else if d.workerAlive[id] {
		d.workerBeats[id] = d.supervisorTime()
	}
}

// Health describes supervisor liveness, not whether a provider call succeeds.
// Each worker heartbeat continues during inference through lease renewal.
func (d *Dispatcher) Health() health.Check {
	check := health.Check{Component: "supervisor", Status: "unknown", Code: "supervisor_starting"}
	if d == nil {
		return check
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		check.Status = "unavailable"
		check.Code = "supervisor_stopped"
		return check
	}
	if d.closing {
		check.Status = "unavailable"
		check.Code = "supervisor_stopping"
		return check
	}
	if d.err != nil {
		check.Status = "degraded"
		check.Code = "supervisor_error"
		return check
	}
	if d.configuredWorkers < 1 || d.startedWorkers < d.configuredWorkers || !d.reconcilerStarted {
		return check
	}
	check.Status = "degraded"
	check.Code = "supervisor_stalled"
	now := d.supervisorTime()
	if !d.reconcilerAlive || d.reconcilerBeat.IsZero() || now.Sub(d.reconcilerBeat) > 15*time.Second {
		return check
	}
	for id := 0; id < d.configuredWorkers; id++ {
		if !d.workerAlive[id] || d.workerBeats[id].IsZero() || now.Sub(d.workerBeats[id]) > 15*time.Second {
			return check
		}
	}
	check.Status = "healthy"
	check.Code = "supervisor_ok"
	return check
}
