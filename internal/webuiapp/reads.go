package webuiapp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

type ReadServices struct {
	JobTasks        func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error)
	Submissions     func(context.Context, submissions.ListOptions) (submissions.Page, error)
	Tasks           func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error)
	Chats           func(context.Context, sessions.ChatListOptions) (sessions.ChatPage, error)
	History         func(context.Context, string, contract.HistoryOptions) (contract.HistoryPage, error)
	CommittedEvents func(context.Context, sessions.EventLogOptions) (sessions.CommittedEventPage, error)
}

func chatMessagesID(base, path string) string {
	prefix, suffix := base+"/api/v1/chats/", "/messages"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if !contract.ValidID(id) {
		return ""
	}
	return id
}

func strictBrowserGET(request *http.Request) bool {
	return request.Method == http.MethodGet && request.Body == http.NoBody && request.ContentLength == 0 && len(request.TransferEncoding) == 0 && !request.URL.ForceQuery
}

func parseChatListQuery(raw string) (sessions.ChatListOptions, error) {
	options := sessions.ChatListOptions{Limit: 25}
	if len(raw) > 4096 {
		return options, sessions.ErrTaskList
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return options, sessions.ErrTaskList
	}
	for key, items := range values {
		if len(items) != 1 {
			return options, sessions.ErrTaskList
		}
		switch key {
		case "after":
			if len(items[0]) > 1024 {
				return options, sessions.ErrTaskList
			}
			options.After = items[0]
		case "limit":
			options.Limit, err = strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(options.Limit) != items[0] {
				return options, sessions.ErrTaskList
			}
		default:
			return options, sessions.ErrTaskList
		}
	}
	return options, options.Validate()
}

func (h *Handler) serveChatList(writer http.ResponseWriter, request *http.Request) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseChatListQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_chat_query")
		return
	}
	if h.reads.Chats == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "chats_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	page, err := safeChats(ctx, h.reads.Chats, options)
	if err != nil || page.Validate() != nil || len(page.Items) > options.Limit {
		h.writeError(writer, request, http.StatusServiceUnavailable, "chats_unavailable")
		return
	}
	h.writeJSON(writer, http.StatusOK, page)
}

func (h *Handler) serveTranscript(writer http.ResponseWriter, request *http.Request, chat string) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseHistoryQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_history_query")
		return
	}
	if h.reads.History == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "transcript_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	history, err := safeHistory(ctx, h.reads.History, chat, options)
	if err != nil || history.Validate() != nil || history.ChatID != chat || len(history.Messages) > options.Limit {
		h.writeError(writer, request, http.StatusServiceUnavailable, "transcript_unavailable")
		return
	}
	h.writeJSON(writer, http.StatusOK, history)
}

func parseHistoryQuery(raw string) (contract.HistoryOptions, error) {
	options := contract.HistoryOptions{Limit: contract.MaxHistoryMessages}
	if len(raw) > 4096 {
		return options, contract.ErrContract
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return options, contract.ErrContract
	}
	for key, items := range values {
		if len(items) != 1 {
			return options, contract.ErrContract
		}
		switch key {
		case "after":
			options.After = items[0]
		case "limit":
			options.Limit, err = strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(options.Limit) != items[0] {
				return options, contract.ErrContract
			}
		default:
			return options, contract.ErrContract
		}
	}
	return options, options.Validate()
}

func safeChats(ctx context.Context, read func(context.Context, sessions.ChatListOptions) (sessions.ChatPage, error), options sessions.ChatListOptions) (page sessions.ChatPage, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("chats unavailable")
		}
	}()
	return read(ctx, options)
}

func safeHistory(ctx context.Context, read func(context.Context, string, contract.HistoryOptions) (contract.HistoryPage, error), chat string, options contract.HistoryOptions) (history contract.HistoryPage, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("transcript unavailable")
		}
	}()
	return read(ctx, chat, options)
}
