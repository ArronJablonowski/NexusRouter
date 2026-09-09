package webuiapp

import (
	"sync"
	"unicode/utf8"
)

const (
	liveTextChunkBytes = 32 << 10
	liveTextQueueDepth = 8
)

type liveTextChunk struct {
	task string
	text string
}

type liveTextSubscriber struct {
	channel chan liveTextChunk
}

// LiveTextHub is a daemon-owned, memory-only fan-out. Publication is always
// non-blocking. Slow observers are detached and must reconcile from durable
// history; they can never stall or cancel execution.
type LiveTextHub struct {
	mu     sync.Mutex
	next   uint64
	byChat map[string]map[uint64]*liveTextSubscriber
}

func NewLiveTextHub() *LiveTextHub {
	return &LiveTextHub{byChat: map[string]map[uint64]*liveTextSubscriber{}}
}

func (h *LiveTextHub) Publish(task, chat, text string) {
	if h == nil || !browserStreamID.MatchString(task) || !browserStreamID.MatchString(chat) || text == "" || !utf8.ValidString(text) {
		return
	}
	for text != "" {
		end := liveTextBoundary(text, liveTextChunkBytes)
		h.publishOne(chat, liveTextChunk{task: task, text: text[:end]})
		text = text[end:]
	}
}

func liveTextBoundary(text string, limit int) int {
	if len(text) <= limit {
		return len(text)
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return end
}

func (h *LiveTextHub) publishOne(chat string, chunk liveTextChunk) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, subscriber := range h.byChat[chat] {
		select {
		case subscriber.channel <- chunk:
		default:
			close(subscriber.channel)
			delete(h.byChat[chat], id)
		}
	}
	if len(h.byChat[chat]) == 0 {
		delete(h.byChat, chat)
	}
}

func (h *LiveTextHub) subscribe(chat string) (<-chan liveTextChunk, func()) {
	if h == nil || !browserStreamID.MatchString(chat) {
		return nil, func() {}
	}
	h.mu.Lock()
	h.next++
	id := h.next
	subscriber := &liveTextSubscriber{channel: make(chan liveTextChunk, liveTextQueueDepth)}
	if h.byChat[chat] == nil {
		h.byChat[chat] = map[uint64]*liveTextSubscriber{}
	}
	h.byChat[chat][id] = subscriber
	h.mu.Unlock()
	var once sync.Once
	return subscriber.channel, func() {
		once.Do(func() {
			h.mu.Lock()
			if current := h.byChat[chat][id]; current == subscriber {
				delete(h.byChat[chat], id)
				close(subscriber.channel)
				if len(h.byChat[chat]) == 0 {
					delete(h.byChat, chat)
				}
			}
			h.mu.Unlock()
		})
	}
}
