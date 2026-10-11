package forge

import (
	"slices"
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
