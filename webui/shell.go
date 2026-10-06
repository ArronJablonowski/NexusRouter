package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode"
)

const (
	ShellAssetVersion    = "v1"
	DefaultShellBasePath = "/app"
	MaxShellBasePath     = 128
	MaxShellRequestPath  = 2048

	shellCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; worker-src 'none'; manifest-src 'self'"
)

var ErrShellConfiguration = errors.New("invalid web UI shell configuration")

//go:embed assets/v1/*
var embeddedShellAssets embed.FS

// ShellAssetDigest identifies the exact checked-in bytes embedded into release
// binaries. The fixed ordering and separators make it independent of host
// filesystem metadata.
func ShellAssetDigest() (string, error) {
	hash := sha256.New()
	for _, name := range []string{"assets/v1/index.html", "assets/v1/app.css", "assets/v1/operation-contract.js", "assets/v1/live.js", "assets/v1/inspector.js", "assets/v1/workboard-client.js", "assets/v1/workboards.js", "assets/v1/active-jobs.js", "assets/v1/workboard-mutations.js", "assets/v1/settings.js", "assets/v1/remote-membership.js", "assets/v1/remote-discovery.js", "assets/v1/remote-pair-form.js", "assets/v1/remote-inspection.js", "assets/v1/remote-task-controls.js", "assets/v1/remote-events.js", "assets/v1/remote-dispatch.js", "assets/v1/remote-automatic.js", "assets/v1/remote-review.js", "assets/v1/stats.js", "assets/v1/skills.js", "assets/v1/models.js", "assets/v1/status.js", "assets/v1/cron.js", "assets/v1/logging.js", "assets/v1/remote-models.js", "assets/v1/routing-map.js", "assets/v1/routing-remote.js", "assets/v1/chat-render.js", "assets/v1/chat-descriptions.js", "assets/v1/app.js", "assets/v1/bootstrap.html", "assets/v1/bootstrap.css", "assets/v1/bootstrap.js"} {
		body, err := fs.ReadFile(embeddedShellAssets, name)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(body)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ShellOptions deliberately receives host and session policy from the daemon.
// The shell never accepts bearer credentials or creates browser authority.
type ShellOptions struct {
	BasePath      string
	HostAllowed   func(host string) bool
	Authenticated func(request *http.Request) bool
}

type BootstrapOptions struct {
	BasePath    string
	HostAllowed func(host string) bool
}

// NewShellHandler returns an authenticated, same-origin static application
// shell. Application API and session endpoints must be mounted separately.
func NewShellHandler(options ShellOptions) (http.Handler, error) {
	basePath, ok := normalizeShellBasePath(options.BasePath)
	if !ok || options.HostAllowed == nil || options.Authenticated == nil {
		return nil, ErrShellConfiguration
	}
	assets, err := loadShellAssets(basePath)
	if err != nil {
		return nil, ErrShellConfiguration
	}
	return &shellHandler{
		basePath:      basePath,
		hostAllowed:   options.HostAllowed,
		authenticated: options.Authenticated,
		assets:        assets,
	}, nil
}

type shellAsset struct {
	body        []byte
	contentType string
}

type shellHandler struct {
	basePath      string
	hostAllowed   func(string) bool
	authenticated func(*http.Request) bool
	assets        map[string]shellAsset
}

type bootstrapHandler struct {
	basePath    string
	hostAllowed func(string) bool
	assets      map[string]shellAsset
}

// NewBootstrapHandler serves only the non-sensitive login initiator required
// to establish browser authentication. It cannot serve application routes or
// authenticated shell assets.
func NewBootstrapHandler(options BootstrapOptions) (http.Handler, error) {
	basePath, ok := normalizeShellBasePath(options.BasePath)
	if !ok || options.HostAllowed == nil {
		return nil, ErrShellConfiguration
	}
	assets := map[string]shellAsset{}
	for _, name := range []string{"bootstrap.html", "bootstrap.css", "bootstrap.js"} {
		body, err := fs.ReadFile(embeddedShellAssets, "assets/v1/"+name)
		if err != nil {
			return nil, ErrShellConfiguration
		}
		body = bytes.ReplaceAll(body, []byte("__DARWIN_BASE_PATH__"), []byte(basePath))
		assets[name] = shellAsset{body: body, contentType: mime.TypeByExtension(path.Ext(name))}
	}
	return &bootstrapHandler{basePath: basePath, hostAllowed: options.HostAllowed, assets: assets}, nil
}

func (h *bootstrapHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setShellHeaders(writer.Header())
	if !h.hostAllowed(request.Host) || request.URL == nil || request.URL.RawPath != "" || request.URL.RawQuery != "" || !safeShellPath(request.URL.Path) {
		writeShellError(writer, request, http.StatusBadRequest)
		return
	}
	if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" && site != "same-origin" {
		writeShellError(writer, request, http.StatusForbidden)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeShellError(writer, request, http.StatusMethodNotAllowed)
		return
	}
	name := ""
	switch request.URL.Path {
	case h.basePath + "/bootstrap", h.basePath + "/bootstrap/":
		name = "bootstrap.html"
	case h.basePath + "/bootstrap/v1/bootstrap.css":
		name = "bootstrap.css"
	case h.basePath + "/bootstrap/v1/bootstrap.js":
		name = "bootstrap.js"
	default:
		writeShellError(writer, request, http.StatusNotFound)
		return
	}
	asset := h.assets[name]
	digest := sha256.Sum256(asset.body)
	writer.Header().Set("ETag", "\""+hex.EncodeToString(digest[:])+"\"")
	writer.Header().Set("Content-Type", asset.contentType)
	writer.Header().Set("Content-Length", decimalLength(len(asset.body)))
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(asset.body)
	}
}

func (h *shellHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setShellHeaders(writer.Header())
	if !h.hostAllowed(request.Host) {
		writeShellError(writer, request, http.StatusBadRequest)
		return
	}
	name, route, ok := h.resolve(request)
	if !ok {
		writeShellError(writer, request, http.StatusNotFound)
		return
	}
	if !h.authenticated(request) {
		writeShellError(writer, request, http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeShellError(writer, request, http.StatusMethodNotAllowed)
		return
	}
	if route {
		name = "index.html"
	}
	asset, exists := h.assets[name]
	if !exists {
		writeShellError(writer, request, http.StatusNotFound)
		return
	}
	digest := sha256.Sum256(asset.body)
	writer.Header().Set("ETag", "\""+hex.EncodeToString(digest[:])+"\"")
	writer.Header().Set("Content-Type", asset.contentType)
	writer.Header().Set("Content-Length", decimalLength(len(asset.body)))
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(asset.body)
	}
}

func (h *shellHandler) resolve(request *http.Request) (name string, route bool, ok bool) {
	if request.URL == nil || request.URL.RawPath != "" || !safeShellPath(request.URL.Path) {
		return "", false, false
	}
	requestPath := request.URL.Path
	if requestPath == h.basePath || requestPath == h.basePath+"/" {
		return "index.html", true, true
	}
	prefix := h.basePath + "/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", false, false
	}
	relative := strings.TrimPrefix(requestPath, prefix)
	if relative == "index.html" {
		return relative, false, true
	}
	if strings.HasPrefix(relative, "assets/") {
		_, exists := h.assets[relative]
		return relative, false, exists
	}
	// Unknown file-like paths are not application routes. This prevents a typo
	// from returning executable HTML under an attacker-controlled file name.
	if strings.Contains(path.Base(relative), ".") {
		return "", false, false
	}
	return "index.html", true, true
}

func loadShellAssets(basePath string) (map[string]shellAsset, error) {
	names := []string{"index.html", "assets/v1/app.css", "assets/v1/operation-contract.js", "assets/v1/live.js", "assets/v1/inspector.js", "assets/v1/workboard-client.js", "assets/v1/workboards.js", "assets/v1/active-jobs.js", "assets/v1/workboard-mutations.js", "assets/v1/settings.js", "assets/v1/remote-membership.js", "assets/v1/remote-discovery.js", "assets/v1/remote-pair-form.js", "assets/v1/remote-inspection.js", "assets/v1/remote-task-controls.js", "assets/v1/remote-events.js", "assets/v1/remote-dispatch.js", "assets/v1/remote-automatic.js", "assets/v1/remote-review.js", "assets/v1/stats.js", "assets/v1/skills.js", "assets/v1/models.js", "assets/v1/status.js", "assets/v1/cron.js", "assets/v1/logging.js", "assets/v1/remote-models.js", "assets/v1/routing-map.js", "assets/v1/routing-remote.js", "assets/v1/chat-render.js", "assets/v1/chat-descriptions.js", "assets/v1/app.js"}
	loaded := make(map[string]shellAsset, len(names))
	for _, name := range names {
		embedName := name
		if name == "index.html" {
			embedName = "assets/v1/index.html"
		}
		body, err := fs.ReadFile(embeddedShellAssets, embedName)
		if err != nil {
			return nil, err
		}
		if name == "index.html" {
			css, e := fs.ReadFile(embeddedShellAssets, "assets/v1/app.css")
			if e != nil {
				return nil, e
			}
			digest := sha256.Sum256(css)
			body = bytes.ReplaceAll(body, []byte("__NEXUS_CSS_DIGEST__"), []byte(hex.EncodeToString(digest[:])))
			body = bytes.ReplaceAll(body, []byte("__DARWIN_BASE_PATH__"), []byte(basePath))
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			return nil, ErrShellConfiguration
		}
		loaded[name] = shellAsset{body: body, contentType: contentType}
	}
	return loaded, nil
}

func normalizeShellBasePath(value string) (string, bool) {
	if value == "" {
		value = DefaultShellBasePath
	}
	if len(value) < 2 || len(value) > MaxShellBasePath || value[0] != '/' || strings.HasSuffix(value, "/") || !safeShellPath(value) {
		return "", false
	}
	return value, true
}

func safeShellPath(value string) bool {
	if value == "" || len(value) > MaxShellRequestPath || !strings.HasPrefix(value, "/") || strings.Contains(value, "//") || strings.ContainsRune(value, '\\') {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if character > unicode.MaxASCII || !(unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("-_.", character)) {
				return false
			}
		}
	}
	return true
}

func setShellHeaders(header http.Header) {
	header.Set("Content-Security-Policy", shellCSP)
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cache-Control", "no-store")
}

// ApplyBrowserSecurityHeaders applies the version-1 browser boundary to shell,
// bootstrap, API, and error responses without enabling CORS.
func ApplyBrowserSecurityHeaders(header http.Header) { setShellHeaders(header) }

func writeShellError(writer http.ResponseWriter, request *http.Request, status int) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	body := []byte(http.StatusText(status) + "\n")
	writer.Header().Set("Content-Length", decimalLength(len(body)))
	writer.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(body)
	}
}

func decimalLength(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
