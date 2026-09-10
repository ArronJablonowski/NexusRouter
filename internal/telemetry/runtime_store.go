package telemetry

// RuntimeStore exposes the exact already-open store to trusted in-process
// admission compositions that require one SQLite transaction across runtime
// and workboard records. Embedded Store wrappers promote this method; callers
// still verify the configured database's filesystem identity before dispatch.
func (s *Store) RuntimeStore() *Store { return s }
