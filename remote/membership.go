package remote

// Pair registers one explicitly scoped peer without changing existing members.
// It is an offline local administrator operation, not network discovery or
// proof that a peer owns a certificate. The administrator must independently
// verify the supplied identity and transport configuration before calling it.
func (f TrustFile) Pair(peer Peer, expected string) (Registry, error) {
	if peer.Validate() != nil {
		return Registry{}, ErrInvalid
	}
	next := Registry{Version: Version}
	if expected != "absent" {
		current, err := f.Read()
		if err != nil {
			return Registry{}, err
		}
		if current.Digest() != expected {
			return Registry{}, ErrConflict
		}
		next = current
	}
	for _, member := range next.Peers {
		if member.ID == peer.ID {
			return Registry{}, ErrConflict
		}
	}
	next.Peers = append(next.Peers, peer)
	if err := f.Replace(next, expected); err != nil {
		return Registry{}, err
	}
	return next, nil
}

// Revoke removes exactly one peer, preserving all other scopes and identities.
// It does not cancel previously admitted tasks. A retry after an uncertain write
// must first reread the registry; missing members and stale digests conflict.
func (f TrustFile) Revoke(instance, expected string) (Registry, error) {
	if !id(instance) || expected == "" || expected == "absent" {
		return Registry{}, ErrInvalid
	}
	current, err := f.Read()
	if err != nil {
		return Registry{}, err
	}
	if current.Digest() != expected {
		return Registry{}, ErrConflict
	}
	found := false
	next := Registry{Version: current.Version, Peers: make([]Peer, 0, len(current.Peers))}
	for _, member := range current.Peers {
		if member.ID == instance {
			found = true
			continue
		}
		next.Peers = append(next.Peers, member)
	}
	if !found {
		return Registry{}, ErrConflict
	}
	if err = f.Replace(next, expected); err != nil {
		return Registry{}, err
	}
	return next, nil
}
