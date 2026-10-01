package goose

import (
	"net/http"
)

type policyTransport func(*http.Request) (*http.Response, error)

func (p policyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return p(r) }
func completionFixture(model string) string {
	return `data: {"id":"one","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant","content":"answer"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"one","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
}
