package telegrambridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/ArronJablonowski/NexusRouter/connectors/telegram"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type fakeAPI struct {
	updates []telegram.Update
	sent    []string
	fail    bool
	offsets []int64
}

func (a *fakeAPI) Updates(_ context.Context, offset int64) ([]telegram.Update, error) {
	a.offsets = append(a.offsets, offset)
	for _, u := range a.updates {
		if u.ID >= offset {
			return []telegram.Update{u}, nil
		}
	}
	return nil, nil
}
func (a *fakeAPI) Send(_ context.Context, chat int64, text string) error {
	a.sent = append(a.sent, text)
	if a.fail {
		return errors.New("lost acknowledgement")
	}
	return nil
}

type fakeBackend struct {
	calls          int
	keys, previous []string
	status         submissions.Status
	fail           bool
}

func (b *fakeBackend) Submit(_ context.Context, key, text, previous string) (submissions.Status, error) {
	b.calls++
	b.keys = append(b.keys, key)
	b.previous = append(b.previous, previous)
	if b.fail {
		return submissions.Status{}, errors.New("lost admission acknowledgement")
	}
	return submissions.Status{ID: "submission"}, nil
}
func (b *fakeBackend) Status(context.Context, string) (submissions.Status, error) {
	return b.status, nil
}
func update(id, user int64, text string) telegram.Update {
	return telegram.Update{ID: id, Message: &telegram.Message{ID: id, From: &telegram.User{ID: user}, Chat: telegram.Chat{ID: user, Type: "private"}, Text: text}}
}
func fixture(t *testing.T) (*bridge, *fakeAPI, *fakeBackend, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "state")
	s, err := openStore(directory, "identity")
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	backend := &fakeBackend{status: submissions.Status{State: "succeeded", Result: &submissions.Result{TaskID: "task", Text: "answer"}}}
	return &bridge{store: s, api: api, backend: backend, allowed: map[int64]bool{7: true}}, api, backend, directory
}
func step(t *testing.T, b *bridge) {
	t.Helper()
	if err := b.step(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateChatConversationRestartAndDuplicateUpdates(t *testing.T) {
	b, api, backend, directory := fixture(t)
	defer func() { b.store.close() }()
	// Unknown users, groups, bots, mismatched sender/chat, and absent messages
	// must not dispatch inference or receive replies.
	bad := []telegram.Update{update(1, 8, "secret"), update(2, 7, "group"), update(3, 7, "bot"), update(4, 7, "mismatch"), {ID: 5}}
	bad[1].Message.Chat.Type = "group"
	bad[2].Message.From.Bot = true
	bad[3].Message.Chat.ID = 8
	api.updates = bad
	for range bad {
		step(t, b)
	}
	if backend.calls != 0 || len(api.sent) != 0 {
		t.Fatal("unauthorized side effect")
	}
	api.updates = append(api.updates, update(6, 7, "hello"))
	step(t, b)
	if backend.calls != 1 || len(api.sent) != 1 {
		t.Fatal("missing reply")
	}
	b.store.close()
	var err error
	b.store, err = openStore(directory, "identity")
	if err != nil {
		t.Fatal(err)
	}
	step(t, b) // finish already-sent update without resend
	if backend.calls != 1 || len(api.sent) != 1 || b.store.value.Offset != 7 {
		t.Fatal("duplicate work")
	}
	api.updates = append(api.updates, update(7, 7, "follow up"))
	step(t, b)
	if backend.previous[1] != "task" {
		t.Fatal("conversation not continued")
	}
	step(t, b)
	api.updates = append(api.updates, update(8, 7, "/new"), update(9, 7, "fresh"))
	step(t, b)
	step(t, b)
	step(t, b)
	if backend.previous[len(backend.previous)-1] != "" {
		t.Fatal("new conversation retained history")
	}
}

func TestUncertainSendNeverResendsAcrossRestart(t *testing.T) {
	b, api, backend, directory := fixture(t)
	api.updates = []telegram.Update{update(1, 7, "hello")}
	api.fail = true
	if err := b.step(context.Background()); err != ErrUncertain {
		t.Fatal(err)
	}
	b.store.close()
	s, err := openStore(directory, "identity")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	b.store = s
	api.fail = false
	if err = b.step(context.Background()); err != ErrUncertain || len(api.sent) != 1 || backend.calls != 1 {
		t.Fatal("uncertain delivery resent", err)
	}
}
func TestLostAdmissionUsesIdenticalKeyAndPreviousOnRestart(t *testing.T) {
	b, api, backend, directory := fixture(t)
	api.updates = []telegram.Update{update(1, 7, "hello")}
	backend.fail = true
	if b.step(context.Background()) == nil {
		t.Fatal("missing failure")
	}
	b.store.close()
	s, err := openStore(directory, "identity")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	b.store = s
	backend.fail = false
	step(t, b)
	if backend.keys[0] != backend.keys[1] || backend.previous[0] != backend.previous[1] {
		t.Fatal("changed idempotent intent")
	}
}
func TestStatePrivacyLockCorruptionAndRevocation(t *testing.T) {
	b, api, _, directory := fixture(t)
	if s, err := openStore(directory, "identity"); err == nil {
		s.close()
		t.Fatal("second worker accepted")
	}
	api.updates = []telegram.Update{update(1, 7, "hello")}
	b.backend.(*fakeBackend).status.State = "running"
	step(t, b)
	b.allowed = map[int64]bool{}
	if err := b.step(context.Background()); err != ErrState {
		t.Fatal("revoked recovered work accepted", err)
	}
	b.store.close()
	if s, err := openStore(directory, "other-model"); err == nil {
		s.close()
		t.Fatal("identity drift accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(directory, "identity"); err == nil {
		s.close()
		t.Fatal("corrupt state discarded")
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(directory, "identity"); err == nil {
		s.close()
		t.Fatal("public state accepted")
	}
}
func TestCommandsFailuresAndBoundedUnicodeReplies(t *testing.T) {
	b, api, backend, _ := fixture(t)
	defer b.store.close()
	for i, text := range []string{"/help", "/start", "", "/unknown", strings.Repeat("x", 16385)} {
		api.updates = append(api.updates, update(int64(i+1), 7, text))
		step(t, b)
		step(t, b)
	}
	if backend.calls != 0 || len(api.sent) != 5 {
		t.Fatal("unsupported input inferred")
	}
	api.updates = append(api.updates, update(6, 7, "hello"))
	backend.status = submissions.Status{State: "failed"}
	step(t, b)
	if backend.calls != 1 || !strings.Contains(api.sent[5], "could not finish") {
		t.Fatal("failed model replay")
	}
	for _, text := range []string{"", strings.Repeat("😀", 50000), strings.Repeat("a", 50000), "a\xff"} {
		chunks := replyChunks(text)
		if len(chunks) < 1 || len(chunks) > 8 {
			t.Fatal("unbounded chunks")
		}
		for _, chunk := range chunks {
			if len(utf16.Encode([]rune(chunk))) > 3500 {
				t.Fatal("Telegram text overflow")
			}
		}
	}
}
func TestDisabledStartHasNoSideEffects(t *testing.T) {
	s := config.Defaults()
	s.Telegram.StateDirectory = filepath.Join(t.TempDir(), "not-created")
	w, err := Start(context.Background(), s, func(string) string { t.Fatal("secret read while disabled"); return "" }, "", nil)
	if err != nil || w != nil {
		t.Fatal(w, err)
	}
	w.Close()
	if _, err = os.Stat(s.Telegram.StateDirectory); !os.IsNotExist(err) {
		t.Fatal("disabled connector created state")
	}
}
func TestChatSettingsCannotInheritHostToolsOrMemory(t *testing.T) {
	s := config.Defaults()
	s.Telegram = config.Telegram{TokenEnv: "TELEGRAM_TOKEN", StateDirectory: "/private/telegram"}
	s.Tools.AutoApprove = true
	s.Tools.CreateEnabled = true
	s.Tools.ReplaceEnabled = true
	s.Tools.CollaborationEnabled = true
	got := chatSettings(s)
	if got.Tools.Enabled || got.Tools.AutoApprove || got.Tools.CreateEnabled || got.Tools.ReplaceEnabled || got.Tools.CollaborationEnabled || got.Skills.Enabled || got.Memory.Enabled || got.Workers.DelegateReadTools {
		t.Fatal("host authority inherited")
	}
	if got.Telemetry.Database == s.Telemetry.Database || !strings.HasPrefix(got.Telemetry.Database, s.Telegram.StateDirectory+"/") {
		t.Fatal("shared history")
	}
}

func TestStateRejectsSymlinksAndUncertainIntentBeforeSend(t *testing.T) {
	for _, name := range []string{"state.json", "tasks.db", "worker.lock"} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "protected")
			if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(directory, name)); err != nil {
				t.Fatal(err)
			}
			s, err := openStore(directory, "identity")
			if err == nil {
				s.close()
				t.Fatal("symlink accepted")
			}
			body, err := os.ReadFile(target)
			if err != nil || string(body) != "preserve" {
				t.Fatal("external file changed")
			}
		})
	}
	b, api, backend, directory := fixture(t)
	b.store.value.Pending = &pending{Update: 1, Chat: 7, Replies: []string{"answer"}, Sending: true}
	if err := b.store.save(); err != nil {
		t.Fatal(err)
	}
	b.store.close()
	s, err := openStore(directory, "identity")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	b.store = s
	if err = b.step(context.Background()); err != ErrUncertain || len(api.sent) != 0 || backend.calls != 0 {
		t.Fatal("crash before send caused replay", err)
	}
}

type blockingAPI struct{ started chan struct{} }

func (a blockingAPI) Updates(ctx context.Context, _ int64) ([]telegram.Update, error) {
	close(a.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (blockingAPI) Send(context.Context, int64, string) error { return nil }
func TestPollingCancellationAndTerminalFailureReporting(t *testing.T) {
	b, _, _, _ := fixture(t)
	defer b.store.close()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	b.api = blockingAPI{started: started}
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx, func(string) { t.Error("cancellation reported as failure") }) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller failed to stop")
	}
	b.store.value.Pending = &pending{Update: 1, Chat: 7, Sending: true, Replies: []string{"answer"}}
	reports := 0
	b.run(context.Background(), func(message string) {
		reports++
		if !strings.Contains(message, "uncertain") {
			t.Error("missing actionable diagnostic")
		}
	})
	if reports != 1 {
		t.Fatal("terminal failure did not stop")
	}
}

func TestStoreDoesNotAdoptAnUnrelatedTaskDatabase(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "tasks.db")
	if err := os.WriteFile(target, []byte("existing private history"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(directory, "identity"); err == nil {
		s.close()
		t.Fatal("unowned database adopted")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "existing private history" {
		t.Fatal("history modified")
	}
}

func TestWhitespaceAcrossReplyBoundaryStillDelivers(t *testing.T) {
	b, api, backend, _ := fixture(t)
	defer b.store.close()
	api.updates = []telegram.Update{update(1, 7, "hello")}
	answer := strings.Repeat("a", 3500) + strings.Repeat(" ", 3500) + "tail" + strings.Repeat(" ", 3500)
	backend.status.Result.Text = answer
	for range 5 {
		step(t, b)
	}
	for _, chunk := range api.sent {
		if strings.TrimSpace(chunk) == "" {
			t.Fatal("bridge produced a blank message, which the Telegram client rejects")
		}
	}
	if got := strings.TrimRight(strings.Join(api.sent, ""), " "); got != strings.Repeat("a", 3500)+"tail" || b.store.value.Offset != 2 {
		t.Fatal("nonblank answer chunks were lost or the update did not finish")
	}
}
