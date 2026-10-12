package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The policy gate's run: gathering the snapshot the rules judge, publishing
// the verdict, and moving the tickets the verdict implies (spec 09).
//
// The rules themselves are pure (spec 09 §2 Decisions) and know nothing about
// a network. Everything that has to ask somebody something lives here, above
// the adapters and below the command, which is where spec 01 §2 puts work that
// needs providers but must not know which providers they are.

// GateVersion is the schema of the gate's --json document.
const GateVersion = "flow.policy/v1"

// What the gate did about the status and the comment. One vocabulary covers
// both, so that a reader of the JSON document does not have to learn two.
const (
	// GatePosted means flow wrote it.
	GatePosted = "posted"
	// GateUnchanged means what was already there said the same thing.
	GateUnchanged = "unchanged"
	// GateDeleted means flow removed what it had written before.
	GateDeleted = "deleted"
	// GateNone means nothing was wanted and nothing was there.
	GateNone = "none"
	// GateDryRun means flow decided but was told not to act.
	GateDryRun = "dry-run"
	// GateReadOnly means the credential may not write here (spec 09 §1).
	GateReadOnly = "read-only"
)

// GateInput is everything `flow check pr` needs to judge one pull request and
// then act on the verdict.
type GateInput struct {
	// PR is the pull request number under judgement.
	PR int
	// Config is the workflow being enforced.
	Config *config.Config
	// Lock is what flow created before, which is how a renamed board column
	// is still recognised.
	Lock *config.Lock
	// Adapters are the providers to read and write through.
	Adapters Adapters
	// Now is the moment the snapshot is taken at.
	Now time.Time
	// DryRun evaluates and decides, and publishes nothing.
	DryRun bool
	// NoTransitions gates without moving any ticket.
	NoTransitions bool
	// TargetURL is the CI run that produced the verdict, for the status to
	// point at. Only the caller knows it.
	TargetURL string
}

// Gate is what one run of the policy gate decided and did.
type Gate struct {
	// Result is the verdict.
	Result PolicyResult
	// Status is the commit status the verdict asks for.
	Status core.CommitStatus
	// StatusOutcome and CommentOutcome are what became of them.
	StatusOutcome  string
	CommentOutcome string
	// Moves are the ticket transitions this run considered, in the order the
	// tickets were linked.
	Moves []TicketMove
	// ReadOnly reports a credential that may not write to this repository,
	// which is the ordinary case on a fork's pull request.
	ReadOnly bool
}

// Pass reports a pull request nothing blocks.
func (g Gate) Pass() bool { return g.Result.Pass() }

// RunGate judges one pull request and acts on the verdict: status first, then
// the comment, then the transitions. The order is the one spec 09 gives, and
// it matters — the status is the thing other machinery waits for, so it goes
// out before anything slower can fail.
func RunGate(ctx context.Context, in GateInput) (Gate, error) {
	snapshot, err := GateSnapshot(ctx, in)
	if err != nil {
		return Gate{}, err
	}
	gate := Gate{Result: CheckPolicy(snapshot)}
	gate.Status = gate.Result.Status(in.Config)
	gate.Status.TargetURL = in.TargetURL

	if err := gate.publish(ctx, in); err != nil {
		return gate, err
	}
	if err := gate.move(ctx, in, snapshot); err != nil {
		return gate, err
	}
	return gate, nil
}

// GateSnapshot gathers everything the rules read, in one pass over the forge
// and one pass over the board. It is taken once so that no two rules can be
// judging different worlds (spec 09 §2).
func GateSnapshot(ctx context.Context, in GateInput) (PolicySnapshot, error) {
	if in.Config == nil {
		return PolicySnapshot{}, core.Errorf(core.ErrValidation, "evaluate the policy gate").
			WithWhy("there is no configuration to enforce").
			WithFix("run flow init first")
	}
	if in.Adapters.Forge == nil || in.Adapters.Tracker == nil {
		return PolicySnapshot{}, core.Errorf(core.ErrValidation, "evaluate the policy gate").
			WithWhy("it needs both a forge and a tracker, and this repository has not got both").
			WithFix("check forge and tracker in .flow.yaml")
	}

	pr, err := in.Adapters.Forge.PullRequest(ctx, in.PR)
	if err != nil {
		return PolicySnapshot{}, core.Explain(err, "read pull request #%d", in.PR)
	}
	snapshot := PolicySnapshot{PR: pr, Config: in.Config, Now: in.Now}

	board, err := in.Adapters.Tracker.Tickets(ctx, core.TicketQuery{})
	if err != nil {
		return PolicySnapshot{}, core.Explain(err, "read the board")
	}
	snapshot.WIP = wipFrom(in.Config, in.Lock, board)

	for _, key := range in.Adapters.Tracker.LinkedKeys(pr) {
		ticket, err := in.Adapters.Tracker.Ticket(ctx, key)
		switch {
		case errors.Is(err, core.ErrNotFound):
			snapshot.Unlinkable = append(snapshot.Unlinkable, key)
			continue
		case err != nil:
			return PolicySnapshot{}, core.Explain(err, "read ticket %s", key)
		}
		snapshot.Tickets = append(snapshot.Tickets, LinkedTicket{
			Ticket:    ticket,
			Column:    ColumnOf(in.Config, in.Lock, ticket),
			InProject: onBoard(board, ticket.Key),
		})
	}
	return snapshot, nil
}

// onBoard reports whether the board listing holds this ticket.
//
// It is the board's own answer rather than a second question to the tracker:
// the listing is every item of the project, so an issue missing from it is an
// issue that is not an item, which is exactly what pr.ticket.in_project asks
// (spec 09 §2). Asking the tracker instead would mean trusting an empty
// column to mean "not on the board", and an item with no status set has an
// empty column too.
func onBoard(board []core.Ticket, key string) bool {
	for _, item := range board {
		if sameTicket(item.Key, key) {
			return true
		}
	}
	return false
}

// sameTicket compares two ticket keys the way people write them, which is
// sometimes with the # and sometimes without.
func sameTicket(a, b string) bool {
	return strings.EqualFold(strings.TrimPrefix(a, "#"), strings.TrimPrefix(b, "#"))
}

// ColumnOf is the configured column a ticket sits in, empty when the board has
// it somewhere the configuration does not name.
//
// The tracker's own column identifier is opaque — on GitHub Projects it is the
// status option's id — so the mapping is by the name the team gave the column,
// which is the name flow created the option under. A column somebody has since
// renamed on the board is found through the lock instead: flow recorded the
// identifier it created and the name it used, so the old name still leads back
// to the right column (spec 04 §2).
func ColumnOf(cfg *config.Config, lock *config.Lock, ticket core.Ticket) string {
	if cfg == nil {
		return ""
	}
	if id, found := columnNamed(cfg, ticket.Status.Name); found {
		return id
	}
	if id, found := columnNamed(cfg, createdAs(lock, string(ticket.ColumnID))); found {
		return id
	}
	return ""
}

// columnNamed finds the configured column with that display name.
func columnNamed(cfg *config.Config, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, column := range cfg.Workflow.Columns {
		if strings.EqualFold(strings.TrimSpace(column.Name), name) {
			return column.ID, true
		}
	}
	return "", false
}

// createdAs is the name flow gave a status option when it made it.
func createdAs(lock *config.Lock, id string) string {
	if lock == nil || id == "" {
		return ""
	}
	kind := CreationKind(core.ItemTrackerStatusOptions)
	for _, creation := range lock.Created {
		if creation.Kind == kind && creation.ID == id {
			return creation.Name
		}
	}
	return ""
}

// wipFrom buckets the board into the two columns the WIP rules read: the one
// pr_ready_for_review lands in and the one pr_opened_draft lands in (spec 09
// §2 Decisions). Everything else on the board is not in flight as far as those
// rules are concerned.
//
// Nothing here says when an item arrived in its column. No tracker flow speaks
// to records that, and the rule's Decision is explicit that a tracker which
// will not say gives no arrival order at all — so the field is left zero
// rather than filled with the last-edited time, which would let an item that
// was merely touched jump the queue.
func wipFrom(cfg *config.Config, lock *config.Lock, board []core.Ticket) WIPCounts {
	review, hasReview := columnFor(cfg, config.EventPRReadyForReview)
	progress, hasProgress := columnFor(cfg, config.EventPROpenedDraft)
	var counts WIPCounts
	for _, ticket := range board {
		switch column := ColumnOf(cfg, lock, ticket); {
		case column == "":
		case hasReview && column == review.ID:
			counts.Review = append(counts.Review, wipItem(ticket))
		case hasProgress && column == progress.ID:
			counts.InProgress = append(counts.InProgress, wipItem(ticket))
		}
	}
	return counts
}

// wipItem is one board item as the WIP rules read it.
func wipItem(ticket core.Ticket) WIPItem {
	return WIPItem{
		Key:       ticket.Key,
		Assignees: ticket.Assignees,
		Labels:    ticket.Labels,
		URL:       ticket.URL,
	}
}

// publish writes the verdict where people and machines will see it: the commit
// status, then the explanation comment.
//
// A credential that may not write is not a failure. A pull request from a fork
// runs with a read-only token, and the maintainers' re-run catches up (spec 09
// §1) — so a refusal is recorded and the run still succeeds. It is discovered
// by being refused rather than predicted from a scope list: the forge is the
// only authority on what a credential may do, and a gate that guessed would
// either skip posting it could have done or claim it was allowed and fail.
func (g *Gate) publish(ctx context.Context, in GateInput) error {
	if in.DryRun {
		g.StatusOutcome, g.CommentOutcome = GateDryRun, GateDryRun
		return nil
	}
	forge := in.Adapters.Forge
	sha := g.Result.SHA

	latest, err := forge.CommitStatuses(ctx, sha)
	if err != nil {
		return core.Explain(err, "read the statuses on %s", sha)
	}
	if statusAlreadySaid(latest, g.Status) {
		g.StatusOutcome = GateUnchanged
	} else if err := forge.SetCommitStatus(ctx, sha, g.Status); err != nil {
		if !errors.Is(err, core.ErrPermission) {
			return core.Explain(err, "publish the %s status", g.Status.Context)
		}
		g.ReadOnly = true
		g.StatusOutcome, g.CommentOutcome = GateReadOnly, GateReadOnly
		return nil
	} else {
		g.StatusOutcome = GatePosted
	}

	plan := g.Result.Comment(in.Config)
	marker := PolicyMarker()
	switch {
	case plan.Want:
		err = forge.UpsertComment(ctx, in.PR, marker, plan.Body)
		g.CommentOutcome = GatePosted
	default:
		err = forge.DeleteComment(ctx, in.PR, marker)
		g.CommentOutcome = GateDeleted
	}
	switch {
	case errors.Is(err, core.ErrPermission):
		g.ReadOnly, g.CommentOutcome = true, GateReadOnly
	case errors.Is(err, core.ErrNotFound) && !plan.Want:
		g.CommentOutcome = GateNone
	case err != nil:
		return core.Explain(err, "write the gate's comment on #%d", in.PR)
	}
	return nil
}

// statusAlreadySaid reports whether one of the statuses already on the commit
// is flow's, and says exactly what flow is about to say (spec 09 §2).
func statusAlreadySaid(latest []core.CommitStatus, want core.CommitStatus) bool {
	for _, status := range latest {
		if status.Context == want.Context {
			return StatusUnchanged(status, want)
		}
	}
	return false
}

// move performs the flow-owned transitions this pull request implies, for each
// ticket it is linked to (spec 09 §3, spec 04 §5).
//
// A read-only credential skips the moves for the same reason it skipped the
// status: it would be refused. A dry run decides them and reports them without
// acting, which is what makes --dry-run worth running.
func (g *Gate) move(ctx context.Context, in GateInput, snapshot PolicySnapshot) error {
	if in.NoTransitions || len(snapshot.Tickets) == 0 {
		return nil
	}
	linked, err := g.linkedPRs(ctx, in, snapshot)
	if err != nil {
		return err
	}
	for _, ticket := range snapshot.Tickets {
		move := Transition(TransitionSnapshot{
			Ticket: ticket.Ticket,
			Column: ticket.Column,
			PRs:    prsFor(in.Adapters.Tracker, linked, ticket.Ticket.Key),
			Config: in.Config,
		})
		g.Moves = append(g.Moves, move)
		if !move.Moves() || in.DryRun || g.ReadOnly {
			continue
		}
		to, found := columnTarget(in.Config, move.To)
		if !found {
			continue
		}
		if err := in.Adapters.Tracker.Move(ctx, ticket.Ticket.Key, to); err != nil {
			if errors.Is(err, core.ErrPermission) {
				g.ReadOnly = true
				continue
			}
			return core.Explain(err, "move %s to %s", ticket.Ticket.Key, move.To)
		}
	}
	return nil
}

// linkedPRs is every pull request that might have a say about one of these
// tickets: the open ones, and the one under judgement.
//
// Spec 04 §5 rule 4 wants all of a ticket's work considered, and open work is
// what a ticket is still waiting on. A merged or abandoned pull request is
// not: the gate never runs on a closed pull request (spec 09 §1 lists the
// events), the merge is `check deploy`'s to react to, and reading every
// pull request the repository ever had to answer a question about one would
// put an unbounded walk in a CI job with a two-minute deadline.
func (g *Gate) linkedPRs(ctx context.Context, in GateInput, snapshot PolicySnapshot) ([]LinkedPR, error) {
	open, err := in.Adapters.Forge.PullRequests(ctx, core.PRQuery{State: core.PRStateOpen})
	if err != nil {
		return nil, core.Explain(err, "read the open pull requests")
	}
	required := g.requiredApprovals(ctx, in, snapshot.PR.BaseRef)
	linked := []LinkedPR{{PR: snapshot.PR, RequiredApprovals: required}}
	for _, pr := range open {
		if pr.Number == snapshot.PR.Number {
			continue
		}
		linked = append(linked, LinkedPR{PR: pr, RequiredApprovals: required})
	}
	return linked, nil
}

// requiredApprovals is how many reviews the base branch demands, which is what
// makes the `approved` event true. A branch flow cannot read protection for
// answers zero: the pull request's own approvals are then what counts, which
// is the reading that does not invent a requirement nobody configured.
func (g *Gate) requiredApprovals(ctx context.Context, in GateInput, branch string) int {
	if branch == "" {
		return 0
	}
	protection, err := in.Adapters.Forge.Protection(ctx, branch)
	if err != nil {
		return 0
	}
	return protection.RequiredApprovals
}

// prsFor is the pull requests linked to one ticket.
func prsFor(tracker core.Tracker, linked []LinkedPR, key string) []LinkedPR {
	var out []LinkedPR
	for _, candidate := range linked {
		for _, linkedKey := range tracker.LinkedKeys(candidate.PR) {
			if sameTicket(linkedKey, key) {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}

// columnTarget is the tracker's identifier for a configured column, which is
// the name the team gave it: the tracker's own id belongs to the tracker, and
// a column flow has never created has no id flow could know.
func columnTarget(cfg *config.Config, id string) (core.ColumnID, bool) {
	column, found := cfg.Workflow.Column(id)
	if !found {
		return "", false
	}
	return core.ColumnID(column.Name), true
}

// gateDoc is the --json document, schema flow.policy/v1.
type gateDoc struct {
	Schema      string       `json:"schema"`
	PR          int          `json:"pr"`
	SHA         string       `json:"sha"`
	Draft       bool         `json:"draft"`
	State       string       `json:"state"`
	Description string       `json:"description"`
	Pass        bool         `json:"pass"`
	Findings    []Finding    `json:"findings"`
	Status      string       `json:"status"`
	Comment     string       `json:"comment"`
	ReadOnly    bool         `json:"read_only,omitempty"`
	Moves       []TicketMove `json:"moves,omitempty"`
}

// JSON renders the gate's single document (spec 02 §3).
func (g Gate) JSON() ([]byte, error) {
	doc := gateDoc{
		Schema:      GateVersion,
		PR:          g.Result.PR,
		SHA:         g.Result.SHA,
		Draft:       g.Result.Draft,
		State:       g.Status.State,
		Description: g.Status.Description,
		Pass:        g.Pass(),
		Findings:    g.Result.Findings,
		Status:      g.StatusOutcome,
		Comment:     g.CommentOutcome,
		ReadOnly:    g.ReadOnly,
		Moves:       g.Moves,
	}
	if doc.Findings == nil {
		doc.Findings = []Finding{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the policy result: %w", err)
	}
	return append(data, '\n'), nil
}

// Text renders the run for a terminal: the verdict, then what became of it.
func (g Gate) Text() string {
	var out strings.Builder
	out.WriteString(g.Result.Text())
	for _, move := range g.Moves {
		switch {
		case move.Warning != "":
			fmt.Fprintf(&out, "· %s  %s\n", move.Key, move.Warning)
		case move.Moves():
			fmt.Fprintf(&out, "→ %s  %s\n", move.Key, moveText(move))
		}
	}
	if line := publishedText(g); line != "" {
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// moveText says where a ticket is going and what asked for it.
func moveText(move TicketMove) string {
	if move.From == "" {
		return fmt.Sprintf("%s (%s)", move.To, move.Event)
	}
	return fmt.Sprintf("%s → %s (%s)", move.From, move.To, move.Event)
}

// publishedText is the one line about what the gate wrote, and nothing at all
// when it wrote what it meant to.
func publishedText(g Gate) string {
	switch {
	case g.ReadOnly:
		return "status not posted: this credential may not write here"
	case g.StatusOutcome == GateDryRun:
		return "dry run: nothing was posted"
	default:
		return ""
	}
}
