package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/daemon"
)

func TestDaemonControlAPI(t *testing.T) {
	var stops atomic.Int32
	c, _ := daemon.New(daemon.NewID(), func() { stops.Add(1) })
	status, _ := c.Current(context.Background())
	s := services()
	s.DaemonStatus = c.Current
	s.StopDaemon = c.Stop
	h, _ := New(token, 1, s)
	get := request("GET", "/v1/daemon/status", "")
	get.Body = http.NoBody
	w := httptest.NewRecorder()
	h.ServeHTTP(w, get)
	var got daemon.Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got != status {
		t.Fatal(w.Code, w.Body.String())
	}
	for range 2 {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/daemon/stop", `{"instance_id":"`+status.InstanceID+`"}`))
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.State != "stopping" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if stops.Load() != 1 {
		t.Fatal(stops.Load())
	}
}

func TestDaemonControlRejectsWithoutMutation(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "query", "forcequery", "method", "media", "duplicate", "alias", "unknown", "malformed", "null", "oversize", "transfer", "length", "wrong_instance", "capacity", "canceled", "status_body"} {
		t.Run(mode, func(t *testing.T) {
			var stops atomic.Int32
			c, _ := daemon.New(daemon.NewID(), func() { stops.Add(1) })
			initial, _ := c.Current(context.Background())
			s := services()
			s.DaemonStatus = c.Current
			s.StopDaemon = c.Stop
			h, _ := New(token, 1, s)
			body := `{"instance_id":"` + initial.InstanceID + `"}`
			r := request("POST", "/v1/daemon/stop", body)
			want := 400
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "x=y"
			case "forcequery":
				r.URL.ForceQuery = true
			case "method":
				r.Method = "GET"
				want = 405
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			case "duplicate":
				r = request("POST", r.URL.Path, `{"instance_id":"`+initial.InstanceID+`","instance_id":"`+initial.InstanceID+`"}`)
			case "alias":
				r = request("POST", r.URL.Path, strings.Replace(body, "instance_id", "Instance_ID", 1))
			case "unknown":
				r = request("POST", r.URL.Path, `{"instance_id":"`+initial.InstanceID+`","other":true}`)
			case "malformed":
				r = request("POST", r.URL.Path, `{`)
			case "null":
				r = request("POST", r.URL.Path, `{"instance_id":null}`)
			case "oversize":
				r = request("POST", r.URL.Path, strings.Repeat("x", 1025))
				want = 413
			case "transfer":
				r.TransferEncoding = []string{"chunked"}
			case "length":
				r.ContentLength = -1
			case "wrong_instance":
				r = request("POST", r.URL.Path, `{"instance_id":"`+strings.Repeat("b", 64)+`"}`)
				want = 409
			case "capacity":
				h.controls <- struct{}{}
				h.controls <- struct{}{}
				want = 503
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				want = 503
			case "status_body":
				r = request("GET", "/v1/daemon/status", body)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			current, _ := c.Current(context.Background())
			if w.Code != want || stops.Load() != 0 || current.State != "ready" {
				t.Fatal(w.Code, w.Body.String(), stops.Load())
			}
		})
	}
}

func TestDaemonControlBackendSanitization(t *testing.T) {
	for _, mode := range []string{"panic", "invalid", "wrongid"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			id := daemon.NewID()
			s.StopDaemon = func(context.Context, string) (daemon.Status, error) {
				if mode == "panic" {
					panic("PRIVATE")
				}
				c, _ := daemon.New(id, func() {})
				out, _ := c.Stop(context.Background(), id)
				if mode == "invalid" {
					out.State = "PRIVATE"
				} else {
					out.InstanceID = daemon.NewID()
				}
				return out, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/daemon/stop", `{"instance_id":"`+id+`"}`))
			if w.Code < 500 || strings.Contains(w.Body.String(), "PRIVATE") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
