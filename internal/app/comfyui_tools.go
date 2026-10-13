package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/connectors/comfyui"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func registerComfyTools(registry *tools.Registry, cfg config.ComfyUI) error {
	service, e := comfyui.OpenService(cfg.Endpoint, cfg.ReceiptDirectory)
	if e != nil {
		return ErrAdmission
	}
	type args struct {
		Key      string          `json:"key"`
		Kind     string          `json:"kind"`
		Node     string          `json:"node"`
		Workflow json.RawMessage `json:"workflow"`
	}
	specs := []struct {
		name, description, schema string
		write                     bool
	}{
		{"comfy_inspect", "Inspect configured ComfyUI info, owned jobs, or one installed node schema (kind=node, node=class name) before designing a workflow. Never selects a host.", `{"type":"object","properties":{"kind":{"enum":["info","jobs","node"]},"node":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,128}$"}},"required":["kind"],"allOf":[{"if":{"properties":{"kind":{"const":"node"}}},"then":{"required":["node"]}}],"additionalProperties":false}`, false},
		{"comfy_status", "Observe an owned ComfyUI job using its original submission key; no retry or cancellation.", `{"type":"object","properties":{"key":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,128}$"}},"required":["key"],"additionalProperties":false}`, false},
		{"comfy_submit", "Submit a trusted API-format ComfyUI workflow after exact operator approval. Use a unique key; reusing it only observes the same graph's saved receipt. Uncertain/prepared jobs MUST NOT be resent under a new key. Call comfy_status for images/output metadata.", `{"type":"object","properties":{"key":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,128}$"},"workflow":{"type":"object","minProperties":1,"maxProperties":256}},"required":["key","workflow"],"additionalProperties":false}`, true},
		{"comfy_cancel_pending", "Request removal of this owned job from pending queue after operator approval. Does not interrupt running work.", `{"type":"object","properties":{"key":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,128}$"}},"required":["key"],"additionalProperties":false}`, true},
	}
	for _, spec := range specs {
		def := tools.Definition{Tool: providers.Tool{Name: spec.name, Description: spec.description, Parameters: json.RawMessage(spec.schema)}, Scope: "comfyui", ReadOnly: !spec.write, Behavior: runtime.BehaviorReadOnly}
		def.ResolveScope = func(raw json.RawMessage) (string, error) {
			if len(raw) > 48<<10 || contract.RejectDuplicateJSONFields(raw) != nil {
				return "", tools.ErrArguments
			}
			return "comfyui", nil
		}
		if spec.write {
			def.Behavior = runtime.BehaviorNonIdempotentWrite
		}
		def.Handler = func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			out := runtime.ToolResult{Effect: runtime.NoEffect}
			var a args
			if json.Unmarshal(raw, &a) != nil || len(raw) > 128<<10 {
				return out, tools.ErrArguments
			}
			var value any
			var e error
			switch spec.name {
			case "comfy_inspect":
				if a.Kind == "node" {
					value, e = service.Client.NodeInfo(ctx, a.Node)
				} else if a.Kind == "jobs" {
					value, e = service.List()
				} else {
					value, e = service.Client.Read(ctx, "info")
				}
			case "comfy_status":
				value, e = service.Status(ctx, a.Key)
			case "comfy_submit":
				var w comfyui.Workflow
				w, e = comfyui.ParseWorkflow(a.Workflow)
				if e == nil {
					var j comfyui.Job
					j, e = service.Submit(ctx, a.Key, w)
					value = j
					if j.State != "" && !j.Replayed {
						out.Effect = runtime.ConfirmedEffect
					}
					if !j.Replayed && (errors.Is(e, comfyui.ErrUncertain) || j.State == "prepared" || j.State == "uncertain") {
						out.Effect = runtime.UncertainEffect
					}
				}
			case "comfy_cancel_pending":
				e = service.CancelPending(ctx, a.Key)
				if e == nil {
					out.Effect = runtime.ConfirmedEffect
				} else if errors.Is(e, comfyui.ErrUncertain) {
					out.Effect = runtime.UncertainEffect
				}
				value = map[string]string{"key": a.Key, "state": "pending_delete_requested"}
			}
			if e != nil {
				out.Failed = true
				out.Content = "ComfyUI operation failed; inspect saved job state before taking further action"
				return out, nil
			}
			b, e := json.Marshal(value)
			if e != nil || len(b) > 256<<10 {
				out.Failed = true
				out.Content = "ComfyUI result exceeds tool output limit"
				return out, nil
			}
			out.Content = string(b)
			return out, nil
		}
		if registry.Register(def) != nil {
			return ErrAdmission
		}
	}
	return nil
}
