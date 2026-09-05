package codexrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"id":999999999999999999999999999999999,"method":"initialize","params":{}}`,
		`{"id":"<\\escaped\u0061>","method":"item/tool/call","params":{"x":1}}`,
		`{"jsonrpc":"2.0","method":"initialized"}`,
		`{"method":"fixture/notice","params":{},"emittedAtMs":1234}`,
		`{"id":-2,"result":null}`,
		`{"id":"x","error":{"code":-32600,"message":"secret message","data":{"secret":true}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			want, err := Decode([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := NewEncoder(&out, 0).Write(want); err != nil {
				t.Fatal(err)
			}
			got, err := NewDecoder(&out, 0).Read()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.ID, want.ID) {
				t.Fatalf("ID changed: %q => %q", want.ID, got.ID)
			}
			wk, _ := want.Kind()
			gk, _ := got.Kind()
			if wk != gk {
				t.Fatal("kind changed")
			}
		})
	}
}

func TestRejectAmbiguousEnvelopes(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"id":1,"result":1} {}`, `{"id":null,"result":1}`,
		`{"id":1.0,"result":1}`, `{"id":1e1,"result":1}`, `{"id":true,"result":1}`,
		`{"id":[],"result":1}`, `{"id":1,"id":2,"result":1}`,
		`{"id":1,"result":1,"error":{"code":1,"message":"x"}}`,
		`{"id":1,"method":"x","result":1}`, `{"method":""}`, `{"method":null}`,
		`{"method":"x","params":null}`, `{"method":"x","params":"secret"}`,
		`{"jsonrpc":"1.0","method":"x"}`, `{"jsonrpc":null,"method":"x"}`,
		`{"method":"x","unknown":"secret"}`, `{"id":1,"error":null}`,
		`{"method":"x","emittedAtMs":null}`, `{"method":"x","emittedAtMs":-1}`,
		`{"method":"x","emittedAtMs":1.5}`, `{"method":"x","emittedAtMs":1,"emittedAtMs":2}`,
		`{"id":1,"result":{},"emittedAtMs":1}`, `{"id":1,"method":"x","emittedAtMs":1}`,
		`{"id":1,"error":{"code":1}}`, `{"id":1,"error":{"message":"secret"}}`,
		`{"id":1,"error":{"code":1,"message":null}}`,
		`{"id":1,"error":{"code":1,"message":"a","message":"secret"}}`,
		`{"id":1,"error":{"code":1.2,"message":"secret"}}`,
		`{"id":1,"error":{"code":1,"message":"a","extra":true}}`,
		"{\"method\":\"\xff\"}",
	} {
		if _, err := Decode([]byte(raw)); err != ErrFrame {
			t.Errorf("accepted %q: %v", raw, err)
		}
	}
}

type fragmented struct{ io.Reader }

func (r fragmented) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestFraming(t *testing.T) {
	raw := `{"method":"ready"}`
	d := NewDecoder(fragmented{strings.NewReader(raw + "\r\n" + raw + "\n")}, len(raw)+1)
	for i := 0; i < 2; i++ {
		if _, err := d.Read(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Read(); err != io.EOF {
		t.Fatal(err)
	}
	if _, err := NewDecoder(strings.NewReader(raw), 0).Read(); err != ErrRead {
		t.Fatal(err)
	}
	if _, err := NewDecoder(strings.NewReader("\n"), 0).Read(); err != ErrFrame {
		t.Fatal(err)
	}
	if _, err := NewDecoder(strings.NewReader(raw+"\n"), len(raw)-1).Read(); err != ErrLimit {
		t.Fatal(err)
	}
	if _, err := NewDecoder(strings.NewReader(raw+"\n"), len(raw)).Read(); err != nil {
		t.Fatal(err)
	}
	large := `{"method":"` + strings.Repeat("x", 10000) + `"}`
	if _, err := NewDecoder(strings.NewReader(large+"\n"), len(large)).Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDecoder(strings.NewReader(strings.Repeat("x", 10000)), 5000).Read(); err != ErrLimit {
		t.Fatal(err)
	}
	if _, err := Decode(make([]byte, DefaultMaxFrame+1)); err != ErrLimit {
		t.Fatal(err)
	}
}

type failedIO struct{}

func (failedIO) Read([]byte) (int, error)  { return 0, errors.New("private transport secret") }
func (failedIO) Write([]byte) (int, error) { return 0, errors.New("private transport secret") }

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestSanitizedIOErrorsAndWriteBounds(t *testing.T) {
	e := Envelope{Method: "ready"}
	if _, err := NewDecoder(failedIO{}, 0).Read(); err != ErrRead {
		t.Fatal(err)
	}
	if err := NewEncoder(failedIO{}, 0).Write(e); err != ErrWrite {
		t.Fatal(err)
	}
	if err := NewEncoder(zeroWriter{}, 0).Write(e); err != ErrWrite {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := NewEncoder(&out, 1).Write(e); err != ErrLimit || out.Len() != 0 {
		t.Fatal(err)
	}
	if err := NewEncoder(&out, 0).Write(Envelope{ID: json.RawMessage("null"), Result: json.RawMessage("null")}); err != ErrFrame || out.Len() != 0 {
		t.Fatal(err)
	}
}

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return w.Buffer.Write(p)
}

func TestConcurrentWrites(t *testing.T) {
	var out shortWriter
	w := NewEncoder(&out, 0)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Write(Envelope{Method: "ready"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	d := NewDecoder(&out, 0)
	for i := 0; i < 100; i++ {
		if _, err := d.Read(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Read(); err != io.EOF {
		t.Fatal(err)
	}
}
