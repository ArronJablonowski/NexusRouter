package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/webui"
)

func TestUploadRedactionJournalHistoryAndClassifier(t *testing.T) {
	for _, secret := range []string{`synthetic<&"credential`, "secret"} {
		s := submissionService(t)
		s.secret = func(string) string { return secret }
		ctx := context.Background()
		store, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		content := attachments.Encode("Read", []attachments.Input{{Name: "notes.txt", Text: secret}, {Name: "photo.jpg", Image: "/9j/secretAA"}})
		e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: content}}}}
		j := redactingJournal{db: store, secrets: []string{secret}}
		if err := j.Append(ctx, 0, e); err != nil {
			t.Fatal(err)
		}
		stored, err := store.Read(ctx, "task", 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		assertRedactedUpload(t, stored[0].Data.Messages[0].Content, secret)
		e.ID = "failed"
		e.Sequence = 2
		e.Kind = runtime.TaskFailed
		e.Data = runtime.Data{Code: "test_failure"}
		if err := j.Append(ctx, 1, e); err != nil {
			t.Fatal(err)
		}
		page, err := s.ChatHistory(ctx, "chat", webui.HistoryOptions{Limit: 20})
		if err != nil || len(page.Messages) != 1 {
			t.Fatal(page, err)
		}
		assertUploadTextView(t, page.Messages[0].Text, secret)
		input, _, ok := classifierInput(Request{Prompt: content}, []string{secret})
		if !ok {
			t.Fatal("classifier rejected small upload")
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if json.Unmarshal([]byte(input.Task), &body) != nil {
			t.Fatal("classifier JSON")
		}
		assertUploadTextView(t, body.Prompt, secret)
	}
}

func assertRedactedUpload(t *testing.T, content, secret string) {
	t.Helper()
	_, files := attachments.Decode(content)
	if len(files) != 2 || strings.Contains(files[0].Text, secret) || files[1].Image != "/9j/secretAA" {
		t.Fatal("redaction damaged frame or failed to scrub text")
	}
}

func TestUploadRedactionExpansionFailsClosed(t *testing.T) {
	content := attachments.Encode("Read", []attachments.Input{{Name: "file.txt", Text: strings.Repeat("a", attachments.MaxTextBytes)}})
	if _, err := redactUploadContent(content, []string{"a"}); !errors.Is(err, ErrAdmission) {
		t.Fatal("over-limit redaction admitted", err)
	}
}

func assertUploadTextView(t *testing.T, content, secret string) {
	t.Helper()
	_, files := attachments.Decode(content)
	if len(files) != 2 || strings.Contains(files[0].Text, secret) || files[1].Image != "" || !strings.Contains(files[1].Text, "Image attached") {
		t.Fatal("unsafe or damaged upload text view")
	}
}
