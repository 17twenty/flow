package core

import (
	"slices"
	"strings"
	"time"
)

// Merge strategy settings. flow's lean workflow wants one commit per pull
// request, titled by the pull request, so that the trunk's history is the list
// of merged work and every header can be linted (spec 06 §1).
const (
	// SquashTitlePR takes the squash commit's header from the pull request
	// title.
	SquashTitlePR = "pr_title"
	// SquashTitleCommit takes it from the first commit on the branch.
	SquashTitleCommit = "commit_title"
	// SquashMessagePRBody takes the squash commit's body from the pull
	// request body.
	SquashMessagePRBody = "pr_body"
	// SquashMessageCommits takes it from the branch's commit messages.
	SquashMessageCommits = "commit_messages"
	// SquashMessageBlank leaves it empty.
	SquashMessageBlank = "blank"
)

// MergeSettings are the repository's merge strategies and the text a squash
// merge takes its header and body from.
type MergeSettings struct {
	// AllowSquash permits squash merging.
	AllowSquash bool
	// AllowMerge permits merge commits.
	AllowMerge bool
	// AllowRebase permits rebase merging.
	AllowRebase bool
	// SquashTitle is where a squash commit's header comes from.
	SquashTitle string
	// SquashMessage is where a squash commit's body comes from.
	SquashMessage string
	// DeleteBranchOnMerge removes the head branch after a merge.
	DeleteBranchOnMerge bool
}

// RepoInfo is what flow needs to know about the repository itself.
type RepoInfo struct {
	// Owner is the account or organisation the repository belongs to.
	Owner string
	// Name is the repository's own name.
	Name string
	// DefaultBranch is the trunk every pull request targets.
	DefaultBranch string
	// URL is the repository's web address.
	URL string
	// Merge is how the repository allows pull requests to land.
	Merge MergeSettings
	// Archived reports a repository that can no longer be written to.
	Archived bool
}

// Slug is the repository as people write it: owner/name.
func (r RepoInfo) Slug() string {
	if r.Owner == "" {
		return r.Name
	}
	return r.Owner + "/" + r.Name
}

// Protection is the effective protection of one branch: what the forge will
// refuse, whichever mechanism configured it. GitHub's classic protection and
// its rulesets both land here (spec 10 §2).
type Protection struct {
	// Branch is the branch this protects.
	Branch string
	// Enabled reports whether the branch is protected at all.
	Enabled bool
	// RequiredChecks are the status contexts that must succeed before a merge.
	RequiredChecks []string
	// StrictChecks requires the branch to be up to date with its base.
	StrictChecks bool
	// RequiredApprovals is how many reviews a pull request needs.
	RequiredApprovals int
	// DismissStaleReviews drops approvals when new commits arrive.
	DismissStaleReviews bool
	// RequireLinearHistory refuses merge commits on the branch.
	RequireLinearHistory bool
	// RequireConversationResolution refuses a merge with open review threads.
	RequireConversationResolution bool
	// EnforceAdmins applies the rules to administrators too.
	EnforceAdmins bool
	// AllowForcePush permits rewriting the branch.
	AllowForcePush bool
	// AllowDeletions permits deleting the branch.
	AllowDeletions bool
	// Dormant are rules the forge holds for this branch that are not in
	// force. Nothing in one is enforced, so none of it is protection — but
	// the difference between "somebody has to write this" and "somebody has
	// to switch this on" is a day's work against one click, and a report
	// that cannot tell them apart asks for the wrong thing (spec 10 §2).
	Dormant []DormantRule
}

// DormantRule is a rule a branch would be subject to if somebody enabled it:
// on GitHub, a ruleset whose enforcement is `disabled` or `evaluate`.
type DormantRule struct {
	// Name is what the forge calls it.
	Name string
	// State is the forge's own word for why it is not in force.
	State string
	// URL is the page where it is switched on, empty when there is none.
	URL string
}

// RequiresCheck reports whether context is already required.
func (p Protection) RequiresCheck(context string) bool {
	return slices.Contains(p.RequiredChecks, context)
}

// Merge returns p with everything want adds, and nothing it takes away: the
// union of the required checks, the greater number of approvals, and a flag
// set in either. Protection changes are additive, because flow is not the only
// thing that has an opinion about the trunk (spec 06 §4).
func (p Protection) Merge(want Protection) Protection {
	merged := p
	merged.Enabled = p.Enabled || want.Enabled
	for _, context := range want.RequiredChecks {
		if !merged.RequiresCheck(context) {
			merged.RequiredChecks = append(merged.RequiredChecks, context)
		}
	}
	slices.Sort(merged.RequiredChecks)
	merged.StrictChecks = p.StrictChecks || want.StrictChecks
	merged.RequiredApprovals = max(p.RequiredApprovals, want.RequiredApprovals)
	merged.DismissStaleReviews = p.DismissStaleReviews || want.DismissStaleReviews
	merged.RequireLinearHistory = p.RequireLinearHistory || want.RequireLinearHistory
	merged.RequireConversationResolution = p.RequireConversationResolution || want.RequireConversationResolution
	merged.EnforceAdmins = p.EnforceAdmins || want.EnforceAdmins
	merged.AllowForcePush = p.AllowForcePush && want.AllowForcePush
	merged.AllowDeletions = p.AllowDeletions && want.AllowDeletions
	// Dormant rules are not merged. They are something observed about the
	// branch rather than something either side is asking for, so the merge
	// keeps what the receiver saw: a caller asking for protection can
	// neither claim a rule is dormant nor make one stop being so.
	return merged
}

// Commit status states, as every forge researched names them (spec 10).
const (
	// StatusSuccess means the check passed.
	StatusSuccess = "success"
	// StatusFailure means the check failed.
	StatusFailure = "failure"
	// StatusPending means the check has not finished, or is not enforced.
	StatusPending = "pending"
	// StatusError means the check could not run.
	StatusError = "error"
)

// CommitStatus is one check's verdict on one commit. flow publishes the policy
// gate's verdict as a commit status, and reads them back to prove a merged
// pull request was gated (spec 09 §2, spec 07 §6).
type CommitStatus struct {
	// Context names the check, e.g. flow/policy.
	Context string
	// State is one of the status states above.
	State string
	// Description is the one-line summary, at most 140 characters.
	Description string
	// TargetURL points at the run that produced the verdict.
	TargetURL string
	// CreatedAt is when the status was published.
	CreatedAt time.Time
}

// Commit is one commit on a branch, as the forge or git reports it.
type Commit struct {
	// SHA is the commit's identity.
	SHA string
	// Message is the full commit message, header and body.
	Message string
	// Author is the login or name of whoever wrote it.
	Author string
	// AuthoredAt is when it was written.
	AuthoredAt time.Time
	// CommittedAt is when it landed.
	CommittedAt time.Time
	// URL is the commit's web address.
	URL string
}

// Header is the commit message's first line, which the conventional commit
// linter reads (spec 08 §1).
func (c Commit) Header() string {
	header, _, _ := strings.Cut(c.Message, "\n")
	return header
}

// CommitQuery selects commits on a branch within a window.
type CommitQuery struct {
	// Ref is the branch or commit to walk back from. Empty means the default
	// branch.
	Ref string
	// Since bounds the window; a zero time means no bound.
	Since time.Time
	// Until bounds the window at the newer end.
	Until time.Time
	// Limit caps how many commits to return. Zero means no cap.
	Limit int
}

// Pull request states.
const (
	// PRStateOpen means the pull request is still open.
	PRStateOpen = "open"
	// PRStateMerged means it was merged.
	PRStateMerged = "merged"
	// PRStateClosed means it was closed without merging.
	PRStateClosed = "closed"
)

// Review states, in the vocabulary of spec 04 §4's events.
const (
	// ReviewApproved is an approval.
	ReviewApproved = "approved"
	// ReviewChangesRequested is a review asking for changes.
	ReviewChangesRequested = "changes_requested"
	// ReviewCommented is a review that neither approved nor refused.
	ReviewCommented = "commented"
	// ReviewDismissed is a review the forge no longer counts.
	ReviewDismissed = "dismissed"
)

// Review is one person's verdict on a pull request.
type Review struct {
	// Author is the reviewer's login.
	Author string
	// State is one of the review states above.
	State string
	// SubmittedAt is when the review was given.
	SubmittedAt time.Time
}

// FileSummary describes what a pull request changed, without its diff.
type FileSummary struct {
	// Changed is how many files it touches.
	Changed int
	// Additions and Deletions are the lines added and removed.
	Additions int
	Deletions int
	// Paths are the files it touches, in sorted order. A forge that caps the
	// list returns the first page only.
	Paths []string
}

// PullRequest is a proposed change, with everything the policy gate and the
// live-health audit read about it.
type PullRequest struct {
	// Number identifies the pull request within the repository.
	Number int
	// Title is its header, which must pass the commit convention.
	Title string
	// Body is its description.
	Body string
	// Author is the login that opened it.
	Author string
	// State is one of the pull request states above.
	State string
	// Draft reports a pull request not yet offered for review.
	Draft bool
	// HeadSHA is the tip of the branch being proposed.
	HeadSHA string
	// HeadRef and BaseRef are the branches it comes from and targets.
	HeadRef string
	BaseRef string
	// MergeSHA is the commit the merge produced, empty until merged.
	MergeSHA string
	// Labels are the labels on the pull request.
	Labels []string
	// Reviews are the reviews submitted on it, oldest first.
	Reviews []Review
	// ClosingIssues are the tickets the forge itself says this closes — for
	// GitHub, closingIssuesReferences. Trackers that have no such link leave
	// it empty and fall back to their own convention (spec 04 §3).
	ClosingIssues []string
	// Files summarises what it changes.
	Files FileSummary
	// CreatedAt, UpdatedAt, MergedAt and ClosedAt are its timeline. The zero
	// time means it has not happened.
	CreatedAt time.Time
	UpdatedAt time.Time
	MergedAt  time.Time
	ClosedAt  time.Time
	// URL is its web address.
	URL string
}

// Merged reports whether the pull request landed.
func (p PullRequest) Merged() bool { return p.State == PRStateMerged }

// Approvals returns the logins that approved and have not been dismissed or
// overtaken by a later review from the same person.
func (p PullRequest) Approvals() []string {
	latest, order := p.latestReviews()
	var approvals []string
	for _, author := range order {
		if latest[author] == ReviewApproved {
			approvals = append(approvals, author)
		}
	}
	slices.Sort(approvals)
	return approvals
}

// ChangesRequested reports whether anybody's latest verdict asks for changes.
// It is what makes spec 04 §4's `changes_requested` event true of a pull
// request as it stands, rather than of a moment that has passed: a review
// answered by a later review from the same person no longer holds the work up.
func (p PullRequest) ChangesRequested() bool {
	latest, _ := p.latestReviews()
	for _, state := range latest {
		if state == ReviewChangesRequested {
			return true
		}
	}
	return false
}

// latestReviews is each reviewer's last verdict, and the order they first gave
// one in. A review that only commented is not a verdict and does not overtake
// the one before it.
func (p PullRequest) latestReviews() (map[string]string, []string) {
	latest := map[string]string{}
	var order []string
	for _, review := range p.Reviews {
		if review.State == ReviewCommented {
			continue
		}
		if _, seen := latest[review.Author]; !seen {
			order = append(order, review.Author)
		}
		latest[review.Author] = review.State
	}
	return latest, order
}

// PRQuery selects pull requests. The live-health audit asks for everything
// updated within its window, newest first, and stops when it goes past the
// window's start (spec 07 §6).
type PRQuery struct {
	// State is one of the pull request states above, or empty for all of them.
	State string
	// Base limits the query to pull requests targeting that branch.
	Base string
	// UpdatedSince bounds the window; a zero time means no bound.
	UpdatedSince time.Time
	// Limit caps how many to return. Zero means no cap.
	Limit int
}

// Comparison statuses, as GitHub's compare API names them (spec 10 §2).
const (
	// CompareIdentical means the two refs are the same commit.
	CompareIdentical = "identical"
	// CompareAhead means head contains base and more.
	CompareAhead = "ahead"
	// CompareBehind means base contains head and more.
	CompareBehind = "behind"
	// CompareDiverged means neither contains the other.
	CompareDiverged = "diverged"
)

// compareStatuses are the ancestries a comparison may report.
var compareStatuses = []string{CompareIdentical, CompareAhead, CompareBehind, CompareDiverged}

// Comparison is the ancestry between two commits, which is how flow proves a
// merge reached an environment without parsing anything (spec 13 §3). It is
// written to disk by [Ancestry], so its fields are tagged: the file outlives
// the field names.
type Comparison struct {
	// Base and Head are the commits compared.
	Base string `json:"base"`
	Head string `json:"head"`
	// Status is one of the comparison statuses above.
	Status string `json:"status"`
	// AheadBy and BehindBy are how many commits separate them.
	AheadBy  int `json:"ahead_by"`
	BehindBy int `json:"behind_by"`
}

// Known reports whether the comparison's status is one flow understands. An
// unknown one proves nothing, so it is never treated as an answer.
func (c Comparison) Known() bool { return slices.Contains(compareStatuses, c.Status) }

// Contains reports whether head contains base: the deployed commit is the
// merged commit, or came after it.
func (c Comparison) Contains() bool {
	return c.Status == CompareIdentical || c.Status == CompareAhead
}

// Comment is one comment flow wrote on a pull request, found again by its
// marker so that it is edited rather than repeated (spec 11 §1).
type Comment struct {
	// ID is the forge's identifier for the comment.
	ID string
	// Author is the login that wrote it, which is flow's own credential.
	Author string
	// Body is the comment's text, marker and all.
	Body string
	// Marker is the marker the body carries.
	Marker Marker
	// CreatedAt and UpdatedAt are when it was written and last edited.
	CreatedAt time.Time
	UpdatedAt time.Time
	// URL is its web address.
	URL string
}

// ProposedFile is one file in a [FileProposal].
type ProposedFile struct {
	// Path is where the file goes, relative to the repository root.
	Path string
	// Content is the file's new content.
	Content []byte
}

// FileProposal is a set of files flow wants in the repository. flow never
// writes the default branch: a proposal becomes a branch and a pull request,
// updated in place when it is made again (spec 06 §4).
type FileProposal struct {
	// ID names the proposal, and with it the branch flow/setup-<id>. Making
	// the same proposal twice updates the same pull request.
	ID string
	// Title is the pull request's title, which must pass the convention.
	Title string
	// Body explains the change.
	Body string
	// Files are the files to write.
	Files []ProposedFile
}

// Branch is the branch a proposal is made on.
func (p FileProposal) Branch() string { return "flow/setup-" + p.ID }
