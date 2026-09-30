package app

import (
	"context"
	"strings"
	"testing"
	"time"

	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardBridgeProjectsDependencyPage(t *testing.T) {
	repository := &bridgeBoardRepository{}
	repository.dependencyPage = workboard.DependencyPage{Version: 1, BoardID: "board-a", CardID: "card-a",
		Direction: workboard.DependencyDependents, GraphRevision: 3, GraphDigest: strings.Repeat("a", 64),
		Items: []workboard.DependencyLink{{Version: 1, BoardID: "board-a", CardID: "card-b", DependencyID: "card-a"}}}
	bridge, err := NewWorkboardBridge(repository, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	options := contract.DependencyOptions{Limit: 1, Direction: contract.DependencyDependents}
	page, err := bridge.BrowserDependencies(context.Background(), strings.Repeat("c", 64), "board-a", "card-a", options)
	if err != nil || page.Validate() != nil || page.Direction != contract.DependencyDependents || len(page.Items) != 1 || page.Items[0].CardID != "card-b" || repository.dependencyOptions.Direction != workboard.DependencyDependents {
		t.Fatalf("page=%+v options=%+v err=%v", page, repository.dependencyOptions, err)
	}
}
