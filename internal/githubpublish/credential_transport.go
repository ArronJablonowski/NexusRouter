package githubpublish

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

var ErrCredentialTransport = errors.New("GitHub credential transport rejected request")

type SecretSource interface {
	GitHubCredential(context.Context) (Credential, error)
}

type Credential struct {
	Token              []byte
	ContentsWrite      bool
	AdministrationRead bool
}

// credentialTransport never escapes this package. The only production creator
// is PublishCredentialedRelease, which leases it for one exact operation.
type credentialTransport struct {
	base       RoundTripper
	repository string
	mu         sync.RWMutex
	token      []byte
	closed     bool
}

func newCredentialTransport(ctx context.Context, base RoundTripper, secrets SecretSource, repository string) (*credentialTransport, error) {
	if ctx == nil || ctx.Err() != nil || base == nil || secrets == nil || !validCredentialRepository(repository) {
		return nil, ErrCredentialTransport
	}
	credential, err := secrets.GitHubCredential(ctx)
	if err != nil || !credential.ContentsWrite || !credential.AdministrationRead || !validCredentialToken(credential.Token) {
		clear(credential.Token)
		return nil, ErrCredentialTransport
	}
	token := bytes.Clone(credential.Token)
	clear(credential.Token)
	return &credentialTransport{base: base, repository: repository, token: token}, nil
}

func (t *credentialTransport) Close() error {
	if t == nil {
		return ErrCredentialTransport
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrCredentialTransport
	}
	clear(t.token)
	t.token, t.closed = nil, true
	return nil
}

func (t *credentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil {
		return nil, ErrCredentialTransport
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed || !credentialRequestAllowed(request, t.repository) {
		return nil, ErrCredentialTransport
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+string(t.token))
	response, err := t.base.RoundTrip(clone)
	clone.Header.Del("Authorization")
	if err != nil || response == nil {
		return nil, ErrCredentialTransport
	}
	response.Request = request
	return response, nil
}

func credentialRequestAllowed(request *http.Request, repository string) bool {
	if request == nil || request.Context() == nil || request.Context().Err() != nil || request.URL == nil ||
		request.URL.Scheme != "https" || request.URL.User != nil || request.URL.RawPath != "" ||
		(request.Host != "" && request.Host != request.URL.Host) || request.Response != nil || request.Header.Get("Authorization") != "" ||
		request.Header.Get("X-GitHub-Api-Version") != APIVersion {
		return false
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || query.Encode() != request.URL.RawQuery {
		return false
	}
	host := request.URL.Hostname()
	if request.URL.Port() != "" && request.URL.Port() != "443" {
		return false
	}
	if strings.EqualFold(host, "api.github.com") {
		return allowedAPIRequest(request, repository)
	}
	if strings.EqualFold(host, "uploads.github.com") {
		return allowedUploadRequest(request, repository)
	}
	return false
}

func allowedAPIRequest(request *http.Request, repository string) bool {
	base := "/repos/" + repository
	requestPath := request.URL.Path
	query := request.URL.Query()
	if len(query) != 0 && (request.Method != http.MethodGet || !regexp.MustCompile(`^`+regexp.QuoteMeta(base)+`/releases/[1-9][0-9]*/assets$`).MatchString(requestPath) || len(query) != 2 || query.Get("page") != "1" || query.Get("per_page") != "100") {
		return false
	}
	switch request.Method {
	case http.MethodGet:
		return requestPath == base+"/immutable-releases" || requestPath == base+"/releases/latest" || regexp.MustCompile(`^`+regexp.QuoteMeta(base)+`/(git/ref/tags|releases/tags)/[^/]+$`).MatchString(requestPath) || regexp.MustCompile(`^`+regexp.QuoteMeta(base)+`/git/tags/[0-9a-f]{40}$`).MatchString(requestPath) || regexp.MustCompile(`^`+regexp.QuoteMeta(base)+`/releases/[1-9][0-9]*(/assets)?$`).MatchString(requestPath)
	case http.MethodPost:
		return requestPath == base+"/git/tags" || requestPath == base+"/git/refs" || requestPath == base+"/releases"
	case http.MethodPatch:
		return regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `/releases/[1-9][0-9]*$`).MatchString(requestPath)
	default:
		return false
	}
}

func allowedUploadRequest(request *http.Request, repository string) bool {
	if request.Method != http.MethodPost || !regexp.MustCompile(`^/repos/`+regexp.QuoteMeta(repository)+`/releases/[1-9][0-9]*/assets$`).MatchString(request.URL.Path) {
		return false
	}
	query := request.URL.Query()
	return len(query) == 1 && query.Get("name") != "" && len(query["name"]) == 1 && assetNameRE.MatchString(query.Get("name"))
}

func validCredentialRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && nameRE.MatchString(parts[0]) && nameRE.MatchString(parts[1])
}

func validCredentialToken(token []byte) bool {
	if len(token) < 1 || len(token) > 4096 {
		return false
	}
	for _, value := range token {
		if value < 0x21 || value > 0x7e {
			return false
		}
	}
	return true
}
