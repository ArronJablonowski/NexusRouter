package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

type historyCursor struct {
	Version int    `json:"version"`
	ChatID  string `json:"chat_id"`
	TaskID  string `json:"task_id"`
	Head    int64  `json:"head"`
	Offset  int    `json:"offset"`
}

func encodeHistoryCursor(cursor historyCursor) (string, error) {
	if cursor.Version != 1 || !contract.ValidID(cursor.ChatID) || !contract.ValidID(cursor.TaskID) || cursor.Head < 1 || cursor.Offset < 1 || cursor.Offset > sessions.MaxTranscriptMessages {
		return "", contract.ErrContract
	}
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", contract.ErrContract
	}
	value := base64.RawURLEncoding.EncodeToString(body)
	if len(value) > contract.MaxCursorBytes {
		return "", contract.ErrContract
	}
	return value, nil
}

func decodeHistoryCursor(value string) (historyCursor, error) {
	if len(value) < 1 || len(value) > contract.MaxCursorBytes {
		return historyCursor{}, contract.ErrContract
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	var cursor historyCursor
	if err != nil || json.Unmarshal(body, &cursor) != nil {
		return historyCursor{}, contract.ErrContract
	}
	canonical, err := encodeHistoryCursor(cursor)
	if err != nil || canonical != value {
		return historyCursor{}, contract.ErrContract
	}
	return cursor, nil
}

// ChatHistory returns only allowlisted user/assistant presentation text.
func (s *Service) ChatHistory(ctx context.Context, chat string, options contract.HistoryOptions) (contract.HistoryPage, error) {
	if ctx == nil || !contract.ValidID(chat) || options.Validate() != nil {
		return contract.HistoryPage{}, ErrAdmission
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]any{chat, options}, secrets) {
		return contract.HistoryPage{}, ErrAdmission
	}
	tasks, err := s.ListSessionTasks(ctx, chat, sessions.SessionTaskListOptions{Limit: 1})
	if err != nil || len(tasks.Items) != 1 {
		return contract.HistoryPage{}, ErrInspection
	}
	head := tasks.Items[0]
	offset := 0
	if options.After != "" {
		cursor, cursorErr := decodeHistoryCursor(options.After)
		if cursorErr != nil || cursor.ChatID != chat || cursor.TaskID != head.TaskID || cursor.Head != head.Fence.HeadSequence {
			return contract.HistoryPage{}, ErrAdmission
		}
		offset = cursor.Offset
	}
	reader, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if ctx.Err() != nil {
			return contract.HistoryPage{}, ctx.Err()
		}
		return contract.HistoryPage{}, ErrInspection
	}
	defer reader.Close()
	snapshot, err := sessions.Replay(ctx, reader, head.TaskID)
	if err != nil || snapshot.SessionID != chat || snapshot.Sequence != head.Fence.HeadSequence {
		return contract.HistoryPage{}, ErrInspection
	}
	transcript, err := sessions.ProjectTranscript(snapshot)
	if err != nil {
		return contract.HistoryPage{}, ErrInspection
	}
	clean, err := redactCodexHistoryMessages(transcript.Messages, secrets)
	if err != nil {
		return contract.HistoryPage{}, ErrInspection
	}
	messages := make([]contract.HistoryMessage, 0, len(clean))
	for index, message := range clean {
		if (message.Role != "user" && message.Role != "assistant") || message.Content == "" {
			continue
		}
		digest := sha256.Sum256([]byte(head.TaskID + "\x00" + strconv.Itoa(index) + "\x00" + strconv.FormatInt(snapshot.MessageSequences[index], 10)))
		messages = append(messages, contract.HistoryMessage{ID: "msg_" + hex.EncodeToString(digest[:12]), Role: message.Role, Text: message.Content, Revision: int64(len(messages) + 1), SourceRevision: snapshot.MessageSequences[index]})
	}
	if offset > len(messages) {
		return contract.HistoryPage{}, ErrAdmission
	}
	end := offset + options.Limit
	if end > len(messages) {
		end = len(messages)
	}
	page := contract.HistoryPage{Version: 1, ChatID: chat, TaskID: head.TaskID, HeadRevision: head.Fence.HeadSequence, Messages: append([]contract.HistoryMessage(nil), messages[offset:end]...), HasMore: end < len(messages)}
	if page.HasMore {
		page.NextCursor, err = encodeHistoryCursor(historyCursor{Version: 1, ChatID: chat, TaskID: head.TaskID, Head: head.Fence.HeadSequence, Offset: end})
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || page.Validate() != nil || !selectionValueClean(page, secrets) {
		return contract.HistoryPage{}, ErrInspection
	}
	return page, nil
}
