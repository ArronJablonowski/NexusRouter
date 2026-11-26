package telegrambridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/connectors/telegram"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type botAPI interface {
	Updates(context.Context, int64) ([]telegram.Update, error)
	Send(context.Context, int64, string) error
}
type backend interface {
	Submit(context.Context, string, string, string) (submissions.Status, error)
	Status(context.Context, string) (submissions.Status, error)
}
type bridge struct {
	store   *store
	api     botAPI
	backend backend
	allowed map[int64]bool
	observe func(error)
}

func identity(bot, model string) string {
	sum := sha256.Sum256([]byte("telegram-v1\x00" + bot + "\x00" + model))
	return hex.EncodeToString(sum[:])
}

// step is sequential: persist intake before inference, and persist send intent
// before outbound I/O. No acknowledged update is lost on restart.
func (b *bridge) step(ctx context.Context) error {
	s := &b.store.value
	if s.Pending == nil {
		updates, err := b.api.Updates(ctx, s.Offset)
		if err != nil {
			return err
		}
		if len(updates) == 0 {
			return nil
		}
		u := updates[0]
		if u.ID < s.Offset || u.ID == math.MaxInt64 || u.ID < 0 {
			return ErrState
		}
		m := u.Message
		if m == nil || m.From == nil || m.From.Bot || m.Chat.Type != "private" || m.Chat.ID != m.From.ID || !b.allowed[m.From.ID] {
			s.Offset = u.ID + 1
			return b.store.save()
		}
		p := &pending{Update: u.ID, Chat: m.Chat.ID, Previous: s.Chats[m.Chat.ID], Text: m.Text}
		switch {
		case m.Text == "/new":
			delete(s.Chats, m.Chat.ID)
			p.Previous = ""
			p.Replies = []string{"New conversation started."}
		case m.Text == "/start" || m.Text == "/help":
			p.Replies = []string{"NexusRouter private text chat. Send a message, or /new to start fresh. Files, images, voice and tool approvals are not supported here yet. Host tools are disabled."}
		case !utf8.ValidString(m.Text) || len(m.Text) > 16384:
			p.Text = ""
			p.Replies = []string{"Please send a shorter text message (up to 16 KiB)."}
		case strings.TrimSpace(m.Text) == "" || strings.HasPrefix(m.Text, "/"):
			p.Replies = []string{"Send text to chat, /new for a new conversation, or /help. This connector currently supports text only."}
		}
		s.Pending = p
		if err := b.store.save(); err != nil {
			return err
		}
	}
	p := s.Pending
	// Recheck authorization on recovered work. Revocation never sends private
	// results or creates new inference. Restore authorization or inspect offline.
	if !b.allowed[p.Chat] {
		return ErrState
	}
	if p.Sending {
		return ErrUncertain
	}
	if len(p.Replies) == 0 {
		if p.Submission == "" {
			key := fmt.Sprintf("telegram-%s-%d", s.Identity, p.Update)
			status, err := b.backend.Submit(ctx, key, p.Text, p.Previous)
			if err != nil {
				return err
			}
			if status.ID == "" {
				return ErrState
			}
			p.Submission = status.ID
			if err := b.store.save(); err != nil {
				return err
			}
		}
		status, err := b.backend.Status(ctx, p.Submission)
		if err != nil {
			return err
		}
		switch status.State {
		case "queued", "running":
			return nil
		case "succeeded":
			if status.Result == nil || status.Result.TaskID == "" {
				return ErrState
			}
			s.Chats[p.Chat] = status.Result.TaskID
			p.Replies = replyChunks(status.Result.Text)
		case "failed", "canceled":
			p.Replies = []string{"This request could not finish. No automatic retry was started. Try a new message or /new; inspect the private task record on the host for details."}
		default:
			return ErrState
		}
		if err := b.store.save(); err != nil {
			return err
		}
	}
	if p.Next < len(p.Replies) {
		p.Sending = true
		if err := b.store.save(); err != nil {
			return err
		}
		if err := b.api.Send(ctx, p.Chat, p.Replies[p.Next]); err != nil {
			return ErrUncertain
		}
		p.Next++
		p.Sending = false
		if err := b.store.save(); err != nil {
			return err
		}
		return nil
	}
	s.Offset = p.Update + 1
	s.Pending = nil
	return b.store.save()
}

func replyChunks(text string) []string {
	if !utf8.ValidString(text) {
		return []string{"The model returned invalid text. Inspect the task record on the host."}
	}
	if strings.TrimSpace(text) == "" {
		return []string{"The model returned no text."}
	}
	// Use UTF-16 code units conservatively, below Telegram's 4096 character cap.
	var chunks []string
	appendChunk := func(chunk string) bool {
		// The transport rejects whitespace-only messages. Treat these
		// boundaries as formatting, not as an uncertain send.
		if strings.TrimSpace(chunk) == "" {
			return true
		}
		if len(chunks) == 7 {
			chunks = append(chunks, "Reply truncated. The full answer is retained in the private task record on the host.")
			return false
		}
		chunks = append(chunks, chunk)
		return true
	}
	var buf strings.Builder
	units := 0
	for _, r := range text {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 3500 {
			if !appendChunk(buf.String()) {
				return chunks
			}
			buf.Reset()
			units = 0
		}
		buf.WriteRune(r)
		units += n
	}
	appendChunk(buf.String())
	return chunks
}

func (b *bridge) run(ctx context.Context, report func(string)) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := b.step(ctx)
		if b.observe != nil {
			b.observe(err)
		}
		if ctx.Err() != nil {
			return
		}
		if err == ErrUncertain || err == ErrState {
			report(err.Error())
			return
		}
		delay := time.Second
		if err != nil {
			report("Telegram temporarily unavailable; saved work retained")
			delay = backoff
			backoff = min(60*time.Second, backoff*2)
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
