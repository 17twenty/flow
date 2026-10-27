package forge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// The wire types below are GitHub's REST payloads, named for the endpoint
// they come from and decoded with the standard library alone (spec 01 §4).
// Each knows how to become the core type flow actually works with, so that the
// provider's vocabulary stops here.

// ghRepo is GET /repos/{owner}/{repo}.
type ghRepo struct {
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	DefaultBranch            string `json:"default_branch"`
	HTMLURL                  string `json:"html_url"`
	Archived                 bool   `json:"archived"`
	AllowSquashMerge         bool   `json:"allow_squash_merge"`
	AllowMergeCommit         bool   `json:"allow_merge_commit"`
	AllowRebaseMerge         bool   `json:"allow_rebase_merge"`
	SquashMergeCommitTitle   string `json:"squash_merge_commit_title"`
	SquashMergeCommitMessage string `json:"squash_merge_commit_message"`
	DeleteBranchOnMerge      bool   `json:"delete_branch_on_merge"`
	AllowAutoMerge           bool   `json:"allow_auto_merge"`
}

// toRepoInfo converts the payload.
func (r ghRepo) toRepoInfo() core.RepoInfo {
	return core.RepoInfo{
		Owner:         r.Owner.Login,
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		URL:           r.HTMLURL,
		Archived:      r.Archived,
		Merge: core.MergeSettings{
			AllowSquash:         r.AllowSquashMerge,
			AllowMerge:          r.AllowMergeCommit,
			AllowRebase:         r.AllowRebaseMerge,
			SquashTitle:         squashTitleFromGitHub(r.SquashMergeCommitTitle),
			SquashMessage:       squashMessageFromGitHub(r.SquashMergeCommitMessage),
			DeleteBranchOnMerge: r.DeleteBranchOnMerge,
			AllowAutoMerge:      r.AllowAutoMerge,
		},
	}
}

// GitHub's squash-commit vocabulary (spec 10 §2).
const (
	ghSquashTitlePR        = "PR_TITLE"
	ghSquashTitleCommit    = "COMMIT_OR_PR_TITLE"
	ghSquashMessagePRBody  = "PR_BODY"
	ghSquashMessageCommits = "COMMIT_MESSAGES"
	ghSquashMessageBlank   = "BLANK"
)

// squashTitleFromGitHub maps GitHub's squash title setting onto flow's.
func squashTitleFromGitHub(raw string) string {
	switch raw {
	case ghSquashTitlePR:
		return core.SquashTitlePR
	case ghSquashTitleCommit:
		return core.SquashTitleCommit
	default:
		return ""
	}
}

// squashMessageFromGitHub maps GitHub's squash message setting onto flow's.
func squashMessageFromGitHub(raw string) string {
	switch raw {
	case ghSquashMessagePRBody:
		return core.SquashMessagePRBody
	case ghSquashMessageCommits:
		return core.SquashMessageCommits
	case ghSquashMessageBlank:
		return core.SquashMessageBlank
	default:
		return ""
	}
}

// ghPull is GET /repos/{owner}/{repo}/pulls[/{number}]. The list endpoint
// omits the file counts and says nothing about reviews.
type ghPull struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	State          string     `json:"state"`
	Draft          bool       `json:"draft"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	MergedAt       *time.Time `json:"merged_at"`
	ClosedAt       *time.Time `json:"closed_at"`
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	ChangedFiles int    `json:"changed_files"`
	HTMLURL      string `json:"html_url"`
	// Mergeable is null until GitHub has worked it out in the background,
	// and absent from a listing altogether, so it is a pointer: false and
	// "not said" are different answers and only one of them is a finding.
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	// AutoMerge is the pending request to land this pull request when its
	// checks pass, and null when there is none. Like Mergeable it is absent
	// from a listing, which is why nothing reads it from one.
	AutoMerge *struct {
		MergeMethod string `json:"merge_method"`
	} `json:"auto_merge"`
}

// GitHub's mergeable_state values flow has a use for. The rest — clean,
// blocked, unstable, has_hooks, draft — all mean the same thing to flow:
// nothing is in the way that moving the branch would fix.
const (
	// ghMergeableDirty is a conflict with the base branch.
	ghMergeableDirty = "dirty"
	// ghMergeableBehind is a branch out of date with a base that requires
	// it to be current. GitHub reports it only where the protection has
	// strict required status checks, which is exactly flow's question.
	ghMergeableBehind = "behind"
	// ghMergeableUnknown is GitHub still computing it.
	ghMergeableUnknown = "unknown"
)

// mergeabilityFromGitHub maps the pair GitHub answers with onto flow's.
//
// Both halves are read because they disagree usefully: mergeable_state is the
// specific reason and mergeable is the yes or no, and a state flow has no name
// for alongside mergeable false is still a conflict as far as anybody acting
// on it is concerned.
func mergeabilityFromGitHub(mergeable *bool, state string) string {
	switch {
	case state == ghMergeableDirty:
		return core.MergeabilityConflicted
	case mergeable == nil, state == "", state == ghMergeableUnknown:
		// Nothing below here may read through the pointer: GitHub
		// sends null while the background job that works it out has
		// not run, and a state flow has no name for alongside it is
		// still GitHub declining to say.
		return core.MergeabilityUnknown
	case !*mergeable:
		return core.MergeabilityConflicted
	case state == ghMergeableBehind:
		return core.MergeabilityBehind
	default:
		return core.MergeabilityClean
	}
}

// toPullRequest converts the payload. GitHub has no merged state: a merged
// pull request is a closed one that carries a merge time.
//
// The squash header is composed rather than read, because GitHub does not
// serve it: with squash_merge_commit_title set to PR_TITLE the merge box
// offers "<title> (#<number>)" and that is what lands. It is composed for
// every pull request, open or merged, since the gate's whole job is to judge
// the header before it exists.
func (p ghPull) toPullRequest() core.PullRequest {
	pr := core.PullRequest{
		Number:       p.Number,
		Title:        p.Title,
		SquashHeader: fmt.Sprintf("%s (#%d)", p.Title, p.Number),
		Body:         p.Body,
		Author:       p.User.Login,
		State:        core.PRStateOpen,
		Draft:        p.Draft,
		HeadSHA:      p.Head.SHA,
		HeadRef:      p.Head.Ref,
		BaseRef:      p.Base.Ref,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
		URL:          p.HTMLURL,
		Files:        core.FileSummary{Changed: p.ChangedFiles},
	}
	for _, label := range p.Labels {
		pr.Labels = append(pr.Labels, label.Name)
	}
	if p.ClosedAt != nil {
		pr.ClosedAt = *p.ClosedAt
		pr.State = core.PRStateClosed
	}
	if p.MergedAt != nil {
		pr.MergedAt = *p.MergedAt
		pr.State = core.PRStateMerged
		pr.MergeSHA = p.MergeCommitSHA
	}
	if pr.State == core.PRStateOpen {
		// Only an open pull request has a mergeability worth reading.
		// GitHub keeps answering for a closed one, with whatever it last
		// computed, and a merged pull request reported as conflicted is
		// a reading that can only mislead whoever acts on it.
		pr.Mergeability = mergeabilityFromGitHub(p.Mergeable, p.MergeableState)
		pr.AutoMerge = p.AutoMerge != nil
	}
	return pr
}

// ghReview is GET /repos/{owner}/{repo}/pulls/{number}/reviews.
type ghReview struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

// ghReviewStates maps GitHub's review vocabulary onto spec 04 §4's. PENDING is
// absent deliberately: a review not yet submitted is not a verdict.
var ghReviewStates = map[string]string{
	"APPROVED":          core.ReviewApproved,
	"CHANGES_REQUESTED": core.ReviewChangesRequested,
	"COMMENTED":         core.ReviewCommented,
	"DISMISSED":         core.ReviewDismissed,
}

// toReview converts the payload, reporting false for anything that is not a
// submitted verdict.
func (r ghReview) toReview() (core.Review, bool) {
	state, ok := ghReviewStates[strings.ToUpper(r.State)]
	if !ok {
		return core.Review{}, false
	}
	review := core.Review{Author: r.User.Login, State: state}
	if r.SubmittedAt != nil {
		review.SubmittedAt = *r.SubmittedAt
	}
	return review, true
}

// ghFile is GET /repos/{owner}/{repo}/pulls/{number}/files.
type ghFile struct {
	Filename string `json:"filename"`
}

// ghCommit is GET /repos/{owner}/{repo}/commits.
type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	HTMLURL string `json:"html_url"`
}

// toCommit converts the payload. The author is the GitHub login when the
// commit is attributed to an account, and the name on the commit otherwise —
// an unattributed commit still has an author.
func (c ghCommit) toCommit() core.Commit {
	author := c.Commit.Author.Name
	if c.Author != nil && c.Author.Login != "" {
		author = c.Author.Login
	}
	return core.Commit{
		SHA:         c.SHA,
		Message:     c.Commit.Message,
		Author:      author,
		AuthoredAt:  c.Commit.Author.Date,
		CommittedAt: c.Commit.Committer.Date,
		URL:         c.HTMLURL,
	}
}

// ghCompare is GET /repos/{owner}/{repo}/compare/{base}...{head}.
type ghCompare struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
	Commits  []struct {
		SHA string `json:"sha"`
	} `json:"commits"`
}

// ghCompareStatuses are the statuses GitHub's compare endpoint reports, which
// core adopts verbatim (spec 10 §2).
var ghCompareStatuses = []string{
	core.CompareIdentical, core.CompareAhead, core.CompareBehind, core.CompareDiverged,
}

// known reports whether the comparison status is one flow understands.
func (c ghCompare) known() bool { return slices.Contains(ghCompareStatuses, c.Status) }

// ghStatus is GET /repos/{owner}/{repo}/commits/{sha}/statuses, which GitHub
// serves newest first.
type ghStatus struct {
	ID          int64     `json:"id"`
	Context     string    `json:"context"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	TargetURL   string    `json:"target_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toCommitStatus converts the payload.
//
// An absent identity stays absent rather than becoming "0": a record built
// out of a status points at it by this, and a link to status zero is worse
// than no link at all. The dates are taken as given — GitHub writes both on
// every status — so a status with no date reads as one, which the forge
// contract refuses rather than papering over.
func (s ghStatus) toCommitStatus() core.CommitStatus {
	id := ""
	if s.ID != 0 {
		id = strconv.FormatInt(s.ID, 10)
	}
	return core.CommitStatus{
		ID:          id,
		Context:     s.Context,
		State:       s.State,
		Description: s.Description,
		TargetURL:   s.TargetURL,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
}

// ghCheckRuns is GET /repos/{owner}/{repo}/commits/{sha}/check-runs.
type ghCheckRuns struct {
	CheckRuns []ghCheckRun `json:"check_runs"`
}

// ghCheckRun is one check run.
type ghCheckRun struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	DetailsURL  string    `json:"details_url"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// toCommitStatus reads a check run as the status it amounts to.
func (r ghCheckRun) toCommitStatus() core.CommitStatus {
	state := core.StatusPending
	if r.Status == "completed" {
		switch r.Conclusion {
		case "success", "neutral", "skipped":
			state = core.StatusSuccess
		default:
			state = core.StatusFailure
		}
	}
	updated := r.CompletedAt
	if updated.IsZero() {
		updated = r.StartedAt
	}
	return core.CommitStatus{
		ID:        strconv.FormatInt(r.ID, 10),
		Context:   r.Name,
		State:     state,
		TargetURL: r.DetailsURL,
		CreatedAt: r.StartedAt,
		UpdatedAt: updated,
	}
}

// ghStatusRequest is POST /repos/{owner}/{repo}/statuses/{sha}.
type ghStatusRequest struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

// ghComment is GET /repos/{owner}/{repo}/issues/{number}/comments. A pull
// request is an issue to GitHub's comment endpoints.
type ghComment struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
}

// toComment converts the payload, reporting false for a comment that does not
// carry a marker the wanted one matches — which is every comment a person
// wrote.
func (c ghComment) toComment(want core.Marker) (core.Comment, bool) {
	marker, ok := core.FindMarker(c.Body)
	if !ok || !want.Matches(marker) {
		return core.Comment{}, false
	}
	return core.Comment{
		ID:        strconv.FormatInt(c.ID, 10),
		Author:    c.User.Login,
		Body:      c.Body,
		Marker:    marker,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
		URL:       c.HTMLURL,
	}, true
}

// ghCommentRequest is the body of POST and PATCH on a comment.
type ghCommentRequest struct {
	Body string `json:"body"`
}

// ghToggle is GitHub's way of saying a boolean in branch protection: an
// object with one field, so that the setting can carry more later.
type ghToggle struct {
	Enabled bool `json:"enabled"`
}

// ghProtection is GET /repos/{owner}/{repo}/branches/{branch}/protection, the
// classic mechanism. A branch with none answers 404.
type ghProtection struct {
	RequiredStatusChecks *struct {
		Strict   bool     `json:"strict"`
		Contexts []string `json:"contexts"`
		Checks   []struct {
			Context string `json:"context"`
		} `json:"checks"`
	} `json:"required_status_checks"`
	RequiredPullRequestReviews *struct {
		DismissStaleReviews          bool `json:"dismiss_stale_reviews"`
		RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
	} `json:"required_pull_request_reviews"`
	EnforceAdmins                  ghToggle `json:"enforce_admins"`
	RequiredLinearHistory          ghToggle `json:"required_linear_history"`
	AllowForcePushes               ghToggle `json:"allow_force_pushes"`
	AllowDeletions                 ghToggle `json:"allow_deletions"`
	RequiredConversationResolution ghToggle `json:"required_conversation_resolution"`
}

// toProtection converts the payload. A protected branch permits nothing it
// does not say it permits, which is the opposite of an unprotected one.
func (p ghProtection) toProtection(branch string) core.Protection {
	protection := core.Protection{
		Branch:                        branch,
		Enabled:                       true,
		EnforceAdmins:                 p.EnforceAdmins.Enabled,
		RequireLinearHistory:          p.RequiredLinearHistory.Enabled,
		RequireConversationResolution: p.RequiredConversationResolution.Enabled,
		AllowForcePush:                p.AllowForcePushes.Enabled,
		AllowDeletions:                p.AllowDeletions.Enabled,
	}
	if checks := p.RequiredStatusChecks; checks != nil {
		protection.StrictChecks = checks.Strict
		// checks[] is the newer field and contexts[] the deprecated one;
		// GitHub answers with both, and a context named in either is
		// required.
		for _, check := range checks.Checks {
			protection.RequiredChecks = append(protection.RequiredChecks, check.Context)
		}
		for _, context := range checks.Contexts {
			if !slices.Contains(protection.RequiredChecks, context) {
				protection.RequiredChecks = append(protection.RequiredChecks, context)
			}
		}
		slices.Sort(protection.RequiredChecks)
	}
	if reviews := p.RequiredPullRequestReviews; reviews != nil {
		protection.DismissStaleReviews = reviews.DismissStaleReviews
		protection.RequiredApprovals = reviews.RequiredApprovingReviewCount
	}
	return protection
}

// Rule types of GET /repos/{owner}/{repo}/rules/branches/{branch}, the
// ruleset mechanism. A rule flow has no name for is somebody else's and is
// left out of what flow reports.
const (
	ghRuleStatusChecks   = "required_status_checks"
	ghRulePullRequest    = "pull_request"
	ghRuleLinearHistory  = "required_linear_history"
	ghRuleNonFastForward = "non_fast_forward"
	ghRuleDeletion       = "deletion"
)

// ghRule is one effective rule. The parameters differ per type, so they are
// decoded once the type is known.
type ghRule struct {
	Type       string          `json:"type"`
	Parameters json.RawMessage `json:"parameters"`
}

// ghStatusCheckRule is the parameters of a required_status_checks rule.
type ghStatusCheckRule struct {
	Strict bool `json:"strict_required_status_checks_policy"`
	Checks []struct {
		Context string `json:"context"`
	} `json:"required_status_checks"`
}

// ghPullRequestRule is the parameters of a pull_request rule.
type ghPullRequestRule struct {
	RequiredApprovals       int  `json:"required_approving_review_count"`
	DismissStaleReviews     bool `json:"dismiss_stale_reviews_on_push"`
	RequireThreadResolution bool `json:"required_review_thread_resolution"`
}

// ghEnforcementActive is the one enforcement value that makes a ruleset do
// anything. `disabled` is what GitHub creates one as; `evaluate` reports
// without blocking, which for flow's purposes is the same as off.
const ghEnforcementActive = "active"

// Ref name conditions GitHub spells with a leading `~`.
const (
	ghRefAll           = "~ALL"
	ghRefDefaultBranch = "~DEFAULT_BRANCH"
)

// ghRulesetSummary is a ruleset as the list endpoint gives it: enough to know
// whether it is worth fetching in full, and nothing else. Conditions are not
// in the list, which is why a dormant one costs a second call.
type ghRulesetSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
}

// ghRuleset is one ruleset in full.
type ghRuleset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []ghRule `json:"rules"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"_links"`
}

// covers reports whether this ruleset would apply to a branch if it were
// enforced.
//
// Only branch rulesets are considered: a tag ruleset has nothing to say about
// a trunk. The patterns are GitHub's own — `~ALL`, `~DEFAULT_BRANCH`, or a
// `refs/heads/` pattern — and an exclusion beats an inclusion, which is how
// GitHub reads them too.
func (r ghRuleset) covers(branch, defaultBranch string) bool {
	if r.Target != "" && r.Target != "branch" {
		return false
	}
	for _, pattern := range r.Conditions.RefName.Exclude {
		if ghRefMatches(pattern, branch, defaultBranch) {
			return false
		}
	}
	for _, pattern := range r.Conditions.RefName.Include {
		if ghRefMatches(pattern, branch, defaultBranch) {
			return true
		}
	}
	return false
}

// ghRefMatches reports whether one ref-name pattern names a branch.
//
// GitHub's own two names come first, and the rest is fnmatch over the branch
// name, in which `*` stands for any run of characters within one path segment
// — which is what [path.Match] means by it too. `**`, which crosses a slash,
// is not read at all: a pattern flow cannot read is treated as not matching,
// because the consequence of guessing wrong is a remedy pointing at a ruleset
// that would never have applied, and that is somebody's afternoon.
func ghRefMatches(pattern, branch, defaultBranch string) bool {
	switch pattern {
	case ghRefAll:
		return true
	case ghRefDefaultBranch:
		return defaultBranch != "" && branch == defaultBranch
	}
	name, ok := strings.CutPrefix(pattern, "refs/heads/")
	if !ok || strings.Contains(name, "**") {
		return false
	}
	matched, err := path.Match(name, branch)
	return err == nil && matched
}

// rulesToProtection folds the effective rules into one protection. Rules only
// ever forbid, so the branch starts out permitting what no rule denies.
func rulesToProtection(branch string, rules []ghRule) core.Protection {
	protection := core.Protection{
		Branch:         branch,
		Enabled:        true,
		AllowForcePush: true,
		AllowDeletions: true,
	}
	for _, rule := range rules {
		switch rule.Type {
		case ghRuleStatusChecks:
			var params ghStatusCheckRule
			if json.Unmarshal(rule.Parameters, &params) != nil {
				continue
			}
			protection.StrictChecks = protection.StrictChecks || params.Strict
			for _, check := range params.Checks {
				if check.Context != "" && !protection.RequiresCheck(check.Context) {
					protection.RequiredChecks = append(protection.RequiredChecks, check.Context)
				}
			}
		case ghRulePullRequest:
			var params ghPullRequestRule
			if json.Unmarshal(rule.Parameters, &params) != nil {
				continue
			}
			protection.RequiredApprovals = max(protection.RequiredApprovals, params.RequiredApprovals)
			protection.DismissStaleReviews = protection.DismissStaleReviews || params.DismissStaleReviews
			protection.RequireConversationResolution = protection.RequireConversationResolution || params.RequireThreadResolution
		case ghRuleLinearHistory:
			protection.RequireLinearHistory = true
		case ghRuleNonFastForward:
			protection.AllowForcePush = false
		case ghRuleDeletion:
			protection.AllowDeletions = false
		}
	}
	slices.Sort(protection.RequiredChecks)
	return protection
}

// ghBranch is GET /repos/{owner}/{repo}/branches/{branch}, which anybody who
// can read the repository may ask.
type ghBranch struct {
	Protected  bool `json:"protected"`
	Protection struct {
		RequiredStatusChecks struct {
			Contexts []string `json:"contexts"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

// ghRulesetRequest is POST /repos/{owner}/{repo}/rulesets, and the body of a
// PUT to one.
type ghRulesetRequest struct {
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []map[string]any `json:"rules"`
}

// protectionToRules is the inverse of [rulesToProtection]: the rules that say
// what a protection says. Approvals and conversation resolution travel in one
// pull_request rule; force pushes and deletions are forbidden by a rule each,
// and allowed by its absence.
func protectionToRules(p core.Protection) []map[string]any {
	var rules []map[string]any
	if len(p.RequiredChecks) > 0 || p.StrictChecks {
		checks := make([]map[string]any, 0, len(p.RequiredChecks))
		for _, context := range p.RequiredChecks {
			checks = append(checks, map[string]any{"context": context})
		}
		rules = append(rules, map[string]any{"type": ghRuleStatusChecks, "parameters": map[string]any{
			"strict_required_status_checks_policy": p.StrictChecks,
			"required_status_checks":               checks,
		}})
	}
	if p.RequiredApprovals > 0 || p.DismissStaleReviews || p.RequireConversationResolution {
		rules = append(rules, map[string]any{"type": ghRulePullRequest, "parameters": map[string]any{
			"required_approving_review_count":   p.RequiredApprovals,
			"dismiss_stale_reviews_on_push":     p.DismissStaleReviews,
			"required_review_thread_resolution": p.RequireConversationResolution,
			"require_code_owner_review":         false,
			"require_last_push_approval":        false,
		}})
	}
	if p.RequireLinearHistory {
		rules = append(rules, map[string]any{"type": ghRuleLinearHistory})
	}
	if !p.AllowForcePush {
		rules = append(rules, map[string]any{"type": ghRuleNonFastForward})
	}
	if !p.AllowDeletions {
		rules = append(rules, map[string]any{"type": ghRuleDeletion})
	}
	return rules
}

// squashTitleToGitHub maps flow's squash title setting onto GitHub's. A
// setting flow does not name is sent as nothing, so that the repository keeps
// what it had rather than being given a value nobody chose.
func squashTitleToGitHub(title string) string {
	switch title {
	case core.SquashTitlePR:
		return ghSquashTitlePR
	case core.SquashTitleCommit:
		return ghSquashTitleCommit
	default:
		return ""
	}
}

// squashMessageToGitHub maps flow's squash message setting onto GitHub's.
func squashMessageToGitHub(message string) string {
	switch message {
	case core.SquashMessagePRBody:
		return ghSquashMessagePRBody
	case core.SquashMessageCommits:
		return ghSquashMessageCommits
	case core.SquashMessageBlank:
		return ghSquashMessageBlank
	default:
		return ""
	}
}

// mergeRequest renders the merge settings flow wants as GitHub's own fields.
func mergeRequest(s core.MergeSettings) ghRepoRequest {
	return ghRepoRequest{
		AllowSquashMerge:         s.AllowSquash,
		AllowMergeCommit:         s.AllowMerge,
		AllowRebaseMerge:         s.AllowRebase,
		SquashMergeCommitTitle:   squashTitleToGitHub(s.SquashTitle),
		SquashMergeCommitMessage: squashMessageToGitHub(s.SquashMessage),
		DeleteBranchOnMerge:      s.DeleteBranchOnMerge,
		AllowAutoMerge:           asked(s.AllowAutoMerge),
	}
}

// asked renders a setting flow only ever turns on. A configuration that does
// not want auto-merge asks for nothing rather than for a "no" (spec 06 §4),
// so a false here is the absence of a request and not a request for the
// absence — and a field left out of the payload is a setting GitHub leaves
// exactly as it found it.
func asked(on bool) *bool {
	if !on {
		return nil
	}
	return &on
}

// ghRef is GET /repos/{owner}/{repo}/git/ref/{ref}.
type ghRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

// ghRefRequest is POST /repos/{owner}/{repo}/git/refs.
type ghRefRequest struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// ghContents is GET /repos/{owner}/{repo}/contents/{path} asked for as JSON,
// which carries the blob's identity as well as its bytes. The identity is
// what an update has to quote, so this is read even though [GitHub.File]
// fetches the bytes raw.
type ghContents struct {
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// decoded returns the file's bytes, reporting false when GitHub sent an
// encoding it did not promise or left the content out, which it does for a
// file too large to inline.
func (c ghContents) decoded() ([]byte, bool) {
	if c.Encoding != "base64" {
		return nil, false
	}
	// GitHub wraps the base64 at 60 columns, which the decoder will not
	// have.
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.Content), ""))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// ghContentsRequest is PUT /repos/{owner}/{repo}/contents/{path}. SHA names
// the blob being replaced and is left out when the file is new.
type ghContentsRequest struct {
	Message string `json:"message"`
	Content string `json:"content"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha,omitempty"`
}

// ghPullCreate is POST /repos/{owner}/{repo}/pulls.
type ghPullCreate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Head  string `json:"head"`
	Base  string `json:"base"`
}

// ghPullUpdate is PATCH /repos/{owner}/{repo}/pulls/{number}.
type ghPullUpdate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// ghRepoRequest is the body of PATCH /repos/{owner}/{repo}. Merge strategies
// are one setting written whole (spec 06 §4), so every field is sent.
type ghRepoRequest struct {
	AllowSquashMerge         bool   `json:"allow_squash_merge"`
	AllowMergeCommit         bool   `json:"allow_merge_commit"`
	AllowRebaseMerge         bool   `json:"allow_rebase_merge"`
	SquashMergeCommitTitle   string `json:"squash_merge_commit_title,omitempty"`
	SquashMergeCommitMessage string `json:"squash_merge_commit_message,omitempty"`
	DeleteBranchOnMerge      bool   `json:"delete_branch_on_merge"`
	AllowAutoMerge           *bool  `json:"allow_auto_merge,omitempty"`
}

// The rest of this file is GraphQL's, not REST's. The envelope itself is
// [core.GraphQLResponse], since every adapter that speaks GraphQL unwraps the
// same one; what stays here is GitHub's own reading of it and the shapes of
// the few queries flow sends (spec 10 §2).

// gitHubGraphQLSentinel is the core error a GraphQL error type means.
// GitHub's documented types map onto flow's own; anything else is the
// provider refusing for a reason flow has no name for, which is unavailable
// rather than invalid input, because the query is flow's and was not the
// user's to get wrong.
func gitHubGraphQLSentinel(errType string) error {
	switch errType {
	case "NOT_FOUND":
		return core.ErrNotFound
	case "FORBIDDEN", "INSUFFICIENT_SCOPES", "UNAUTHORIZED":
		return core.ErrPermission
	case "RATE_LIMITED":
		return core.ErrRateLimited
	case "UNPROCESSABLE":
		// The query was understood and the thing asked for cannot be
		// done to this subject: a repository with auto-merge switched
		// off, a draft that has nothing to wait for. It is the caller's
		// request that is wrong, not the provider that is unavailable,
		// and only the provider could have known.
		return core.ErrValidation
	default:
		return core.ErrUnavailable
	}
}

// GitHub's PullRequestMergeMethod enum, which is the merge strategies again
// in capitals. The mapping is written out rather than upper-cased, so that a
// strategy flow grows a name for does not silently become a GraphQL value
// that does not exist.
func gitHubMergeMethod(method string) (string, bool) {
	switch method {
	case core.MergeMethodSquash:
		return "SQUASH", true
	case core.MergeMethodMerge:
		return "MERGE", true
	case core.MergeMethodRebase:
		return "REBASE", true
	default:
		return "", false
	}
}

// ghAutoMergeState is the data of the auto-merge query: the node id the
// mutation needs, and the pending request if there already is one.
type ghAutoMergeState struct {
	Repository struct {
		PullRequest *struct {
			ID               string `json:"id"`
			AutoMergeRequest *struct {
				EnabledAt string `json:"enabledAt"`
			} `json:"autoMergeRequest"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// ghEnableAutoMerge is the data of the arming mutation. Nothing reads it: the
// absence of an error is the answer, and a type is still wanted because the
// GraphQL helper decodes into one.
type ghEnableAutoMerge struct {
	EnablePullRequestAutoMerge struct {
		PullRequest struct {
			AutoMergeRequest *struct {
				EnabledAt string `json:"enabledAt"`
			} `json:"autoMergeRequest"`
		} `json:"pullRequest"`
	} `json:"enablePullRequestAutoMerge"`
}

// ghClosingIssues is the data of the closingIssuesReferences query: the
// tickets GitHub itself says a pull request closes, one page at a time.
type ghClosingIssues struct {
	Repository struct {
		PullRequest *struct {
			ClosingIssuesReferences struct {
				Nodes []struct {
					Number     int `json:"number"`
					Repository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"repository"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"closingIssuesReferences"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// keys returns the page's tickets as flow writes them. An issue in the
// repository being spoken for is "#7", as everybody writes it; an issue
// anywhere else is "owner/repo#7", which is how GitHub itself writes a
// reference it has to qualify — and the only form the tracker can resolve
// later without having to guess which repository was meant (spec 04 §3).
func (c ghClosingIssues) keys(owner, repo string) []string {
	if c.Repository.PullRequest == nil {
		return nil
	}
	nodes := c.Repository.PullRequest.ClosingIssuesReferences.Nodes
	here := owner + "/" + repo
	keys := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Number <= 0 {
			continue
		}
		key := "#" + strconv.Itoa(node.Number)
		if slug := node.Repository.NameWithOwner; slug != "" && !strings.EqualFold(slug, here) {
			key = slug + key
		}
		keys = append(keys, key)
	}
	return keys
}

// ghProbe is the part of GET /repos/{owner}/{repo} a probe reads: who the
// credential is to this repository, and what the repository is (spec 05 §3).
type ghProbe struct {
	Private     bool              `json:"private"`
	Visibility  string            `json:"visibility"`
	Permissions ghRepoPermissions `json:"permissions"`
}

// public reports whether the repository is one a `public_repo` scope covers.
// GitHub sends both fields; `visibility` is the newer and more precise, since
// an internal repository on an enterprise is not private and not public
// either.
func (p ghProbe) public() bool {
	if p.Visibility != "" {
		return p.Visibility == "public"
	}
	return !p.Private
}

// ghRepoPermissions is the role GitHub reports the credential holds on a
// repository. GitHub sends the whole implied set — an admin is also a
// maintainer — but an older GitHub Enterprise Server sends only the three it
// knew about, so the implication is applied here rather than trusted.
type ghRepoPermissions struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

// writes reports whether the role may change the repository.
func (p ghRepoPermissions) writes() bool { return p.Admin || p.Maintain || p.Push }

// tokens are the role's permission tokens, strongest first.
func (p ghRepoPermissions) tokens() []string {
	var tokens []string
	if p.Admin {
		tokens = append(tokens, "admin")
	}
	if p.Admin || p.Maintain {
		tokens = append(tokens, "maintain")
	}
	if p.writes() {
		tokens = append(tokens, "push")
	}
	if p.writes() || p.Triage {
		tokens = append(tokens, "triage")
	}
	if p.writes() || p.Triage || p.Pull {
		tokens = append(tokens, "pull")
	}
	return tokens
}

// String renders the role for the evidence line.
func (p ghRepoPermissions) String() string {
	tokens := p.tokens()
	if len(tokens) == 0 {
		return "none"
	}
	return strings.Join(tokens, ", ")
}
