package core

import "context"

// Forge is the Git host: pull requests, reviews, branch protection, commit
// statuses and the files that make the workflow run (spec 01 §3).
//
// Every method that talks to a provider takes a context and returns an error
// wrapping one of the sentinels in errors.go, so that the caller can decide
// what to do without knowing which forge it was talking to.
type Forge interface {
	// Kind is the adapter's name, matching the capability declaration and the
	// forge.type in configuration.
	Kind() string
	// Capabilities is what this adapter can do, as declared (spec 05 §1).
	Capabilities() CapabilitySet
	// Slug is the repository this adapter speaks for, as owner/name. It is
	// what the adapter was built with rather than what the provider
	// answers, so that an error about a repository flow cannot reach can
	// still name it.
	Slug() string
	// Probe reports what the credential is allowed to do.
	Probe(ctx context.Context) (Permissions, error)

	// Repo describes the repository: its default branch and merge settings.
	Repo(ctx context.Context) (RepoInfo, error)
	// PullRequest returns one pull request, with its reviews, head SHA, file
	// summary and the issues the forge says it closes.
	PullRequest(ctx context.Context, number int) (PullRequest, error)
	// PullRequests returns the pull requests matching a query, newest first.
	// A listing is a summary: reviews and closing issues are separate reads
	// (spec 10 §2), so a caller that needs either asks PullRequest for it.
	PullRequests(ctx context.Context, q PRQuery) ([]PullRequest, error)
	// Commits returns commits on a branch, newest first.
	Commits(ctx context.Context, q CommitQuery) ([]Commit, error)
	// Compare reports the ancestry between two commits, which is how flow
	// proves a merge reached an environment (spec 13 §3).
	Compare(ctx context.Context, base, head string) (Comparison, error)
	// File returns the content of a file at a ref, so that doctor can see
	// whether the workflow that runs flow is actually on the trunk.
	File(ctx context.Context, path, ref string) ([]byte, error)

	// Protection returns the effective protection of a branch, whichever
	// mechanism configured it.
	Protection(ctx context.Context, branch string) (Protection, error)
	// SetProtection applies protection additively: it never removes a
	// required check or a setting flow did not ask for (spec 06 §4).
	SetProtection(ctx context.Context, branch string, p Protection) error
	// SetMergeSettings sets the repository's merge strategies.
	SetMergeSettings(ctx context.Context, s MergeSettings) error
	// ArmAutoMerge asks the forge to land this pull request with that merge
	// method as soon as everything it is waiting for is satisfied, so that
	// an approval given while nobody is watching is not a queue that stops.
	//
	// It is idempotent: arming a pull request already armed does nothing.
	// It is refused with ErrUnsupported by a forge that has no such thing,
	// and with ErrValidation by one whose repository has it switched off or
	// whose pull request is a draft — a caller cannot tell those apart
	// beforehand, because only the forge knows.
	ArmAutoMerge(ctx context.Context, number int, method string) error

	// CommitStatuses returns the statuses on a commit, newest first.
	CommitStatuses(ctx context.Context, sha string) ([]CommitStatus, error)
	// SetCommitStatus publishes a status on a commit. Publishing the same
	// context twice replaces the earlier verdict.
	SetCommitStatus(ctx context.Context, sha string, s CommitStatus) error

	// UpsertComment writes flow's comment on a pull request, editing the one
	// carrying that marker if it is already there (spec 11 §1).
	UpsertComment(ctx context.Context, number int, m Marker, body string) error
	// Comment returns flow's comment carrying that marker, or nil when there
	// is none.
	Comment(ctx context.Context, number int, m Marker) (*Comment, error)
	// DeleteComment removes the comment carrying that marker. Deleting a
	// comment that is not there is not an error.
	DeleteComment(ctx context.Context, number int, m Marker) error

	// ProposeFiles offers a set of files as a pull request from the
	// proposal's own branch. It never writes the default branch, and making
	// the same proposal again updates the open pull request in place.
	ProposeFiles(ctx context.Context, p FileProposal) (PullRequest, error)
}

// CheckRunReader is a forge whose CI can report through something other than
// commit statuses, and which can read that too: GitHub, whose Actions jobs
// report as check runs. A forge whose CI writes commit statuses — Gitea,
// Forgejo, GitLab — needs nothing beyond [Forge.CommitStatuses], and does not
// implement it.
//
// It is kept apart from CommitStatuses because the two need different grants:
// the gate's own token may read statuses and not check runs, and a gate that
// could not read its own status because of a permission it never needed would
// be a broken gate (spec 20 §6).
type CheckRunReader interface {
	// CheckRuns returns the check runs reported against a commit, as
	// statuses: the run's name as the context, and its outcome as the state.
	CheckRuns(ctx context.Context, sha string) ([]CommitStatus, error)
}

// BranchUpdater is a forge that can bring a pull request's branch up to date
// with its base itself, which a trunk requiring branches to be up to date
// asks of every pull request after each merge (issue #234). headSHA is the
// head the caller judged: a forge refuses the update if the branch has moved
// since, so flow never updates work it has not looked at.
type BranchUpdater interface {
	UpdateBranch(ctx context.Context, number int, headSHA string) error
}

// Tracker is the ticket system: the board, its columns, its items, and the
// follow-ups flow files against itself (spec 01 §3).
type Tracker interface {
	// Kind is the adapter's name, matching the capability declaration and the
	// tracker.type in configuration.
	Kind() string
	// Capabilities is what this adapter can do, as declared (spec 05 §1).
	Capabilities() CapabilitySet
	// Probe reports what the credential is allowed to do.
	Probe(ctx context.Context) (Permissions, error)

	// Discover offers the candidates that could answer a placeholder, given
	// whatever hints configuration already provides (spec 07 §3).
	Discover(ctx context.Context, what string, hint map[string]string) ([]Candidate, error)
	// ParseRef turns a URL a human pasted into a reference flow can record.
	ParseRef(raw string) (Ref, error)

	// Observe returns the current state of one desired item, canonically, so
	// that it can be diffed and hashed (spec 06 §2).
	Observe(ctx context.Context, what string) (Observation, error)
	// Apply makes one change, and reports what it became.
	Apply(ctx context.Context, c Change) (ApplyResult, error)

	// Ticket returns one ticket by key.
	Ticket(ctx context.Context, key string) (Ticket, error)
	// Tickets returns the tickets matching a query.
	Tickets(ctx context.Context, q TicketQuery) ([]Ticket, error)
	// Move takes a ticket to another column. The target is the tracker's
	// own column id, or the column's display name — which is all the
	// configuration knows, since `workflow.columns` is written by hand and
	// a column flow never created has no id in the lock. A column the board
	// does not have is ErrValidation. It is idempotent: moving a ticket to
	// the column it is already in does nothing. Only transitions the
	// configuration gives flow are performed this way (spec 04 §5).
	Move(ctx context.Context, key string, to ColumnID) error

	// CreateFollowUp files a ticket for work flow cannot do, or returns the
	// one already filed: the marker makes it idempotent (spec 07 §4).
	CreateFollowUp(ctx context.Context, f FollowUp) (Ticket, error)
	// FindFollowUp returns the ticket carrying a marker, or nil when there is
	// none. A marker naming fewer fields matches more tickets (spec 11 §1).
	FindFollowUp(ctx context.Context, m Marker) (*Ticket, error)
	// UpdateFollowUp rewrites the title and body of a ticket flow opened,
	// keeping its marker. It is how the pinned health report stays one issue
	// that is edited rather than a new one every morning (spec 07 §10), and
	// how a follow-up filed before flow could say what to do gets the steps
	// once it can (spec 07 §4). A ticket whose body does not carry the marker
	// is somebody else's writing and is refused; rewriting a ticket to what
	// it already says is no work.
	UpdateFollowUp(ctx context.Context, key string, m Marker, title, body string) error
	// Comment writes a marked comment on a ticket.
	Comment(ctx context.Context, key string, m Marker, body string) error
	// Comments returns every comment on a ticket, oldest first. It is how
	// flow asks whether anybody has said why a ticket is blocked (spec 07
	// §6); flow's own writing is in there too, and [Marked] is what tells
	// the two apart.
	Comments(ctx context.Context, key string) ([]TicketComment, error)
	// Close closes a ticket, recording why, with one of the close reasons the
	// tracker distinguishes. It is idempotent: closing a closed ticket does
	// nothing. Flow closes only tickets it opened (spec 07 §4).
	Close(ctx context.Context, key string, reason string) error
	// Reopen reopens a closed ticket, recording why.
	Reopen(ctx context.Context, key string, reason string) error

	// LinkedKeys returns the ticket keys a pull request is for, using the
	// forge's structured link first and the tracker's own documented
	// convention as a fallback. It never applies a user-supplied pattern
	// (P12, spec 04 §3).
	LinkedKeys(pr PullRequest) []string
}

// DeploySource is where flow learns that a commit reached an environment
// (spec 13).
type DeploySource interface {
	// Kind is [SourceForgeDeployments] or [SourceCommitStatus].
	Kind() string
	// Capabilities is what this source can do, as declared (spec 05 §1).
	Capabilities() CapabilitySet
	// Deployments returns the records matching a query, newest first.
	Deployments(ctx context.Context, q DeployQuery) ([]Deployment, error)
}
