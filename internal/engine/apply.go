package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Apply (spec 06 §3–4) carries out the native items of a plan, in the order
// the plan put them, and records what it did in the lock as it goes.
//
// Everything here is written on the assumption that it will be interrupted:
// a credential expires, a provider rate-limits, a laptop closes. The lock is
// written after every successful mutation rather than at the end, a failure
// stops only the items that depend on it, and nothing is ever inferred about
// a change flow did not see succeed.

// ErrStale is what [Stale] reports when a saved plan is no longer a
// description of the world (spec 06 §3).
var ErrStale = errors.New("the plan is out of date")

// Stale checks a plan that was loaded from a file against one made now, and
// refuses the first difference it finds. `flow apply --plan` exits 1 on this
// rather than applying a change nobody approved (spec 02 §2, spec 06 §3).
func Stale(loaded, fresh Plan) error {
	if why, ok := loaded.StaleAgainst(fresh); ok {
		return fmt.Errorf("%w: %s", ErrStale, why)
	}
	return nil
}

// Mutations are the changes apply would actually attempt: the ones belonging
// to items the plan has flow making itself.
//
// A compensated, attested or unsupported item still lists what would have to
// happen (spec 06 §2) because a human reading the plan is owed it. Counting
// those as work would mean asking somebody to approve changes flow is not
// going to make, and reporting an apply as having had something to do when it
// did not.
func (p Plan) Mutations() []core.Change {
	var changes []core.Change
	for _, item := range p.Items {
		if item.Outcome == OutcomeNative {
			changes = append(changes, item.Changes...)
		}
	}
	return changes
}

// ApplyInput is a plan and the world to carry it out against.
type ApplyInput struct {
	// Plan is what was decided, and approved.
	Plan Plan
	// Config is the team's own configuration. Apply reads it only for the
	// conventions a pull request flow opens itself has to keep to: what is
	// made is decided by the plan, which was made from the configuration
	// already.
	Config *config.Config
	// Desired is the state the plan was made from. The changes say what to
	// make; the desired item says what the whole thing should be, which is
	// what a forge setting is written as.
	Desired Desired
	// Adapters are the providers to act on.
	Adapters Adapters
	// Lock is what flow has learned, and is updated as the work lands.
	Lock *config.Lock
	// Clock is where the times written to the lock come from.
	Clock core.Clock
	// Prune permits removing what the configuration no longer asks for.
	// Without it a removal is reported and not made (spec 02 §2).
	Prune bool
	// Save writes the lock where it belongs, atomically. It is called after
	// every successful mutation and once at the end. A nil Save keeps the
	// lock in memory, which is what a dry run wants.
	Save func(*config.Lock) error
	// Log receives one line per mutating call (spec 06 §4). Nil discards.
	Log *slog.Logger
}

// Applied is what became of a plan.
type Applied struct {
	// FlowVersion is the build that carried it out, and AppliedAt when.
	FlowVersion string
	AppliedAt   time.Time
	// Items is one entry per planned item, in the order they were attempted.
	Items []AppliedItem
	// FollowUps are the tickets the run filed, nudged or closed for the work
	// it could not do itself (spec 06 §3). They are set by [ApplyFollowUps],
	// which the root calls once the providers have been dealt with.
	FollowUps []FollowUpOutcome
}

// AppliedItem is what became of one planned item.
type AppliedItem struct {
	// ID is the item's id.
	ID string
	// Outcome is what the plan said flow would do about it, unless a
	// provider retreated from a change it could only make unsafely: a
	// native item every change of which became somebody's follow-up is a
	// follow_up item, whatever the plan hoped for (spec 06 §4).
	Outcome Outcome
	// Changes is what became of each change the plan listed.
	Changes []AppliedChange
	// Skipped says why nothing was attempted, empty when something was.
	Skipped string
	// BlockedBy names the item whose failure stopped this one.
	BlockedBy string
	// Err is why the item failed, nil when it did not.
	Err error
}

// Done reports whether the item was carried out in full.
func (i AppliedItem) Done() bool { return i.Err == nil && i.Skipped == "" }

// AppliedChange is what became of one change.
type AppliedChange struct {
	// Change is what was to be made.
	Change core.Change
	// Note is what the provider said it did.
	Note string
	// Refused says why flow declined to make it: a removal without --prune,
	// an option somebody is using, something flow did not create.
	Refused string
	// FollowUp says the provider would not make it and a human must, with
	// the reason. It is a refusal by the provider rather than by flow, and
	// it is what turns the item's outcome into follow_up.
	FollowUp string
	// Err is why the provider refused or failed.
	Err error
}

// Done reports whether the change was made.
func (c AppliedChange) Done() bool { return c.Err == nil && c.Refused == "" && c.FollowUp == "" }

// Counts of what happened, for the summary a human reads.
func (a Applied) Counts() (done, skipped, failed int) {
	for _, item := range a.Items {
		switch {
		case item.Err != nil:
			failed++
		case item.Skipped != "":
			skipped++
		default:
			done++
		}
	}
	return done, skipped, failed
}

// Err returns the first failure, or nil when everything that was attempted
// worked. The caller still has the whole [Applied] to report from.
func (a Applied) Err() error {
	for _, item := range a.Items {
		if item.Err != nil {
			return fmt.Errorf("%s: %w", item.ID, item.Err)
		}
	}
	return nil
}

// Apply carries out a plan.
//
// It returns an error only when the run could not be started at all. Anything
// that goes wrong while carrying it out belongs to the item it went wrong on:
// a plan is many independent pieces of work, and one provider refusing is not
// a reason to abandon the rest.
func Apply(ctx context.Context, in ApplyInput) (Applied, error) {
	if in.Lock == nil {
		return Applied{}, fmt.Errorf("apply: no lock was supplied")
	}
	if in.Clock == nil {
		return Applied{}, fmt.Errorf("apply: no clock was supplied")
	}
	if in.Log == nil {
		in.Log = slog.New(slog.DiscardHandler)
	}

	runner := &applier{in: in}
	if err := runner.writable(ctx); err != nil {
		return Applied{}, err
	}
	applied := Applied{
		FlowVersion: in.Plan.FlowVersion,
		AppliedAt:   in.Clock.Now().UTC().Truncate(time.Second),
	}
	for _, item := range in.Plan.Items {
		applied.Items = append(applied.Items, runner.item(ctx, item))
	}
	if err := runner.save(); err != nil {
		return applied, fmt.Errorf("apply: write the lock: %w", err)
	}
	return applied, nil
}

// applier carries the state one pass accumulates: which items failed, so that
// their dependants are not attempted, and the repository, which every forge
// setting is about and whose trunk they all name.
type applier struct {
	in      ApplyInput
	failed  map[string]string
	repo    *core.RepoInfo
	repoErr error
}

// writable refuses a run against a repository that takes no writes.
//
// An archived repository is read-only on every forge that has the feature, so
// without this each forge item fails with whatever its endpoint said, one at
// a time, when the whole truth is one sentence. Refusing before the first
// item is what makes it one sentence, and it is here rather than in each
// adapter so that Gitea and GitLab inherit it with the flag (spec 10 §2).
//
// The tracker items go with it. A board is configured for work in a
// repository, and nobody is working in a repository nobody can write to; an
// apply that reconciled half the world would also leave a lock saying it had.
//
// A repository flow could not read is not a repository that refused. The read
// error is kept rather than raised, so a forge that was rate-limited fails
// the items that need it, one by one with the reason, and a plan with nothing
// of the forge in it is not stopped by a question it never had to ask.
func (a *applier) writable(ctx context.Context) error {
	repo, err := a.repoInfo(ctx)
	if err != nil || !repo.Archived {
		return nil
	}
	return core.Errorf(core.ErrConflict, "apply the plan to %s", repo.Slug()).
		WithWhy("the repository is archived, and an archived repository takes no writes").
		WithFix("unarchive %s, or point flow at the repository the work moved to", repo.Slug())
}

// repoInfo reads the repository once, failure included.
//
// Apply asks whether it may write at all before it asks what the trunk is
// called, and those are the same question of the same endpoint: asking twice
// would spend two calls to learn one thing, and could be answered differently
// each time.
func (a *applier) repoInfo(ctx context.Context) (core.RepoInfo, error) {
	if a.repo != nil || a.repoErr != nil {
		if a.repoErr != nil {
			return core.RepoInfo{}, a.repoErr
		}
		return *a.repo, nil
	}
	if a.in.Adapters.Forge == nil {
		a.repoErr = fmt.Errorf("%w: no forge", core.ErrUnsupported)
		return core.RepoInfo{}, a.repoErr
	}
	repo, err := a.in.Adapters.Forge.Repo(ctx)
	if err != nil {
		a.repoErr = err
		return core.RepoInfo{}, err
	}
	a.repo = &repo
	return repo, nil
}

// item carries out one planned item.
func (a *applier) item(ctx context.Context, planned PlanItem) AppliedItem {
	applied := AppliedItem{ID: planned.ID, Outcome: planned.Outcome}
	switch {
	case planned.Outcome != OutcomeNative:
		applied.Skipped = "the plan does not have flow making this change: " + string(planned.Outcome)
		return applied
	case len(planned.Changes) == 0:
		applied.Skipped = "there is nothing to change"
		return applied
	}
	if blocker, ok := a.blocked(planned); ok {
		applied.Skipped = "the item it depends on did not succeed"
		applied.BlockedBy = blocker
		a.fail(planned.ID, blocker)
		return applied
	}

	wanted, ok := a.in.Desired.Find(planned.ID)
	if !ok {
		applied.Err = fmt.Errorf("the desired state the plan was made from no longer has %s", planned.ID)
		a.fail(planned.ID, planned.ID)
		return applied
	}

	switch planned.Target {
	case TargetTracker:
		applied.Changes = a.tracker(ctx, planned, wanted)
	case TargetForge:
		applied.Changes = a.forge(ctx, planned, wanted)
	default:
		applied.Skipped = "a deployment is somebody else's event: flow only reads them"
		return applied
	}

	for _, change := range applied.Changes {
		if change.Err != nil {
			applied.Err = change.Err
			a.fail(planned.ID, planned.ID)
			break
		}
		// A provider that retreated from a change owes it to a human,
		// and an item one of whose changes is owed to a human is not
		// an item flow carried out natively.
		if change.FollowUp != "" {
			applied.Outcome = OutcomeFollowUp
		}
	}
	return applied
}

// blocked reports the item whose failure stops this one. Because the plan is
// in dependency order and a blocked item is itself recorded as failed, this
// covers a chain of any length.
func (a *applier) blocked(planned PlanItem) (string, bool) {
	for _, needed := range planned.DependsOn {
		if blocker, ok := a.failed[needed]; ok {
			return blocker, true
		}
	}
	return "", false
}

// fail records that an item did not succeed, naming the original cause so
// that everything downstream can point at the same thing.
func (a *applier) fail(id, because string) {
	if a.failed == nil {
		a.failed = map[string]string{}
	}
	a.failed[id] = because
}

// tracker makes each change through the tracker, one at a time, recording
// every one that lands before attempting the next.
func (a *applier) tracker(ctx context.Context, planned PlanItem, wanted DesiredItem) []AppliedChange {
	if a.in.Adapters.Tracker == nil {
		return failAll(planned.Changes, fmt.Errorf("%w: flow was given no tracker", core.ErrUnsupported))
	}
	applied := make([]AppliedChange, 0, len(planned.Changes))
	for _, change := range planned.Changes {
		if why := a.refuse(ctx, wanted, change); why != "" {
			applied = append(applied, AppliedChange{Change: change, Refused: why})
			continue
		}
		result, err := a.in.Adapters.Tracker.Apply(ctx, change)
		if err != nil {
			a.log(planned.ID, change.Op, change.Item.Name, "", err)
			return append(applied, AppliedChange{Change: change, Err: err})
		}
		// A retreat is not a mutation: nothing was written, so there is
		// nothing to record in the lock and nothing to save.
		if result.FollowUp != "" {
			a.in.Log.Info("the provider will not make the change",
				"item", planned.ID, "op", change.Op, "name", change.Item.Name, "why", result.FollowUp)
			applied = append(applied, AppliedChange{Change: change, FollowUp: result.FollowUp})
			continue
		}
		a.log(planned.ID, change.Op, change.Item.Name, result.Item.ID, nil)
		a.record(wanted, change, result.Item)
		applied = append(applied, AppliedChange{Change: change, Note: result.Note})
		if err := a.save(); err != nil {
			return append(applied, AppliedChange{Change: change, Err: err})
		}
	}
	return applied
}

// forge makes a whole setting at once. A repository setting is not a list of
// mutations: protection, merge strategies and the required checks are each
// one value, written as a whole and merged additively by the adapter (spec 06
// §4), and the proposed files are one pull request however many of them are
// missing. The changes the plan listed are what that one call accounts for,
// so they all stand or fall together.
func (a *applier) forge(ctx context.Context, planned PlanItem, wanted DesiredItem) []AppliedChange {
	if a.in.Adapters.Forge == nil {
		return failAll(planned.Changes, fmt.Errorf("%w: flow was given no forge", core.ErrUnsupported))
	}
	for _, change := range planned.Changes {
		if why := a.refuse(ctx, wanted, change); why != "" {
			return refusedAll(planned.Changes, why)
		}
	}

	note, err := a.writeForge(ctx, planned, wanted)
	if err != nil {
		a.in.Log.Error("flow could not write the setting", "item", planned.ID, "op", "set", "error", err)
		return failAll(planned.Changes, err)
	}
	a.in.Log.Info("flow wrote the setting", "item", planned.ID, "op", "set", "note", note)
	for _, change := range planned.Changes {
		a.record(wanted, change, change.Item)
	}
	if err := a.save(); err != nil {
		return failAll(planned.Changes, err)
	}
	return noteAll(planned.Changes, note)
}

// writeForge is the one call that makes a forge item true.
func (a *applier) writeForge(ctx context.Context, planned PlanItem, wanted DesiredItem) (string, error) {
	switch planned.ID {
	case core.ItemForgeProtection, core.ItemForgeRequiredStatus:
		return a.protect(ctx)

	case core.ItemForgeMergeSettings, core.ItemForgeSquashText:
		return a.mergeSettings(ctx)

	case core.ItemForgeFiles:
		return a.propose(ctx, planned, wanted)
	}
	return "", fmt.Errorf("%w: nothing applies %s", core.ErrUnsupported, planned.ID)
}

// protect writes the trunk's protection, required checks and all.
//
// Protection and the required checks are two desired items but one setting at
// the forge, so both are written from the whole of the desired state rather
// than from the item that happened to differ. Writing half of it — the
// required contexts with no protection flags, or the flags with no contexts —
// would be sending the forge a value nobody asked for. The adapter merges it
// onto what the branch already has (spec 06 §4), so the team's own rules
// survive.
func (a *applier) protect(ctx context.Context) (string, error) {
	branch, err := a.trunk(ctx)
	if err != nil {
		return "", err
	}
	var protection core.Item
	if wanted, ok := a.in.Desired.Find(core.ItemForgeProtection); ok && len(wanted.Items) > 0 {
		protection = wanted.Items[0]
	}
	var checks []core.Item
	if wanted, ok := a.in.Desired.Find(core.ItemForgeRequiredStatus); ok {
		checks = wanted.Items
	}
	want := ProtectionFor(branch, protection, checks)
	if err := a.in.Adapters.Forge.SetProtection(ctx, branch, want); err != nil {
		return "", err
	}
	if len(want.RequiredChecks) == 0 {
		return "protected " + branch, nil
	}
	return "protected " + branch + ", requiring " + strings.Join(want.RequiredChecks, ", "), nil
}

// mergeSettings writes the repository's one merge setting: the strategies it
// allows and where a squash commit takes its words from. They are two desired
// items and two capabilities, so either may be the one that brought us here,
// and both are written together — a forge told only which strategies to allow
// would read the rest of the call as "take the squash title from the first
// commit" and quietly undo the other half.
//
// The same is true of the fields no desired item names at all, which is why
// the repository is read first and written back over. Reading it costs
// nothing here: apply has already asked whether the repository takes writes,
// and that answer is kept.
func (a *applier) mergeSettings(ctx context.Context) (string, error) {
	var strategies, text core.Item
	wanted, ok := a.in.Desired.Find(core.ItemForgeMergeSettings)
	if !ok || len(wanted.Items) == 0 {
		return "", fmt.Errorf("the desired merge settings have nothing in them")
	}
	strategies = wanted.Items[0]
	if squash, ok := a.in.Desired.Find(core.ItemForgeSquashText); ok && len(squash.Items) > 0 {
		text = squash.Items[0]
	}
	repo, err := a.repoInfo(ctx)
	if err != nil {
		return "", err
	}
	if err := a.in.Adapters.Forge.SetMergeSettings(ctx, MergeSettingsFor(repo.Merge, strategies, text)); err != nil {
		return "", err
	}
	if text.Field(FieldSquashTitle) == "" {
		return "set the merge strategies", nil
	}
	return "set the merge strategies, and where a squash takes its words from", nil
}

// propose offers the files the plan is short of as a pull request. Only the
// files the plan named are offered: a file that is already right is left
// exactly as the team wrote it, because the check is that the workflow runs
// flow's commands and not that the bytes match what flow ships (spec 06 §1).
func (a *applier) propose(ctx context.Context, planned PlanItem, wanted DesiredItem) (string, error) {
	var files []core.ProposedFile
	for _, change := range planned.Changes {
		at := slices.IndexFunc(wanted.Files, func(f core.ProposedFile) bool { return f.Path == change.Item.Name })
		if at < 0 {
			return "", fmt.Errorf("flow has no content for %s", change.Item.Name)
		}
		files = append(files, wanted.Files[at])
	}
	if len(files) == 0 {
		return "", fmt.Errorf("there is nothing to propose")
	}

	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	// Judged by the linter the gate will judge it with, against this team's
	// own convention: a pull request flow's own gate refuses is worse than
	// no pull request (issue #152).
	title, err := FilesProposal.Title(a.in.Config)
	if err != nil {
		return "", err
	}
	pr, err := a.in.Adapters.Forge.ProposeFiles(ctx, core.FileProposal{
		ID:    FilesProposal.ID,
		Title: title,
		Body:  proposalBody(paths),
		Files: files,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("proposed %s in #%d", strings.Join(paths, ", "), pr.Number), nil
}

// proposalBody explains the pull request to whoever has to approve it.
func proposalBody(paths []string) string {
	var body strings.Builder
	body.WriteString("flow proposes these files so that the workflow in `.flow.yaml` can be enforced:\n\n")
	for _, path := range paths {
		body.WriteString("- `" + path + "`\n")
	}
	body.WriteString("\nThey are a starting point: edit them freely. ")
	body.WriteString("flow checks that the workflow runs its commands, not that the file matches what it shipped.\n")
	return body.String()
}

// refuse reports why flow will not make a change, empty when it will.
//
// Everything here is about removal, because removal is the only thing apply
// does that cannot be undone by running it again. A creation that was wrong
// is a line in the configuration; a column that was deleted takes every
// ticket's status with it.
func (a *applier) refuse(ctx context.Context, wanted DesiredItem, change core.Change) string {
	if change.Op != core.ChangeDelete {
		return ""
	}
	if !a.in.Prune {
		return "flow keeps what it did not ask for: --prune is what removes it"
	}
	if wanted.Additive && !created(a.in.Lock, wanted.ID, change.Item) {
		return "the lock does not record flow as having created it"
	}
	if users := a.inUse(ctx, wanted, change.Item); users > 0 {
		return fmt.Sprintf("%s is in use by %d %s", change.Item.Name, users, plural(users, "ticket"))
	}
	return ""
}

// inUse counts the tickets that would lose their status if this option went.
//
// This is spec 06 §4's refusal, and it is deliberately a question flow asks
// the tracker rather than one it answers from the lock: the whole hazard is
// that somebody put work in a column flow does not know about.
func (a *applier) inUse(ctx context.Context, wanted DesiredItem, item core.Item) int {
	if wanted.ID != core.ItemTrackerStatusOptions || a.in.Adapters.Tracker == nil {
		return 0
	}
	tickets, err := a.in.Adapters.Tracker.Tickets(ctx, core.TicketQuery{})
	if err != nil {
		// Not being able to look is not permission to delete.
		return 1
	}
	users := 0
	for _, ticket := range tickets {
		if ticket.Status.Name == item.Name || (item.ID != "" && ticket.Status.ID == item.ID) {
			users++
		}
	}
	return users
}

// record writes what just happened into the lock: what flow created, so that
// §4 lets it be removed later, and what flow no longer created, so that a
// second apply does not offer to delete something that is already gone.
func (a *applier) record(wanted DesiredItem, change core.Change, item core.Item) {
	kind := CreationKind(wanted.ID)
	switch change.Op {
	case core.ChangeCreate:
		a.in.Lock.Record(config.Creation{
			Kind: kind,
			ID:   item.ID,
			Name: item.Name,
			At:   a.in.Clock.Now().UTC(),
		})
	case core.ChangeDelete:
		a.in.Lock.Created = slices.DeleteFunc(a.in.Lock.Created, func(creation config.Creation) bool {
			return creation.Kind == kind && sameThing(creation, item)
		})
	}
}

// trunk is the default branch of the repository read once above.
func (a *applier) trunk(ctx context.Context) (string, error) {
	repo, err := a.repoInfo(ctx)
	if err != nil {
		return "", err
	}
	if repo.DefaultBranch == "" {
		return "", fmt.Errorf("%w: the repository has no default branch", core.ErrNotFound)
	}
	return repo.DefaultBranch, nil
}

// save writes the lock where the caller said to put it. It is called after
// every mutation that lands, so that an apply which stops half way has told
// the truth about the half that happened.
func (a *applier) save() error {
	if a.in.Save == nil {
		return nil
	}
	a.in.Lock.FlowVersion = a.in.Plan.FlowVersion
	if a.in.Plan.ConfigHash != "" {
		a.in.Lock.ConfigHash = a.in.Plan.ConfigHash
	}
	return a.in.Save(a.in.Lock)
}

// log records one mutating call to the tracker (spec 06 §4).
func (a *applier) log(id, op, subject, resulting string, err error) {
	attrs := []any{"item", id, "op", op, "name", subject}
	if resulting != "" {
		attrs = append(attrs, "id", resulting)
	}
	if err != nil {
		a.in.Log.Error("flow could not make the change", append(attrs, "error", err)...)
		return
	}
	a.in.Log.Info("flow made the change", attrs...)
}

// The three ways a set of changes can share one answer.

func failAll(changes []core.Change, err error) []AppliedChange {
	out := make([]AppliedChange, 0, len(changes))
	for _, change := range changes {
		out = append(out, AppliedChange{Change: change, Err: err})
	}
	return out
}

func refusedAll(changes []core.Change, why string) []AppliedChange {
	out := make([]AppliedChange, 0, len(changes))
	for _, change := range changes {
		out = append(out, AppliedChange{Change: change, Refused: why})
	}
	return out
}

func noteAll(changes []core.Change, note string) []AppliedChange {
	out := make([]AppliedChange, 0, len(changes))
	for _, change := range changes {
		out = append(out, AppliedChange{Change: change, Note: note})
	}
	return out
}

// plural is the English of a count, for a refusal a person reads.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
