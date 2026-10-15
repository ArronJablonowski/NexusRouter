package webuiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/chatfiles"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"slices"
)

// Conversation payloads are private snapshots, separate from prompt-free route
// bindings and original downloads. Publication precedes network dispatch.
type remoteConversation struct {
	Version   int                         `json:"version"`
	Instance  string                      `json:"instance"`
	RequestID string                      `json:"request_id"`
	Task      remote.Task                 `json:"task"`
	Messages  []remoteConversationMessage `json:"messages"`
}
type remoteConversationMessage struct {
	Role      string `json:"role"`
	Text      string `json:"text"`
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
	Host      string `json:"host"`
}

func conversationScope(instance, key string) string {
	return "conversation_" + taskDigest(instance+"\x00"+key)
}
func taskDigest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func (d *RecordedRemoteDispatcher) saveConversation(ctx context.Context, c remoteConversation) error {
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		return e
	}
	defer store.Close()
	body, e := json.Marshal(c)
	if e != nil || len(body) > 2*remote.MaxBody {
		return remote.ErrInvalid
	}
	scope := conversationScope(c.Instance, c.RequestID)
	return store.Put(ctx, scope, webui.ChatFile{ID: chatfiles.ID(scope, "history"), TaskID: scope, Name: "history.json", MediaType: "application/octet-stream", Size: len(body)}, body)
}
func (d *RecordedRemoteDispatcher) conversation(ctx context.Context, instance, key string) (remoteConversation, error) {
	var c remoteConversation
	if d == nil || d.Store == nil || d.UploadDirectory == "" || !remoteControlID(instance, 1) || !remoteControlID(key, 16) {
		return c, remote.ErrInvalid
	}
	binding, e := d.Store.Lookup(key)
	if e != nil || binding.Destination != instance {
		return c, remote.ErrInvalid
	}
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		return c, e
	}
	defer store.Close()
	scope := conversationScope(instance, key)
	_, body, e := store.Get(ctx, scope, chatfiles.ID(scope, "history"))
	if e != nil || len(body) > 2*remote.MaxBody {
		return c, remote.ErrInvalid
	}
	if json.Unmarshal(body, &c) != nil || c.Version != 1 || c.Instance != instance || c.RequestID != key || !remoteTaskFits(c.Task) || taskDigest(c.Task) != binding.TaskSHA256 || len(c.Messages) < 1 || len(c.Messages) > 255 {
		return remoteConversation{}, remote.ErrInvalid
	}
	for i, m := range c.Messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if m.Role != role || m.Host != instance || m.Model != c.Task.ExpectedModel || !remoteControlID(m.RequestID, 16) || len(m.Text) > remote.MaxBody/2 {
			return remoteConversation{}, remote.ErrInvalid
		}
	}
	return c, nil
}
func (d *RecordedRemoteDispatcher) continuation(ctx context.Context, instance, key string, task remote.Task) (remoteConversation, error) {
	c, e := d.conversation(ctx, instance, key)
	if e != nil {
		return c, e
	}
	// A pinned original success is context, never permission to retry its work.
	observed, e := d.Client.InspectRecorded(ctx, d.Store, key)
	if e != nil || observed.Destination != instance || observed.Status.State != "succeeded" || observed.Status.CancelRequested || observed.Status.Result == nil || observed.Status.Result.Text == "" || !slices.Contains(observed.Status.TaskIDs, observed.Status.Result.TaskID) || len(observed.Status.Result.Text) > remote.MaxBody/2 {
		return remoteConversation{}, remote.ErrInvalid
	}
	expected := c.Task
	expected.Prompt = task.Prompt
	expected.Messages = nil
	expected.Execution = nil
	if taskDigest(expected) != taskDigest(task) || !task.Private || task.MaxCost != 0 || task.ExpectedModel == "" || task.HarnessID != "" || len(c.Messages) > 251 {
		return remoteConversation{}, remote.ErrInvalid
	}
	info, e := d.Client.Info(ctx, instance)
	if e != nil || !info.Available || info.ConversationVersion != 1 {
		return remoteConversation{}, remote.ErrInvalid
	}
	prior := append([]providers.Message(nil), c.Task.Messages...)
	if len(prior) == 0 {
		prior = []providers.Message{{Role: "user", Content: c.Task.Prompt}}
	}
	prior = append(prior, providers.Message{Role: "assistant", Content: observed.Status.Result.Text})
	c.Messages = append(c.Messages, remoteConversationMessage{Role: "assistant", Text: observed.Status.Result.Text, RequestID: key, Model: c.Task.ExpectedModel, Host: instance})
	c.Task = task
	c.Task.Messages = prior
	c.Task.Execution = &runtime.RemoteExecution{Mode: "direct", Depth: 1}
	return c, nil
}
func (d *RecordedRemoteDispatcher) DispatchContinuation(ctx context.Context, instance, key, parent string, task remote.Task, files []attachments.File) (submissions.Status, error) {
	return d.dispatchConversation(ctx, instance, key, parent, task, files)
}
