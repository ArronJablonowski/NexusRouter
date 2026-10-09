package harness

import "github.com/ArronJablonowski/NexusRouter/routing"

// RoutingObservations projects only canonical current review heads for one exact
// execution identity and task scope. Completion alone contributes no accuracy.
// Verdict accuracy is used for cross-host comparison; AI reviews stay advisory.
func (s *Snapshot) RoutingObservations(identity Identity, task TaskClass, key routing.Key) routing.ObservationSet {
	out := routing.ObservationSet{}
	if s == nil || key.Model != identity.Model || key.Domain != task.Domain || key.Profile != task.Profile {
		return out
	}
	for _, id := range s.order {
		b := s.entries[id]
		e, h := b.execution, b.head
		if e.Actual != identity || e.Task != task || e.Status != "completed" || h == nil || (h.Verdict != "passed" && h.Verdict != "failed") {
			continue
		}
		quality := 0.0
		if h.Verdict == "passed" {
			quality = 1
		}
		observationID := "remote-review-" + id
		if h.Method == "automated_ai" || h.Confidence < 1 {
			out.Advisory = append(out.Advisory, routing.AdvisoryObservation{ID: observationID, Key: key, Quality: quality, Confidence: h.Confidence, Time: e.CompletedAt})
		} else {
			out.Fitness = append(out.Fitness, routing.FitnessObservation{ID: observationID, BaseID: observationID, Key: key, Quality: quality, Reliability: true, Time: e.CompletedAt})
		}
	}
	return out
}
