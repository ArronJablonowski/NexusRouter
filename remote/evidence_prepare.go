package remote

import "os"

// PrepareOutcomeEvidence checks the private root and its ability to durably
// create/remove a small file before an optional dispatch-and-review workflow.
// It creates no receipt, evaluation attempt or quality vote. Later persistence
// still rechecks the directory: this is not a promise against disk exhaustion,
// permission changes or storage failure after original work has been admitted.
func PrepareOutcomeEvidence(root string) error {
	directory, err := OpenRouteStore(root)
	if err != nil {
		return err
	}
	probe, err := os.CreateTemp(directory.directory, ".review-storage-check-*")
	if err != nil {
		return err
	}
	path := probe.Name()
	defer os.Remove(path)
	if _, err = probe.Write([]byte("nexus-review-storage-v1\n")); err != nil {
		probe.Close()
		return err
	}
	if err = probe.Sync(); err != nil {
		probe.Close()
		return err
	}
	if err = probe.Close(); err != nil {
		return err
	}
	if err = directory.syncDirectory(); err != nil {
		return err
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	return directory.syncDirectory()
}
