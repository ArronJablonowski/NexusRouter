package cli

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestBrowserToolPresenterNarrowConfiguredScope(t *testing.T) {
	for _, test := range []struct {
		name                     string
		web, create, comfy, want bool
	}{{"create_file", true, true, false, true}, {"create_file", true, false, true, false}, {"create_file", false, true, false, false}, {"comfy_submit", true, false, true, true}, {"comfy_cancel_pending", true, true, false, false}, {"replace_file", true, true, true, false}, {"read_file", true, true, true, false}} {
		s := config.Defaults()
		s.WebUI.Enabled = test.web
		s.Tools.Enabled = true
		s.Tools.CreateEnabled = test.create
		s.ComfyUI.Enabled = test.comfy
		e := browserToolPresenter(s)(context.Background(), tools.ApprovalPrompt{Request: approvals.Request{ToolName: test.name}})
		if (e == nil) != test.want {
			t.Fatal(test, e)
		}
	}
}
