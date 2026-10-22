package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// What `flow check deploy` does about one deployment (spec 02 §2, spec 04 §6,
// spec 13).
//
// A deployment is the one event in the workflow that is not about a pull
// request. Under `done_on: deployed` it is what finishes the work: the tickets
// whose merges are running in the environment belong in a done column, and
// nothing else will put them there. Under `done_on: merged` it is not a status
// at all — doctor proves the lag instead — so this is a no-op, and the no-op is
// the common case, which is why it costs neither a credential nor a request.
//
// As everywhere else, the decision is the transition engine's and is taken from
// state. Gathering that state is this file's work, and it is the only part that
// talks to a provider.

// DeployVersion is the schema of check deploy's --json document.
const DeployVersion = "flow.deploy/v1"

// DeployInput is everything one run of `flow check deploy` needs.
type DeployInput struct {
	// SHA is the application commit the environment is running.
	SHA string
	// Environment is where it was deployed. Empty means the configured one.
	Environment string
	// Config is the workflow being enforced.
	Config *config.Config
	// Lock is what flow created before, which is how a renamed board column
	// is still recognised.
	Lock *config.Lock
	// Adapters are the providers to read and write through.
	Adapters Adapters
	// Ancestry answers whether a merge is contained in the deployed commit.
	// Nil asks the forge directly, which is the same answer without the
	// cache in front of it (spec 13 §3).
	Ancestry core.Comparer
	// Now is the moment the window is measured back from.
	Now time.Time
	// DryRun decides and reports, and moves nothing.
	DryRun bool
}

// Deploy is what one run decided and did.
type Deploy struct {
	// SHA and Environment are the deployment this run was about.
	SHA         string
	Environment string
	// Watched reports a workflow that tracks deployments at all.
	Watched bool
	// Confirmed reports a successful record for this commit and
	// environment, which is the evidence the moves rest on.
	Confirmed bool
	// Why says why nothing was done, and is empty when something was.
	Why string
	// PRs are the merged pull requests this deployment is running.
	PRs []DeployedPR
	// Moves are the ticket transitions it implies, in the order the tickets
	// were linked.
	Moves []TicketMove
	// DryRun reports that the moves were decided and not made.
	DryRun bool
}

// DeployedPR is one merged pull request the deployed commit contains.
type DeployedPR struct {
	// Number is the pull request.
	Number int `json:"pr"`
	// MergeSHA is the commit its merge produced, which is what was proven to
	// be contained in the deployed commit.
	MergeSHA string `json:"merge_sha,omitempty"`
}

// Acted reports whether this run had anything to do.
func (d Deploy) Acted() bool { return d.Why == "" }

// WatchesDeployments reports whether this workflow treats a deployment as a
// status, and says why not when it does not.
//
// It is separate from the run because it is the whole answer for most
// repositories, and answering it needs no credential: `done_on: merged` is the
// default (spec 04 §6), and a CI job that asked for a token only to discover it
// had nothing to do would make every repository pay for a feature it had turned
// off.
func WatchesDeployments(cfg *config.Config) (bool, string) {
	if cfg == nil || cfg.Workflow.DoneOn != config.DoneOnDeployed {
		return false, fmt.Sprintf("workflow.done_on is %s, so a deployment is verified as lag rather than tracked as status", config.DoneOnMerged)
	}
	return true, ""
}

// RunDeploy reacts to one deployment: it confirms the record, works out which
// merges the environment is now running, and performs the flow-owned
// transitions those merges imply (spec 04 §6).
func RunDeploy(ctx context.Context, in DeployInput) (Deploy, error) {
	if in.Config == nil {
		return Deploy{}, core.Errorf(core.ErrValidation, "react to a deployment").
			WithWhy("there is no configuration to act on").
			WithFix("run flow init first")
	}
	deploy := Deploy{SHA: in.SHA, Environment: in.Environment, DryRun: in.DryRun}
	if deploy.Environment == "" {
		deploy.Environment = in.Config.Deploys.Environment
	}

	watched, why := WatchesDeployments(in.Config)
	deploy.Watched = watched
	if !watched {
		deploy.Why = why
		return deploy, nil
	}
	// A repository deploys to more environments than the one the workflow is
	// about, and a review app reaching staging says nothing about whether the
	// work is finished.
	//
	// The name arrives from whatever started the run and the configured one
	// was typed by a person, so the two are compared without regard to case
	// and the configured spelling is then the one flow uses: having decided
	// which environment this is, it asks about it and reports it by the name
	// the workflow calls it.
	if !strings.EqualFold(deploy.Environment, in.Config.Deploys.Environment) {
		deploy.Why = fmt.Sprintf("this deployment went to %s, and the workflow watches %s", deploy.Environment, in.Config.Deploys.Environment)
		return deploy, nil
	}
	deploy.Environment = in.Config.Deploys.Environment
	if deploy.SHA == "" {
		return deploy, core.Errorf(core.ErrNeedsInput, "find the commit that was deployed").
			WithWhy("no --sha was given and no CI event payload names one").
			WithFix("pass --sha with the commit the environment is running")
	}
	if in.Adapters.Forge == nil || in.Adapters.Tracker == nil || in.Adapters.Deploys == nil {
		return deploy, core.Errorf(core.ErrValidation, "react to a deployment").
			WithWhy("it needs a forge, a tracker and a deployment source, and this repository has not got all three").
			WithFix("check forge, tracker and deploys.source in .flow.yaml")
	}

	confirmed, err := confirmDeployment(ctx, in, deploy)
	if err != nil {
		return deploy, err
	}
	deploy.Confirmed = confirmed
	if !confirmed {
		deploy.Why = fmt.Sprintf("no successful deployment of %s to %s is recorded", shortSHA(deploy.SHA), deploy.Environment)
		return deploy, nil
	}

	linked, err := deployLinkedPRs(ctx, in, &deploy)
	if err != nil {
		return deploy, err
	}
	if len(deploy.PRs) == 0 {
		deploy.Why = fmt.Sprintf("%s contains no merge the board is waiting on", shortSHA(deploy.SHA))
		return deploy, nil
	}
	if err := deployMove(ctx, in, &deploy, linked); err != nil {
		return deploy, err
	}
	return deploy, nil
}

// confirmDeployment asks the deployment source whether the environment really
// is running that commit.
//
// The event payload already claims it, and `--sha` claims it with nothing
// behind it at all. Spec 13 §1 makes the record the contract, so the record is
// what flow reads: it is the same question whether a webhook or a person
// started the run, a failed or half-finished deployment answers no, and the
// board is never moved on a claim flow did not check.
func confirmDeployment(ctx context.Context, in DeployInput, deploy Deploy) (bool, error) {
	records, err := in.Adapters.Deploys.Deployments(ctx, core.DeployQuery{
		Environment: deploy.Environment,
		SHA:         deploy.SHA,
	})
	if err != nil {
		return false, core.Explain(err, "read the deployments of %s", deploy.Environment)
	}
	for _, record := range records {
		if record.Succeeded() {
			return true, nil
		}
	}
	return false, nil
}

// deployLinkedPRs is every pull request that has a say about the tickets this
// deployment finishes, and fills in the ones it is running.
//
// Two walks: the merges that might be contained in the deployed commit, and the
// work still open. The open ones are not candidates — they have not merged — but
// spec 04 §5 rule 4 says a ticket is done when all of its work is, so a ticket
// with a second pull request still open must be seen to have one.
func deployLinkedPRs(ctx context.Context, in DeployInput, deploy *Deploy) ([]LinkedPR, error) {
	merged, err := in.Adapters.Forge.PullRequests(ctx, core.PRQuery{
		State:        core.PRStateMerged,
		UpdatedSince: deployWindow(in.Config, in.Now),
	})
	if err != nil {
		return nil, core.Explain(err, "read the merged pull requests")
	}
	merged, err = detailed(ctx, in.Adapters.Forge, merged)
	if err != nil {
		return nil, err
	}
	comparer := in.Ancestry
	if comparer == nil {
		comparer = in.Adapters.Forge
	}

	var linked []LinkedPR
	for _, pr := range merged {
		running, err := deployedWith(ctx, comparer, pr, deploy.SHA)
		if err != nil {
			return nil, err
		}
		linked = append(linked, LinkedPR{PR: pr, Deployed: running})
		if running {
			deploy.PRs = append(deploy.PRs, DeployedPR{Number: pr.Number, MergeSHA: pr.MergeSHA})
		}
	}

	open, err := in.Adapters.Forge.PullRequests(ctx, core.PRQuery{State: core.PRStateOpen})
	if err != nil {
		return nil, core.Explain(err, "read the open pull requests")
	}
	open, err = detailed(ctx, in.Adapters.Forge, open)
	if err != nil {
		return nil, err
	}
	for _, pr := range open {
		linked = append(linked, LinkedPR{PR: pr})
	}
	return linked, nil
}

// deployWindow is how far back the walk for merges goes: the lag the team
// agreed to in `deploys.max_lag`.
//
// A merge that took longer than its own objective to reach the environment is
// something doctor reports and catches up (spec 07 §6), not something a CI job
// reacting to one deployment should walk the repository's whole history to
// find. A workflow that set no objective gets no bound, because flow has no
// number of its own to put there.
func deployWindow(cfg *config.Config, now time.Time) time.Time {
	lag := cfg.Deploys.MaxLag.Std()
	if lag <= 0 || now.IsZero() {
		return time.Time{}
	}
	return now.Add(-lag)
}

// deployedWith reports whether a merge is contained in the deployed commit:
// the deployed commit is the merge, or came after it (spec 13 §3). A pull
// request the forge merged without saying what the merge produced cannot be
// proven running, and is not guessed at.
func deployedWith(ctx context.Context, comparer core.Comparer, pr core.PullRequest, sha string) (bool, error) {
	if pr.MergeSHA == "" {
		return false, nil
	}
	comparison, err := comparer.Compare(ctx, pr.MergeSHA, sha)
	if err != nil {
		return false, core.Explain(err, "compare #%d's merge with %s", pr.Number, shortSHA(sha))
	}
	if !comparison.Known() {
		return false, core.Errorf(core.ErrUnavailable, "tell whether #%d is running", pr.Number).
			WithWhy("the forge reports the ancestry of %s and %s as %q, which flow does not understand", shortSHA(pr.MergeSHA), shortSHA(sha), comparison.Status).
			WithFix("report this: flow understands identical, ahead, behind and diverged")
	}
	return comparison.Contains(), nil
}

// deployMove performs the transitions the deployment implies, for each ticket
// linked to a merge it is running.
//
// Only those tickets are considered. A board holds work this deployment has
// nothing to do with, and asking the tracker about all of it would turn one
// deployment into a full audit — which is what doctor is for.
func deployMove(ctx context.Context, in DeployInput, deploy *Deploy, linked []LinkedPR) error {
	for _, key := range deployedKeys(in.Adapters.Tracker, linked, deploy.PRs) {
		ticket, err := in.Adapters.Tracker.Ticket(ctx, key)
		if err != nil {
			return core.Explain(err, "read ticket %s", key)
		}
		move := Transition(TransitionSnapshot{
			Ticket: ticket,
			Column: ColumnOf(in.Config, in.Lock, ticket),
			PRs:    prsFor(in.Adapters.Tracker, linked, key),
			Config: in.Config,
		})
		deploy.Moves = append(deploy.Moves, move)
		if !move.Moves() || in.DryRun {
			continue
		}
		if err := in.Adapters.Tracker.Move(ctx, key, columnTarget(in.Config, move.To)); err != nil {
			return core.Explain(err, "move %s to %s", key, move.To)
		}
	}
	return nil
}

// deployedKeys is every ticket linked to a pull request this deployment is
// running, once each and in the order the pull requests were read.
func deployedKeys(tracker core.Tracker, linked []LinkedPR, running []DeployedPR) []string {
	var keys []string
	for _, deployed := range running {
		for _, candidate := range linked {
			if candidate.PR.Number != deployed.Number {
				continue
			}
			for _, key := range tracker.LinkedKeys(candidate.PR) {
				if !hasTicket(keys, key) {
					keys = append(keys, key)
				}
			}
		}
	}
	return keys
}

// hasTicket reports whether a key is already in the list.
func hasTicket(keys []string, key string) bool {
	for _, held := range keys {
		if sameTicket(held, key) {
			return true
		}
	}
	return false
}

// shortSHA abbreviates a commit the way git does, leaving anything that is not
// a long object name alone.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// deployDoc is the --json document, schema flow.deploy/v1.
type deployDoc struct {
	Schema      string       `json:"schema"`
	SHA         string       `json:"sha"`
	Environment string       `json:"environment"`
	Watched     bool         `json:"watched"`
	Confirmed   bool         `json:"confirmed"`
	Acted       bool         `json:"acted"`
	Why         string       `json:"why,omitempty"`
	DryRun      bool         `json:"dry_run,omitempty"`
	PRs         []DeployedPR `json:"prs"`
	Moves       []TicketMove `json:"moves"`
}

// JSON renders the run's single document (spec 02 §3).
func (d Deploy) JSON() ([]byte, error) {
	doc := deployDoc{
		Schema:      DeployVersion,
		SHA:         d.SHA,
		Environment: d.Environment,
		Watched:     d.Watched,
		Confirmed:   d.Confirmed,
		Acted:       d.Acted(),
		Why:         d.Why,
		DryRun:      d.DryRun,
		PRs:         d.PRs,
		Moves:       d.Moves,
	}
	if doc.PRs == nil {
		doc.PRs = []DeployedPR{}
	}
	if doc.Moves == nil {
		doc.Moves = []TicketMove{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the deployment result: %w", err)
	}
	return append(data, '\n'), nil
}

// Text renders the run for a terminal: what was deployed, and what that
// finished.
func (d Deploy) Text() string {
	var out strings.Builder
	if d.SHA == "" {
		fmt.Fprintf(&out, "deployment of %s\n", d.Environment)
	} else {
		fmt.Fprintf(&out, "deployment %s → %s\n", shortSHA(d.SHA), d.Environment)
	}
	if d.Why != "" {
		fmt.Fprintf(&out, "nothing to do: %s\n", d.Why)
		return out.String()
	}
	for _, pr := range d.PRs {
		fmt.Fprintf(&out, "· #%d is running\n", pr.Number)
	}
	for _, move := range d.Moves {
		switch {
		case move.Warning != "":
			fmt.Fprintf(&out, "· %s  %s\n", move.Key, move.Warning)
		case move.Moves():
			fmt.Fprintf(&out, "→ %s  %s\n", move.Key, moveText(move))
		default:
			fmt.Fprintf(&out, "· %s  %s\n", move.Key, deployHeldWhy(move))
		}
	}
	if d.DryRun {
		out.WriteString("dry run: nothing was moved\n")
	}
	return out.String()
}

// deployHeldWhy is why a ticket this deployment finished work on stayed where
// it was, taken from the last thing the transition engine considered.
func deployHeldWhy(move TicketMove) string {
	for at := len(move.Steps) - 1; at >= 0; at-- {
		if step := move.Steps[at]; step.Event == config.EventDeployed && step.Why != "" {
			return step.Why
		}
	}
	return "nothing to move"
}
