package webuiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/chatfiles"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"net/http"
	"slices"
	"time"
)

func remoteTaskFits(task remote.Task) bool {
	b, e := json.Marshal(task)
	return e == nil && task.Validate() == nil && len(b) <= remote.MaxBody-4096
}

var errRemoteUpload = errors.New("remote upload rejected before dispatch")

type remoteFileDispatcher interface {
	DispatchFiles(context.Context, string, string, remote.Task, []attachments.File) (submissions.Status, error)
}

func remoteUploadScope(instance, key string) string {
	h := sha256.Sum256([]byte(instance + "\x00" + key))
	return "remote_" + hex.EncodeToString(h[:])
}

// Bind the complete original request before any network dispatch. Different
// original bytes cannot reuse a key even when extraction yields identical text.
func (d *RecordedRemoteDispatcher) DispatchFiles(ctx context.Context, instance, key string, task remote.Task, files []attachments.File) (submissions.Status, error) {
	return d.dispatchConversation(ctx, instance, key, "", task, files)
}
func (d *RecordedRemoteDispatcher) dispatchConversation(ctx context.Context, instance, key, parent string, task remote.Task, files []attachments.File) (submissions.Status, error) {
	reject := func() (submissions.Status, error) { return submissions.Status{}, errRemoteUpload }
	if d == nil || d.Client == nil || d.Store == nil {
		return reject()
	}
	if d.UploadDirectory == "" {
		if len(files) > 0 || parent != "" {
			return reject()
		}
		return d.Client.DispatchRecorded(ctx, d.Store, instance, key, task)
	}
	if !remoteControlID(instance, 1) || !remoteControlID(key, 16) || !remoteTaskFits(task) || attachments.Validate(files) != nil {
		return reject()
	}
	// Browser attachment input is a plain private user turn. Only the server
	// may reconstruct a pinned prior conversation after validating its success.
	if len(files) > 0 && (!task.Private || len(task.Messages) > 0 || task.Execution != nil || task.HarnessID != "" || d.PrepareUploads == nil) {
		return reject()
	}
	original, _ := json.Marshal(struct {
		Task   remote.Task
		Files  []attachments.File
		Parent string `json:",omitempty"`
	}{task, files, parent})
	digest := sha256.Sum256(original)
	scope := remoteUploadScope(instance, key)
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		return reject()
	}
	defer store.Close()
	// Pin the original intent, including an empty file list, before preparation.
	if e = store.BindInputs(ctx, scope, "plan_"+hex.EncodeToString(digest[:])); e != nil {
		return reject()
	}

	displayPrompt := task.Prompt
	history := remoteConversation{CreatedAt: time.Now().UTC(), Version: 1, Instance: instance, RequestID: key, Task: task}
	if parent != "" {
		if parent == key {
			return reject()
		}
		history, e = d.continuation(ctx, instance, parent, task)
		if e != nil {
			return reject()
		}
		history.RequestID = key
		history.CreatedAt = time.Now().UTC()
		task = history.Task
	}
	if len(files) > 0 {
		task.Prompt, e = d.PrepareUploads(ctx, task.Prompt, files)
		if e != nil || !remoteTaskFits(task) {
			return reject()
		}
		if remote.HasImageInputs(task) {
			info, err := d.Client.Info(ctx, instance)
			if err != nil || !remoteUploadVisionAllowed(info, task) {
				return reject()
			}
		}
		for _, f := range files {
			meta := webui.ChatFile{ID: chatfiles.ID(scope, f.Name), TaskID: scope, Name: f.Name, MediaType: remoteUploadMedia(f.Data), Size: len(f.Data)}
			if e = store.Put(ctx, scope, meta, f.Data); e != nil {
				return reject()
			}
		}
	}

	if parent != "" {
		task.Messages = append(task.Messages, providers.Message{Role: "user", Content: task.Prompt})
	}
	if !remoteTaskFits(task) {
		return reject()
	}
	if parent != "" && remote.HasImageInputs(task) {
		info, err := d.Client.Info(ctx, instance)
		if err != nil || !remoteUploadVisionAllowed(info, task) {
			return reject()
		}
	}
	if task.Private && task.MaxCost == 0 && task.ExpectedModel != "" && task.HarnessID == "" && (task.Execution == nil || parent != "") {
		history.Task = task
		history.Messages = append(history.Messages, remoteConversationMessage{Role: "user", Text: displayPrompt, RequestID: key, Model: task.ExpectedModel, Host: instance})
		if e = d.saveConversation(ctx, history); e != nil {
			return reject()
		}
	}
	status, dispatchErr := d.Client.DispatchRecorded(ctx, d.Store, instance, key, task)
	// Index recovery is read-only and can complete after uncertain delivery. Never
	// reinterpret an indexing error as permission to dispatch the request again.
	d.indexDirty.Store(true)
	return status, dispatchErr
}

func remoteUploadMedia(b []byte) string {
	kind := http.DetectContentType(b)
	if kind == "image/png" || kind == "image/jpeg" {
		return kind
	}
	return "application/octet-stream"
}
func remoteUploadVisionAllowed(info remote.Info, task remote.Task) bool {
	if !info.Available {
		return false
	}
	for _, m := range info.Models {
		if m.ID == task.ModelID && m.Local && m.EstimatedCost != nil && *m.EstimatedCost == 0 && (task.ExpectedModel == "" || task.ExpectedModel == m.Model) && slices.Contains(m.Capabilities, "vision") {
			return true
		}
	}
	return false
}
