package approvals

// ScopeLeaseObservation describes overlapping unreleased holders in one read
// snapshot, without exposing lease capabilities or holder identities. Version 1
// counts at most 1,000 holders. Overlap policy 1 uses exact generic scopes and
// one reserved filesystem family: workspace and every create_* scope.
// Both live and expired holders conflict. Zero counts are not authorization or
// proof of continued availability; this observation cannot prove termination.
type ScopeLeaseObservation struct {
	Version              int `json:"version"`
	OverlapPolicyVersion int `json:"overlap_policy_version"`
	LiveReaders          int `json:"live_readers"`
	ExpiredReaders       int `json:"expired_readers"`
	LiveWriters          int `json:"live_writers"`
	ExpiredWriters       int `json:"expired_writers"`
}

func (s ScopeLeaseObservation) Validate() error {
	if s.Version != 1 || s.OverlapPolicyVersion != 1 {
		return ErrInvalid
	}
	total := 0
	for _, count := range []int{s.LiveReaders, s.ExpiredReaders, s.LiveWriters, s.ExpiredWriters} {
		if count < 0 || count > 1000 {
			return ErrInvalid
		}
		total += count
	}
	if total > 1000 {
		return ErrInvalid
	}
	return nil
}
