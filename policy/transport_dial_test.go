package policy

import (
	"errors"
	"testing"
)

func TestTransportDialAddressPinsLoopbackEveryMode(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		for _, tc := range []struct{ host, want string }{
			{"localhost", "127.0.0.1:11434"},
			{"LOCALHOST", "127.0.0.1:11434"},
			{"127.0.0.2", "127.0.0.2:11434"},
			{"::1", "[::1]:11434"},
			{"::ffff:127.0.0.1", "127.0.0.1:11434"},
		} {
			got, err := transportDialAddress(localOnly, tc.host, "11434")
			if err != nil || got != tc.want {
				t.Errorf("localOnly=%v host=%s: %s %v", localOnly, tc.host, got, err)
			}
		}
	}
}

func TestTransportDialAddressRemotePolicy(t *testing.T) {
	for _, host := range []string{"provider.example", "localhost.example", "localhost.", "192.168.1.2", "169.254.169.254"} {
		if got, err := transportDialAddress(true, host, "443"); got != "" || !errors.Is(err, ErrEgress) {
			t.Errorf("local-only accepted %s: %s %v", host, got, err)
		}
		if got, err := transportDialAddress(false, host, "443"); got != host+":443" || err != nil {
			t.Errorf("remote HTTPS authority changed %s: %s %v", host, got, err)
		}
	}
}
