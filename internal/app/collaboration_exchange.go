package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"unicode/utf8"
)

// recordExchange records explicit assignments and replies, not hidden reasoning
// or arbitrary user histories. Each bounded chunk has a stable work identity.
func recordExchange(ctx context.Context, cfg config.Settings, work, phase, text string, sender webui.CollaborationMessage) error {
	if !cfg.Tools.CollaborationEnabled {
		return nil
	}
	store, e := agentchat.Open(ctx, cfg.Telemetry.Database+".collaboration")
	if e != nil {
		return e
	}
	defer store.Close()
	sender.TaskID = work
	sender.Topic = "delegation"
	sender.Recipient = "*"
	if sender.SessionID == "" {
		sender.SessionID = work
	}
	for chunk := 0; len(text) > 0; chunk++ {
		n := min(len(text), 6<<10)
		for n > 0 && !utf8.ValidString(text[:n]) {
			n--
		}
		if n == 0 {
			return agentchat.ErrInvalid
		}
		sender.Text = text[:n]
		text = text[n:]
		encoded, _ := json.Marshal([]any{work, phase, chunk})
		sum := sha256.Sum256(encoded)
		if _, e = store.Append(ctx, hex.EncodeToString(sum[:]), sender); e != nil {
			return e
		}
	}
	return nil
}
