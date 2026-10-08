package sessions

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type ChatPreference struct {
	Title    string `json:"title"`
	Pinned   bool   `json:"pinned"`
	Revision int64  `json:"revision"`
}
type ChatPreferenceUpdate struct {
	Version          int     `json:"version"`
	ChatID           string  `json:"chat_id"`
	ExpectedRevision int64   `json:"expected_revision"`
	Title            *string `json:"title,omitempty"`
	Pinned           *bool   `json:"pinned,omitempty"`
}

func ValidChatTitle(title string) bool {
	return utf8.ValidString(title) && utf8.RuneCountInString(title) <= 100 && strings.TrimSpace(title) == title && !strings.ContainsFunc(title, unicode.IsControl)
}
func (r ChatPreferenceUpdate) Validate() error {
	if r.Version != 1 || !presentationIDPattern.MatchString(r.ChatID) || r.ExpectedRevision < 0 || r.ExpectedRevision >= 9007199254740991 || (r.Title == nil) == (r.Pinned == nil) {
		return ErrTaskList
	}
	if r.Title != nil && (!ValidChatTitle(*r.Title) || *r.Title == "") {
		return ErrTaskList
	}
	return nil
}
