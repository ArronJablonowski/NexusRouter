package webui

import (
	"os/exec"
	"strings"
	"testing"
)

func TestWorkboardClientProjectsProvisionalPositionsWithoutMutatingAuthority(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script := `
const client = require(process.argv[1]);
const cards = Object.freeze([
  Object.freeze({id:"a",state:"backlog",rank:"a",revision:2,title:"A"}),
  Object.freeze({id:"b",state:"backlog",rank:"b",revision:3,title:"B"}),
  Object.freeze({id:"c",state:"ready",rank:"a",revision:4,title:"C"}),
  Object.freeze({id:"d",state:"in_progress",rank:"a",revision:5,title:"D"})
]);
const reorder = {action:"card.reorder",cardID:"a",cardRevision:2,sourceState:"backlog",anchorID:"b",anchorRank:"b",beforeCardID:"",afterCardID:"b"};
const reordered = client.provisionalPosition(cards,reorder);
if (!reordered || reordered.map(card=>card.id).join("") !== "bacd" || !reordered[1].provisional || reordered[1].revision !== 2) process.exit(1);
const move = {action:"card.move",cardID:"a",cardRevision:2,sourceState:"backlog",targetState:"ready",anchorID:""};
const moved = client.provisionalPosition(cards,move);
if (!moved || moved.map(card=>card.id).join("") !== "bcad" || moved[2].state !== "ready" || !moved[2].provisional) process.exit(2);
if (cards.map(card=>card.id).join("") !== "abcd" || cards[0].state !== "backlog" || cards[0].provisional !== undefined) process.exit(3);
if (client.provisionalPosition(cards,{...reorder,anchorRank:"stale"}) || client.provisionalPosition(cards,{...move,cardRevision:9}) || client.provisionalPosition(cards,{...move,targetState:"done"})) process.exit(4);`
	command := exec.Command(node, "-e", script, "./assets/v1/workboard-client.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("provisional position projection failed: %v\n%s", err, output)
	}
}

func TestEmbeddedWorkboardOptimismIsExplicitAndAuthoritativelyReconciled(t *testing.T) {
	mutations, err := embeddedShellAssets.ReadFile("assets/v1/workboard-mutations.js")
	if err != nil {
		t.Fatal(err)
	}
	boards, err := embeddedShellAssets.ReadFile("assets/v1/workboards.js")
	if err != nil {
		t.Fatal(err)
	}
	mutationSource, boardSource := string(mutations), string(boards)
	for _, required := range []string{
		`window.NexusWorkboards.previewPosition(intent.capture)`,
		`Pending position preview — not saved.`,
		`if (positioned) window.NexusWorkboards.refresh()`,
		`The provisional position was removed`,
	} {
		if !strings.Contains(mutationSource, required) {
			t.Fatalf("optimistic mutation integration missing %q", required)
		}
	}
	for _, required := range []string{
		`pendingPosition ? client.provisionalPosition(loadedCards, pendingPosition) : loadedCards`,
		`Pending position — not saved`,
		`provisional: Boolean(card.provisional)`,
		`pendingPosition = null`,
	} {
		if !strings.Contains(boardSource, required) {
			t.Fatalf("provisional board model missing %q", required)
		}
	}
	if !strings.Contains(string(mustAsset(t, "assets/v1/index.html")), `id="workboard-mutation-status" role="status" aria-live="polite"`) {
		t.Fatal("provisional position announcements lack a polite live status")
	}
}

func mustAsset(t *testing.T, name string) []byte {
	t.Helper()
	value, err := embeddedShellAssets.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
