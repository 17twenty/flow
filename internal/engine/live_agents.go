package engine

import "context"

// The live-health audit of agent work (spec 07 §6).
//
// Everything else in the audit grades: a check counts the things that held
// and the team's delivery is better or worse for the number. This one counts
// nothing of the kind. How much a machine merged in a fortnight, and how much
// of it had to be sent back, are facts about how a team is using an agent,
// and there is no share of them that is right — a team landing forty agent
// pull requests a fortnight with a quarter sent back may be doing better work
// than one landing two with none.
//
// So it is reported and never graded, and it cannot move the exit code. A
// number that could fail the run would need a threshold, and a threshold
// nobody can justify is a number flow made up.
//
// It is one check and not two. Throughput on its own is the denominator of
// the rework rate: a line saying "12 merged" beside a line saying "3 of 12
// were sent back" is the same sentence written twice, and the second has to
// repeat the first to mean anything. And it is a check rather than a new kind
// of thing in the report, because the report has one shape — a check renders
// as a line with counts and evidence in text, in markdown and in JSON, and in
// the issue the morning run edits in place. A measurement with a shape of its
// own would have to be rendered four more times to say less.

// CheckLiveAgentsRework is how much of what a machine landed came back first.
const CheckLiveAgentsRework = "live.agents.rework"

// agentsRework measures the agent work the window holds: how much of it
// merged, and how much was sent back at least once before it did.
//
// Sent back is "was ever asked for changes", not "is asking now": a merged
// pull request has been approved by the time it lands, so
// [core.PullRequest.ChangesRequested], which reads each reviewer's latest
// verdict, is false of every one of them. Rework is a thing that happened,
// and the review that asked for it counts even after a later one overtook it.
func (a *audit) agentsRework() Check {
	return Check{
		ID:        CheckLiveAgentsRework,
		Title:     "How much agent work came back before it landed",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			if env.Config == nil || (len(env.Config.Agents.Authors) == 0 && len(env.Config.Agents.Labels) == 0) {
				return Skip("the configuration names no machine contributors")
			}
			merged, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			var t tally
			for _, pr := range merged {
				if !agentAuthored(env.Config, pr) {
					continue
				}
				if !pr.SentBack() {
					t.held()
					continue
				}
				t.broke(pr.URL, "#%d was sent back before it landed", pr.Number)
			}

			counts := t.counts
			result := Result{State: StatePass, Counts: &counts, Evidence: t.evidence}
			sent := t.missed()
			switch {
			case counts.Total == 0:
				// Not 0%: a share of nothing reads as a team doing well,
				// and what happened is that nobody asked a machine for
				// anything.
				return result.Said("nothing a machine wrote merged since %s", day(env.Since()))
			case sent == 0:
				return result.Said("a machine merged %d %s since %s, none of them sent back",
					counts.Total, plural(counts.Total, "pull request"), day(env.Since()))
			}
			return result.Said("a machine merged %d %s since %s, %d of them sent back at least once (%d%%)",
				counts.Total, plural(counts.Total, "pull request"), day(env.Since()),
				sent, percent(sent, counts.Total))
		},
	}
}

// percent is a share as whole per cent, rounded half up. The caller has
// already established that there is something to divide by.
func percent(part, whole int) int { return (part*200 + whole) / (whole * 2) }
