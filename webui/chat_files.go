package webui

import (
	"path"
	"strings"
)

type ChatFile struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int    `json:"size"`
}

func (f ChatFile) Validate() error {
	if !ValidID(f.ID) || !ValidID(f.TaskID) || len(f.Name) < 1 || len(f.Name) > 200 || f.Name == "." || f.Name == ".." || path.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, "\\\r\n\x00") || f.Size < 0 || f.Size > 32<<20 {
		return ErrContract
	}
	for _, r := range f.Name {
		if r < 32 || r == 127 {
			return ErrContract
		}
	}
	switch f.MediaType {
	case "image/png", "image/jpeg", "image/webp", "text/plain", "application/octet-stream":
		return nil
	}
	return ErrContract
}

type ChatFilePage struct {
	Version int        `json:"version"`
	ChatID  string     `json:"chat_id"`
	Files   []ChatFile `json:"files"`
	Pending bool       `json:"pending"`
}

func (p ChatFilePage) Validate() error {
	if p.Version != 1 || !ValidID(p.ChatID) || p.Files == nil || len(p.Files) > 100 {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, f := range p.Files {
		if f.Validate() != nil || seen[f.ID] {
			return ErrContract
		}
		seen[f.ID] = true
	}
	return nil
}
