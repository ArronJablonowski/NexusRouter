package remote

import (
	"context"
	"testing"
)

func TestModelPolicyRejectsBeforeNetwork(t *testing.T) {
	c := &Client{ModelAllowed: func(host, model string) bool { return false }}
	if err := c.callPinned(context.Background(), "spark", "dispatch", "POST", "/v1/remote/tasks/test", &Task{ModelID: "m"}, nil, nil, ""); err != ErrDenied {
		t.Fatal(err)
	}
}
