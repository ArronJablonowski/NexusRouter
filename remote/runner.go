package remote

import "context"

// RunnerStatus contains no executable, credentials, or model filesystem paths.
type RunnerStatus struct {
	Version int    `json:"version"`
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	ModelID string `json:"model_id,omitempty"`
}

func (s RunnerStatus) Validate() error {
	if s.Version != 1 || s.ModelID != "" && !name(s.ModelID) {
		return ErrInvalid
	}
	switch s.State {
	case "disabled", "active", "inactive", "activating", "deactivating", "failed", "unavailable":
		return nil
	}
	return ErrInvalid
}
func (c *Client) Runner(ctx context.Context, destination, action string) (RunnerStatus, error) {
	var out RunnerStatus
	if action != "status" && action != "start" && action != "stop" {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, "runner", "POST", "/v1/remote/runner/"+action, nil, nil, &out)
	if err == nil {
		err = out.Validate()
	}
	return out, err
}
func (b *SDKBackend) Runner(ctx context.Context, action string) (RunnerStatus, error) {
	if b.ControlRunner == nil {
		return RunnerStatus{Version: 1, State: "disabled"}, nil
	}
	return b.ControlRunner(ctx, action)
}
