package remotecli

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"strings"
	"testing"
)

func TestAutomaticCLIRejectsAmbiguousAndExplorationInputBeforeNetwork(t *testing.T) {
	for _, operation := range []string{"candidates", "rank", "auto-dispatch"} {
		for _, body := range []string{`{`, `{} {}`, `{"untrusted":true}`, `{"AllowExploration":true}`, `{"Routing":{"AllowExploration":true}}`} {
			t.Run(operation+body, func(t *testing.T) {
				_, e := automaticOperation(context.Background(), &remote.Client{}, operation, "", "", "", strings.NewReader(body))
				if !errors.Is(e, remote.ErrInvalid) {
					t.Fatal(e)
				}
			})
		}
	}
}
