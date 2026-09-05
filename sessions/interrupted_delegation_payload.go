package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func recoveryArgumentCount(name string, raw []byte) (int, error) {
	if len(raw) > 512<<10 || !utf8.Valid(raw) {
		return 0, ErrHistory
	}
	var items []json.RawMessage
	if name == "delegate_batch" {
		fields, err := recoveryObject(raw, "tasks")
		if err != nil || json.Unmarshal(fields["tasks"], &items) != nil || len(items) < 2 || len(items) > 4 {
			return 0, ErrHistory
		}
	} else {
		items = []json.RawMessage{raw}
	}
	for _, item := range items {
		fields, err := recoveryObject(item, "prompt", "validation")
		var prompt, validation string
		if err != nil || json.Unmarshal(fields["prompt"], &prompt) != nil || json.Unmarshal(fields["validation"], &validation) != nil || len(prompt) > 16<<10 || strings.TrimSpace(prompt) == "" || (validation != "text" && validation != "go_source") {
			return 0, ErrHistory
		}
	}
	return len(items), nil
}

func recoveryObject(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrHistory
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || fields[name] != nil {
			return nil, ErrHistory
		}
		allowed := false
		for _, want := range names {
			if name == want {
				allowed = true
			}
		}
		if !allowed {
			return nil, ErrHistory
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrHistory
		}
		fields[name] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(fields) != len(names) {
		return nil, ErrHistory
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrHistory
	}
	return fields, nil
}

type recoveryFailureEvidence struct {
	TaskID   string       `json:"task_id"`
	Sequence int64        `json:"sequence"`
	Kind     runtime.Kind `json:"kind"`
	Code     string       `json:"code"`
}

func recoveryWorkResult(work, execution []runtime.Event) (json.RawMessage, error) {
	text, accepted, err := projectWorkerTerminal(work)
	if err != nil {
		return nil, ErrHistory
	}
	var child TerminalOutcome
	if len(execution) > 0 {
		snapshot, replayErr := Replay(context.Background(), terminalReader(execution), execution[0].TaskID)
		if replayErr != nil || snapshot.UncertainEffects || len(snapshot.Pending) != 0 {
			return nil, ErrHistory
		}
		child, err = ProjectTerminalSubmission(execution)
		if err != nil {
			return nil, ErrHistory
		}
	}
	if accepted {
		if child.State != "succeeded" || child.Result == nil || child.Result.Text != text {
			return nil, ErrHistory
		}
		return json.Marshal(struct {
			WorkID      string `json:"work_task_id"`
			ExecutionID string `json:"execution_task_id"`
			Output      string `json:"untrusted_output"`
		}{work[0].TaskID, execution[0].TaskID, text})
	}
	project := func(history []runtime.Event) (recoveryFailureEvidence, error) {
		e := history[len(history)-1]
		if e.Kind != runtime.TaskFailed && e.Kind != runtime.TaskCanceled {
			return recoveryFailureEvidence{}, ErrHistory
		}
		switch e.Data.Code {
		case "worker_failed", "canceled", "invalid_output", "empty_output", "execution_failed", "budget_exhausted", "provider_retryable_no_output", "execution_lease_lost":
		default:
			return recoveryFailureEvidence{}, ErrHistory
		}
		return recoveryFailureEvidence{e.TaskID, e.Sequence, e.Kind, e.Data.Code}, nil
	}
	w, err := project(work)
	if err != nil {
		return nil, err
	}
	report := struct {
		Version     int                       `json:"version"`
		Error       string                    `json:"error"`
		Reason      string                    `json:"reason"`
		WorkID      string                    `json:"work_task_id"`
		ExecutionID string                    `json:"execution_task_id,omitempty"`
		Evidence    []recoveryFailureEvidence `json:"evidence"`
	}{Version: 1, Error: "delegate_unavailable_or_rejected", Reason: w.Code, WorkID: work[0].TaskID, Evidence: []recoveryFailureEvidence{w}}
	if len(execution) > 0 {
		report.ExecutionID = execution[0].TaskID
		if child.State != "succeeded" {
			e, err := project(execution)
			if err != nil {
				return nil, err
			}
			report.Evidence = append(report.Evidence, e)
			report.Reason = e.Code
		}
	}
	if work[len(work)-1].Kind == runtime.TaskCanceled {
		report.Reason = "canceled"
	}
	body, err := json.Marshal(report)
	if err != nil || len(body) > 2048 {
		return nil, ErrHistory
	}
	return body, nil
}
