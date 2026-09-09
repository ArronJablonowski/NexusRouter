package browserauth

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func authFixture(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	store, err := New(Options{SessionTTL: time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return store, &now
}

func TestChallengeApprovalConsumptionAndRevocation(t *testing.T) {
	store, _ := authFixture(t)
	challenge, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	_, consumeErr := store.Consume(challenge.ID, challenge.Cookie)
	if store.Approve(challenge.ID, "00000000") == nil || consumeErr == nil {
		t.Fatal("unapproved or incorrect proof accepted")
	}
	if err := store.Approve(challenge.ID, challenge.DisplayCode); err != nil {
		t.Fatal(err)
	}
	session, err := store.Consume(challenge.ID, challenge.Cookie)
	if err != nil || !store.Authenticate(session.Token) || !store.AuthorizeMutation(session.Token, session.CSRFToken) {
		t.Fatal("approved browser session rejected", err)
	}
	if _, err := store.Consume(challenge.ID, challenge.Cookie); err == nil {
		t.Fatal("challenge replay accepted")
	}
	if store.AuthorizeMutation(session.Token, session.CSRFToken+"x") {
		t.Fatal("invalid CSRF accepted")
	}
	rotated, err := store.RotateCSRF(session.Token)
	if err != nil || !store.AuthorizeMutation(session.Token, rotated.CSRFToken) || !store.AuthorizeMutation(session.Token, session.CSRFToken) {
		t.Fatal("bounded CSRF grant rotation broke an active tab", err)
	}
	for range MaxCSRFGrantsPerSession {
		if _, err := store.RotateCSRF(session.Token); err != nil {
			t.Fatal(err)
		}
	}
	if store.AuthorizeMutation(session.Token, session.CSRFToken) {
		t.Fatal("oldest CSRF grant was not evicted")
	}
	store.Revoke(session.Token)
	if store.Authenticate(session.Token) {
		t.Fatal("revoked session accepted")
	}
}

func TestAuthorityGlobalRateWindowsAreBounded(t *testing.T) {
	now := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	store, err := New(Options{SessionTTL: time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for range MaxChallengeCreationsPerMinute {
		if _, err := store.Create(); err != nil {
			t.Fatal("challenge rate exhausted early", err)
		}
	}
	if _, err := store.Create(); !errors.Is(err, ErrExhausted) {
		t.Fatal("challenge rate limit not enforced", err)
	}
	now = now.Add(time.Minute)
	if _, err := store.Create(); err != nil {
		t.Fatal("challenge rate window did not reset", err)
	}
	challenge, _ := store.Create()
	for range MaxApprovalsPerMinute {
		_ = store.Approve(challenge.ID, "00000000")
	}
	if err := store.Approve(challenge.ID, challenge.DisplayCode); !errors.Is(err, ErrExhausted) {
		t.Fatal("approval rate limit not enforced", err)
	}
}

func TestAuthorityExpiresAndRaceConsumesOnce(t *testing.T) {
	store, now := authFixture(t)
	challenge, _ := store.Create()
	*now = now.Add(6 * time.Minute)
	if store.Approve(challenge.ID, challenge.DisplayCode) == nil {
		t.Fatal("expired challenge accepted")
	}
	*now = now.Add(-6 * time.Minute)
	challenge, _ = store.Create()
	if err := store.Approve(challenge.ID, challenge.DisplayCode); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Consume(challenge.ID, challenge.Cookie)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("challenge consumed unexpected number of times", success)
	}
}

func TestInvalidConfigurationAndProofAttemptLimit(t *testing.T) {
	if _, err := New(Options{SessionTTL: time.Minute}); err == nil {
		t.Fatal("short session accepted")
	}
	store, _ := authFixture(t)
	challenge, _ := store.Create()
	for range MaxApprovalAttempts {
		_ = store.Approve(challenge.ID, "00000000")
	}
	if store.Approve(challenge.ID, challenge.DisplayCode) == nil {
		t.Fatal("exhausted proof accepted")
	}
}
