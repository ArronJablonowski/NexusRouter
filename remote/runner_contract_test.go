package remote

import "testing"

func TestRunnerStatusRejectsUnknownState(t *testing.T) {
	for _, state := range []string{"active", "inactive", "activating", "deactivating", "failed", "disabled", "unavailable"} {
		if (RunnerStatus{Version: 1, State: state}).Validate() != nil {
			t.Fatal(state)
		}
	}
	for _, s := range []RunnerStatus{{Version: 1, State: "ready"}, {Version: 2, State: "active"}, {Version: 1, State: "active", ModelID: "unsafe\nname"}} {
		if s.Validate() == nil {
			t.Fatal("invalid runner response accepted")
		}
	}
}
