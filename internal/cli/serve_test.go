package cli

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeShutdownCancelsInFlightRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	canceled := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		w.WriteHeader(503)
	})
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, listener, handler, io.Discard) }()
	client := &http.Client{Timeout: 2 * time.Second}
	response := make(chan error, 1)
	go func() {
		r, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			r.Body.Close()
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not accept request")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("request was not canceled")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
	<-response
}

func TestServeOutputFailureClosesListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := serveHTTP(context.Background(), listener, http.NotFoundHandler(), brokenWriter{}); err == nil {
		t.Fatal("output failure ignored")
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("listener remained open")
	}
}
