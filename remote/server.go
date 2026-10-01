package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type Server struct {
	instance string
	trust    TrustFile
	journal  *Journal
	backend  Backend
	slots    chan struct{}
	limits   requestLimiter
}

func NewServer(instance string, trust TrustFile, journal *Journal, backend Backend) (*Server, error) {
	if !id(instance) || journal == nil || journal.instance != instance || backend == nil || (reflect.ValueOf(backend).Kind() == reflect.Pointer && reflect.ValueOf(backend).IsNil()) {
		return nil, ErrInvalid
	}
	if _, err := trust.Read(); err != nil {
		return nil, err
	}
	return &Server{instance: instance, trust: trust, journal: journal, backend: backend, slots: make(chan struct{}, 32)}, nil
}

// HTTPServer uses TLS 1.3 mutual authentication and bounded HTTP/1.1 requests.
// ServeTLS with empty certificate paths uses the loaded certificate. Certificate
// rotation on the listener requires a controlled restart; peer revocation does not.
func (s *Server) HTTPServer(address string, credentials Credentials) (*http.Server, error) {
	tlsConfig, err := credentials.serverTLS(s.trust)
	if err != nil {
		return nil, err
	}
	return &http.Server{Addr: address, Handler: s, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Nexus-Instance", s.instance)
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		s.fail(w, 503)
		return
	}
	// A listener or proxy cannot substitute identity headers for verified mTLS.
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		s.fail(w, 403)
		return
	}
	// Recheck validity on every request, including a reused TLS connection.
	validChain := false
	now := time.Now()
	for _, chain := range r.TLS.VerifiedChains {
		valid := true
		for _, cert := range chain {
			if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
				valid = false
			}
		}
		validChain = validChain || valid
	}
	if !validChain {
		s.fail(w, 403)
		return
	}
	registry, err := s.trust.Read()
	if err != nil {
		s.fail(w, 503)
		return
	}
	peer, err := registry.authenticate(r.TLS.PeerCertificates[0])
	if err != nil {
		s.fail(w, 403)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip, e := netip.ParseAddr(host)
	private := err == nil && e == nil && (ip.Unmap().IsPrivate() || ip.IsLoopback())
	if !peer.AllowPublicNetwork && !private {
		s.fail(w, 403)
		return
	}
	if r.Header.Get("X-Nexus-Instance") != s.instance || r.URL.RawPath != "" || r.URL.RawQuery != "" {
		s.fail(w, 400)
		return
	}
	op, key := "", ""
	if r.URL.Path == "/v1/remote/info" && r.Method == "GET" {
		op = "info"
	} else if r.URL.Path == "/v1/remote/tasks" && r.Method == "GET" {
		op = "inspect"
	} else {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/remote/tasks/"), "/")
		if strings.HasPrefix(r.URL.Path, "/v1/remote/tasks/") && len(parts) >= 1 && requestID(parts[0]) {
			key = parts[0]
			if len(parts) == 1 {
				if r.Method == "POST" {
					op = "dispatch"
				} else if r.Method == "GET" {
					op = "inspect"
				}
			}
			if len(parts) == 2 && parts[1] == "cancel" && r.Method == "POST" {
				op = "cancel"
			}
			if len(parts) == 2 && parts[1] == "events" && r.Method == "GET" {
				op = "inspect"
			}
		}
	}
	if op == "" {
		s.fail(w, 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if allowed, first, retry := s.limits.allow(registry, peer, op, time.Now()); !allowed {
		if first && s.journal.audit(ctx, peer.ID, op, key, "rate_limited") != nil {
			s.fail(w, 503)
			return
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		s.fail(w, http.StatusTooManyRequests)
		return
	}
	if !peer.permits(op) {
		if s.journal.audit(ctx, peer.ID, op, key, "denied") != nil {
			s.fail(w, 503)
		} else {
			s.fail(w, 403)
		}
		return
	}
	if s.journal.audit(ctx, peer.ID, op, key, "admitted") != nil {
		s.fail(w, 503)
		return
	}
	var result any
	if r.URL.Path == "/v1/remote/tasks" {
		result, err = s.taskPage(ctx, peer.ID, r.Header.Get("X-Nexus-After-Request"))
	} else if op == "info" {
		var info Info
		if scoped, ok := s.backend.(interface {
			InfoForHarnesses(context.Context, []string, bool, []string) (Info, error)
		}); ok {
			info, err = scoped.InfoForHarnesses(ctx, slices.Clone(peer.Models), peer.AllowCloudInference, slices.Clone(peer.Harnesses))
		} else if scoped, ok := s.backend.(interface {
			InfoFor(context.Context, []string, bool) (Info, error)
		}); ok {
			info, err = scoped.InfoFor(ctx, slices.Clone(peer.Models), peer.AllowCloudInference)
		} else {
			info, err = s.backend.Info(ctx)
		}
		if err == nil {
			info.Version = Version
			info.Instance = s.instance
			filtered := []Model{}
			for _, m := range info.Models {
				if slices.Contains(peer.Models, m.ID) && (peer.AllowCloudInference || m.Local) {
					filtered = append(filtered, m)
				}
			}
			info.Models = filtered
			info.Harnesses = filterHarnesses(info.Harnesses, filtered, peer.Harnesses, false)
			result = info
		}
	} else if op == "dispatch" {
		var task Task
		if r.Header.Get("Content-Type") != "application/json" {
			s.finish(w, ctx, peer.ID, op, key, nil, ErrInvalid)
			return
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody))
		d.DisallowUnknownFields()
		err = d.Decode(&task)
		var extra any
		if err != nil || d.Decode(&extra) != io.EOF || task.Validate() != nil {
			s.finish(w, ctx, peer.ID, op, key, nil, ErrInvalid)
			return
		}
		if !peer.permitsTask(task) || task.Private && !private {
			s.finish(w, ctx, peer.ID, op, key, nil, ErrDenied)
			return
		}
		var submission string
		submission, err = s.journal.reserve(ctx, peer.ID, key, hash(task))
		if err == nil {
			var status submissions.Status
			if submission != "" {
				status, err = s.backend.Status(ctx, submission)
			} else {
				// Stable destination/caller/request key survives loss of either response or
				// ownership binding. Submit's durable idempotency remains authoritative.
				status, err = s.backend.Submit(ctx, hash([]string{s.instance, peer.ID, key}), task)
				if err == nil && status.ID != "" {
					err = s.journal.bind(ctx, peer.ID, key, status.ID)
				} else if err == nil {
					err = ErrUnavailable
				}
			}
			result = status
		}
	} else {
		var submission string
		submission, err = s.journal.lookup(ctx, peer.ID, key)
		if err == nil {
			if strings.HasSuffix(r.URL.Path, "/events") {
				var status submissions.Status
				status, err = s.backend.Status(ctx, submission)
				taskID := r.Header.Get("X-Nexus-Task")
				after, e := strconv.ParseInt(r.Header.Get("X-Nexus-After"), 10, 64)
				if err == nil && (e != nil || after < 0 || !slices.Contains(status.TaskIDs, taskID)) {
					err = ErrDenied
				}
				if err == nil {
					result, err = s.backend.Events(ctx, taskID, after, 100)
				}
			} else if op == "cancel" {
				result, err = s.backend.Cancel(ctx, submission)
			} else {
				result, err = s.backend.Status(ctx, submission)
			}
		}
	}
	s.finish(w, ctx, peer.ID, op, key, result, err)
}
func (s *Server) finish(w http.ResponseWriter, ctx context.Context, caller, op, key string, result any, err error) {
	code, outcome := 200, "succeeded"
	if err != nil {
		code = 503
		outcome = "uncertain"
		switch {
		case errors.Is(err, ErrInvalid):
			code = 400
			outcome = "invalid"
		case errors.Is(err, ErrDenied):
			code = 403
			outcome = "denied"
		case errors.Is(err, ErrConflict), errors.Is(err, submissions.ErrConflict):
			code = 409
			outcome = "conflict"
		}
	}
	// Audit completion even when the caller disconnects. If persistence fails,
	// suppress results; a mutation may already be durable, so delivery is uncertain.
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if s.journal.audit(auditCtx, caller, op, key, outcome) != nil {
		s.fail(w, 503)
		return
	}
	if err != nil {
		s.fail(w, code)
		return
	}
	body, e := json.Marshal(result)
	if e != nil || len(body) > 8<<20 {
		s.fail(w, 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
func (s *Server) fail(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `{"error":"remote_request_failed"}`)
}
