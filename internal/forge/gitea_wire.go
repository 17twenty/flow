package forge

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// The wire types below are Gitea's /api/v1 payloads, named for the endpoint
// they come from and decoded with the standard library alone (spec 01 §4).
// Forgejo is Gitea's fork and serves the same API, so one set of types speaks
// for both. Each knows how to become the core type flow actually works with,
// so that the provider's vocabulary stops here.

// gtUser is the account object Gitea embeds everywhere.
type gtUser struct {
	Login string `json:"login"`
}

// gtPermissions is the `permissions` object on a repository: what the
// credential that asked for it may do with it.
type gtPermissions struct {
	Admin bool `json:"admin"`
	Push  bool `json:"push"`
	Pull  bool `json:"pull"`
}

// tokens returns the permission words the capability declaration is written
// in (assets/capabilities/gitea.yaml). A probe and a declaration that do not
// share a vocabulary can never agree (spec 05 §1).
func (p gtPermissions) tokens() []string {
	var tokens []string
	if p.Pull {
		tokens = append(tokens, giteaTokenRead)
	}
	if p.Push || p.Admin {
		tokens = append(tokens, giteaTokenWrite)
	}
	if p.Admin {
		tokens = append(tokens, giteaTokenAdmin)
	}
	return tokens
}

// String names the permissions for the evidence line of a probe.
func (p gtPermissions) String() string {
	words := p.tokens()
	if len(words) == 0 {
		return "none"
	}
	return strings.Join(words, ", ")
}

// gtExternalTracker is the `external_tracker` object, present only when the
// repository sends its issues somewhere else. Gitea writes `!` rather than
// `#` in front of a pull request's number when it does, which changes the
// header a squash lands (spec 10 §3).
type gtExternalTracker struct {
	URL string `json:"external_tracker_url"`
}

// gtRepo is GET /repos/{owner}/{repo}.
type gtRepo struct {
	Name  string `json:"name"`
	Owner gtUser `json:"owner"`

	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	Archived      bool   `json:"archived"`

	AllowSquashMerge bool `json:"allow_squash_merge"`
	AllowMergeCommit bool `json:"allow_merge_commits"`
	AllowRebase      bool `json:"allow_rebase"`
	DeleteBranch     bool `json:"default_delete_branch_after_merge"`

	ExternalTracker *gtExternalTracker `json:"external_tracker"`
	Permissions     gtPermissions      `json:"permissions"`
}

// toRepoInfo converts the payload.
//
// Where a squash takes its words from is not a setting Gitea has: it composes
// the commit itself, taking the header from the pull request's title and
// leaving the body to its own trailers. So the two fields are constants here
// rather than a reading, and `squash_text.set: false` in the declaration is
// what stops flow proposing to change them (spec 05 §2, spec 10 §3).
//
// Auto-merge is a constant for the opposite reason: Gitea has no setting that
// could forbid it, so every repository permits it and "true" is the only
// honest reading. A false here would describe a refusal no Gitea has ever
// made, and flow would propose a change to a setting that does not exist.
func (r gtRepo) toRepoInfo() core.RepoInfo {
	return core.RepoInfo{
		Owner:         r.Owner.Login,
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		URL:           r.HTMLURL,
		Archived:      r.Archived,
		Merge: core.MergeSettings{
			AllowSquash:         r.AllowSquashMerge,
			AllowMerge:          r.AllowMergeCommit,
			AllowRebase:         r.AllowRebase,
			SquashTitle:         core.SquashTitlePR,
			SquashMessage:       core.SquashMessageBlank,
			DeleteBranchOnMerge: r.DeleteBranch,
			AllowAutoMerge:      true,
		},
	}
}

// reference is the character Gitea puts in front of a pull request's number
// in the header it composes. It is `!` for a repository whose issues live in
// another tracker and `#` for every other one.
func (r gtRepo) reference() string {
	if r.ExternalTracker != nil && r.ExternalTracker.URL != "" {
		return giteaExternalReference
	}
	return giteaReference
}

// gtRepoRequest is PATCH /repos/{owner}/{repo}, carrying only the merge
// strategies flow manages. Gitea reads an absent field as "leave it alone",
// so naming four fields is how a write stays about four settings.
type gtRepoRequest struct {
	AllowSquashMerge bool `json:"allow_squash_merge"`
	AllowMergeCommit bool `json:"allow_merge_commits"`
	AllowRebase      bool `json:"allow_rebase"`
	DeleteBranch     bool `json:"default_delete_branch_after_merge"`
}

// mergeRequestGitea turns the wanted merge settings into Gitea's vocabulary.
func mergeRequestGitea(s core.MergeSettings) gtRepoRequest {
	return gtRepoRequest{
		AllowSquashMerge: s.AllowSquash,
		AllowMergeCommit: s.AllowMerge,
		AllowRebase:      s.AllowRebase,
		DeleteBranch:     s.DeleteBranchOnMerge,
	}
}

// gtMergeRequest is the body of POST /repos/{owner}/{repo}/pulls/{index}/merge.
//
// The same endpoint merges now and merges later: with MergeWhenChecksSucceed
// the forge schedules it, and without it the pull request lands immediately.
// Flow only ever sends the first, and sends it explicitly rather than relying
// on a zero value, because the field being dropped from the payload would
// turn an arming into a merge.
type gtMergeRequest struct {
	Do                     string `json:"Do"`
	MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed"`
}

// giteaMergeStyle is Gitea's name for a merge method. Gitea offers more of
// them than flow has names for; the ones flow does name map one to one.
func giteaMergeStyle(method string) (string, bool) {
	switch method {
	case core.MergeMethodSquash:
		return "squash", true
	case core.MergeMethodMerge:
		return "merge", true
	case core.MergeMethodRebase:
		return "rebase", true
	default:
		return "", false
	}
}

// gtBranchInfo is the `head` and `base` object of a pull request.
type gtBranchInfo struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// gtLabel is one label on a pull request.
type gtLabel struct {
	Name string `json:"name"`
}

// gtPull is GET /repos/{owner}/{repo}/pulls/{index}.
type gtPull struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	User   gtUser `json:"user"`

	State          string     `json:"state"`
	Draft          bool       `json:"draft"`
	Merged         bool       `json:"merged"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	MergedAt       *time.Time `json:"merged_at"`
	ClosedAt       *time.Time `json:"closed_at"`

	Head gtBranchInfo `json:"head"`
	Base gtBranchInfo `json:"base"`

	Labels []gtLabel `json:"labels"`

	ChangedFiles int    `json:"changed_files"`
	HTMLURL      string `json:"html_url"`

	// Mergeable is Gitea's whole answer about landing it: one bool, with
	// no way to say "still working it out" and no separate word for a
	// branch that has fallen behind. Gitea computes it from the conflict
	// status alone — `block_on_outdated_branch` is applied at merge time
	// and never shows up here — so flow reads it as conflicted or not and
	// never reports `behind` for a Gitea pull request.
	Mergeable bool `json:"mergeable"`
}

// toPullRequest converts the payload.
//
// The squash header is composed rather than read, because Gitea does not
// serve it: services/pull/merge.go writes "<title> (#<number>)" for every
// squash a repository without a merge-message template takes, and that is
// what the trunk ends up holding. It is composed for every pull request, open
// or merged, since the gate's whole job is to judge the header before it
// exists (spec 09 §2).
func (p gtPull) toPullRequest(reference string) core.PullRequest {
	pr := core.PullRequest{
		Number:       p.Number,
		Title:        p.Title,
		SquashHeader: fmt.Sprintf("%s (%s%d)", p.Title, reference, p.Number),
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
	// Gitea says a pull request is merged in a field of its own, and a
	// renovate-style branch that was deleted on merge still has the time.
	// Both are read, because a merged pull request with neither would read
	// as closed and the audit counts those as abandoned.
	if p.MergedAt != nil {
		pr.MergedAt = *p.MergedAt
	}
	if p.Merged || p.MergedAt != nil {
		pr.State = core.PRStateMerged
		pr.MergeSHA = p.MergeCommitSHA
	}
	if pr.State == core.PRStateOpen {
		pr.Mergeability = core.MergeabilityConflicted
		if p.Mergeable {
			pr.Mergeability = core.MergeabilityClean
		}
	}
	return pr
}

// gtReview is GET /repos/{owner}/{repo}/pulls/{index}/reviews.
type gtReview struct {
	User        gtUser     `json:"user"`
	State       string     `json:"state"`
	Dismissed   bool       `json:"dismissed"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

// gtReviewStates maps Gitea's review vocabulary onto spec 04 §4's. PENDING
// and REQUEST_REVIEW are absent deliberately: a review not yet submitted, and
// a review somebody has merely been asked for, are not verdicts.
var gtReviewStates = map[string]string{
	"APPROVED":        core.ReviewApproved,
	"REQUEST_CHANGES": core.ReviewChangesRequested,
	"COMMENT":         core.ReviewCommented,
}

// toReview converts the payload, reporting false for anything that is not a
// submitted verdict.
//
// Gitea has no dismissed state: it keeps the verdict and sets a flag beside
// it. The flag wins, because an approval somebody took back is not an
// approval and spec 04 §4 has a word for that.
func (r gtReview) toReview() (core.Review, bool) {
	state, ok := gtReviewStates[strings.ToUpper(r.State)]
	if !ok {
		return core.Review{}, false
	}
	if r.Dismissed {
		state = core.ReviewDismissed
	}
	review := core.Review{Author: r.User.Login, State: state}
	if r.SubmittedAt != nil {
		review.SubmittedAt = *r.SubmittedAt
	}
	return review, true
}

// gtChangedFile is GET /repos/{owner}/{repo}/pulls/{index}/files.
type gtChangedFile struct {
	Filename string `json:"filename"`
}

// gtCommit is GET /repos/{owner}/{repo}/commits. The author of record is the
// account Gitea resolved the commit to; the embedded git identity is the
// fallback for a commit written by somebody with no account.
type gtCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Author  gtUser `json:"author"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// toCommit converts the payload.
func (c gtCommit) toCommit() core.Commit {
	author := c.Author.Login
	if author == "" {
		author = c.Commit.Author.Name
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

// gtCompare is GET /repos/{owner}/{repo}/compare/{base}...{head}. Gitea
// answers with the commits head has and base does not, and counts them; it
// never names the relation, so [GiteaKind] asks twice and names it itself.
type gtCompare struct {
	TotalCommits int `json:"total_commits"`
}

// compareStatus names the relation between two refs from the two counts a
// pair of comparisons gives.
func compareStatus(ahead, behind int) string {
	switch {
	case ahead == 0 && behind == 0:
		return core.CompareIdentical
	case behind == 0:
		return core.CompareAhead
	case ahead == 0:
		return core.CompareBehind
	default:
		return core.CompareDiverged
	}
}

// gtStatus is GET /repos/{owner}/{repo}/commits/{ref}/statuses.
//
// Gitea answers with `status` and is told with `state`, which is why the two
// payloads here are not one type: a single struct would have to name one of
// them and would then write a status nothing could read back.
type gtStatus struct {
	ID          int64     `json:"id"`
	Context     string    `json:"context"`
	Status      string    `json:"status"`
	Description string    `json:"description"`
	TargetURL   string    `json:"target_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toCommitStatus converts the payload. An absent identity stays absent rather
// than becoming "0": a record built out of a status points at it by this, and
// a link to status zero is worse than no link at all.
func (s gtStatus) toCommitStatus() core.CommitStatus {
	id := ""
	if s.ID != 0 {
		id = strconv.FormatInt(s.ID, 10)
	}
	return core.CommitStatus{
		ID:          id,
		Context:     s.Context,
		State:       s.Status,
		Description: s.Description,
		TargetURL:   s.TargetURL,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
}

// gtStatusRequest is POST /repos/{owner}/{repo}/statuses/{sha}.
type gtStatusRequest struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

// gtComment is GET /repos/{owner}/{repo}/issues/{index}/comments. A pull
// request is an issue to Gitea's comment endpoints, as it is to GitHub's.
type gtComment struct {
	ID        int64     `json:"id"`
	User      gtUser    `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
}

// toComment converts the payload, reporting false for a comment that does not
// carry a marker the wanted one matches — which is every comment a person
// wrote.
func (c gtComment) toComment(want core.Marker) (core.Comment, bool) {
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

// gtCommentRequest is the body of POST and PATCH on a comment.
type gtCommentRequest struct {
	Body string `json:"body"`
}

// gtBranch is GET /repos/{owner}/{repo}/branches/{branch}, which is where
// Gitea answers what a branch's protection comes to once every rule that
// covers it has been applied. It is the effective reading, so a branch
// governed by a glob rule answers here without flow having to work out which
// rule that was (spec 10 §3).
type gtBranch struct {
	Name                string   `json:"name"`
	Protected           bool     `json:"protected"`
	EnableStatusCheck   bool     `json:"enable_status_check"`
	StatusCheckContexts []string `json:"status_check_contexts"`
	RequiredApprovals   int      `json:"required_approvals"`
	// EffectiveProtection names the rule that governs this branch, which is
	// not always the branch's own name: a rule may be a glob covering many
	// branches. Gitea leaves it empty on instances that predate the field.
	EffectiveProtection string `json:"effective_branch_protection_name"`
}

// ruleName is the rule to ask for the settings the branch does not mention.
// A Gitea that does not name it governs the branch by a rule of its own name,
// which is what the web interface creates and what flow writes.
func (b gtBranch) ruleName(branch string) string {
	if b.EffectiveProtection != "" {
		return b.EffectiveProtection
	}
	return branch
}

// toProtection converts the effective reading.
func (b gtBranch) toProtection(branch string) core.Protection {
	p := core.Protection{
		Branch:            branch,
		Enabled:           b.Protected,
		RequiredApprovals: b.RequiredApprovals,
	}
	if !b.Protected {
		return p
	}
	// A protected branch cannot be deleted or force-pushed in Gitea unless
	// a rule says otherwise, and the rule is the next read. What the branch
	// alone proves is that neither is open to everybody.
	if b.EnableStatusCheck {
		p.RequiredChecks = append(p.RequiredChecks, b.StatusCheckContexts...)
	}
	return p
}

// gtProtection is GET /repos/{owner}/{repo}/branch_protections/{name}: one
// rule, which a credential without admin may not read at all.
type gtProtection struct {
	RuleName            string   `json:"rule_name"`
	EnableStatusCheck   bool     `json:"enable_status_check"`
	StatusCheckContexts []string `json:"status_check_contexts"`
	RequiredApprovals   int      `json:"required_approvals"`
	DismissStale        bool     `json:"dismiss_stale_approvals"`
	BlockOutdated       bool     `json:"block_on_outdated_branch"`
	ApplyToAdmins       bool     `json:"apply_to_admins"`
}

// refine fills in the settings only the rule itself carries, leaving the
// effective reading in charge of everything the branch already answered.
//
// Whether the branch may be force-pushed is not among them. Gitea has no
// setting for it: a protected branch refuses a force push, and an
// unprotected one takes whatever its pusher may do. So the answer is the one
// protection already implies rather than one flow invents, and
// `AllowForcePush` is left as it stands.
func (r gtProtection) refine(p core.Protection) core.Protection {
	p.StrictChecks = r.BlockOutdated
	p.DismissStaleReviews = r.DismissStale
	p.EnforceAdmins = r.ApplyToAdmins
	return p
}

// gtProtectionRequest is POST /repos/{owner}/{repo}/branch_protections and
// PATCH of one. The rule name is only accepted on the create, so it carries
// `omitempty`.
type gtProtectionRequest struct {
	RuleName            string   `json:"rule_name,omitempty"`
	BranchName          string   `json:"branch_name,omitempty"`
	EnableStatusCheck   bool     `json:"enable_status_check"`
	StatusCheckContexts []string `json:"status_check_contexts"`
	RequiredApprovals   int      `json:"required_approvals"`
	DismissStale        bool     `json:"dismiss_stale_approvals"`
	BlockOutdated       bool     `json:"block_on_outdated_branch"`
	ApplyToAdmins       bool     `json:"apply_to_admins"`
}

// protectionRequestGitea turns a wanted protection into Gitea's vocabulary.
// The contexts are always sent, even when there are none: Gitea reads an
// absent list as "leave it alone" and an empty one as "require nothing", and
// a rule that requires a check nobody asked for is not what was planned.
func protectionRequestGitea(p core.Protection) gtProtectionRequest {
	contexts := p.RequiredChecks
	if contexts == nil {
		contexts = []string{}
	}
	return gtProtectionRequest{
		EnableStatusCheck:   len(contexts) > 0,
		StatusCheckContexts: contexts,
		RequiredApprovals:   p.RequiredApprovals,
		DismissStale:        p.DismissStaleReviews,
		BlockOutdated:       p.StrictChecks,
		ApplyToAdmins:       p.EnforceAdmins,
	}
}

// gtContents is GET /repos/{owner}/{repo}/contents/{path}, read for the blob
// identity an update has to quote.
type gtContents struct {
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// decoded returns the bytes the payload carries, and whether it carried any.
// Gitea sends the content base64-encoded and says so; a payload that says
// anything else is a file flow should not claim to have compared.
func (c gtContents) decoded() ([]byte, bool) {
	if c.Encoding != "base64" {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(c.Content)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// gtContentsRequest is POST and PUT on a path. Gitea takes the content
// base64-encoded, and an update quotes the blob it is replacing so that two
// writers cannot silently overwrite one another.
type gtContentsRequest struct {
	Content string `json:"content"`
	Message string `json:"message"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha,omitempty"`
}

// gtBranchRequest is POST /repos/{owner}/{repo}/branches.
type gtBranchRequest struct {
	NewBranchName string `json:"new_branch_name"`
	OldBranchName string `json:"old_branch_name"`
}

// gtPullCreate is POST /repos/{owner}/{repo}/pulls.
type gtPullCreate struct {
	Head  string `json:"head"`
	Base  string `json:"base"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// gtPullUpdate is PATCH /repos/{owner}/{repo}/pulls/{index}.
type gtPullUpdate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}
