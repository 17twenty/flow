package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/17twenty/flow/internal/commits"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The live-health audit (spec 07 §6): the half of doctor that asks whether the
// team is actually working the way the configuration says they do. The setup
// checks prove the rules are switched on; these prove they were obeyed.
//
// Three things are decided here once, for every live check there will ever be.
//
// A setup check reads the plan, because the plan already observed the
// providers. A live check cannot — nothing about a pull request merged last
// Tuesday is part of what flow wants the repository to look like — so the
// window is read here instead, lazily and once. Four checks each asking the
// forge for the same fortnight would be four times the rate limit and, worse,
// four slightly different fortnights.
//
// A window nothing happened in is not a failure. [Counts.Ratio] is built so
// that nothing looked at is a pass, and the checks say "nothing merged" rather
// than "all 0 merged pull requests were fine", because a dashboard that cannot
// tell a quiet fortnight from a healthy one is telling nobody anything.
//
// And a live check never offers to fix what it found. A pull request that
// merged ungated last week cannot be gated now; what can be done is to stop
// the next one, which is a setup check's business. So the remediation is
// always [RemedyManual] and always points at the rule that would have caught
// it — which is also what keeps `--fix` to the five things spec 07 §6 allows.

// The live-health checks about pull requests (spec 07 §6).
const (
	// CheckLivePRsLinked is whether every merged pull request said what it
	// was for.
	CheckLivePRsLinked = "live.prs.linked"
	// CheckLivePRsGated is whether every merged pull request passed the gate
	// before it merged.
	CheckLivePRsGated = "live.prs.gated"
	// CheckLivePRsConventional is whether the trunk's history reads as the
	// convention the team chose.
	CheckLivePRsConventional = "live.prs.conventional"
	// CheckLivePRsReviewed is whether a person approved what a machine wrote.
	CheckLivePRsReviewed = "live.prs.reviewed"
)

// DefaultWindow is how far back the audit looks when nobody said (spec 07 §6).
const DefaultWindow = 14 * 24 * time.Hour

// LiveChecks builds the live-health audit, in report order.
//
// The checks returned share one reading of the window, so the slice is good
// for exactly one run of doctor. That is the same contract [SetupChecks] has —
// a registry is built per run — and it is what lets four checks be four
// questions rather than four round trips.
func LiveChecks(cfg *config.Config) []Check {
	a := &audit{}
	checks := append([]Check{
		a.prsLinked(),
		a.prsGated(),
		a.prsConventional(),
		a.prsReviewed(),
	}, a.deployChecks()...)
	checks = append(checks, a.ticketChecks()...)
	checks = append(checks, a.transitionChecks(cfg)...)
	return append(checks, a.transitionsFlow())
}

// errNoForge is what a live check reports when it is asked to look at a forge
// that is not there. It cannot happen through doctor, because every live check
// depends on `forge.repo`, which fails first — but a check that panicked on a
// nil adapter would take the rest of the report down with it.
var errNoForge = errors.New("there is no forge adapter to ask")

// audit is the window, read once per run. Every loader is lazy: a run narrowed
// to one check with `--check` must not pay for the pull requests it is never
// going to look at.
type audit struct {
	repoOnce sync.Once
	repo     core.RepoInfo
	repoErr  error

	prsOnce sync.Once
	prs     []core.PullRequest
	prsErr  error

	logOnce sync.Once
	log     []core.Commit
	logErr  error

	openOnce sync.Once
	open     []core.PullRequest
	openErr  error

	boardOnce sync.Once
	tickets   []core.Ticket
	boardErr  error

	shipOnce sync.Once
	ships    []core.Deployment
	shipErr  error
}

// trunk is the branch the workflow is about, which is the repository's own
// default branch (spec 10 §2).
func (a *audit) trunk(ctx context.Context, env *Env) (string, error) {
	a.repoOnce.Do(func() {
		if env.Adapters.Forge == nil {
			a.repoErr = errNoForge
			return
		}
		a.repo, a.repoErr = env.Adapters.Forge.Repo(ctx)
	})
	return a.repo.DefaultBranch, a.repoErr
}

// merged is the pull requests that landed on the trunk inside the window, in
// number order.
//
// The query is by update time, because that is what every forge indexes and a
// pull request is updated when it merges. Merging is not the last thing that
// can happen to one, though — a comment afterwards moves it too — so the
// window is applied a second time here, to the merge itself, which is the date
// the audit is actually about.
func (a *audit) merged(ctx context.Context, env *Env) ([]core.PullRequest, error) {
	a.prsOnce.Do(func() {
		base, err := a.trunk(ctx, env)
		if err != nil {
			a.prsErr = err
			return
		}
		since := env.Since()
		prs, err := env.Adapters.Forge.PullRequests(ctx, core.PRQuery{
			State:        core.PRStateMerged,
			Base:         base,
			UpdatedSince: since,
		})
		if err != nil {
			a.prsErr = core.Explain(err, "read what merged into %s since %s", base, day(since))
			return
		}
		for _, pr := range prs {
			if pr.MergedAt.Before(since) {
				continue
			}
			a.prs = append(a.prs, pr)
		}
		slices.SortStableFunc(a.prs, func(x, y core.PullRequest) int {
			return cmp.Compare(x.Number, y.Number)
		})
	})
	return a.prs, a.prsErr
}

// history is the commits on the trunk inside the window, newest first.
func (a *audit) history(ctx context.Context, env *Env) ([]core.Commit, error) {
	a.logOnce.Do(func() {
		ref, err := a.trunk(ctx, env)
		if err != nil {
			a.logErr = err
			return
		}
		since := env.Since()
		log, err := env.Adapters.Forge.Commits(ctx, core.CommitQuery{Ref: ref, Since: since})
		if err != nil {
			a.logErr = core.Explain(err, "read %s since %s", ref, day(since))
			return
		}
		a.log = log
	})
	return a.log, a.logErr
}

// prsLinked asks whether every merged pull request said what it was for.
func (a *audit) prsLinked() Check {
	return Check{
		ID:        CheckLivePRsLinked,
		Title:     "Every merged pull request said what it was for",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			var t tally
			for _, pr := range prs {
				_, exempt := unlinkedType(env.Config, pr.Title)
				if exempt || len(linkedKeys(env, pr)) > 0 {
					t.held()
					continue
				}
				t.broke(pr.URL, "#%d %s", pr.Number, pr.Title)
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing merged in the window")
			case result.State == StatePass:
				return result.Said("all %d merged %s named a ticket",
					t.counts.Total, plural(t.counts.Total, "pull request"))
			}
			return result.Said("%d of %d merged pull requests named no ticket", t.missed(), t.counts.Total).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "link each of these to the work it was for, or say that its type may merge unlinked",
					Steps: []string{
						linkFix(env.Config),
						"if changes of this kind should merge without a ticket, list their types in commits.unlinked_types",
						"the gate refuses an unlinked pull request; check forge.required_status in this report to see whether it can be bypassed",
					},
				})
		},
	}
}

// prsGated asks whether the gate had actually passed each pull request by the
// time it merged. It is critical: one merge getting past the gate is the thing
// the gate exists to make impossible, so there is no threshold at which it is
// merely a warning (spec 07 §6, scenario V02).
func (a *audit) prsGated() Check {
	return Check{
		ID:        CheckLivePRsGated,
		Title:     "Every merged pull request passed the gate first",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			gate := StatusContext(env.Config)
			var t tally
			for _, pr := range prs {
				statuses, err := env.Adapters.Forge.CommitStatuses(ctx, pr.HeadSHA)
				if err != nil {
					return Failed(core.Explain(err, "read the checks on #%d", pr.Number))
				}
				if gated(statuses, gate, pr.MergedAt) {
					t.held()
					continue
				}
				t.broke(pr.URL, "#%d merged with no %s success on %s", pr.Number, gate, shortSHA(pr.HeadSHA))
			}
			result := t.grade(env, true)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing merged in the window")
			case result.State == StatePass:
				return result.Said("all %d merged %s carried a %s success",
					t.counts.Total, plural(t.counts.Total, "pull request"), gate)
			}
			return result.Said("%d of %d merged pull requests were never gated", t.missed(), t.counts.Total).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "make " + gate + " a required check on the trunk, so the next one cannot merge without it",
					Steps: []string{
						"check forge.required_status in this report: publishing the verdict is not the same as requiring it",
						"a required check an administrator may bypass is not required; see forge.protection",
						"ask whoever merged these what they were working around",
					},
				})
		},
	}
}

// prsConventional asks whether the trunk's history can be read by the
// convention the team chose, which is what makes the log the list of work.
//
// It reads the trunk rather than the pull requests, because the pull request
// title is only the commit header when the merge was a squash — and a merge
// that was not a squash is exactly the case where the title the gate checked
// is not what landed. Commits git wrote itself are not held to the grammar;
// the linter already knows that, and knowing it twice is how two answers to
// one question start to differ.
func (a *audit) prsConventional() Check {
	return Check{
		ID:        CheckLivePRsConventional,
		Title:     "The trunk reads as the commit convention the team chose",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			log, err := a.history(ctx, env)
			if err != nil {
				return Failed(err)
			}
			options := commits.LintOptions{
				Settings: CommitSettings(env.Config),
				Subject:  commits.SubjectCommit,
			}
			var t tally
			for _, commit := range log {
				violations := commits.Errors(commits.Lint(commits.Parse(commit.Message), options))
				if len(violations) == 0 {
					t.held()
					continue
				}
				t.broke(commit.URL, "%s %s — %s", shortSHA(commit.SHA), commit.Header(), violations[0].Message)
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing landed on the trunk in the window")
			case result.State == StatePass:
				return result.Said("all %d %s on the trunk read as the convention",
					t.counts.Total, plural(t.counts.Total, "commit"))
			}
			return result.Said("%d of %d commits on the trunk do not read as the convention",
				t.missed(), t.counts.Total).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "the header that lands must be the one the gate checked",
					Steps: []string{
						"check forge.merge_settings in this report: a squash merge must take its header from the pull request title",
						"check forge.required_status: the gate refuses a title that does not parse, if it is required",
						"a commit pushed straight to the trunk was never a pull request at all; see forge.protection",
					},
				})
		},
	}
}

// prsReviewed asks whether a person approved what a machine wrote.
//
// A team that has not asked for a human review is not audited for one: there
// is no claim to hold them to, which is the same answer the gate's
// `agent.human_review` rule gives. So whenever this check runs, the team has
// said it matters, and spec 07 §6's "critical when require_human_review" is
// the condition under which the check exists at all.
func (a *audit) prsReviewed() Check {
	return Check{
		ID:        CheckLivePRsReviewed,
		Title:     "A person approved what a machine wrote",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			if env.Config == nil || !env.Config.Agents.RequireHumanReview {
				return Skip("the team does not require a human review of agent work")
			}
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			var t tally
			for _, pr := range prs {
				if !agentAuthored(env.Config, pr) {
					continue
				}
				if _, approved := humanApprover(env.Config, pr); approved {
					t.held()
					continue
				}
				t.broke(pr.URL, "#%d by %s merged with nobody's approval but an agent's", pr.Number, pr.Author)
			}
			result := t.grade(env, true)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing a machine wrote merged in the window")
			case result.State == StatePass:
				return result.Said("a person approved all %d agent %s",
					t.counts.Total, plural(t.counts.Total, "pull request"))
			}
			return result.Said("%d of %d agent pull requests merged without a person approving",
				t.missed(), t.counts.Total).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "require an approval on the trunk, so agent work cannot merge on its own say-so",
					Steps: []string{
						"check forge.protection in this report: the trunk should require at least one approving review",
						"an agent approving an agent does not count; keep agents.authors complete",
						"flow's own gate refuses this, so check forge.required_status too",
					},
				})
		},
	}
}

// gated reports whether the gate had passed this commit by the time it merged.
// A success published afterwards proves the rule was evaluated, not that it was
// enforced, and enforcement is the whole of what this check is about.
func gated(statuses []core.CommitStatus, gate string, mergedAt time.Time) bool {
	for _, status := range statuses {
		if status.Context != gate || status.State != core.StatusSuccess {
			continue
		}
		if !mergedAt.IsZero() && status.CreatedAt.After(mergedAt) {
			continue
		}
		return true
	}
	return false
}

// linkedKeys is the tickets a pull request is for. The tracker is asked,
// because only it knows its own convention and the forge's structured link
// (spec 04 §3). With no tracker adapter the forge's own closing references are
// all there is, which is better than calling every pull request unlinked.
func linkedKeys(env *Env, pr core.PullRequest) []string {
	if env.Adapters.Tracker != nil {
		return env.Adapters.Tracker.LinkedKeys(pr)
	}
	return pr.ClosingIssues
}

// day is a date in the words an error message wants.
func day(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// tally is what a live check counted as it went: how many of the things it
// looked at held, and what to say about the ones that did not.
type tally struct {
	counts   Counts
	evidence []Evidence
}

// held records an item that was as it should be.
func (t *tally) held() {
	t.counts.Pass++
	t.counts.Total++
}

// broke records one that was not, with somewhere to go and look at it.
func (t *tally) broke(url, format string, args ...any) {
	t.counts.Total++
	t.evidence = append(t.evidence, Evidence{Text: fmt.Sprintf(format, args...), URL: url})
}

// missed is how many did not hold.
func (t *tally) missed() int { return t.counts.Total - t.counts.Pass }

// grade turns the count into a result at spec 07 §6's thresholds, carrying
// what was counted and the items that did not hold. The message is the
// check's own: only it knows what it was counting.
func (t *tally) grade(env *Env, critical bool) Result {
	counts := t.counts
	return Result{
		State:    Grade(counts, env.Threshold(), critical),
		Evidence: t.evidence,
		Counts:   &counts,
	}
}

// gradeWarning is grade for a check spec 07 §6 marks "warn only": the count
// still decides whether anything is wrong, and nothing it finds is ever worse
// than a warning. A check that could fail is one somebody turns off.
func (t *tally) gradeWarning(env *Env) Result {
	result := t.grade(env, false)
	if result.State == StateFail {
		result.State = StateWarn
	}
	return result
}
