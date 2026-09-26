// Package webuiapp composes the process-local browser authority with the
// embedded shell. It is a separate same-origin BFF boundary, never a cookie
// authentication mode for the native bearer API.
package webuiapp

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/browserauth"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

const (
	challengeCookie    = "darwin_browser_challenge"
	sessionCookie      = "darwin_browser_session"
	maxBrowserBody     = 4 << 10
	maxBrowserInFlight = 32
	maxBrowserStreams  = 8
	maxBoardStreams    = 8
)

var ErrConfiguration = errors.New("invalid browser application configuration")

type Options struct {
	BasePath       string
	AllowedHosts   []string
	AllowedOrigins []string
	SecureCookies  bool
	Store          *browserauth.Store
	Reads          ReadServices
	Mutations      MutationServices
	Inspections    InspectionServices
	Workboards     WorkboardServices
	LiveText       *LiveTextHub
	CursorKey      []byte
}

type Handler struct {
	basePath         string
	hosts            map[string]bool
	origins          map[string]bool
	secureCookies    bool
	store            *browserauth.Store
	reads            ReadServices
	mutations        MutationServices
	inspections      InspectionServices
	workboards       WorkboardServices
	liveText         *LiveTextHub
	shell            http.Handler
	bootstrap        http.Handler
	slots            chan struct{}
	streamSlots      chan struct{}
	boardStreamSlots chan struct{}
	mutationSlots    chan struct{}
	controlSlots     chan struct{}
	cursorKey        [32]byte
	boardStreamLife  time.Duration
}

func New(options Options) (*Handler, error) {
	if options.Store == nil || len(options.AllowedHosts) == 0 || len(options.AllowedHosts) > 8 {
		return nil, ErrConfiguration
	}
	hosts := map[string]bool{}
	for _, host := range options.AllowedHosts {
		if host == "" || strings.ContainsAny(host, "/\\?#@ ") || hosts[host] {
			return nil, ErrConfiguration
		}
		hosts[host] = true
	}
	origins := map[string]bool{}
	for _, origin := range options.AllowedOrigins {
		if origin == "" || origins[origin] {
			return nil, ErrConfiguration
		}
		origins[origin] = true
	}
	var cursorKey [32]byte
	if len(options.CursorKey) == 0 {
		if _, err := rand.Read(cursorKey[:]); err != nil {
			return nil, ErrConfiguration
		}
	} else if len(options.CursorKey) != len(cursorKey) {
		return nil, ErrConfiguration
	} else {
		copy(cursorKey[:], options.CursorKey)
	}
	handler := &Handler{basePath: options.BasePath, hosts: hosts, origins: origins, secureCookies: options.SecureCookies, store: options.Store, reads: options.Reads, mutations: options.Mutations, inspections: options.Inspections, workboards: options.Workboards, liveText: options.LiveText, slots: make(chan struct{}, maxBrowserInFlight), streamSlots: make(chan struct{}, maxBrowserStreams), boardStreamSlots: make(chan struct{}, maxBoardStreams), mutationSlots: make(chan struct{}, 8), controlSlots: make(chan struct{}, 4), cursorKey: cursorKey, boardStreamLife: 30 * time.Second}
	shell, err := contract.NewShellHandler(contract.ShellOptions{BasePath: options.BasePath, HostAllowed: handler.hostAllowed, Authenticated: handler.authenticated})
	if err != nil {
		return nil, ErrConfiguration
	}
	if handler.basePath == "" {
		handler.basePath = contract.DefaultShellBasePath
	}
	handler.shell = shell
	bootstrap, err := contract.NewBootstrapHandler(contract.BootstrapOptions{BasePath: handler.basePath, HostAllowed: handler.hostAllowed})
	if err != nil {
		return nil, ErrConfiguration
	}
	handler.bootstrap = bootstrap
	return handler, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	contract.ApplyBrowserSecurityHeaders(writer.Header())
	chatListPath := h.basePath + "/api/v1/chats"
	queryReadPath := request.URL != nil && (workboardBrowserQueryPath(h.basePath, request.URL.Path, request.Method) || request.URL.Path == chatListPath || request.URL.Path == h.basePath+"/api/v1/operations" || chatMessagesID(h.basePath, request.URL.Path) != "" || chatEventsID(h.basePath, request.URL.Path) != "" || boardEventsID(h.basePath, request.URL.Path) != "" || approvalListTaskID(h.basePath, request.URL.Path) != "" || inspectionQueryPath(h.basePath, request.URL.Path))
	if !h.hostAllowed(request.Host) || hasForwardedAuthority(request) || request.URL == nil || (request.URL.RawQuery != "" && !queryReadPath) || request.URL.RawPath != "" {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	if request.URL.Path != h.basePath && !strings.HasPrefix(request.URL.Path, h.basePath+"/") {
		h.writeError(writer, request, http.StatusNotFound, "not_found")
		return
	}
	if chat := chatEventsID(h.basePath, request.URL.Path); chat != "" {
		h.serveChatEvents(writer, request, chat)
		return
	}
	if board := boardEventsID(h.basePath, request.URL.Path); board != "" {
		h.serveBoardEvents(writer, request, board)
		return
	}
	if h.serveMutationAPI(writer, request) {
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		writer.Header().Set("Retry-After", "1")
		h.writeError(writer, request, http.StatusServiceUnavailable, "browser_capacity")
		return
	}
	if h.serveWorkboardAPI(writer, request) {
		return
	}
	challengePath := h.basePath + "/api/v1/session/challenges"
	sessionPath := h.basePath + "/api/v1/session"
	csrfPath := h.basePath + "/api/v1/session/csrf"
	logoutPath := h.basePath + "/api/v1/session/logout"
	if request.URL.Path == h.basePath+"/bootstrap" || strings.HasPrefix(request.URL.Path, h.basePath+"/bootstrap/") {
		h.bootstrap.ServeHTTP(writer, request)
		return
	}
	switch request.URL.Path {
	case challengePath:
		h.createChallenge(writer, request)
	case sessionPath:
		h.createSession(writer, request)
	case csrfPath:
		h.rotateCSRF(writer, request)
	case logoutPath:
		h.logout(writer, request)
	case chatListPath:
		h.serveChatList(writer, request)
	default:
		if h.serveInspectionAPI(writer, request) {
			return
		}
		if chat := chatMessagesID(h.basePath, request.URL.Path); chat != "" {
			h.serveTranscript(writer, request, chat)
			return
		}
		if request.URL.Path == h.basePath+"/api" || strings.HasPrefix(request.URL.Path, h.basePath+"/api/") {
			h.authenticatedAPINotFound(writer, request)
			return
		}
		if browserShellNavigation(h.basePath, request.URL.Path) && !h.authenticated(request) && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
			writer.Header().Set("Location", h.basePath+"/bootstrap")
			writer.WriteHeader(http.StatusFound)
			return
		}
		h.shell.ServeHTTP(writer, request)
	}
}

func browserShellNavigation(base, requestPath string) bool {
	if requestPath == base || requestPath == base+"/" || requestPath == base+"/chats" || requestPath == base+"/workboards" || requestPath == base+"/models" || requestPath == base+"/routing-map" || requestPath == base+"/model-elimination" || requestPath == base+"/settings" || requestPath == base+"/stats" {
		return true
	}
	for _, prefix := range []string{base + "/chats/", base + "/workboards/"} {
		if strings.HasPrefix(requestPath, prefix) && contract.ValidID(strings.TrimPrefix(requestPath, prefix)) {
			return true
		}
	}
	return false
}

func (h *Handler) ApproveChallenge(id, displayCode string) error {
	return h.store.Approve(id, displayCode)
}

func (h *Handler) createChallenge(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.sameOriginMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "bootstrap_denied")
		return
	}
	var input contract.BrowserChallengeRequest
	if decodeBrowserJSON(request, &input) != nil || input.Validate() != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	challenge, err := h.store.Create()
	if err != nil {
		writer.Header().Set("Retry-After", "60")
		h.writeError(writer, request, http.StatusTooManyRequests, "bootstrap_rate_limited")
		return
	}
	h.setCookie(writer, challengeCookie, challenge.Cookie, challenge.ExpiresAt)
	response := contract.BrowserChallengeResponse{Version: 1, ChallengeID: challenge.ID, DisplayCode: challenge.DisplayCode, ApprovalCode: challenge.ID + "." + challenge.DisplayCode, ExpiresAt: challenge.ExpiresAt}
	h.writeJSON(writer, http.StatusCreated, response)
}

func (h *Handler) createSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.sameOriginMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "bootstrap_denied")
		return
	}
	cookie, ok := exactCookie(request, challengeCookie)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "bootstrap_unavailable")
		return
	}
	var input contract.BrowserSessionRequest
	if decodeBrowserJSON(request, &input) != nil || input.Validate() != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	session, err := h.store.Consume(input.ChallengeID, cookie.Value)
	if err != nil {
		if errors.Is(err, browserauth.ErrExhausted) {
			writer.Header().Set("Retry-After", "60")
			h.writeError(writer, request, http.StatusTooManyRequests, "session_capacity")
			return
		}
		h.writeError(writer, request, http.StatusUnauthorized, "bootstrap_unavailable")
		return
	}
	if previous, exists := exactCookie(request, sessionCookie); exists {
		h.store.Revoke(previous.Value)
	}
	h.clearCookie(writer, challengeCookie)
	h.setCookie(writer, sessionCookie, session.Token, session.ExpiresAt)
	h.writeJSON(writer, http.StatusCreated, contract.BrowserSessionResponse{Version: 1, CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt})
}

func (h *Handler) rotateCSRF(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.sameOriginMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "request_denied")
		return
	}
	cookie, ok := exactCookie(request, sessionCookie)
	if !ok || !h.store.Authenticate(cookie.Value) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input contract.BrowserCSRFRequest
	if decodeBrowserJSON(request, &input) != nil || input.Validate() != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	session, err := h.store.RotateCSRF(cookie.Value)
	if err != nil {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	h.writeJSON(writer, http.StatusOK, contract.BrowserSessionResponse{Version: 1, CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt})
}

func (h *Handler) logout(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.sameOriginMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "request_denied")
		return
	}
	cookie, ok := exactCookie(request, sessionCookie)
	if !ok || !h.store.AuthorizeMutation(cookie.Value, request.Header.Get("X-Darwin-CSRF")) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input contract.BrowserLogoutRequest
	if decodeBrowserJSON(request, &input) != nil || input.Validate() != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	h.store.Revoke(cookie.Value)
	h.clearCookie(writer, sessionCookie)
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) authenticatedAPINotFound(writer http.ResponseWriter, request *http.Request) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead && !h.authorizedMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "request_denied")
		return
	}
	h.writeError(writer, request, http.StatusNotFound, "not_found")
}

func (h *Handler) authenticated(request *http.Request) bool {
	cookie, ok := exactCookie(request, sessionCookie)
	return ok && h.store.Authenticate(cookie.Value)
}

func (h *Handler) browserSubject(request *http.Request) (string, bool) {
	cookie, ok := exactCookie(request, sessionCookie)
	if !ok {
		return "", false
	}
	return h.store.Subject(cookie.Value)
}

func (h *Handler) authorizedMutation(request *http.Request) bool {
	cookie, ok := exactCookie(request, sessionCookie)
	return ok && h.sameOriginMutation(request) && h.store.AuthorizeMutation(cookie.Value, request.Header.Get("X-Darwin-CSRF"))
}

func (h *Handler) sameOriginMutation(request *http.Request) bool {
	if request.Header.Get("Sec-Fetch-Site") != "same-origin" || request.Header.Get("Origin") == "" {
		return false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	expected := scheme + "://" + request.Host
	if request.Header.Get("Origin") != expected {
		return false
	}
	return len(h.origins) == 0 || h.origins[expected]
}

func (h *Handler) hostAllowed(host string) bool { return h.hosts[host] }

func (h *Handler) setCookie(writer http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(writer, &http.Cookie{Name: name, Value: value, Path: h.basePath, Expires: expires, HttpOnly: true, Secure: h.secureCookies, SameSite: http.SameSiteStrictMode})
}

func (h *Handler) clearCookie(writer http.ResponseWriter, name string) {
	http.SetCookie(writer, &http.Cookie{Name: name, Path: h.basePath, MaxAge: -1, Expires: time.Unix(1, 0).UTC(), HttpOnly: true, Secure: h.secureCookies, SameSite: http.SameSiteStrictMode})
}

func (h *Handler) writeJSON(writer http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		h.writeError(writer, nil, http.StatusInternalServerError, "internal_error")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(body, '\n'))
}

func (h *Handler) writeError(writer http.ResponseWriter, request *http.Request, status int, code string) {
	h.writeJSONDirect(writer, status, map[string]any{"version": 1, "error": map[string]string{"code": code}})
}

func (h *Handler) writeJSONDirect(writer http.ResponseWriter, status int, value any) {
	body, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(body, '\n'))
}

func decodeBrowserJSON(request *http.Request, target any) error {
	if request.Body == nil || request.Header.Get("Content-Type") != "application/json" {
		return errors.New("invalid body")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBrowserBody+1))
	if err != nil || len(body) > maxBrowserBody || contract.RejectDuplicateJSONFields(body) != nil {
		return errors.New("invalid body")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing body")
	}
	return nil
}

func exactCookie(request *http.Request, name string) (*http.Cookie, bool) {
	var found *http.Cookie
	for _, cookie := range request.Cookies() {
		if cookie.Name != name {
			continue
		}
		if found != nil || cookie.Value == "" {
			return nil, false
		}
		copy := *cookie
		found = &copy
	}
	return found, found != nil
}

func hasForwardedAuthority(request *http.Request) bool {
	for _, name := range []string{"Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Original-Host"} {
		if request.Header.Get(name) != "" {
			return true
		}
	}
	return false
}
