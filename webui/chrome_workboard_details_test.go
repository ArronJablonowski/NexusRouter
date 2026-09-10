package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestChromeWorkboardFiltersAndDetails qualifies server-backed filters and the
// bounded, progressively disclosed relationship and attempt views in Chrome.
// Every fixture is same-origin, authenticated, and rejects unexpected query
// shapes so this is also evidence for the browser facade contract.
func TestChromeWorkboardFiltersAndDetails(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	fixture := newChromeDetailFixture()
	var fullSnapshots, stateSnapshots, compoundSnapshots atomic.Int32
	var dependencyReads, historyReads, detailReads, reviewReads atomic.Int32

	shell, err := NewShellHandler(ShellOptions{
		BasePath:    "/app",
		HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") },
		Authenticated: func(request *http.Request) bool {
			cookie, err := request.Cookie("darwin_session")
			return err == nil && cookie.Value == "valid"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/app/api/") {
			shell.ServeHTTP(writer, request)
			return
		}
		if cookie, err := request.Cookie("darwin_session"); err != nil || cookie.Value != "valid" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/app/api/v1/session/csrf":
			writeChromeJSON(writer, BrowserSessionResponse{Version: 1, CSRFToken: strings.Repeat("c", BrowserCSRFTokBytes), ExpiresAt: time.Now().Add(time.Hour)})
		case "/app/api/v1/operations":
			if request.Method != http.MethodGet || request.URL.RawQuery != "limit=25" {
				http.Error(writer, "invalid operation query", http.StatusBadRequest)
				return
			}
			_, _ = writer.Write([]byte(`{"version":1,"items":[],"next_cursor":"","has_more":false}`))
		case "/app/api/v1/workboards":
			if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "25" || request.URL.Query().Get("state") != "active" || len(request.URL.Query()) != 2 {
				http.Error(writer, "invalid board query", http.StatusBadRequest)
				return
			}
			writeChromeJSON(writer, Page{Version: 1, Items: []Board{fixture.board}})
		case "/app/api/v1/workboards/board-a":
			cards, lifecycle, kind, ok := fixture.filtered(request)
			if !ok {
				http.Error(writer, "invalid snapshot query", http.StatusBadRequest)
				return
			}
			switch kind {
			case "full":
				fullSnapshots.Add(1)
			case "state":
				stateSnapshots.Add(1)
			case "compound":
				compoundSnapshots.Add(1)
			}
			writeChromeJSON(writer, BoardSnapshot{Version: 1, Board: fixture.board, Columns: chromeWorkboardColumns(fixture.board.ID), Cards: cards, Lifecycle: lifecycle, GraphRevision: fixture.graphRevision, GraphDigest: fixture.graphDigest})
		case "/app/api/v1/workboards/board-a/events":
			writer.WriteHeader(http.StatusNoContent)
		case "/app/api/v1/workboards/board-a/cards/card-blocked/dependencies":
			direction := request.URL.Query().Get("direction")
			if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "100" || len(request.URL.Query()) != 2 || (direction != "prerequisites" && direction != "dependents") {
				http.Error(writer, "invalid dependency query", http.StatusBadRequest)
				return
			}
			dependencyReads.Add(1)
			items := []DependencyLink{{Version: 1, BoardID: fixture.board.ID, CardID: "card-blocked", DependencyID: "card-ready"}}
			if direction == "dependents" {
				items = []DependencyLink{{Version: 1, BoardID: fixture.board.ID, CardID: "card-dependent", DependencyID: "card-blocked"}}
			}
			writeChromeJSON(writer, DependencyPage{Version: 1, BoardID: fixture.board.ID, CardID: "card-blocked", Direction: DependencyDirection(direction), GraphRevision: fixture.graphRevision, GraphDigest: fixture.graphDigest, Items: items})
		case "/app/api/v1/workboards/board-a/cards/card-blocked/attempts":
			if request.Method != http.MethodGet || request.URL.RawQuery != "limit=25" {
				http.Error(writer, "invalid attempt-history query", http.StatusBadRequest)
				return
			}
			historyReads.Add(1)
			writeChromeJSON(writer, fixture.blockedHistory)
		case "/app/api/v1/workboards/board-a/cards/card-blocked/attempts/attempt-blocked":
			if request.Method != http.MethodGet || request.URL.RawQuery != "limit=25" {
				http.Error(writer, "invalid attempt-detail query", http.StatusBadRequest)
				return
			}
			detailReads.Add(1)
			writeChromeJSON(writer, fixture.blockedDetail)
		case "/app/api/v1/workboards/board-a/cards/card-review/attempts/attempt-review":
			if request.Method != http.MethodGet || request.URL.RawQuery != "limit=25" {
				http.Error(writer, "invalid review-detail query", http.StatusBadRequest)
				return
			}
			reviewReads.Add(1)
			writeChromeJSON(writer, fixture.reviewDetail)
		default:
			http.Error(writer, "unavailable", http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stopChrome := startChromeForTest(t, ctx, chrome)
	defer stopChrome()
	output, err := exec.CommandContext(ctx, node, "-e", chromeWorkboardDetailsCDP, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("real Chrome workboard filter/detail qualification failed: %v\n%s", err, output)
	}
	if fullSnapshots.Load() < 2 || stateSnapshots.Load() < 1 || compoundSnapshots.Load() < 1 {
		t.Fatalf("filter snapshots not observed: full=%d state=%d compound=%d", fullSnapshots.Load(), stateSnapshots.Load(), compoundSnapshots.Load())
	}
	if dependencyReads.Load() != 2 || historyReads.Load() != 1 || detailReads.Load() != 1 || reviewReads.Load() != 1 {
		t.Fatalf("bounded details not observed: dependencies=%d history=%d detail=%d review=%d", dependencyReads.Load(), historyReads.Load(), detailReads.Load(), reviewReads.Load())
	}
}

type chromeDetailFixture struct {
	board          Board
	cards          []Card
	lifecycle      []CardLifecycle
	graphRevision  int64
	graphDigest    string
	blockedHistory AttemptHistoryPage
	blockedDetail  AttemptDetailPage
	reviewDetail   AttemptDetailPage
}

func newChromeDetailFixture() chromeDetailFixture {
	now := workboardTime()
	budget := WorkBudget{AttemptLimit: 3, TimeLimitMS: 3_600_000, TokenLimit: 50_000, CostMicros: 1_000_000}
	objective := AcceptanceCriterion{Version: 1, ID: "checks", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "validator-ci", Description: "Automated checks pass", Required: true}
	subjective := AcceptanceCriterion{Version: 1, ID: "approval", Kind: "subjective", RequiredSource: "user_feedback", ValidatorID: "operator-review", Description: "Operator approves rollout", Required: true}
	board := Board{Version: 1, ID: "board-a", Revision: 9, LayoutRevision: 4, EventSequence: 14, State: "active", Title: "Filter and detail board", Description: "Authenticated Chrome fixture", CardCount: 4, ActiveClaims: 1, CreatedAt: now, UpdatedAt: now}
	dependent := Card{Version: 1, ID: "card-dependent", BoardID: board.ID, Revision: 2, CriteriaRevision: 1, State: "backlog", Rank: "a1", Title: "Promote rollout", Description: "Depends on deployment.", Priority: "normal", Labels: []string{"release"}, Dependencies: []string{"card-blocked"}, RemainingDependencies: 1, Budget: budget, Criteria: []AcceptanceCriterion{objective}, CreatedAt: now, UpdatedAt: now}
	ready := Card{Version: 1, ID: "card-ready", BoardID: board.ID, Revision: 2, CriteriaRevision: 1, State: "ready", Rank: "b1", Title: "Prepare release", Description: "Prerequisite work.", Priority: "high", Labels: []string{"release"}, Dependencies: []string{}, AssigneeID: "worker-b", Budget: budget, Criteria: []AcceptanceCriterion{objective}, CreatedAt: now, UpdatedAt: now}
	blocked := Card{Version: 1, ID: "card-blocked", BoardID: board.ID, Revision: 5, CriteriaRevision: 1, State: "blocked", Rank: "d1", Title: "Deploy production", Description: "Waiting on a prerequisite and operator input.", Priority: "urgent", Labels: []string{"production"}, Dependencies: []string{"card-ready"}, RemainingDependencies: 1, AssigneeID: "worker-a", AttemptCount: 1, CurrentAttemptID: "attempt-blocked", CurrentClaimID: "claim-blocked", BlockReason: "operator_input", Budget: budget, Criteria: []AcceptanceCriterion{objective}, CreatedAt: now, UpdatedAt: now}
	review := Card{Version: 1, ID: "card-review", BoardID: board.ID, Revision: 7, CriteriaRevision: 2, State: "review", Rank: "e1", Title: "Approve rollout", Description: "Review exact acceptance evidence.", Priority: "high", Labels: []string{"review"}, Dependencies: []string{}, AssigneeID: "worker-review", AttemptCount: 1, CurrentAttemptID: "attempt-review", Budget: budget, Criteria: []AcceptanceCriterion{objective, subjective}, CreatedAt: now, UpdatedAt: now}

	blockedClaim := Claim{Version: 1, ID: "claim-blocked", BoardID: board.ID, CardID: blocked.ID, AttemptID: blocked.CurrentAttemptID, Revision: 3, State: "attention", OwnerID: "worker-a", OwnerType: "worker", TaskID: "task-blocked", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute)}
	blockedDigest := AcceptanceCriteriaDigest(blocked.Criteria)
	blockedAttempt := Attempt{Version: 1, ID: blocked.CurrentAttemptID, BoardID: board.ID, CardID: blocked.ID, Ordinal: 1, Revision: 3, State: "running", WorkerID: "worker-a", CriteriaRevision: 1, CriteriaDigest: blockedDigest, PolicyDigest: strings.Repeat("b", 64), Budget: budget, Criteria: blocked.Criteria, TaskIDs: []string{"task-blocked"}, SessionIDs: []string{"session-blocked"}, Claim: &blockedClaim, Evidence: []EvidenceRecord{}, StartedAt: now}
	checkpoint := WorkCheckpoint{Version: 1, ID: "checkpoint-one", BoardID: board.ID, CardID: blocked.ID, AttemptID: blockedAttempt.ID, ClaimID: blockedClaim.ID, Revision: 1, ClaimRevision: blockedClaim.Revision, CriteriaRevision: blockedAttempt.CriteriaRevision, CriteriaDigest: blockedAttempt.CriteriaDigest, PolicyDigest: blockedAttempt.PolicyDigest, Evidence: "Deployment plan captured before requesting operator input.", EvidenceDigest: strings.Repeat("f", 64), ActorID: blockedAttempt.WorkerID, ActorType: "worker", CreatedAt: now.Add(10 * time.Second)}
	blockedLifecycle := CardLifecycle{Version: 1, CardID: blocked.ID, Attempt: blockedAttempt, Checkpoints: []WorkCheckpoint{checkpoint}, CheckpointCount: 1}
	blockedHistory := AttemptHistoryPage{Version: 1, BoardID: board.ID, CardID: blocked.ID, HighWaterOrdinal: 1, Items: []AttemptHistoryRecord{{Version: 1, ID: blockedAttempt.ID, BoardID: board.ID, CardID: blocked.ID, Ordinal: 1, Revision: blockedAttempt.Revision, State: blockedAttempt.State, WorkerID: blockedAttempt.WorkerID, CriteriaRevision: blockedAttempt.CriteriaRevision, CheckpointCount: 1, StartedAt: blockedAttempt.StartedAt}}}
	blockedDetail := AttemptDetailPage{Version: 1, Attempt: blockedAttempt, CheckpointHighWaterRevision: 1, Checkpoints: []WorkCheckpoint{checkpoint}}

	reviewClaim := Claim{Version: 1, ID: "claim-review", BoardID: board.ID, CardID: review.ID, AttemptID: review.CurrentAttemptID, Revision: 4, State: "released", OwnerID: "worker-review", OwnerType: "worker", TaskID: "task-review", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute)}
	releasedAt := now.Add(30 * time.Second)
	reviewClaim.ReleasedAt = &releasedAt
	reviewDigest, policyDigest := AcceptanceCriteriaDigest(review.Criteria), strings.Repeat("c", 64)
	candidate := Candidate{Version: 1, ID: "candidate-review", BoardID: board.ID, CardID: review.ID, AttemptID: review.CurrentAttemptID, Revision: 1, Digest: strings.Repeat("d", 64), CriteriaDigest: reviewDigest, PolicyDigest: policyDigest, Summary: "Release checks and operator review are ready.", ArtifactRefs: []string{"release-report"}, SubmittedBy: "worker-review", CreatedAt: now.Add(31 * time.Second)}
	evidence := []EvidenceRecord{
		{Version: 1, ID: "evidence-checks", Revision: 1, BoardID: board.ID, CardID: review.ID, AttemptID: review.CurrentAttemptID, CandidateID: candidate.ID, CriterionID: objective.ID, Source: "deterministic", Outcome: "passed", ActorID: "validator-ci", ActorType: "validator", Reference: "ci-report", CandidateDigest: candidate.Digest, CriteriaDigest: reviewDigest, PolicyDigest: policyDigest, CreatedAt: now.Add(32 * time.Second)},
		{Version: 1, ID: "evidence-approval", Revision: 2, BoardID: board.ID, CardID: review.ID, AttemptID: review.CurrentAttemptID, CandidateID: candidate.ID, CriterionID: subjective.ID, Source: "user_feedback", Outcome: "passed", ActorID: "operator-review", ActorType: "operator", Reference: "approval-ticket", CandidateDigest: candidate.Digest, CriteriaDigest: reviewDigest, PolicyDigest: policyDigest, CreatedAt: now.Add(33 * time.Second)},
	}
	candidate.EvidenceCount, candidate.EvidenceDigest = len(evidence), EvidenceDigest(evidence)
	reviewEnded := now.Add(time.Minute)
	reviewAttempt := Attempt{Version: 1, ID: review.CurrentAttemptID, BoardID: board.ID, CardID: review.ID, Ordinal: 1, Revision: 5, State: "review", WorkerID: "worker-review", CriteriaRevision: 2, CriteriaDigest: reviewDigest, PolicyDigest: policyDigest, Budget: budget, Criteria: review.Criteria, TaskIDs: []string{"task-review"}, SessionIDs: []string{"session-review"}, Claim: &reviewClaim, Candidate: &candidate, Evidence: evidence, StartedAt: now, EndedAt: &reviewEnded}
	reviewLifecycle := CardLifecycle{Version: 1, CardID: review.ID, Attempt: reviewAttempt, Checkpoints: []WorkCheckpoint{}, CheckpointCount: 0}
	reviewDetail := AttemptDetailPage{Version: 1, Attempt: reviewAttempt, Checkpoints: []WorkCheckpoint{}}

	result := chromeDetailFixture{board: board, cards: []Card{dependent, ready, blocked, review}, lifecycle: []CardLifecycle{blockedLifecycle, reviewLifecycle}, graphRevision: 6, graphDigest: strings.Repeat("a", 64), blockedHistory: blockedHistory, blockedDetail: blockedDetail, reviewDetail: reviewDetail}
	if board.Validate() != nil || result.snapshot(result.cards, result.lifecycle).Validate() != nil || blockedHistory.Validate() != nil || blockedDetail.Validate() != nil || reviewDetail.Validate() != nil {
		panic("invalid Chrome detail fixture")
	}
	return result
}

func (f chromeDetailFixture) snapshot(cards []Card, lifecycle []CardLifecycle) BoardSnapshot {
	return BoardSnapshot{Version: 1, Board: f.board, Columns: chromeWorkboardColumns(f.board.ID), Cards: cards, Lifecycle: lifecycle, GraphRevision: f.graphRevision, GraphDigest: f.graphDigest}
}

func (f chromeDetailFixture) filtered(request *http.Request) ([]Card, []CardLifecycle, string, bool) {
	if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "100" {
		return nil, nil, "", false
	}
	query := request.URL.Query()
	if len(query) == 1 {
		return f.cards, f.lifecycle, "full", true
	}
	if query.Get("state") != "blocked" {
		return nil, nil, "", false
	}
	if len(query) == 2 {
		return []Card{f.cards[2]}, []CardLifecycle{f.lifecycle[0]}, "state", true
	}
	if len(query) == 5 && query.Get("assignee_id") == "worker-a" && query.Get("owner_id") == "worker-a" && query.Get("claim_state") == "attention" {
		return []Card{f.cards[2]}, []CardLifecycle{f.lifecycle[0]}, "compound", true
	}
	return nil, nil, "", false
}

const chromeWorkboardDetailsCDP = `
const port = process.argv[1], origin = process.argv[2];
const targets = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
const target = targets.find(item => item.type === 'page'); if (!target) throw new Error('Chrome did not expose a page target');
const socket = new WebSocket(target.webSocketDebuggerUrl), pending = new Map(); let sequence = 0;
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}); });
socket.addEventListener('message', event => { const message = JSON.parse(event.data); if (!message.id) return; const waiter = pending.get(message.id); if (!waiter) return; pending.delete(message.id); message.error ? waiter.reject(new Error(message.error.message)) : waiter.resolve(message.result); });
function cdp(method, params = {}) { const id = ++sequence; return new Promise((resolve, reject) => { pending.set(id, {resolve, reject}); socket.send(JSON.stringify({id, method, params})); }); }
async function evaluate(expression) { const result = await cdp('Runtime.evaluate', {expression, returnByValue:true, awaitPromise:true}); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text); return result.result.value; }
async function eventually(expression, label) { const deadline = Date.now() + 5000; let last; while (Date.now() < deadline) { try { last = await evaluate(expression); if (last) return; } catch (error) { last = error.message; } await new Promise(resolve => setTimeout(resolve, 20)); } throw new Error(label + ' (last value: ' + JSON.stringify(last) + ')'); }
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
await cdp('Page.addScriptToEvaluateOnNewDocument', {source:'Object.defineProperty(window, "EventSource", {value:undefined, writable:false});'});
const cookie = await cdp('Network.setCookie', {name:'darwin_session', value:'valid', url:origin + '/app/'}); if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/workboards/board-a'});
await eventually('document.readyState === "complete" && document.querySelector("#selected-board-title").textContent === "Filter and detail board" && document.querySelectorAll(".kanban-card").length === 4', 'populated detail board did not load');
await evaluate('(() => { const control = document.querySelector("#card-state-filter"); control.value = "blocked"; control.dispatchEvent(new Event("change", {bubbles:true})); return true; })()');
await eventually('document.querySelectorAll(".kanban-card").length === 1 && document.querySelector(".kanban-card").dataset.cardId === "card-blocked" && document.querySelector("#selected-board-meta").textContent.includes("1 matching cards loaded · 4 total on board") && document.querySelector("#selected-board-meta").textContent.includes("filters clear")', 'state filter did not produce a bounded populated board');
await evaluate('(() => { document.querySelector("#assignee-filter").value = "worker-a"; document.querySelector("#owner-filter").value = "worker-a"; document.querySelector("#claim-state-filter").value = "attention"; document.querySelector("#card-filters").requestSubmit(); return true; })()');
await eventually('document.querySelector("#workboard-filter-status").textContent === "Filters applied." && document.querySelectorAll(".kanban-card").length === 1 && document.querySelector("[data-card-id=card-blocked]").textContent.includes("stale/orphan claim attention")', 'compound ownership and claim filter did not render');
await evaluate('document.querySelector("#reset-card-filters").click()');
await eventually('document.querySelectorAll(".kanban-card").length === 4 && document.activeElement.id === "assignee-filter" && document.querySelector("#selected-board-meta").textContent.includes("position controls ready")', 'filter reset did not restore the complete board and focus');
await evaluate('document.querySelector("[data-card-id=card-blocked] .card-toggle").click()');
await eventually('(() => { const card = document.querySelector("[data-card-id=card-blocked]"); const text = card.querySelector(".card-details").textContent; return card.querySelector(".card-toggle").getAttribute("aria-expanded") === "true" && text.includes("Prerequisites preview") && text.includes("card-ready") && text.includes("Dependents preview") && text.includes("card-dependent") && text.includes("Attempt history preview") && text.includes("Attempt 1 · running"); })()', 'expanded relationship and attempt previews did not render');
await evaluate('document.querySelector("[data-card-id=card-blocked] .attempt-toggle").click()');
await eventually('(() => { const detail = document.querySelector("[data-card-id=card-blocked] .attempt-detail"); return detail.textContent.includes("Bounded preview: worker worker-a · running · revision 3") && detail.textContent.includes("Lease owner worker-a · attention") && detail.textContent.includes("stale/orphan attention required") && detail.textContent.includes("Checkpoint 1 ·"); })()', 'attempt lease and checkpoint detail did not render');
await evaluate('document.querySelector("[data-card-id=card-review] .card-review").click()');
await eventually('(() => { const review = document.querySelector("[data-card-id=card-review] .candidate-review"); const text = review.textContent; return review.hidden === false && review.getAttribute("aria-busy") === "false" && text.includes("Exact review candidate from attempt attempt-review") && text.includes("Acceptance criteria and evidence") && text.includes("deterministic · passed · reference ci-report") && text.includes("user_feedback · passed · reference approval-ticket") && text.includes("Model-audit evidence is advisory"); })()', 'exact candidate acceptance evidence did not render');
socket.close();
`
