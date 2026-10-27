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

// The merge methods a forge is asked to land a pull request with. They are
// the strategies above, named as a verb rather than as a permission, because
// arming auto-merge is telling the forge which one to use rather than which
// ones to allow.
const (
	// MergeMethodSquash lands the branch as one commit.
	MergeMethodSquash = "squash"
	// MergeMethodMerge lands it behind a merge commit.
	MergeMethodMerge = "merge"
	// MergeMethodRebase replays its commits onto the trunk.
	MergeMethodRebase = "rebase"
)

// ValidMergeMethod reports a method a forge can be asked to merge with. It is
// here rather than in each adapter because the vocabulary is flow's: an
// adapter translates it into the provider's own spelling, and a name flow
// does not have is a caller's mistake before it is any provider's.
func ValidMergeMethod(method string) bool {
	switch method {
	case MergeMethodSquash, MergeMethodMerge, MergeMethodRebase:
		return true
	default:
		return false
	}
}

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
	// AllowAutoMerge permits a pull request to be set to merge itself once
	// everything it is waiting for has gone green. It is a repository
	// setting rather than a property of one pull request: a forge with it
	// switched off refuses the arming, so flow asks for the setting in the
	// plan and arms from the gate (spec 09 §1).
	AllowAutoMerge bool
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
	// Unreadable says the branch is protected in a way this credential may
	// not read in full: only Enabled and RequiredChecks are known, and the
	// other fields say nothing (issue #255).
	Unreadable bool
	// AdminOnly says some of the protection is kept where only an
	// administrator may read it — GitHub's classic branch protection — so a
	// run on any other credential cannot see it (issue #258). Read by an
	// admin, it is a thing flow can move; read by anybody else it is
	// Unreadable instead.
	AdminOnly bool
	// Immovable names what in the admin-only protection flow has no way to
	// carry anywhere else, so moving it would lose it. Empty when it can all
	// be moved.
	Immovable []string
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
	// ID is the provider's own identity for this status, as text. It is
	// what a record built out of a status is identified by, so a source
	// reading statuses as deployments has something to point at that the
	// provider would recognise (spec 13 §1).
	ID string
	// Context names the check, e.g. flow/policy.
	Context string
	// State is one of the status states above.
	State string
	// Description is the one-line summary, at most 140 characters.
	Description string
	// TargetURL points at the run that produced the verdict.
	TargetURL string
	// CreatedAt is when the status was published, and UpdatedAt when it was
	// last written. A context's history is one verdict's progress, so the
	// two differ whenever a check reported pending before it finished.
	CreatedAt time.Time
	UpdatedAt time.Time
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

// What a forge says about landing a pull request as it stands (spec 10 §2).
//
// This is the one thing about a pull request that goes wrong without anything
// going red: the checks that passed were computed on a head that has not
// changed, so they stay green for ever, and no forge sends mail about a
// conflict. Work that cannot land looks exactly like work that can.
const (
	// MergeabilityUnknown is the absence of a reading, not a reading that
	// the pull request is fine. GitHub computes mergeability in the
	// background and answers null until it has; every forge answers
	// nothing at all for a pull request it has finished with, and a
	// listing does not carry it.
	MergeabilityUnknown = ""
	// MergeabilityClean is the forge saying it would take it. Something
	// else may still be in the way — an approval owing, a check running —
	// but nothing that moving the branch would fix.
	MergeabilityClean = "clean"
	// MergeabilityConflicted is a pull request that no longer merges into
	// its base without somebody resolving the overlap.
	MergeabilityConflicted = "conflicted"
	// MergeabilityBehind is a pull request the forge would take once it is
	// level with the branch it targets. A forge reports it only where the
	// protection demands it — GitHub's strict required status checks,
	// GitLab's fast-forward merges — because everywhere else falling
	// behind the trunk is not a reason to refuse a merge.
	MergeabilityBehind = "behind"
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
	// SquashHeader is the commit header a squash merge of this pull request
	// would put on the trunk. It is not always the title: GitHub and Gitea
	// append the pull request's number, so a 66-character title lands as a
	// 73-character header and the convention applies to the header. GitLab
	// appends nothing and writes the title back unchanged. Only the adapter
	// knows which, so only the adapter fills this in; a listing carries it,
	// because a number and a title are all it is made of.
	SquashHeader string
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
	// Reviews are the reviews submitted on it, oldest first. A listing does
	// not carry them: spec 10 §2 reads reviews from the pull request's own
	// endpoint, so only [Forge.PullRequest] fills this in.
	Reviews []Review
	// ClosingIssues are the tickets the forge itself says this closes — for
	// GitHub, closingIssuesReferences. Trackers that have no such link leave
	// it empty and fall back to their own convention (spec 04 §3). Like
	// Reviews, it is a separate read and a listing leaves it empty.
	ClosingIssues []string
	// Files summarises what it changes.
	Files FileSummary
	// AutoMerge reports a pull request already set to merge itself when its
	// checks pass. Like Mergeability it is a reading of an open pull
	// request and nothing a listing carries, so only [Forge.PullRequest]
	// fills it in — which is what keeps flow from arming what is armed.
	AutoMerge bool
	// Mergeability is what the forge says about landing it as it stands,
	// one of the mergeability values above. Like Reviews it is a separate
	// read: no forge puts a reading flow can rely on in a listing, and a
	// pull request already merged or abandoned has none to give, so only
	// [Forge.PullRequest] fills it in and only for an open one.
	Mergeability string
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

// MergeHeader is the commit header the trunk will carry when this pull request
// is squashed: what the forge said it would write, and the title when the
// forge has said nothing.
//
// The fallback is the title rather than an error because a forge that appends
// nothing lands the title unchanged, which makes the title the right answer
// for every forge that has not been asked and for every pull request built by
// hand. What stops that from quietly hiding a silent adapter is the forge
// contract, which requires SquashHeader of every pull request an adapter
// produces.
func (p PullRequest) MergeHeader() string {
	if p.SquashHeader != "" {
		return p.SquashHeader
	}
	return p.Title
}

// Stuck reports whether the forge has said it would not take this pull
// request as it stands, and in a few words why.
//
// An unknown mergeability is not stuck. A forge that has not worked it out
// has not said no, and treating silence as a refusal would make a check fail
// hardest on the pull request somebody opened a moment ago.
func (p PullRequest) Stuck() (string, bool) {
	switch p.Mergeability {
	case MergeabilityConflicted:
		return "it conflicts with " + branchName(p.BaseRef), true
	case MergeabilityBehind:
		return "it is behind " + branchName(p.BaseRef) + ", which it has to be level with to merge", true
	default:
		return "", false
	}
}

// branchName is a base branch to put in a sentence, for a forge that did not
// say which one it was.
func branchName(ref string) string {
	if ref == "" {
		return "the branch it targets"
	}
	return ref
}

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
	// Commits are the commits head has and base does not, as the forge lists
	// them in the same answer (GitHub: up to 250). Empty where it lists none.
	Commits []string `json:"commits,omitempty"`
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

// SentBack reports whether anybody ever asked for changes on this pull
// request, including a review that a later one overtook or that was
// dismissed.
//
// It is the other question to [PullRequest.ChangesRequested], which asks
// whether the work is held up as it stands and is therefore false of
// everything that merged. Rework is a thing that happened: the review that
// asked for it counts for ever, and the approval that followed is what makes
// it rework rather than a refusal (spec 07 §6).
func (p PullRequest) SentBack() bool {
	for _, review := range p.Reviews {
		if review.State == ReviewChangesRequested {
			return true
		}
	}
	return false
}
