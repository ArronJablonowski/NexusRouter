package webui

import "time"

const (
	MaxBrowserChallengeIDBytes = 24
	BrowserDisplayCodeBytes    = 8
	BrowserCSRFTokBytes        = 43
)

type BrowserChallengeRequest struct {
	Version int `json:"version"`
}

type BrowserChallengeResponse struct {
	Version      int       `json:"version"`
	ChallengeID  string    `json:"challenge_id"`
	DisplayCode  string    `json:"display_code"`
	ApprovalCode string    `json:"approval_code"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type BrowserSessionRequest struct {
	Version     int    `json:"version"`
	ChallengeID string `json:"challenge_id"`
}

type BrowserSessionResponse struct {
	Version   int       `json:"version"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type BrowserLogoutRequest struct {
	Version int `json:"version"`
}

type BrowserCSRFRequest struct {
	Version int `json:"version"`
}

type BrowserChallengeApprovalRequest struct {
	Version     int    `json:"version"`
	DisplayCode string `json:"display_code"`
}

func (r BrowserChallengeRequest) Validate() error {
	if r.Version != ContractVersion {
		return ErrContract
	}
	return nil
}

func (r BrowserSessionRequest) Validate() error {
	if r.Version != ContractVersion || len(r.ChallengeID) != MaxBrowserChallengeIDBytes || !validBrowserToken(r.ChallengeID) {
		return ErrContract
	}
	return nil
}

func (r BrowserLogoutRequest) Validate() error {
	if r.Version != ContractVersion {
		return ErrContract
	}
	return nil
}

func (r BrowserCSRFRequest) Validate() error {
	if r.Version != ContractVersion {
		return ErrContract
	}
	return nil
}

func (r BrowserChallengeApprovalRequest) Validate() error {
	if r.Version != ContractVersion || len(r.DisplayCode) != BrowserDisplayCodeBytes {
		return ErrContract
	}
	for _, character := range r.DisplayCode {
		if character < '0' || character > '9' {
			return ErrContract
		}
	}
	return nil
}

func (r BrowserChallengeResponse) Validate() error {
	if (BrowserSessionRequest{Version: r.Version, ChallengeID: r.ChallengeID}).Validate() != nil ||
		(BrowserChallengeApprovalRequest{Version: r.Version, DisplayCode: r.DisplayCode}).Validate() != nil ||
		r.ApprovalCode != r.ChallengeID+"."+r.DisplayCode || !validBrowserTime(r.ExpiresAt) {
		return ErrContract
	}
	return nil
}

func (r BrowserSessionResponse) Validate() error {
	if r.Version != ContractVersion || len(r.CSRFToken) != BrowserCSRFTokBytes || !validBrowserToken(r.CSRFToken) || !validBrowserTime(r.ExpiresAt) {
		return ErrContract
	}
	return nil
}

func validBrowserToken(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if !isASCIILetterOrDigit(value[index]) && value[index] != '_' && value[index] != '-' {
			return false
		}
	}
	return true
}

func isASCIILetterOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func validBrowserTime(value time.Time) bool {
	_, offset := value.Zone()
	return value.Year() >= 1970 && value.Year() < 2261 && offset == 0
}
