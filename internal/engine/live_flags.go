package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/git"
)

// The live-health audit of feature flags (spec 07 §6).
//
// A feature flag is a branch in the code somebody promised to come back for.
// The promise is the whole problem: it is made in a commit message, where it
// is read once by a reviewer and then never again, and the thing that makes
// it true — deleting the flag and the path it was guarding — happens weeks
// later, if at all. Flags nobody took out are how a trunk-based codebase
// turns into a codebase with long-lived branches in it after all.
//
// So flow turns the promise into a ticket, and the ticket is the check. A
// commit that announces a flag with a `Flag-Added:` trailer must have a
// cleanup ticket on the board carrying the matching marker; `doctor --fix`
// files the ones that are missing, exactly as it catches up a transition
// somebody's automation dropped.
//
// Two things this deliberately does not do. It does not read the code, so it
// cannot tell whether a flag is still there — the ticket's existence is the
// whole of what is audited, which is why the check holds for a ticket that is
// open and for one that is done. And having filed it, flow never touches it
// again: a follow-up is work flow can verify and therefore close (spec 07
// §4), and taking a flag out is not. Closing it is the team's, which the
// ticket says in as many words.

// CheckLiveFlagsCleanup is whether every feature flag the window added has a
// ticket to take it out again.
const CheckLiveFlagsCleanup = "live.flags.cleanup"

// CheckLiveFlagsAge is whether every feature flag that has stood longer than
// the team agreed has either gone or been told to stay.
const CheckLiveFlagsAge = "live.flags.age"

// FlagTrailer is the trailer a commit announces a new feature flag with
// (spec 07 §6). It is matched the way git matches trailer keys, without
// regard to case.
const FlagTrailer = "Flag-Added"

// TrailerParser reads the trailer block of a commit message. It is git's own
// `interpret-trailers --parse` in a running flow ([git.Repo] satisfies it),
// and a seam here because where the trailers end and the body begins is a
// question only git can answer consistently (spec 08 §2) — flow asks rather
// than guesses, and a check that guessed would be a second answer.
type TrailerParser interface {
	Trailers(ctx context.Context, message string) ([]git.Trailer, error)
}

// errNoTrailers is what the flag audit reports when the run has no working
// copy to parse trailers with. It is not an empty answer: a run that cannot
// read the trailers has not established that nobody added a flag, and the
// two must never render as the same sentence.
var errNoTrailers = errors.New("there is no working copy to read commit trailers with")

// flag is one feature flag the window announced, and the commit that said so.
//
// Where two commits announce the same flag it is the oldest of them, because
// that is when the branch in the code appeared and mentioning it again does
// not make it younger. The cleanup ticket is stamped with that date and the
// age check reads the stamp, so which commit this is decides when the flag's
// clock started.
type flag struct {
	// Name is the trailer's value, as the commit wrote it.
	Name string
	// Commit is where the announcement was, for the evidence and the ticket.
	Commit core.Commit
}

// flagsCleanup asks whether every flag added in the window has a ticket to
// take it out again, and under --fix files the ones that have none.
func (a *audit) flagsCleanup() Check {
	return Check{
		ID:        CheckLiveFlagsCleanup,
		Title:     "Every feature flag added has a ticket to take it out again",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			flags, err := a.flagsAdded(ctx, env)
			if err != nil {
				return Failed(err)
			}
			var t tally
			if len(flags) == 0 {
				// Counted, like every other check in the family: a quiet
				// fortnight is 0 of 0, and the sentence says which quiet it
				// was rather than "all 0 flags were fine".
				return t.grade(env, false).Said("nothing in the window added a feature flag")
			}
			if env.Adapters.Tracker == nil {
				return Failed(errNoTracker)
			}

			var filed []Evidence
			for _, added := range flags {
				marker := FlagMarker(added.Name, repoName(env))
				ticket, err := env.Adapters.Tracker.FindFollowUp(ctx, marker)
				if err != nil {
					return Failed(core.Explain(err, "look for the cleanup ticket for %s", added.Name))
				}
				if ticket != nil {
					t.held()
					continue
				}
				if !env.Fix {
					t.broke(added.Commit.URL, "%s was added in %s and has no cleanup ticket",
						added.Name, shortSHA(added.Commit.SHA))
					continue
				}
				opened, err := fileFlagTicket(ctx, env, added)
				if err != nil {
					t.broke(added.Commit.URL, "%s has no cleanup ticket and flow could not file one: %v",
						added.Name, err)
					continue
				}
				t.held()
				filed = append(filed, Evidence{
					Text: fmt.Sprintf("%s was added in %s, so flow filed %s to take it out again",
						added.Name, shortSHA(added.Commit.SHA), opened.Key),
					URL: opened.URL,
				})
			}

			result := t.grade(env, false)
			for _, evidence := range filed {
				result = result.Saw(evidence.Text, evidence.URL)
			}
			switch {
			case len(filed) > 0 && result.State == StatePass:
				return result.Said("flow filed %d cleanup %s; all %d %s added in the window %s one",
					len(filed), plural(len(filed), "ticket"), t.counts.Total,
					plural(t.counts.Total, "flag"), haveHas(t.counts.Total))
			case result.State == StatePass:
				return result.Said("all %d %s added in the window %s a cleanup ticket",
					t.counts.Total, plural(t.counts.Total, "flag"), haveHas(t.counts.Total))
			}
			return result.Said("%d of %d feature flags added in the window have no cleanup ticket",
				t.missed(), t.counts.Total).
				Fix(flagRemedy(env, t.evidence))
		},
	}
}

// flagsAge asks what became of the flags that have stood too long.
//
// The cleanup check is about the promise being written down; this one is
// about it being kept. They are two checks because they are two failures with
// two remedies: a flag nobody filed a ticket for is something flow can fix
// this morning, and a flag that has been behind an open ticket for three
// months is a conversation.
//
// It reads the board rather than the history, and that is the whole of why it
// works. A flag past ninety days was announced by a commit more than ninety
// days ago, which is further back than any window worth reading every morning
// — so the commit is long gone from what the audit looks at, while the ticket
// is still right there. The ticket carries the date the commit had, written
// into its marker when flow filed it, so the clock is the commit's and
// nothing that happens to the ticket afterwards winds it back.
func (a *audit) flagsAge() Check {
	return Check{
		ID:        CheckLiveFlagsAge,
		Title:     "Every feature flag past the agreed age has gone, or says why it stays",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			board, err := a.board(ctx, env)
			if err != nil {
				return Failed(err)
			}
			age := flagMaxAge(env.Config)
			kept := flagKeptLabel(env.Config)
			deadline := env.Now().Add(-age)

			var t tally
			for _, open := range openCleanups(board, repoName(env)) {
				// A ticket flow filed before it recorded the date has no
				// clock to read. Counting it would be judging it by the only
				// other date there is, which is the ticket's own and the one
				// thing this check must not use.
				if open.Added.IsZero() || !open.Added.Before(deadline) {
					continue
				}
				if hasLabel(open.Ticket.Labels, kept) {
					t.held()
					continue
				}
				t.broke(open.Ticket.URL, "%s has stood %s and %s says nothing about why",
					open.Name, stoodFor(env.Now(), open.Added), open.Ticket.Key)
			}

			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("no feature flag flow has a ticket for has stood longer than %s",
					config.Duration(age))
			case result.State == StatePass:
				return result.Said("every feature flag past %s has gone or said why it stays",
					config.Duration(age))
			}
			return result.Said("%d of %d feature flags past %s are neither gone nor explained",
				t.missed(), t.counts.Total, config.Duration(age)).
				Fix(flagAgeRemedy(kept))
		},
	}
}

// cleanup is one open flag cleanup ticket, and what its marker says about the
// flag: which one it is, and the day the commit announcing it landed.
type cleanup struct {
	// Ticket is the ticket itself.
	Ticket core.Ticket
	// Name is the flag, falling back to the ticket's title for a marker
	// written before flow recorded the name.
	Name string
	// Added is the day the flag appeared, zero where the marker records none.
	Added time.Time
}

// openCleanups is the flag cleanup tickets of this repository that are still
// open, in key order.
//
// Closed is the flag having gone, which is this check's whole aim and so
// never its finding. Archived needs no test of its own here: a listing is the
// board, and an archived item is not on it (spec 11 §1).
func openCleanups(board []core.Ticket, repo string) []cleanup {
	var open []cleanup
	for _, ticket := range board {
		if ticket.Closed {
			continue
		}
		marker, found := core.FindMarker(ticket.Body)
		if !found || marker.Kind != core.MarkerFlag {
			continue
		}
		// One tracker serves many repositories, and a flag in another one is
		// not this audit's business. A ticket naming no repository is taken
		// as this one's, which is what a marker written before flow recorded
		// the repository looks like.
		if at := marker.Fields[flagFieldRepo]; at != "" && at != repo {
			continue
		}
		entry := cleanup{Ticket: ticket, Name: ticket.Title}
		if name := marker.Fields[flagFieldName]; name != "" {
			entry.Name = name
		}
		if added, err := time.Parse(time.DateOnly, marker.Fields[flagFieldAdded]); err == nil {
			entry.Added = added
		}
		open = append(open, entry)
	}
	slices.SortStableFunc(open, func(x, y cleanup) int { return strings.Compare(x.Ticket.Key, y.Ticket.Key) })
	return open
}

// stoodFor is how long the flag has been there, in whole days, which is the
// unit the agreement is written in.
func stoodFor(now, added time.Time) string {
	days := int(now.Sub(added) / (24 * time.Hour))
	return fmt.Sprintf("%d %s", days, plural(days, "day"))
}

// flagAgeRemedy is what to do about a flag that has outstayed the agreement.
// There is no --fix arm: flow cannot read the code, so both ways out of this
// finding are a person's, and a command here would be flow offering to decide
// something it is not able to decide.
func flagAgeRemedy(kept string) Remediation {
	return Remediation{
		Kind:    RemedyManual,
		Summary: "a flag this old is a branch in the code nobody came back for",
		Steps: []string{
			"take the flag out, delete whichever path it was choosing between, and close its cleanup ticket",
			fmt.Sprintf("or label the cleanup ticket %s and write in a comment why the flag stays", kept),
			"raise health.flag_max_age if the team has agreed a longer life for its flags",
		},
	}
}

// flagMaxAge is how long a flag may stand before this check asks about it.
func flagMaxAge(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Health.FlagMaxAge <= 0 {
		return config.DefaultFlagMaxAge.Std()
	}
	return cfg.Health.FlagMaxAge.Std()
}

// flagKeptLabel is the label that says a flag stays on purpose.
//
// A label, rather than a marker or a comment flow reads: the explanation
// itself belongs in the comments, where the team already writes and where
// nobody has to keep to flow's vocabulary, and what the check needs from it
// is only the yes or no. A label is the one field every tracker flow speaks
// to has, it is already read on every ticket flow lists, and a person applies
// it in one click. Flow never applies it: the whole point of it is that it is
// the team's word and not flow's.
func flagKeptLabel(cfg *config.Config) string {
	if cfg == nil || cfg.Health.FlagKeptLabel == "" {
		return config.DefaultFlagKeptLabel
	}
	return cfg.Health.FlagKeptLabel
}

// hasLabel reports whether a ticket carries that label, the way a tracker
// matches one: trackers differ on whether a label's case is part of its
// identity, and no team means two spellings to be two labels.
func hasLabel(labels []string, want string) bool {
	return slices.ContainsFunc(labels, func(have string) bool {
		return strings.EqualFold(have, want)
	})
}

// flagsAdded is every feature flag the window's commits announced, one entry
// per name, in name order.
//
// A flag announced by two commits is one flag and one ticket: the marker
// names the flag, not the commit. Names are compared exactly, because a flag
// name is an identifier in somebody's code and flow is not entitled to decide
// that two spellings are the same symbol.
func (a *audit) flagsAdded(ctx context.Context, env *Env) ([]flag, error) {
	log, err := a.history(ctx, env)
	if err != nil {
		return nil, err
	}
	if env.Trailers == nil {
		return nil, errNoTrailers
	}
	at := map[string]int{}
	var flags []flag
	for _, commit := range log {
		trailers, err := env.Trailers.Trailers(ctx, commit.Message)
		if err != nil {
			return nil, core.Explain(err, "read the trailers of %s", shortSHA(commit.SHA))
		}
		for _, name := range git.Values(trailers, FlagTrailer) {
			// A trailer with no value announces no flag anybody can file a
			// ticket about, and the commit linter stopped judging trailers
			// in v2 (spec 08 §4), so this is not the place to start.
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			// The log arrives newest first, so a name seen again is being
			// announced by an older commit, and the oldest is the one the
			// flag's age is counted from.
			if i, ok := at[name]; ok {
				flags[i].Commit = commit
				continue
			}
			at[name] = len(flags)
			flags = append(flags, flag{Name: name, Commit: commit})
		}
	}
	slices.SortStableFunc(flags, func(x, y flag) int { return strings.Compare(x.Name, y.Name) })
	return flags, nil
}

// The fields of a cleanup ticket's marker: which flag, which repository, and
// the day the commit that announced it landed.
const (
	flagFieldName  = "name"
	flagFieldRepo  = "repo"
	flagFieldAdded = "added"
)

// FlagMarker is what makes filing the same cleanup ticket twice impossible,
// and what finds it again on a board nothing in flow remembers (spec 07 §6).
func FlagMarker(name, repo string) core.Marker {
	if repo == "" {
		return core.NewMarker(core.MarkerFlag, flagFieldName, name)
	}
	return core.NewMarker(core.MarkerFlag, flagFieldName, name, flagFieldRepo, repo)
}

// flagFiling is the marker flow writes on a cleanup ticket it is opening:
// [FlagMarker] and the day the flag appeared.
//
// The date is in the marker rather than in the prose because the age check
// has to read it back, and flow parsing its own paragraphs is a second
// mechanism waiting to disagree with the first. Searching by [FlagMarker]
// still finds a ticket stamped this way — a marker naming fewer fields
// matches more tickets (spec 11 §1) — so the stamp can appear in flow's
// writing without orphaning a ticket filed before it existed.
func flagFiling(name, repo string, commit core.Commit) core.Marker {
	marker := FlagMarker(name, repo)
	marker.Fields[flagFieldAdded] = day(landed(commit))
	return marker
}

// landed is when a commit joined the history: the date the forge's own
// windows are about, falling back to when it was written for a provider that
// records only that.
func landed(commit core.Commit) time.Time {
	if !commit.CommittedAt.IsZero() {
		return commit.CommittedAt
	}
	return commit.AuthoredAt
}

// fileFlagTicket opens the cleanup ticket for one flag.
func fileFlagTicket(ctx context.Context, env *Env, added flag) (core.Ticket, error) {
	owner := Owner(env.Config, env.Lock, OwnerDefault)
	ticket, err := env.Adapters.Tracker.CreateFollowUp(ctx, core.FollowUp{
		Marker:   flagFiling(added.Name, repoName(env), added.Commit),
		Title:    FlagTitle(added.Name, repoName(env)),
		Body:     FlagBody(env.Config, added.Name, added.Commit),
		Labels:   flagLabels(env.Config),
		Assignee: owner,
	})
	if err != nil {
		return core.Ticket{}, err
	}
	if env.Log != nil {
		env.Log.Info("doctor filed a feature flag cleanup ticket",
			"flag", added.Name, "ticket", ticket.Key, "commit", added.Commit.SHA, "owner", owner)
	}
	return ticket, nil
}

// FlagTitle is the cleanup ticket's one line. The repository is in it for the
// same reason it is in a follow-up's: one tracker serves many, and `flow:
// remove the feature flag new_checkout` six times over is a list nobody can
// triage.
func FlagTitle(name, repo string) string {
	if repo == "" {
		return fmt.Sprintf("flow: remove the feature flag %s", name)
	}
	return fmt.Sprintf("flow: remove the feature flag %s (%s)", name, repo)
}

// FlagBody is what the cleanup ticket says: which flag, where it came in, and
// who is going to close this.
//
// The last part is the opposite of a follow-up's promise and matters as much.
// Flow verifies a follow-up and closes it, so the ticket can say nobody needs
// to. Flow cannot see into the code, so it will never know this flag is gone,
// and a ticket that implied otherwise would sit on a board being quietly
// untrue.
func FlagBody(cfg *config.Config, name string, commit core.Commit) string {
	var body strings.Builder
	fmt.Fprintf(&body, "`%s` was added behind a feature flag, and a flag is a branch in the code "+
		"somebody has to come back for.\n\n", name)

	body.WriteString("## What to do\n\n")
	body.WriteString("1. decide whether the flag has done its job — is the new path on everywhere, or has it been abandoned?\n")
	body.WriteString("2. delete the flag and whichever of the two paths it was choosing between\n")
	body.WriteString("3. close this ticket\n")

	body.WriteString("\n## Where it came from\n\n")
	line := "- " + shortSHA(commit.SHA)
	if header := commit.Header(); header != "" {
		line += " " + header
	}
	if commit.URL != "" {
		line += " — " + commit.URL
	}
	body.WriteString(line + "\n")

	body.WriteString("\n## How this ticket ends\n\n")
	fmt.Fprintf(&body, "`flow doctor` files one of these per `%s:` trailer and then leaves it alone, "+
		"as check `%s`.\n", FlagTrailer, CheckLiveFlagsCleanup)
	body.WriteString("Flow cannot read the code, so it will never know the flag is gone: ")
	body.WriteString("this one is closed by a person.\n")
	fmt.Fprintf(&body, "\nAfter %s from that commit, `%s` reports the flag as one nobody came back for. "+
		"If it is staying on purpose, label this ticket `%s` and say why in a comment: "+
		"that is the whole of how a flag is allowed to stay.\n",
		config.Duration(flagMaxAge(cfg)), CheckLiveFlagsAge, flagKeptLabel(cfg))
	return body.String()
}

// flagLabels are what a cleanup ticket carries. They are the follow-up labels
// because they answer the same question — which of these tickets did flow
// open? — and a team that has one label for flow's writing should not need a
// second to find half of it.
func flagLabels(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	return cfg.FollowUps.Labels
}

// flagRemedy is what to do about flags with no cleanup ticket. A run that was
// not allowed to write has the whole remedy in one command; a run that tried
// and was refused says what it hit instead of offering the command that has
// just failed.
func flagRemedy(env *Env, refused []Evidence) Remediation {
	if !env.Fix {
		return Remediation{
			Kind:    RemedyAuto,
			Summary: "flow can file these tickets itself",
			Command: "flow doctor --fix",
		}
	}
	return Remediation{
		Kind: RemedyManual,
		Summary: fmt.Sprintf("flow tried to file %d cleanup %s and could not",
			len(refused), plural(len(refused), "ticket")),
		Steps: []string{
			"check auth.tracker in this report: filing a ticket needs a credential that may write to the tracker",
			"check tracker.labels: a label the tracker does not have is one no ticket can be opened with",
			"file them by hand if you like; a ticket carrying the flow:flag marker for that name is all this check asks for",
		},
	}
}

// repoName is the repository the audit is about, as the marker and the title
// spell it.
func repoName(env *Env) string {
	if env.Config == nil {
		return ""
	}
	return env.Config.Forge.Repo
}

// haveHas agrees the verb with what was counted.
func haveHas(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}
