package githubpublish

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type secretFixture struct {
	credential Credential
	err        error
	calls      int
}

func (s *secretFixture) GitHubCredential(context.Context) (Credential, error) {
	s.calls++
	return s.credential, s.err
}

type transportFixture struct {
	calls int
	check func(*http.Request)
	last  *http.Request
}

func (t *transportFixture) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	t.last = request
	if t.check != nil {
		t.check(request)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
}

func TestCredentialTransportAttachesOnlyToFixedGitHubOrigins(t *testing.T) {
	for _, rawURL := range []string{"https://api.github.com/repos/acme/router/immutable-releases", "https://uploads.github.com/repos/acme/router/releases/1/assets?name=asset.tar.gz"} {
		t.Run(rawURL, func(t *testing.T) {
			token := []byte("github_pat_test-secret")
			secret := &secretFixture{credential: Credential{Token: token, ContentsWrite: true, AdministrationRead: true}}
			base := &transportFixture{check: func(request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer github_pat_test-secret" {
					t.Fatal("credential not attached to exact origin")
				}
			}}
			transport, err := newCredentialTransport(t.Context(), base, secret, "acme/router")
			if err != nil {
				t.Fatal(err)
			}
			method := http.MethodGet
			if strings.HasPrefix(rawURL, "https://uploads.github.com/") {
				method = http.MethodPost
			}
			request, _ := http.NewRequestWithContext(t.Context(), method, rawURL, nil)
			request.Header.Set("X-GitHub-Api-Version", APIVersion)
			if !credentialRequestAllowed(request, "acme/router") {
				t.Fatalf("fixture request rejected before credential lookup: url=%#v host=%q response=%v auth=%q", request.URL, request.Host, request.Response, request.Header.Get("Authorization"))
			}
			response, err := transport.RoundTrip(request)
			if err != nil || response == nil || secret.calls != 1 || base.calls != 1 || request.Header.Get("Authorization") != "" {
				t.Fatal("safe request rejected or caller request mutated", err)
			}
			if response.Request != request {
				t.Fatal("authorized request escaped through response metadata")
			}
			if base.last.Header.Get("Authorization") != "" {
				t.Fatal("cloned authorization header retained after RoundTrip")
			}
			response.Body.Close()
			if err = transport.Close(); err != nil {
				t.Fatal(err)
			}
			for _, value := range token {
				if value != 0 {
					t.Fatal("secret-source token bytes were not cleared")
				}
			}
		})
	}
}

func TestCredentialTransportCloseClearsPrivateLeaseCopy(t *testing.T) {
	source := []byte("github_pat_private-lease-copy")
	transport, err := newCredentialTransport(t.Context(), &transportFixture{}, &secretFixture{
		credential: Credential{Token: source, ContentsWrite: true, AdministrationRead: true},
	}, "acme/router")
	if err != nil {
		t.Fatal(err)
	}
	leaseCopy := transport.token
	if len(leaseCopy) == 0 {
		t.Fatal("credential lease copy missing before close")
	}
	if err = transport.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.token != nil || !transport.closed {
		t.Fatal("closed credential transport retained its token")
	}
	for _, value := range leaseCopy {
		if value != 0 {
			t.Fatal("private credential lease bytes survived close")
		}
	}
	if err = transport.Close(); !errors.Is(err, ErrCredentialTransport) {
		t.Fatal("closed credential transport was reusable", err)
	}
}

func TestCredentialTransportAllowsExactAnnotatedTagRead(t *testing.T) {
	sha := strings.Repeat("a", 40)
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/acme/router/git/tags/"+sha, nil)
	request.Header.Set("X-GitHub-Api-Version", APIVersion)
	request.Host = ""
	if !credentialRequestAllowed(request, "acme/router") {
		t.Fatalf("normal Go request for the exact annotated tag object was rejected: host=%q url_host=%q path=%q raw_path=%q api=%q expected=%q api_allowed=%v", request.Host, request.URL.Host, request.URL.Path, request.URL.RawPath, request.Header.Get("X-GitHub-Api-Version"), APIVersion, allowedAPIRequest(request, "acme/router"))
	}
	request.Host = "evil.example"
	if credentialRequestAllowed(request, "acme/router") {
		t.Fatal("explicit mismatched Host override accepted")
	}
}

func TestCredentialTransportMatchesPublisherOriginsAndLatestRoute(t *testing.T) {
	for _, raw := range []string{
		"https://API.GITHUB.COM/repos/acme/router/releases/latest",
		"https://api.github.com:443/repos/acme/router/immutable-releases",
		"https://UPLOADS.GITHUB.COM:443/repos/acme/router/releases/1/assets?name=asset.tar.gz",
	} {
		method := http.MethodGet
		if strings.Contains(raw, "UPLOADS") {
			method = http.MethodPost
		}
		request, _ := http.NewRequestWithContext(t.Context(), method, raw, nil)
		request.Header.Set("X-GitHub-Api-Version", APIVersion)
		if !credentialRequestAllowed(request, "acme/router") {
			t.Fatal("Publisher-accepted endpoint rejected", raw)
		}
	}
}

func TestCredentialTransportRejectsBeforeBaseOrSecretDisclosure(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request, *secretFixture){
		"http":                   func(r *http.Request, _ *secretFixture) { r.URL.Scheme = "http" },
		"custom_origin":          func(r *http.Request, _ *secretFixture) { r.URL.Host = "evil.example" },
		"port":                   func(r *http.Request, _ *secretFixture) { r.URL.Host = "api.github.com:444" },
		"host_override":          func(r *http.Request, _ *secretFixture) { r.Host = "evil.example" },
		"redirect":               func(r *http.Request, _ *secretFixture) { r.Response = &http.Response{StatusCode: 302} },
		"existing_authorization": func(r *http.Request, _ *secretFixture) { r.Header.Set("Authorization", "Bearer attacker") },
		"malformed_query":        func(r *http.Request, _ *secretFixture) { r.URL.RawQuery = "name=x;y" },
		"noncanonical_query":     func(r *http.Request, _ *secretFixture) { r.URL.RawQuery = "page=%31&per_page=100" },
	} {
		t.Run(name, func(t *testing.T) {
			secret := &secretFixture{credential: Credential{Token: []byte("secret-value"), ContentsWrite: true, AdministrationRead: true}}
			base := &transportFixture{}
			transport, err := newCredentialTransport(t.Context(), base, secret, "acme/router")
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/acme/router/immutable-releases", nil)
			request.Header.Set("X-GitHub-Api-Version", APIVersion)
			mutate(request, secret)
			_, err = transport.RoundTrip(request)
			if !errors.Is(err, ErrCredentialTransport) || base.calls != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe credential request was not generically rejected", err, base.calls)
			}
		})
	}
}

func TestCredentialTransportRedactsSecretSourceFailures(t *testing.T) {
	secret := &secretFixture{err: errors.New("token github_pat_leak failed")}
	base := &transportFixture{}
	transport, _ := newCredentialTransport(t.Context(), base, secret, "acme/router")
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/acme/router/immutable-releases", nil)
	request.Header.Set("X-GitHub-Api-Version", APIVersion)
	_, err := transport.RoundTrip(request)
	if !errors.Is(err, ErrCredentialTransport) || strings.Contains(err.Error(), "github_pat") || base.calls != 0 {
		t.Fatal("secret source failure leaked", err)
	}
}

func TestCredentialTransportRejectsInsufficientLeaseBeforeUse(t *testing.T) {
	for name, credential := range map[string]Credential{
		"missing_contents":       {Token: []byte("token"), AdministrationRead: true},
		"missing_administration": {Token: []byte("token"), ContentsWrite: true},
		"newline":                {Token: []byte("token\n"), ContentsWrite: true, AdministrationRead: true},
	} {
		t.Run(name, func(t *testing.T) {
			secret := &secretFixture{credential: credential}
			if _, err := newCredentialTransport(t.Context(), &transportFixture{}, secret, "acme/router"); !errors.Is(err, ErrCredentialTransport) || strings.Contains(err.Error(), "token") {
				t.Fatal("insufficient credential lease accepted or leaked", err)
			}
		})
	}
}

func TestCredentialedPublicationRejectsArbitraryPlanBeforeCredentialLease(t *testing.T) {
	secret := &secretFixture{credential: Credential{Token: []byte("secret"), ContentsWrite: true, AdministrationRead: true}}
	plan := publicationPlan(t)
	plan.Tag = "arbitrary"
	if _, err := PublishCredentialedRelease(t.Context(), CredentialPublicationConfig{
		APIBase: "https://api.github.com", UploadBase: "https://uploads.github.com",
		Transport: &transportFixture{}, Secrets: secret,
	}, plan); !errors.Is(err, ErrCredentialTransport) || secret.calls != 0 {
		t.Fatal("unauthorized arbitrary plan acquired credential", err, secret.calls)
	}
}

func TestCredentialedPublicationLeaseIsScopedToOneValidatedOperation(t *testing.T) {
	token := []byte("operation-secret")
	secret := &secretFixture{credential: Credential{Token: token, ContentsWrite: true, AdministrationRead: true}}
	base := &transportFixture{check: func(request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer operation-secret" || request.URL.Path != "/repos/acme/darwin/immutable-releases" {
			t.Fatal("credential escaped first exact operation request")
		}
	}}
	baseResponse := func(request *http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"message":"disabled"}`)), Request: request}
	}
	base.check = func(request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer operation-secret" {
			t.Fatal("credential missing during leased request")
		}
	}
	// Override the fixture response to the policy endpoint's disabled status.
	wrapped := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		base.calls++
		base.last = request
		base.check(request)
		return baseResponse(request), nil
	})
	_, err := PublishCredentialedRelease(t.Context(), CredentialPublicationConfig{
		APIBase: "https://api.github.com", UploadBase: "https://uploads.github.com", Transport: wrapped, Secrets: secret,
	}, publicationPlan(t))
	if !errors.Is(err, ErrPublish) || secret.calls != 1 || base.calls != 1 || base.last.Header.Get("Authorization") != "" {
		t.Fatal("credential lease was not bounded", err, secret.calls, base.calls)
	}
	for _, value := range token {
		if value != 0 {
			t.Fatal("secret source token was not cleared")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
