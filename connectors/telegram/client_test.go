package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "123456789:abcdefghijklmnopqrstuvwxyzABCDE123456"

func TestClientPrivateJSONContract(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("request contract")
		}
		switch r.URL.Path {
		case "/getUpdates":
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":4,"message":{"message_id":2,"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"hello"}}]}`)
		case "/sendMessage":
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":3,"chat":{"id":7,"type":"private"}}}`)
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	c, err := NewClient(testToken)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("constructor contacted Telegram")
	}
	c.endpoint = server.URL + "/"
	updates, err := c.Updates(context.Background(), 4)
	if err != nil || len(updates) != 1 || updates[0].Message.Text != "hello" {
		t.Fatal(updates, err)
	}
	if err = c.Send(context.Background(), 7, "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestClientBoundsRedirectsAndRedactedErrors(t *testing.T) {
	for _, mode := range []string{"oversize", "error", "redirect", "bad-chat", "many-updates", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			redirected := false
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
			defer other.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "oversize":
					fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
				case "error":
					w.WriteHeader(429)
					fmt.Fprint(w, testToken)
				case "redirect":
					http.Redirect(w, r, other.URL, 307)
				case "bad-chat":
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":3,"chat":{"id":8,"type":"private"}}}`)
				case "many-updates":
					fmt.Fprint(w, `{"ok":true,"result":[{},{}]}`)
				default:
					fmt.Fprint(w, `{`)
				}
			}))
			defer server.Close()
			c, _ := NewClient(testToken)
			c.endpoint = server.URL + "/"
			var err error
			if mode == "many-updates" {
				_, err = c.Updates(context.Background(), 0)
			} else {
				err = c.Send(context.Background(), 7, "hi")
			}
			if err != ErrAPI || strings.Contains(err.Error(), testToken) || redirected {
				t.Fatal("unsafe response", err, redirected)
			}
		})
	}
	c, _ := NewClient(testToken)
	c.endpoint = "http://127.0.0.1:1/bot" + testToken + "/"
	if err := c.Send(context.Background(), 7, "hi"); err != ErrAPI {
		t.Fatal(err)
	}
	for _, token := range []string{"", "123:too-short", testToken + "/extra", testToken + "\n"} {
		if _, err := NewClient(token); err == nil {
			t.Fatal("accepted invalid token")
		}
	}
}

func TestClientRejectsUnsafeOutboundBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	c, _ := NewClient(testToken)
	c.endpoint = server.URL + "/"
	for _, text := range []string{"", "  ", "bad\xff", strings.Repeat("😀", 2049), strings.Repeat("a", 4097)} {
		if c.Send(context.Background(), 7, text) != ErrAPI {
			t.Fatal("invalid text accepted")
		}
	}
	if c.Send(context.Background(), -7, "hi") != ErrAPI {
		t.Fatal("nonprivate chat accepted")
	}
	if _, err := c.Updates(context.Background(), -1); err != ErrAPI {
		t.Fatal("negative offset accepted")
	}
	if calls != 0 {
		t.Fatal("invalid request reached network")
	}
	c.Close()
}
