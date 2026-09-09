package sessions

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"time"
)

const MaxChatPageBytes = 1 << 20

var presentationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type ChatSummary struct {
	Version      int       `json:"version"`
	ChatID       string    `json:"chat_id"`
	LatestTaskID string    `json:"latest_task_id"`
	State        string    `json:"state"`
	Revision     int64     `json:"revision"`
	StartedAt    time.Time `json:"started_at"`
}

type ChatPage struct {
	Version    int           `json:"version"`
	Items      []ChatSummary `json:"items"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

type ChatListOptions struct {
	After string
	Limit int
}

type ChatListCursor struct {
	Version   int   `json:"version"`
	Last      int64 `json:"last"`
	HighWater int64 `json:"high_water"`
}

func EncodeChatListCursor(cursor ChatListCursor) (string, error) {
	if cursor.Version != 1 || cursor.Last < 1 || cursor.HighWater < cursor.Last {
		return "", ErrTaskList
	}
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", ErrTaskList
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func DecodeChatListCursor(value string) (ChatListCursor, error) {
	if value == "" || len(value) > 1024 {
		return ChatListCursor{}, ErrTaskList
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	var cursor ChatListCursor
	if err != nil || json.Unmarshal(body, &cursor) != nil {
		return ChatListCursor{}, ErrTaskList
	}
	canonical, err := EncodeChatListCursor(cursor)
	if err != nil || canonical != value {
		return ChatListCursor{}, ErrTaskList
	}
	return cursor, nil
}

func (options ChatListOptions) Validate() error {
	if options.Limit < 1 || options.Limit > 100 {
		return ErrTaskList
	}
	if options.After != "" {
		if _, err := DecodeChatListCursor(options.After); err != nil {
			return ErrTaskList
		}
	}
	return nil
}

func (summary ChatSummary) Validate() error {
	if summary.Version != 1 || !presentationIDPattern.MatchString(summary.ChatID) || !presentationIDPattern.MatchString(summary.LatestTaskID) || !ValidTaskState(summary.State) || summary.Revision < 1 || summary.Revision > MaxTaskEvents || summary.StartedAt.IsZero() {
		return ErrTaskList
	}
	return nil
}

func (page ChatPage) Validate() error {
	if page.Version != 1 || len(page.Items) > 100 || page.HasMore != (page.NextCursor != "") || (page.HasMore && len(page.Items) == 0) {
		return ErrTaskList
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		if item.Validate() != nil || seen[item.ChatID] {
			return ErrTaskList
		}
		seen[item.ChatID] = true
	}
	if page.HasMore {
		if _, err := DecodeChatListCursor(page.NextCursor); err != nil {
			return ErrTaskList
		}
	}
	body, err := json.Marshal(page)
	if err != nil || len(body) > MaxChatPageBytes {
		return ErrTaskList
	}
	return nil
}
