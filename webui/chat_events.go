package webui

// The durable chat stream intentionally contains only presentation state.
// Provider/model identities, prompts, tool arguments/results, route candidates,
// worker output, and error detail remain behind the browser BFF boundary.

type ModelChangedData struct {
	TaskID string `json:"task_id"`
	TurnID string `json:"turn_id"`
	State  string `json:"state"`
}

func (d ModelChangedData) Validate() error {
	if !validID(d.TaskID) || !validID(d.TurnID) || (d.State != "started" && d.State != "completed") {
		return ErrContract
	}
	return nil
}

type ToolChangedData struct {
	TaskID     string `json:"task_id"`
	TurnID     string `json:"turn_id"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	State      string `json:"state"`
	Effect     string `json:"effect,omitempty"`
	Code       string `json:"code,omitempty"`
}

func (d ToolChangedData) Validate() error {
	if !validID(d.TaskID) || !validID(d.TurnID) || !validID(d.ToolCallID) || !validID(d.ToolName) || !optionalID(d.Code) {
		return ErrContract
	}
	switch d.State {
	case "started":
		if d.Effect != "" || d.Code != "" {
			return ErrContract
		}
	case "completed":
		if d.Effect != "none" && d.Effect != "confirmed" && d.Effect != "uncertain" {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return nil
}

type RouteChangedData struct {
	TaskID  string `json:"task_id"`
	RouteID string `json:"route_id"`
	State   string `json:"state"`
}

func (d RouteChangedData) Validate() error {
	if !validID(d.TaskID) || !validID(d.RouteID) || d.State != "selected" {
		return ErrContract
	}
	return nil
}

type WorkerChangedData struct {
	TaskID   string `json:"task_id"`
	WorkerID string `json:"worker_id"`
	State    string `json:"state"`
}

func (d WorkerChangedData) Validate() error {
	if !validID(d.TaskID) || !validID(d.WorkerID) {
		return ErrContract
	}
	switch d.State {
	case "started", "heartbeat", "completed":
		return nil
	default:
		return ErrContract
	}
}

type ErrorChangedData struct {
	TaskID string `json:"task_id"`
	Code   string `json:"code"`
}

func (d ErrorChangedData) Validate() error {
	if !validID(d.TaskID) || !validID(d.Code) {
		return ErrContract
	}
	return nil
}

type TaskTerminalData struct {
	TaskID string `json:"task_id"`
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
}

func (d TaskTerminalData) Validate() error {
	if !validID(d.TaskID) || !optionalID(d.Code) {
		return ErrContract
	}
	switch d.State {
	case "completed":
		if d.Code != "" {
			return ErrContract
		}
	case "failed", "canceled":
	default:
		return ErrContract
	}
	return nil
}
