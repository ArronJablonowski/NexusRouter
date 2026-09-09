// Package browserauth owns process-local browser bootstrap and session
// authority. Only hashes of bearer material are retained, and daemon restart
// intentionally revokes every browser challenge and session.
package browserauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	MaxChallenges                  = 128
	MaxSessions                    = 128
	MaxApprovalAttempts            = 5
	MaxChallengeCreationsPerMinute = 16
	MaxApprovalsPerMinute          = 32
	MaxCSRFGrantsPerSession        = 8
)

var (
	ErrInvalid   = errors.New("invalid browser authority")
	ErrExpired   = errors.New("expired browser authority")
	ErrExhausted = errors.New("browser authority capacity exhausted")
)

type Challenge struct {
	ID          string
	DisplayCode string
	Cookie      string
	ExpiresAt   time.Time
}

type Session struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

type Options struct {
	SessionTTL   time.Duration
	ChallengeTTL time.Duration
	Now          func() time.Time
	Random       io.Reader
}

type challengeRecord struct {
	cookieHash [32]byte
	codeHash   [32]byte
	expiresAt  time.Time
	approved   bool
	attempts   int
}

type sessionRecord struct {
	csrfHashes [][32]byte
	expiresAt  time.Time
}

type Store struct {
	mu            sync.Mutex
	sessionTTL    time.Duration
	challengeTTL  time.Duration
	now           func() time.Time
	random        io.Reader
	challenges    map[string]challengeRecord
	sessions      map[[32]byte]sessionRecord
	challengeRate rateWindow
	approvalRate  rateWindow
}

type rateWindow struct {
	started time.Time
	count   int
}

func New(options Options) (*Store, error) {
	if options.SessionTTL < 5*time.Minute || options.SessionTTL > 24*time.Hour {
		return nil, ErrInvalid
	}
	if options.ChallengeTTL == 0 {
		options.ChallengeTTL = 5 * time.Minute
	}
	if options.ChallengeTTL < time.Minute || options.ChallengeTTL > 15*time.Minute {
		return nil, ErrInvalid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Store{sessionTTL: options.SessionTTL, challengeTTL: options.ChallengeTTL, now: options.Now, random: options.Random, challenges: map[string]challengeRecord{}, sessions: map[[32]byte]sessionRecord{}}, nil
}

func (s *Store) Create() (Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	if !permit(&s.challengeRate, now, MaxChallengeCreationsPerMinute) {
		return Challenge{}, ErrExhausted
	}
	if len(s.challenges) >= MaxChallenges {
		return Challenge{}, ErrExhausted
	}
	id, err := s.randomTokenLocked(18)
	if err != nil {
		return Challenge{}, ErrInvalid
	}
	cookie, err := s.randomTokenLocked(32)
	if err != nil {
		return Challenge{}, ErrInvalid
	}
	codeBytes := make([]byte, 6)
	if _, err := io.ReadFull(s.random, codeBytes); err != nil {
		return Challenge{}, ErrInvalid
	}
	code := make([]byte, 8)
	for index := range code {
		code[index] = '0' + codeBytes[index%len(codeBytes)]%10
	}
	record := challengeRecord{cookieHash: sha256.Sum256([]byte(cookie)), codeHash: sha256.Sum256(code), expiresAt: now.Add(s.challengeTTL)}
	if _, exists := s.challenges[id]; exists {
		return Challenge{}, ErrInvalid
	}
	s.challenges[id] = record
	return Challenge{ID: id, DisplayCode: string(code), Cookie: cookie, ExpiresAt: record.expiresAt}, nil
}

func (s *Store) Approve(id, code string) error {
	if !validToken(id, 24) || len(code) != 8 {
		return ErrInvalid
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if !permit(&s.approvalRate, now, MaxApprovalsPerMinute) {
		return ErrExhausted
	}
	record, exists := s.challenges[id]
	if !exists {
		return ErrInvalid
	}
	record.attempts++
	candidate := sha256.Sum256([]byte(code))
	if subtle.ConstantTimeCompare(candidate[:], record.codeHash[:]) != 1 {
		if record.attempts >= MaxApprovalAttempts {
			delete(s.challenges, id)
		} else {
			s.challenges[id] = record
		}
		return ErrInvalid
	}
	record.approved = true
	s.challenges[id] = record
	return nil
}

func (s *Store) Consume(id, cookie string) (Session, error) {
	if !validToken(id, 24) || !validToken(cookie, 43) {
		return Session{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	record, exists := s.challenges[id]
	candidate := sha256.Sum256([]byte(cookie))
	if !exists || !record.approved || subtle.ConstantTimeCompare(candidate[:], record.cookieHash[:]) != 1 {
		return Session{}, ErrInvalid
	}
	delete(s.challenges, id)
	if len(s.sessions) >= MaxSessions {
		return Session{}, ErrExhausted
	}
	token, err := s.randomTokenLocked(32)
	if err != nil {
		return Session{}, ErrInvalid
	}
	csrf, err := s.randomTokenLocked(32)
	if err != nil {
		return Session{}, ErrInvalid
	}
	expires := now.Add(s.sessionTTL)
	s.sessions[sha256.Sum256([]byte(token))] = sessionRecord{csrfHashes: [][32]byte{sha256.Sum256([]byte(csrf))}, expiresAt: expires}
	return Session{Token: token, CSRFToken: csrf, ExpiresAt: expires}, nil
}

func (s *Store) Authenticate(token string) bool {
	if !validToken(token, 43) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	_, exists := s.sessions[sha256.Sum256([]byte(token))]
	return exists
}

// Subject returns a stable, non-secret identity for the lifetime of an
// authenticated browser session. Durable browser records bind to this digest;
// the cookie bearer value itself is never exposed or persisted.
func (s *Store) Subject(token string) (string, bool) {
	if !validToken(token, 43) {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	key := sha256.Sum256([]byte(token))
	if _, exists := s.sessions[key]; !exists {
		return "", false
	}
	return hex.EncodeToString(key[:]), true
}

func (s *Store) AuthorizeMutation(token, csrf string) bool {
	if !validToken(token, 43) || !validToken(csrf, 43) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	record, exists := s.sessions[sha256.Sum256([]byte(token))]
	candidate := sha256.Sum256([]byte(csrf))
	matched := 0
	for _, granted := range record.csrfHashes {
		matched |= subtle.ConstantTimeCompare(candidate[:], granted[:])
	}
	return exists && matched == 1
}

func (s *Store) RotateCSRF(token string) (Session, error) {
	if !validToken(token, 43) {
		return Session{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	key := sha256.Sum256([]byte(token))
	record, exists := s.sessions[key]
	if !exists {
		return Session{}, ErrInvalid
	}
	csrf := ""
	for range 8 {
		candidate, err := s.randomTokenLocked(32)
		if err != nil {
			return Session{}, ErrInvalid
		}
		candidateHash := sha256.Sum256([]byte(candidate))
		duplicate := 0
		for _, granted := range record.csrfHashes {
			duplicate |= subtle.ConstantTimeCompare(candidateHash[:], granted[:])
		}
		if duplicate == 0 {
			csrf = candidate
			record.csrfHashes = append(record.csrfHashes, candidateHash)
			if len(record.csrfHashes) > MaxCSRFGrantsPerSession {
				record.csrfHashes = append([][32]byte(nil), record.csrfHashes[len(record.csrfHashes)-MaxCSRFGrantsPerSession:]...)
			}
			break
		}
	}
	if csrf == "" {
		return Session{}, ErrInvalid
	}
	s.sessions[key] = record
	return Session{CSRFToken: csrf, ExpiresAt: record.expiresAt}, nil
}

func (s *Store) Revoke(token string) {
	if !validToken(token, 43) {
		return
	}
	s.mu.Lock()
	delete(s.sessions, sha256.Sum256([]byte(token)))
	s.mu.Unlock()
}

func (s *Store) randomTokenLocked(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := io.ReadFull(s.random, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func (s *Store) pruneLocked(now time.Time) {
	for id, record := range s.challenges {
		if !now.Before(record.expiresAt) {
			delete(s.challenges, id)
		}
	}
	for token, record := range s.sessions {
		if !now.Before(record.expiresAt) {
			delete(s.sessions, token)
		}
	}
}

func validToken(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}

func permit(window *rateWindow, now time.Time, limit int) bool {
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute || now.Before(window.started) {
		window.started, window.count = now, 0
	}
	if window.count >= limit {
		return false
	}
	window.count++
	return true
}
