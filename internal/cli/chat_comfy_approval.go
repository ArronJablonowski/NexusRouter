package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"strconv"
	"strings"
	"unicode/utf8"
)

func chatComfyApprovalPreview(s config.Settings, secret func(string) string, p tools.ApprovalPrompt) (string, error) {
	if !s.ComfyUI.Enabled || p.Request.Validate() != nil || p.Request.Scope != "comfyui" || p.Request.ToolBehavior != tools.BehaviorNonIdempotentWrite || len(p.Arguments) > 64<<10 || !utf8.Valid(p.Arguments) || webui.RejectDuplicateJSONFields(p.Arguments) != nil {
		return "", tools.ErrDenied
	}
	d := sha256.Sum256(p.Arguments)
	if hex.EncodeToString(d[:]) != p.Request.ArgumentsDigest {
		return "", tools.ErrDenied
	}
	var a any
	if json.Unmarshal(p.Arguments, &a) != nil {
		return "", tools.ErrDenied
	}
	values := []string{s.ComfyUI.Endpoint}
	var collect func(any)
	collect = func(value any) {
		switch v := value.(type) {
		case string:
			values = append(values, v)
		case []any:
			for _, x := range v {
				collect(x)
			}
		case map[string]any:
			for k, x := range v {
				values = append(values, k)
				collect(x)
			}
		}
	}
	collect(a)
	if secret != nil {
		envs := append([]string{"NEXUS_API_TOKEN", "DARWIN_API_TOKEN"}, s.Security.RedactEnv...)
		for _, provider := range s.Providers {
			envs = append(envs, provider.APIKeyEnv)
		}
		for _, name := range envs {
			if value := secret(name); value != "" {
				for _, text := range values {
					if strings.Contains(text, value) {
						return "", tools.ErrDenied
					}
				}
			}
		}
	}
	return fmt.Sprintf("[approval pending %s]\nComfyUI action: %s\nConfigured server: %s\nExact arguments (ASCII-quoted JSON):\n%s\nNo retry or global interruption is authorized.\nUse /approve %s or /deny %s for this request only.\n", p.Request.ID, p.Request.ToolName, strconv.QuoteToASCII(s.ComfyUI.Endpoint), strconv.QuoteToASCII(string(p.Arguments)), p.Request.ID, p.Request.ID), nil
}
