package webui

import (
	"strings"
	"testing"
	"time"
)

func TestBrowserSessionContract(t *testing.T) {
	expires := time.Date(2026, 9, 9, 17, 0, 0, 0, time.UTC)
	id := strings.Repeat("a", MaxBrowserChallengeIDBytes)
	challenge := BrowserChallengeResponse{Version: 1, ChallengeID: id, DisplayCode: "12345678", ApprovalCode: id + ".12345678", ExpiresAt: expires}
	if challenge.Validate() != nil || (BrowserChallengeRequest{Version: 1}).Validate() != nil || (BrowserSessionRequest{Version: 1, ChallengeID: id}).Validate() != nil || (BrowserLogoutRequest{Version: 1}).Validate() != nil || (BrowserCSRFRequest{Version: 1}).Validate() != nil {
		t.Fatal("valid browser authentication contract rejected")
	}
	session := BrowserSessionResponse{Version: 1, CSRFToken: strings.Repeat("a", BrowserCSRFTokBytes), ExpiresAt: expires}
	if session.Validate() != nil {
		t.Fatal("valid browser session response rejected")
	}
	for _, invalid := range []BrowserChallengeApprovalRequest{{Version: 2, DisplayCode: "12345678"}, {Version: 1, DisplayCode: "1234"}, {Version: 1, DisplayCode: "abcdefgh"}} {
		if invalid.Validate() == nil {
			t.Fatal("invalid display proof accepted")
		}
	}
}
