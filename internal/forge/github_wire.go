package forge

import (
	"encoding/base64"
	"encoding/json"
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
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	HTMLURL      string `json:"html_url"`
}

// toPullRequest converts the payload. GitHub has no merged state: a merged
// pull request is a closed one that carries a merge time.
func (p ghPull) toPullRequest() core.PullRequest {
	pr := core.PullRequest{
		Number:    p.Number,
		Title:     p.Title,
		Body:      p.Body,
		Author:    p.User.Login,
		State:     core.PRStateOpen,
		Draft:     p.Draft,
		HeadSHA:   p.Head.SHA,
		HeadRef:   p.Head.Ref,
		BaseRef:   p.Base.Ref,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		URL:       p.HTMLURL,
		Files: core.FileSummary{
			Changed:   p.ChangedFiles,
			Additions: p.Additions,
			Deletions: p.Deletions,
		},
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
	Context     string    `json:"context"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	TargetURL   string    `json:"target_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toCommitStatus converts the payload.
func (s ghStatus) toCommitStatus() core.CommitStatus {
	return core.CommitStatus{
		Context:     s.Context,
		State:       s.State,
		Description: s.Description,
		TargetURL:   s.TargetURL,
		CreatedAt:   s.CreatedAt,
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

// ghProtectionRequest is the body of PUT on a branch's protection. Every field
// is required and nullable, so each one is a pointer and none may be omitted:
// a key left out is read as null, which turns that protection off.
type ghProtectionRequest struct {
	RequiredStatusChecks *struct {
		Strict   bool     `json:"strict"`
		Contexts []string `json:"contexts"`
	} `json:"required_status_checks"`
	EnforceAdmins              *bool `json:"enforce_admins"`
	RequiredPullRequestReviews *struct {
		DismissStaleReviews          bool `json:"dismiss_stale_reviews"`
		RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
	} `json:"required_pull_request_reviews"`
	Restrictions                   *struct{} `json:"restrictions"`
	RequiredLinearHistory          bool      `json:"required_linear_history"`
	AllowForcePushes               bool      `json:"allow_force_pushes"`
	AllowDeletions                 bool      `json:"allow_deletions"`
	RequiredConversationResolution bool      `json:"required_conversation_resolution"`
}

// protectionRequest renders the protection flow wants as the body GitHub
// insists on: the whole state, every time.
func protectionRequest(p core.Protection) ghProtectionRequest {
	body := ghProtectionRequest{
		EnforceAdmins:                  &p.EnforceAdmins,
		RequiredLinearHistory:          p.RequireLinearHistory,
		AllowForcePushes:               p.AllowForcePush,
		AllowDeletions:                 p.AllowDeletions,
		RequiredConversationResolution: p.RequireConversationResolution,
	}
	if len(p.RequiredChecks) > 0 || p.StrictChecks {
		body.RequiredStatusChecks = &struct {
			Strict   bool     `json:"strict"`
			Contexts []string `json:"contexts"`
		}{Strict: p.StrictChecks, Contexts: p.RequiredChecks}
		if body.RequiredStatusChecks.Contexts == nil {
			body.RequiredStatusChecks.Contexts = []string{}
		}
	}
	if p.RequiredApprovals > 0 || p.DismissStaleReviews {
		body.RequiredPullRequestReviews = &struct {
			DismissStaleReviews          bool `json:"dismiss_stale_reviews"`
			RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
		}{DismissStaleReviews: p.DismissStaleReviews, RequiredApprovingReviewCount: p.RequiredApprovals}
	}
	return body
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
	}
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
}

// The types below are GraphQL's, not REST's. GitHub answers a GraphQL request
// with 200 whatever happened, and says what went wrong in an "errors" array
// beside the data, so the envelope is part of the wire format rather than
// something the HTTP client can be left to notice (spec 10 §2).

// ghGraphQLRequest is the body of POST /graphql.
type ghGraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// ghGraphQLError is one entry of the errors array.
type ghGraphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// sentinel is the core error a GraphQL error type means. GitHub's documented
// types map onto flow's own; anything else is the provider refusing for a
// reason flow has no name for, which is unavailable rather than invalid input,
// because the query is flow's and was not the user's to get wrong.
func (e ghGraphQLError) sentinel() error {
	switch e.Type {
	case "NOT_FOUND":
		return core.ErrNotFound
	case "FORBIDDEN", "INSUFFICIENT_SCOPES", "UNAUTHORIZED":
		return core.ErrPermission
	case "RATE_LIMITED":
		return core.ErrRateLimited
	default:
		return core.ErrUnavailable
	}
}

// ghGraphQLResponse is the envelope every GraphQL answer arrives in.
type ghGraphQLResponse[T any] struct {
	Data   T                `json:"data"`
	Errors []ghGraphQLError `json:"errors"`
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
