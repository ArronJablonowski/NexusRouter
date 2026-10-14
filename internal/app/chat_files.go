package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"

	"github.com/ArronJablonowski/NexusRouter/connectors/comfyui"
	"github.com/ArronJablonowski/NexusRouter/internal/chatfiles"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/webui"
)

// ChatFiles collects only journal-proven generated outputs, never assistant
// links, supplied local paths, tool inspection results, or unrelated jobs.
func (s *Service) ChatFiles(ctx context.Context, chat string) (webui.ChatFilePage, error) {
	page := webui.ChatFilePage{Version: 1, ChatID: chat, Files: []webui.ChatFile{}}
	if ctx == nil || !webui.ValidID(chat) {
		return page, ErrInspection
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean(chat, secrets) {
		return page, ErrInspection
	}
	db, e := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if e != nil {
		return page, e
	}
	defer db.Close()
	// Match ordinary chat selection and exclude unknown/unrelated session IDs.
	if _, _, _, e = selectChatTranscript(ctx, db, chat, secrets); e != nil {
		return page, ErrInspection
	}
	store, e := chatfiles.Open(ctx, s.settings.Telemetry.Database+".chat-files")
	if e != nil {
		return page, e
	}
	defer store.Close()
	existing, e := store.List(ctx, chat)
	if e != nil {
		return page, e
	}
	known := map[string]bool{}
	for _, f := range existing {
		known[f.ID] = true
	}
	tasks, e := db.ListSessionTasks(ctx, chat, sessions.SessionTaskListOptions{Limit: 100})
	if e != nil {
		return page, e
	}
	page.Pending = tasks.HasMore
	for _, task := range tasks.Items {
		snapshot, err := sessions.Replay(ctx, db, task.TaskID)
		if err != nil || snapshot.SessionID != chat {
			return page, ErrInspection
		}
		calls := map[string]providers.ToolCall{}
		after := int64(0)
		for after < snapshot.Sequence {
			events, err := db.Read(ctx, task.TaskID, after, 100)
			if err != nil || len(events) == 0 {
				return page, ErrInspection
			}
			for _, event := range events {
				if event.Sequence > snapshot.Sequence {
					break
				}
				after = event.Sequence
				if event.Kind == runtime.TurnCompleted {
					for _, call := range event.Data.ToolCalls {
						calls[call.ID] = call
					}
				}
				if event.Kind != runtime.ToolCompleted || event.Data.Code != "" || event.Data.Effect != runtime.ConfirmedEffect {
					continue
				}
				call, ok := calls[event.Data.ToolCallID]
				if !ok || call.Name != event.Data.ToolName {
					continue
				}
				source := task.TaskID + "/" + call.ID
				if call.Name == "create_file" || call.Name == "replace_file" {
					var a struct {
						Path    string `json:"path"`
						Content string `json:"content"`
					}
					var result struct {
						Created  bool `json:"created"`
						Replaced bool `json:"replaced"`
					}
					if json.Unmarshal(call.Arguments, &a) != nil || json.Unmarshal([]byte(event.Data.Text), &result) != nil || !(call.Name == "create_file" && result.Created || call.Name == "replace_file" && result.Replaced) || !createPath(a.Path) || len(a.Content) > 64<<10 {
						continue
					}
					f := webui.ChatFile{ID: chatfiles.ID(chat, source), TaskID: task.TaskID, Name: filepath.Base(a.Path), MediaType: "text/plain", Size: len(a.Content)}
					if !selectionValueClean([]any{f, a.Content}, secrets) {
						continue
					}
					if !known[f.ID] {
						if len(known) >= 100 {
							page.Pending = true
							continue
						}
						if err = store.Put(ctx, chat, f, []byte(a.Content)); err != nil {
							return page, err
						}
						known[f.ID] = true
					}
				}
				if call.Name == "comfy_submit" {
					var a struct {
						Key string `json:"key"`
					}
					var accepted comfyui.Job
					if json.Unmarshal(call.Arguments, &a) != nil || json.Unmarshal([]byte(event.Data.Text), &accepted) != nil || accepted.Key != a.Key || accepted.State != "accepted" || accepted.PromptID == "" {
						continue
					}
					if !s.settings.ComfyUI.Enabled {
						continue
					}
					service, err := comfyui.OpenService(s.settings.ComfyUI.Endpoint, s.settings.ComfyUI.ReceiptDirectory)
					if err != nil {
						page.Pending = true
						continue
					}
					job, err := service.Status(ctx, a.Key)
					if err != nil || job.PromptID != accepted.PromptID || job.History == nil {
						page.Pending = true
						continue
					}
					if job.History.Status.StatusStr == "error" {
						continue
					}
					if !job.History.Status.Completed {
						page.Pending = true
						continue
					}
					nodes := []string{}
					for node := range job.History.Outputs {
						nodes = append(nodes, node)
					}
					sort.Strings(nodes)
					for _, node := range nodes {
						for i, image := range job.History.Outputs[node].Images {
							id := chatfiles.ID(chat, fmt.Sprintf("%s/%s/%d", source, node, i))
							if known[id] {
								continue
							}
							if len(known) >= 100 {
								page.Pending = true
								break
							}
							body, err := service.Output(ctx, a.Key, node, i)
							if err != nil {
								page.Pending = true
								continue
							}
							kind := http.DetectContentType(body)
							if kind != "image/png" && kind != "image/jpeg" && kind != "image/webp" {
								continue
							}
							f := webui.ChatFile{ID: id, TaskID: task.TaskID, Name: image.Filename, MediaType: kind, Size: len(body)}
							if f.Validate() != nil || !selectionValueClean(f, secrets) {
								continue
							}
							if err = store.Put(ctx, chat, f, body); err != nil {
								page.Pending = true
								continue
							}
							known[id] = true
						}
					}
				}
			}
		}
	}
	page.Files, e = store.List(ctx, chat)
	if e != nil || page.Validate() != nil || !selectionValueClean(page, secrets) {
		return page, ErrInspection
	}
	return page, nil
}
func (s *Service) ChatFile(ctx context.Context, chat, id string) (webui.ChatFile, []byte, error) {
	if ctx == nil || !webui.ValidID(chat) || !webui.ValidID(id) {
		return webui.ChatFile{}, nil, ErrInspection
	}
	db, e := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if e != nil {
		return webui.ChatFile{}, nil, e
	}
	defer db.Close()
	if _, _, _, e = selectChatTranscript(ctx, db, chat, memorySecrets(s.settings, s.secret)); e != nil {
		return webui.ChatFile{}, nil, ErrInspection
	}
	store, e := chatfiles.Open(ctx, s.settings.Telemetry.Database+".chat-files")
	if e != nil {
		return webui.ChatFile{}, nil, e
	}
	defer store.Close()
	f, b, e := store.Get(ctx, chat, id)
	if e == nil && (!selectionValueClean(f, memorySecrets(s.settings, s.secret)) || (f.MediaType == "text/plain" && !selectionValueClean(string(b), memorySecrets(s.settings, s.secret)))) {
		return webui.ChatFile{}, nil, ErrInspection
	}
	return f, b, e
}
