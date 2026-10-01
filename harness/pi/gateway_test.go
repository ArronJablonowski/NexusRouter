package pi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type policyTransport func(*http.Request) (*http.Response, error)

func (p policyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return p(r) }

const gatewayBody = `{"model":"model","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"answer"}]}`

func gatewayCall(t *testing.T, base, key, body string) int {
	t.Helper()
	r, e := http.NewRequest(http.MethodPost, base+"/chat/completions", strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("X-Untrusted", "do-not-forward")
	response, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}
func TestGatewayUsesOnlyPolicyTransportAndProviderCredential(t *testing.T) {
	var calls atomic.Int32
	c := identityConfig()
	c.BaseURL = "https://provider.invalid/v1"
	c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://provider.invalid/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret-one" || r.Header.Get("X-Untrusted") != "" {
			t.Error("policy request changed endpoint or leaked headers")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, nil
	})
	base, key, closeGateway, e := startGateway(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	defer closeGateway()
	if key == c.APIKey {
		t.Fatal("provider credential used as child credential")
	}
	if code := gatewayCall(t, base, c.APIKey, gatewayBody); code != 403 || calls.Load() != 0 {
		t.Fatal("wrong token reached provider", code)
	}
	if code := gatewayCall(t, base, key, gatewayBody); code != 200 || calls.Load() != 1 {
		t.Fatal(code, calls.Load())
	}
	if code := gatewayCall(t, base, key, gatewayBody); code != 409 || calls.Load() != 1 {
		t.Fatal("duplicate dispatched", code, calls.Load())
	}
}
func TestGatewayDeniedTransportHasNoDirectFallback(t *testing.T) {
	var direct, policy atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1) }))
	defer provider.Close()
	c := identityConfig()
	c.BaseURL = provider.URL
	c.Transport = policyTransport(func(*http.Request) (*http.Response, error) { policy.Add(1); return nil, errors.New("policy denied") })
	base, key, closeGateway, e := startGateway(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	defer closeGateway()
	if code := gatewayCall(t, base, key, gatewayBody); code != 502 || direct.Load() != 0 || policy.Load() != 1 {
		t.Fatal("policy bypass", code, direct.Load(), policy.Load())
	}
}
func TestGatewayRejectsUnsupportedRequestsBeforeDispatch(t *testing.T) {
	for _, body := range []string{
		strings.Replace(gatewayBody, `"model":"model"`, `"model":"other"`, 1),
		strings.Replace(gatewayBody, "1024", "2048", 1),
		strings.Replace(gatewayBody, `"stream":true`, `"stream":false`, 1),
		strings.Replace(gatewayBody, `"model":"model"`, `"model":"other","model":"model"`, 1),
		strings.Replace(gatewayBody, `"stream":true`, `"tools":[],"stream":true`, 1),
		strings.Replace(gatewayBody, `"role":"user"`, `"role":"tool"`, 1),
		strings.Replace(gatewayBody, `"content":"answer"`, `"content":"answer","tool_calls":[]`, 1),
		strings.Replace(gatewayBody, `"content":"answer"`, `"content":[{"type":"image_url","image_url":"http://external"}]`, 1),
		strings.Replace(gatewayBody, `"content":"answer"`, `"content":null`, 1),
		strings.Replace(gatewayBody, `"stream":true`, `"store":true,"stream":true`, 1),
	} {
		if validGatewayRequest([]byte(body), "model", 1024) {
			t.Fatal("unsafe request accepted", body)
		}
	}
}
func TestGatewayDoesNotFollowRedirect(t *testing.T) {
	var calls atomic.Int32
	c := identityConfig()
	c.Transport = policyTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.invalid/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	base, key, closeGateway, e := startGateway(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	defer closeGateway()
	if code := gatewayCall(t, base, key, gatewayBody); code != 502 || calls.Load() != 1 {
		t.Fatal("redirect followed", code, calls.Load())
	}
}

func TestGatewayCloseCancelsAndJoinsUpstream(t *testing.T) {
	entered := make(chan struct{})
	joined := make(chan struct{})
	c := identityConfig()
	c.APIKey = ""
	c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("invented provider credential")
		}
		close(entered)
		<-r.Context().Done()
		close(joined)
		return nil, r.Context().Err()
	})
	base, key, closeGateway, e := startGateway(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, _ := http.NewRequest(http.MethodPost, base+"/chat/completions", strings.NewReader(gatewayBody))
		r.Header.Set("Authorization", "Bearer "+key)
		response, e := http.DefaultClient.Do(r)
		if e == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		closeGateway()
		t.Fatal("upstream did not start")
	}
	closeGateway()
	select {
	case <-joined:
	default:
		t.Fatal("gateway returned before upstream joined")
	}
	<-done
}
func TestGatewayConfigRequiresExplicitTransportPolicy(t *testing.T) {
	c := identityConfig()
	c.Transport = nil
	if c.validate() == nil {
		t.Fatal("implicit default transport accepted")
	}
	c = identityConfig()
	var typedNil policyTransport
	c.Transport = typedNil
	if c.validate() == nil {
		t.Fatal("typed nil transport accepted")
	}
	c = identityConfig()
	c.TransportPolicySHA256 = ""
	if c.validate() == nil {
		t.Fatal("missing policy identity accepted")
	}
}
