package workboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	CardMutationVersion = 1
	MaxPageItems        = 100
	MaxCursorBytes      = 512
	MaxTitleBytes       = 256
	MaxDescriptionBytes = 64 << 10
	MaxRankBytes        = 128
	MaxLabels           = 32
	MaxLabelBytes       = 64
)

type Card struct {
	ID                    string
	BoardID               string
	Revision              int64
	State                 State
	Rank                  string
	Title                 string
	Description           string
	Priority              string
	Labels                []string
	AssigneeID            string
	ParentID              string
	Dependencies          []string
	RemainingDependencies int
	CurrentClaimID        string
	HasCandidate          bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (c Card) Validate() error {
	if !validID(c.ID) || !validID(c.BoardID) || c.Revision < 1 || !validState(c.State) ||
		!boundedPrintable(c.Rank, 1, MaxRankBytes) || !boundedText(c.Title, MaxTitleBytes, false) || !boundedText(c.Description, MaxDescriptionBytes, true) ||
		!validPriority(c.Priority) || !validStrings(c.Labels, MaxLabels, MaxLabelBytes) || !optionalID(c.AssigneeID) || !optionalID(c.ParentID) ||
		!validIDs(c.Dependencies, MaxDependencies) || c.ParentID == c.ID || contains(c.Dependencies, c.ID) ||
		c.RemainingDependencies < 0 || c.RemainingDependencies > len(c.Dependencies) || !optionalID(c.CurrentClaimID) ||
		!validTime(c.CreatedAt) || !validTime(c.UpdatedAt) || c.UpdatedAt.Before(c.CreatedAt) {
		return fail(CodeInvalid, "card")
	}
	return nil
}

type CardPatch struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Priority    *string   `json:"priority"`
	Labels      *[]string `json:"labels"`
	AssigneeID  *string   `json:"assignee_id"`
	ParentID    *string   `json:"parent_id"`
}

// NewCard is immutable creation intent. Identity, revisions and timestamps are
// allocated by the durable store in the same transaction as the replay receipt.
type NewCard struct {
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Priority     string   `json:"priority"`
	Labels       []string `json:"labels"`
	AssigneeID   string   `json:"assignee_id"`
	ParentID     string   `json:"parent_id"`
	Dependencies []string `json:"dependencies"`
}

func (c NewCard) validate() error {
	if !boundedText(c.Title, MaxTitleBytes, false) || !boundedText(c.Description, MaxDescriptionBytes, true) ||
		!validPriority(c.Priority) || !validStrings(c.Labels, MaxLabels, MaxLabelBytes) || !optionalID(c.AssigneeID) ||
		!optionalID(c.ParentID) || !validIDs(c.Dependencies, MaxDependencies) {
		return fail(CodeInvalid, "create")
	}
	return nil
}

func (p CardPatch) validate() error {
	if p.Title == nil && p.Description == nil && p.Priority == nil && p.Labels == nil && p.AssigneeID == nil && p.ParentID == nil {
		return fail(CodeInvalid, "patch")
	}
	if p.Title != nil && !boundedText(*p.Title, MaxTitleBytes, false) || p.Description != nil && !boundedText(*p.Description, MaxDescriptionBytes, true) ||
		p.Priority != nil && !validPriority(*p.Priority) || p.Labels != nil && !validStrings(*p.Labels, MaxLabels, MaxLabelBytes) ||
		p.AssigneeID != nil && !optionalID(*p.AssigneeID) || p.ParentID != nil && !optionalID(*p.ParentID) {
		return fail(CodeInvalid, "patch")
	}
	return nil
}

type CardFilter struct {
	State      State
	ParentID   *string
	AssigneeID *string
	Label      string
	After      string
	Limit      int
}

func (f CardFilter) validate() error {
	if f.State != "" && !validState(f.State) || !optionalIDPointer(f.ParentID) || !optionalIDPointer(f.AssigneeID) ||
		!boundedText(f.Label, MaxLabelBytes, true) || !validCursor(f.After) || f.Limit < 1 || f.Limit > MaxPageItems {
		return fail(CodeInvalid, "filter")
	}
	return nil
}

type CardPage struct {
	Items      []Card
	NextCursor string
	HasMore    bool
}

type MutationKind string

const (
	MutationCreate           MutationKind = "create"
	MutationRevise           MutationKind = "revise"
	MutationMove             MutationKind = "move"
	MutationReorder          MutationKind = "reorder"
	MutationDependencyAdd    MutationKind = "dependency.add"
	MutationDependencyRemove MutationKind = "dependency.remove"
)

// CardMutation is the canonical, replay-bound command passed to durable
// storage. Stores atomically compare every nonzero revision and persist the
// idempotency key with RequestDigest before returning success.
type CardMutation struct {
	Version                int          `json:"version"`
	Kind                   MutationKind `json:"kind"`
	BoardID                string       `json:"board_id"`
	CardID                 string       `json:"card_id"`
	IdempotencyKey         string       `json:"-"`
	RequestDigest          string       `json:"-"`
	ExpectedBoardRevision  int64        `json:"expected_board_revision"`
	ExpectedCardRevision   int64        `json:"expected_card_revision"`
	ExpectedGraphRevision  int64        `json:"expected_graph_revision"`
	ExpectedLayoutRevision int64        `json:"expected_layout_revision"`
	Create                 *NewCard     `json:"create"`
	Patch                  CardPatch    `json:"patch"`
	TargetState            State        `json:"target_state"`
	BeforeCardID           string       `json:"before_card_id"`
	AfterCardID            string       `json:"after_card_id"`
	DependencyID           string       `json:"dependency_id"`
}

type CardStore interface {
	GetCard(context.Context, string, string) (Card, error)
	ListCards(context.Context, string, CardFilter) (CardPage, error)
	LoadGraph(context.Context, string) (Graph, error)
	ApplyCardMutation(context.Context, CardMutation) (Card, error)
}

// CardService is a domain transaction coordinator, not an authorization
// boundary. A shared application service must authenticate and authorize the
// principal before invoking any method; transport adapters must never expose a
// CardService directly.
type CardService struct{ store CardStore }

func NewCardService(store CardStore) (*CardService, error) {
	if store == nil {
		return nil, fail(CodeInvalid, "store")
	}
	return &CardService{store: store}, nil
}

type CreateCardRequest struct {
	BoardID               string
	Card                  NewCard
	IdempotencyKey        string
	ExpectedBoardRevision int64
	ExpectedGraphRevision int64
}

type ReviseCardRequest struct {
	BoardID, CardID, IdempotencyKey string
	Patch                           CardPatch
	ExpectedCardRevision            int64
	ExpectedGraphRevision           int64
}

type MoveCardRequest struct {
	BoardID, CardID, IdempotencyKey string
	TargetState                     State
	ExpectedBoardRevision           int64
	ExpectedCardRevision            int64
	ExpectedLayoutRevision          int64
}

type ReorderCardRequest struct {
	BoardID, CardID, IdempotencyKey string
	BeforeCardID, AfterCardID       string
	ExpectedBoardRevision           int64
	ExpectedCardRevision            int64
	ExpectedLayoutRevision          int64
}

type DependencyRequest struct {
	BoardID, CardID, DependencyID, IdempotencyKey string
	ExpectedCardRevision                          int64
	ExpectedGraphRevision                         int64
}

type TraverseDependenciesRequest struct {
	BoardID, CardID       string
	ExpectedGraphRevision int64
	MaxDepth              int
	Direction             TraversalDirection
}

type TraversalDirection string

const (
	TraverseDependencies TraversalDirection = "dependencies"
	TraverseDependents   TraversalDirection = "dependents"
)

type DependencyTraversal struct {
	BoardID, RootCardID string
	GraphRevision       int64
	Cards               []Node
}

func (s *CardService) CreateCard(ctx context.Context, request CreateCardRequest) (Card, error) {
	if !validID(request.BoardID) || request.Card.validate() != nil ||
		!validMutation(request.IdempotencyKey, request.ExpectedBoardRevision, request.ExpectedGraphRevision) {
		return Card{}, fail(CodeInvalid, "create")
	}
	graph, err := s.store.LoadGraph(ctx, request.BoardID)
	if err != nil {
		return Card{}, err
	}
	if _, err = ValidateGraph(graph, request.ExpectedGraphRevision); err != nil {
		return Card{}, err
	}
	if request.Card.ParentID != "" && !graphContains(graph, request.Card.ParentID) {
		return Card{}, fail(CodeMissingNode, "parent")
	}
	for _, dependencyID := range request.Card.Dependencies {
		if !graphContains(graph, dependencyID) {
			return Card{}, fail(CodeMissingNode, "dependency")
		}
	}
	mutation := CardMutation{Version: CardMutationVersion, Kind: MutationCreate, BoardID: request.BoardID, IdempotencyKey: request.IdempotencyKey,
		ExpectedBoardRevision: request.ExpectedBoardRevision, ExpectedGraphRevision: request.ExpectedGraphRevision, Create: copyNewCardPtr(request.Card)}
	return s.apply(ctx, mutation)
}

func (s *CardService) GetCard(ctx context.Context, boardID, cardID string) (Card, error) {
	if !validID(boardID) || !validID(cardID) {
		return Card{}, fail(CodeInvalid, "card")
	}
	card, err := s.store.GetCard(ctx, boardID, cardID)
	if err != nil {
		return Card{}, err
	}
	if card.Validate() != nil || card.BoardID != boardID || card.ID != cardID {
		return Card{}, fail(CodeInvalid, "stored_card")
	}
	return copyCard(card), nil
}

func (s *CardService) ListCards(ctx context.Context, boardID string, filter CardFilter) (CardPage, error) {
	if !validID(boardID) || filter.validate() != nil {
		return CardPage{}, fail(CodeInvalid, "list")
	}
	page, err := s.store.ListCards(ctx, boardID, copyFilter(filter))
	if err != nil {
		return CardPage{}, err
	}
	if len(page.Items) > filter.Limit || page.HasMore != (page.NextCursor != "") || !validCursor(page.NextCursor) || page.HasMore && len(page.Items) == 0 {
		return CardPage{}, fail(CodeInvalid, "stored_page")
	}
	seen := map[string]bool{}
	for index := range page.Items {
		card := page.Items[index]
		if card.Validate() != nil || card.BoardID != boardID || seen[card.ID] {
			return CardPage{}, fail(CodeInvalid, "stored_page")
		}
		seen[card.ID] = true
		page.Items[index] = copyCard(card)
	}
	return page, nil
}

func (s *CardService) ReviseCard(ctx context.Context, request ReviseCardRequest) (Card, error) {
	if !validCardMutation(request.BoardID, request.CardID, request.IdempotencyKey, request.ExpectedCardRevision) || request.Patch.validate() != nil {
		return Card{}, fail(CodeInvalid, "revise")
	}
	if request.Patch.ParentID != nil {
		if request.ExpectedGraphRevision < 1 {
			return Card{}, fail(CodeInvalid, "graph_revision")
		}
		graph, err := s.store.LoadGraph(ctx, request.BoardID)
		if err != nil {
			return Card{}, err
		}
		found := false
		for index := range graph.Nodes {
			if graph.Nodes[index].ID == request.CardID {
				graph.Nodes[index].ParentID = *request.Patch.ParentID
				found = true
			}
		}
		if !found {
			return Card{}, fail(CodeMissingNode, "card")
		}
		if _, err = ValidateGraph(graph, request.ExpectedGraphRevision); err != nil {
			return Card{}, err
		}
	}
	mutation := CardMutation{Version: CardMutationVersion, Kind: MutationRevise, BoardID: request.BoardID, CardID: request.CardID, IdempotencyKey: request.IdempotencyKey,
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedGraphRevision: request.ExpectedGraphRevision, Patch: copyPatch(request.Patch)}
	return s.apply(ctx, mutation)
}

func (s *CardService) MoveCard(ctx context.Context, request MoveCardRequest) (Card, error) {
	if !validBoardCardMutation(request.BoardID, request.CardID, request.IdempotencyKey, request.ExpectedBoardRevision, request.ExpectedCardRevision) ||
		request.ExpectedLayoutRevision < 1 || !validState(request.TargetState) {
		return Card{}, fail(CodeInvalid, "move")
	}
	card, err := s.GetCard(ctx, request.BoardID, request.CardID)
	if err != nil {
		return Card{}, err
	}
	_, err = ValidateTransition(Transition{BoardID: request.BoardID, CardID: request.CardID, Command: Move, From: card.State, To: request.TargetState,
		CurrentCardRevision: card.Revision, ExpectedCardRevision: request.ExpectedCardRevision, CurrentGraphRevision: 1,
		ExpectedGraphRevision: 1, DependenciesSatisfied: card.RemainingDependencies == 0, HasLiveClaim: card.CurrentClaimID != ""})
	if err != nil {
		return Card{}, err
	}
	return s.apply(ctx, CardMutation{Version: CardMutationVersion, Kind: MutationMove, BoardID: request.BoardID, CardID: request.CardID, IdempotencyKey: request.IdempotencyKey,
		ExpectedBoardRevision: request.ExpectedBoardRevision, ExpectedCardRevision: request.ExpectedCardRevision,
		ExpectedLayoutRevision: request.ExpectedLayoutRevision, TargetState: request.TargetState})
}

func (s *CardService) ReorderCard(ctx context.Context, request ReorderCardRequest) (Card, error) {
	if !validBoardCardMutation(request.BoardID, request.CardID, request.IdempotencyKey, request.ExpectedBoardRevision, request.ExpectedCardRevision) ||
		request.ExpectedLayoutRevision < 1 || !optionalID(request.BeforeCardID) || !optionalID(request.AfterCardID) ||
		(request.BeforeCardID == "") == (request.AfterCardID == "") || request.BeforeCardID == request.CardID || request.AfterCardID == request.CardID {
		return Card{}, fail(CodeInvalid, "reorder")
	}
	card, err := s.GetCard(ctx, request.BoardID, request.CardID)
	if err != nil {
		return Card{}, err
	}
	anchorID := request.BeforeCardID
	if anchorID == "" {
		anchorID = request.AfterCardID
	}
	anchor, err := s.GetCard(ctx, request.BoardID, anchorID)
	if err != nil {
		return Card{}, err
	}
	if card.State != anchor.State {
		return Card{}, fail(CodeInvalid, "reorder_state")
	}
	return s.apply(ctx, CardMutation{Version: CardMutationVersion, Kind: MutationReorder, BoardID: request.BoardID, CardID: request.CardID, IdempotencyKey: request.IdempotencyKey,
		ExpectedBoardRevision: request.ExpectedBoardRevision, ExpectedCardRevision: request.ExpectedCardRevision,
		ExpectedLayoutRevision: request.ExpectedLayoutRevision, BeforeCardID: request.BeforeCardID, AfterCardID: request.AfterCardID})
}

func (s *CardService) AddDependency(ctx context.Context, request DependencyRequest) (Card, error) {
	return s.changeDependency(ctx, request, true)
}

func (s *CardService) RemoveDependency(ctx context.Context, request DependencyRequest) (Card, error) {
	return s.changeDependency(ctx, request, false)
}

func (s *CardService) changeDependency(ctx context.Context, request DependencyRequest, add bool) (Card, error) {
	if !validCardMutation(request.BoardID, request.CardID, request.IdempotencyKey, request.ExpectedCardRevision) ||
		!validID(request.DependencyID) || request.DependencyID == request.CardID || request.ExpectedGraphRevision < 1 {
		return Card{}, fail(CodeInvalid, "dependency")
	}
	graph, err := s.store.LoadGraph(ctx, request.BoardID)
	if err != nil {
		return Card{}, err
	}
	if _, err = ValidateGraph(graph, request.ExpectedGraphRevision); err != nil {
		return Card{}, err
	}
	found := false
	for index := range graph.Nodes {
		if graph.Nodes[index].ID != request.CardID {
			continue
		}
		found = true
		has := contains(graph.Nodes[index].Dependencies, request.DependencyID)
		if add == has {
			return Card{}, fail(CodeInvalid, "dependency")
		}
		if add {
			graph.Nodes[index].Dependencies = append(append([]string(nil), graph.Nodes[index].Dependencies...), request.DependencyID)
		} else {
			graph.Nodes[index].Dependencies = remove(graph.Nodes[index].Dependencies, request.DependencyID)
		}
	}
	if !found {
		return Card{}, fail(CodeMissingNode, "card")
	}
	if _, err = ValidateGraph(graph, request.ExpectedGraphRevision); err != nil {
		return Card{}, err
	}
	kind := MutationDependencyRemove
	if add {
		kind = MutationDependencyAdd
	}
	return s.apply(ctx, CardMutation{Version: CardMutationVersion, Kind: kind, BoardID: request.BoardID, CardID: request.CardID, DependencyID: request.DependencyID,
		IdempotencyKey: request.IdempotencyKey, ExpectedCardRevision: request.ExpectedCardRevision,
		ExpectedGraphRevision: request.ExpectedGraphRevision})
}

func (s *CardService) TraverseDependencies(ctx context.Context, request TraverseDependenciesRequest) (DependencyTraversal, error) {
	if !validID(request.BoardID) || !validID(request.CardID) || request.ExpectedGraphRevision < 1 || request.MaxDepth < 1 || request.MaxDepth > MaxGraphDepth ||
		request.Direction != TraverseDependencies && request.Direction != TraverseDependents {
		return DependencyTraversal{}, fail(CodeInvalid, "traversal")
	}
	graph, err := s.store.LoadGraph(ctx, request.BoardID)
	if err != nil {
		return DependencyTraversal{}, err
	}
	if _, err = ValidateGraph(graph, request.ExpectedGraphRevision); err != nil {
		return DependencyTraversal{}, err
	}
	byID := make(map[string]Node, len(graph.Nodes))
	dependents := make(map[string][]string, len(graph.Nodes))
	for _, node := range graph.Nodes {
		byID[node.ID] = node
		for _, dependencyID := range node.Dependencies {
			dependents[dependencyID] = append(dependents[dependencyID], node.ID)
		}
	}
	if _, ok := byID[request.CardID]; !ok {
		return DependencyTraversal{}, fail(CodeMissingNode, "card")
	}
	type queued struct {
		id    string
		depth int
	}
	queue, seen, result := []queued{{request.CardID, 0}}, map[string]bool{request.CardID: true}, []Node{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth == request.MaxDepth {
			continue
		}
		next := byID[current.id].Dependencies
		if request.Direction == TraverseDependents {
			next = dependents[current.id]
		}
		for _, dependencyID := range next {
			if seen[dependencyID] {
				continue
			}
			seen[dependencyID] = true
			result = append(result, copyNode(byID[dependencyID]))
			if len(seen) > MaxGraphVisits {
				return DependencyTraversal{}, fail(CodeVisitsExhausted, "traversal")
			}
			queue = append(queue, queued{dependencyID, current.depth + 1})
		}
	}
	return DependencyTraversal{BoardID: request.BoardID, RootCardID: request.CardID, GraphRevision: graph.GraphRevision, Cards: result}, nil
}

func (s *CardService) apply(ctx context.Context, mutation CardMutation) (Card, error) {
	digest, err := mutationDigest(mutation)
	if err != nil {
		return Card{}, err
	}
	mutation.RequestDigest = digest
	card, err := s.store.ApplyCardMutation(ctx, mutation)
	if err != nil {
		return Card{}, err
	}
	if card.Validate() != nil || card.BoardID != mutation.BoardID || mutation.CardID != "" && card.ID != mutation.CardID {
		return Card{}, fail(CodeInvalid, "stored_card")
	}
	return copyCard(card), nil
}

func mutationDigest(mutation CardMutation) (string, error) {
	copy := mutation
	copy.RequestDigest = ""
	copy.IdempotencyKey = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", fail(CodeInvalid, "mutation")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func validBoardCardMutation(boardID, cardID, key string, boardRevision, cardRevision int64) bool {
	return validID(boardID) && validID(cardID) && validKey(key) && boardRevision >= 1 && cardRevision >= 1
}
func validCardMutation(boardID, cardID, key string, cardRevision int64) bool {
	return validID(boardID) && validID(cardID) && validKey(key) && cardRevision >= 1
}
func validMutation(key string, revisions ...int64) bool {
	if !validKey(key) {
		return false
	}
	for _, revision := range revisions {
		if revision < 1 {
			return false
		}
	}
	return true
}
func validKey(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for index := range value {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func validCursor(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > MaxCursorBytes {
		return false
	}
	for index := range value {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

// bounded is retained for compact single-line domain identifiers and metadata.
func bounded(value string, min, max int) bool {
	return len(value) >= min && len(value) <= max && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func boundedPrintable(value string, min, max int) bool {
	if len(value) < min || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func boundedText(value string, max int, allowEmpty bool) bool {
	if len(value) > max || !utf8.ValidString(value) || !allowEmpty && strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if r != '\n' && r != '\r' && r != '\t' && unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func optionalID(value string) bool         { return value == "" || validID(value) }
func optionalIDPointer(value *string) bool { return value == nil || *value == "" || validID(*value) }
func validPriority(value string) bool {
	return value == "urgent" || value == "high" || value == "normal" || value == "low"
}
func validIDs(values []string, max int) bool {
	if values == nil || len(values) > max {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !validID(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func validStrings(values []string, maxItems, maxBytes int) bool {
	if values == nil || len(values) > maxItems {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if !boundedText(value, maxBytes, false) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func remove(values []string, target string) []string {
	result := make([]string, 0, len(values)-1)
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
func graphContains(graph Graph, id string) bool {
	for _, node := range graph.Nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}
func copyNode(node Node) Node {
	node.Dependencies = append([]string{}, node.Dependencies...)
	return node
}
func copyCard(card Card) Card {
	card.Labels = append([]string{}, card.Labels...)
	card.Dependencies = append([]string{}, card.Dependencies...)
	return card
}
func copyNewCardPtr(card NewCard) *NewCard {
	card.Labels = append([]string{}, card.Labels...)
	card.Dependencies = append([]string{}, card.Dependencies...)
	return &card
}
func copyPatch(patch CardPatch) CardPatch {
	copied := patch
	copied.Title = copyStringPointer(patch.Title)
	copied.Description = copyStringPointer(patch.Description)
	copied.Priority = copyStringPointer(patch.Priority)
	copied.AssigneeID = copyStringPointer(patch.AssigneeID)
	copied.ParentID = copyStringPointer(patch.ParentID)
	if patch.Labels != nil {
		labels := append([]string{}, (*patch.Labels)...)
		copied.Labels = &labels
	}
	return copied
}

func copyFilter(filter CardFilter) CardFilter {
	filter.ParentID = copyStringPointer(filter.ParentID)
	filter.AssigneeID = copyStringPointer(filter.AssigneeID)
	return filter
}

func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
