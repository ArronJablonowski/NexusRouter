package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"strings"
	"testing"
)

func TestComfyTerminalApprovalExactAndPrivate(t *testing.T) {
	s, p := chatApprovalFixture(t, "")
	s.ComfyUI = config.ComfyUI{Enabled: true, Endpoint: "http://127.0.0.1:8188", ReceiptDirectory: "/private/receipts"}
	p.Request.ToolName = "comfy_submit"
	p.Request.Scope = "comfyui"
	p.Request.ToolBehavior = tools.BehaviorNonIdempotentWrite
	p.Arguments = []byte(`{"key":"chat-test","workflow":{"1":{"class_type":"EmptyImage","inputs":{}}}}`)
	d := sha256.Sum256(p.Arguments)
	p.Request.ArgumentsDigest = hex.EncodeToString(d[:])
	preview, e := chatApprovalPreview(s, nil, p)
	if e != nil || !strings.Contains(preview, "EmptyImage") || !strings.Contains(preview, "/approve") {
		t.Fatal(preview, e)
	}
	if _, e = chatApprovalPreview(s, func(name string) string {
		if name == "NEXUS_API_TOKEN" {
			return "EmptyImage"
		}
		return ""
	}, p); e == nil {
		t.Fatal("known secret shown")
	}
	p.Arguments = []byte(`{"key":"a","key":"b"}`)
	d = sha256.Sum256(p.Arguments)
	p.Request.ArgumentsDigest = hex.EncodeToString(d[:])
	if _, e = chatApprovalPreview(s, nil, p); e == nil {
		t.Fatal("duplicate fields shown")
	}
}
