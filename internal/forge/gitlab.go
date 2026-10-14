package forge

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// GitLab adapter constants (spec 10 §4).
const (
	// GitLabKind is the adapter name, matching assets/capabilities/
	// gitlab.yaml and forge.type in configuration.
	GitLabKind = "gitlab"
	// GitLabAPIPath is where gitlab.com and every self-managed instance
	// answer.
	GitLabAPIPath = "/api/v4"
	// GitLabHost is the host everybody means by "GitLab" when they name no
	// other: the SaaS instance.
	GitLabHost = "https://gitlab.com"
	// gitLabAccept is the media type every call asks for. GitLab has no
	// dated API version and no vendor media type: the version is in the
	// path.
	gitLabAccept = "application/json"
	// gitLabPageSize is the largest page GitLab serves.
	gitLabPageSize = 100
	// gitLabApprovalRule is the name of the approval rule flow writes when
	// a caller asks for approvals. Naming it means a later run edits the
	// same rule rather than adding a second one beside it.
	gitLabApprovalRule = "flow"
	// gitLabAnyApprover is the rule type that counts approvals from
	// anybody, as against a named list of users or groups.
	gitLabAnyApprover = "any_approver"
)

// GitLab's merge methods. One project has one, and it decides what landing
// without a squash produces (spec 10 §4).
const (
	// gitLabMergeCommit always writes a merge commit.
	gitLabMergeCommit = "merge"
	// gitLabMergeSemiLinear rebases the source onto the target and then
	// writes a merge commit, so the history is linear and the merge is
	// still recorded.
	gitLabMergeSemiLinear = "rebase_merge"
	// gitLabMergeFastForward refuses anything but a fast-forward.
	gitLabMergeFastForward = "ff"
)

// GitLab's squash options: whether a squash is forbidden, offered or
// compulsory.
const (
	gitLabSquashNever     = "never"
	gitLabSquashDefaultOn = "default_on"
	gitLabSquashAlways    = "always"
)

// GitLab's access levels. They are cumulative — a maintainer may do everything
// a developer may — and the API speaks in the numbers rather than the words.
const (
	gitLabNobody     = 0
	gitLabGuest      = 10
	gitLabReporter   = 20
	gitLabDeveloper  = 30
	gitLabMaintainer = 40
	gitLabOwner      = 50
)

// glAccessStep is one rung of GitLab's access ladder: the number its API
// speaks in and the word assets/capabilities/gitlab.yaml is written in.
type glAccessStep struct {
	level int
	token string
}

// gitLabAccessLevels is the ladder, lowest first.
var gitLabAccessLevels = []glAccessStep{
	{gitLabGuest, "guest"},
	{gitLabReporter, "reporter"},
	{gitLabDeveloper, "developer"},
	{gitLabMaintainer, "maintainer"},
	{gitLabOwner, "owner"},
}

// gitLabStatusStates are the states flow publishes. GitLab has no "error" of
// its own — a check that could not run has failed as far as the forge is
// concerned — so all four of flow's verdicts are writable and two of them land
// as the same word.
var gitLabStatusStates = []string{
	core.StatusSuccess, core.StatusFailure, core.StatusPending, core.StatusError,
}

// GitLabHeader returns the headers every GitLab call carries. The root passes
// it to [core.ClientOptions], as it does GitHub's.
func GitLabHeader() http.Header {
	h := http.Header{}
	h.Set("Accept", gitLabAccept)
	return h
}

// GitLabOptions configures a [GitLab] adapter.
type GitLabOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Owner and Repo name the project the adapter speaks for. Owner is the
	// whole namespace above the project, so a project in a subgroup is
	// `acme/platform/tooling` and `widget`.
	Owner string
	Repo  string
	// Capabilities is the declaration loaded from
	// assets/capabilities/gitlab.yaml.
	Capabilities core.CapabilitySet
}

// GitLab is the GitLab forge adapter.
type GitLab struct {
	client *core.Client
	owner  string
	repo   string
	caps   core.CapabilitySet

	// writing serialises the calls that change the project, as the other
	// two adapters do: a burst of concurrent writes to one project buys
	// nothing and makes a read-then-write race with itself.
	writing sync.Mutex
}

// NewGitLab returns an adapter for one project.
func NewGitLab(o GitLabOptions) (*GitLab, error) {
	switch {
	case o.Client == nil:
		return nil, core.Errorf(core.ErrValidation, "build the gitlab adapter").
			WithWhy("no HTTP client was given")
	case o.Owner == "" || o.Repo == "":
		return nil, core.Errorf(core.ErrValidation, "build the gitlab adapter").
			WithWhy("the project was given as %q/%q", o.Owner, o.Repo).
			WithFix("name the project as namespace/name")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the gitlab adapter").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/gitlab.yaml and pass the declaration")
	}
	return &GitLab{client: o.Client, owner: o.Owner, repo: o.Repo, caps: o.Capabilities}, nil
}

// Kind is the adapter's name.
func (g *GitLab) Kind() string { return GitLabKind }

// Capabilities is what this adapter declares it can do.
func (g *GitLab) Capabilities() core.CapabilitySet { return g.caps }

// Slug is the project as people write it.
func (g *GitLab) Slug() string { return g.owner + "/" + g.repo }

// Repo describes the project: its trunk and how merge requests may land.
func (g *GitLab) Repo(ctx context.Context) (core.RepoInfo, error) {
	payload, err := g.project(ctx)
	if err != nil {
		return core.RepoInfo{}, err
	}
	return payload.toRepoInfo(), nil
}

// project reads the project itself.
func (g *GitLab) project(ctx context.Context) (glProject, error) {
	var payload glProject
	if _, err := g.do(ctx, core.Request{Path: g.path(), Out: &payload}); err != nil {
		return glProject{}, core.Explain(err, "read the project %s", g.Slug())
	}
	return payload, nil
}

// Probe reports what the credential may do with this project.
//
// GitLab answers the whole question in the project payload: `permissions`
// holds the member's own access and the access their group gives them, and
// the greater of the two is what they may do here. There is no second limit
// to apply and no header to read (spec 05 §3).
func (g *GitLab) Probe(ctx context.Context) (core.Permissions, error) {
	payload, err := g.project(ctx)
	if err != nil {
		return core.Permissions{}, core.Explain(err, "find out what the credential may do in %s", g.Slug())
	}
	return core.Permissions{
		Tokens:   payload.Permissions.tokens(),
		Evidence: []string{"project permissions: " + payload.Permissions.String()},
	}, nil
}

// PullRequest returns one merge request with the reviews, the file summary and
// the tickets it closes.
//
// GitLab publishes the links a merge request makes through its own endpoint,
// so `linked_issues_read` is supported and the engine never has to fall back
// to reading the branch name (spec 04 §3, spec 10 §4).
func (g *GitLab) PullRequest(ctx context.Context, number int) (core.PullRequest, error) {
	var payload glMergeRequest
	path := g.path("merge_requests", strconv.Itoa(number))
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		return core.PullRequest{}, core.Explain(err, "read merge request !%d of %s", number, g.Slug())
	}
	pr := payload.toPullRequest()

	reviews, err := g.reviews(ctx, number, pr.UpdatedAt)
	if err != nil {
		return core.PullRequest{}, err
	}
	pr.Reviews = reviews

	closing, err := g.closingIssues(ctx, number)
	if err != nil {
		return core.PullRequest{}, err
	}
	pr.ClosingIssues = closing

	paths, err := g.changedPaths(ctx, number)
	if err != nil {
		return core.PullRequest{}, err
	}
	if len(paths) > 0 {
		pr.Files.Paths = paths
	}
	return pr, nil
}

// reviews returns the verdicts standing on a merge request, oldest first.
//
// GitLab answers it in two places and both are read. A reviewer keeps a
// current state — approved, asked for changes, merely looked — and that state
// is dated. An approval is a separate list, which carries who approved and
// nothing else, so it is read as an addition: somebody who approved without
// being asked to review is a verdict the first list does not have, and
// somebody in both already has a dated verdict there (spec 10 §4).
func (g *GitLab) reviews(ctx context.Context, number int, updatedAt time.Time) ([]core.Review, error) {
	var (
		reviews []core.Review
		seen    = map[string]bool{}
	)
	req := core.Request{
		Path:  g.path("merge_requests", strconv.Itoa(number), "reviewers"),
		Query: g.page(),
	}
	err := paginate(ctx, g, req, func(r glReviewer) bool {
		review, ok := r.toReview()
		if !ok {
			return true
		}
		seen[review.Author] = true
		reviews = append(reviews, review)
		return true
	})
	if err != nil {
		return nil, core.Explain(err, "read the reviewers of merge request !%d of %s", number, g.Slug())
	}

	var approvals glApprovals
	approved := core.Request{
		Path: g.path("merge_requests", strconv.Itoa(number), "approvals"),
		Out:  &approvals,
	}
	if _, err := g.do(ctx, approved); err != nil {
		return nil, core.Explain(err, "read the approvals of merge request !%d of %s", number, g.Slug())
	}
	for _, approver := range approvals.ApprovedBy {
		login := approver.User.login()
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		// An approval carries no time of its own, so it is dated by the
		// merge request's own last update: the latest moment it could
		// have happened, and the only one GitLab offers.
		reviews = append(reviews, core.Review{
			Author:      login,
			State:       core.ReviewApproved,
			SubmittedAt: updatedAt,
		})
	}
	slices.SortStableFunc(reviews, func(a, b core.Review) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	return reviews, nil
}

// closingIssues returns the tickets GitLab says a merge request closes.
func (g *GitLab) closingIssues(ctx context.Context, number int) ([]string, error) {
	slug := g.Slug()
	var keys []string
	req := core.Request{
		Path:  g.path("merge_requests", strconv.Itoa(number), "closes_issues"),
		Query: g.page(),
	}
	err := paginate(ctx, g, req, func(i glIssue) bool {
		if key := i.key(slug); key != "" {
			keys = append(keys, key)
		}
		return true
	})
	if err != nil {
		return nil, core.Explain(err, "read the issues merge request !%d of %s closes", number, g.Slug())
	}
	return keys, nil
}

// changedPaths returns the first page of files a merge request touches,
// sorted. Flow needs a summary rather than a manifest, and the count comes
// from the merge request itself.
func (g *GitLab) changedPaths(ctx context.Context, number int) ([]string, error) {
	var diffs []glDiff
	req := core.Request{
		Path:  g.path("merge_requests", strconv.Itoa(number), "diffs"),
		Query: g.page(),
		Out:   &diffs,
	}
	if _, err := g.do(ctx, req); err != nil {
		return nil, core.Explain(err, "read the files of merge request !%d of %s", number, g.Slug())
	}
	paths := make([]string, 0, len(diffs))
	for _, diff := range diffs {
		if path := diff.path(); path != "" {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths, nil
}

// PullRequests returns the merge requests matching a query, newest first by
// update. Reviews, the closing issues and the file summary are separate reads,
// so a listing leaves them empty (spec 10 §2).
func (g *GitLab) PullRequests(ctx context.Context, q core.PRQuery) ([]core.PullRequest, error) {
	query := g.page()
	query.Set("state", gitLabListState(q.State))
	query.Set("order_by", "updated_at")
	query.Set("sort", "desc")
	if q.Base != "" {
		query.Set("target_branch", q.Base)
	}
	if !q.UpdatedSince.IsZero() {
		query.Set("updated_after", q.UpdatedSince.UTC().Format(time.RFC3339))
	}

	// The state, the branch and the window are all the query's, and GitLab
	// answers them itself. Filtering them again here would be a second
	// mechanism answering the same question, which is a thing that drifts
	// (spec 10 §4).
	var prs []core.PullRequest
	err := paginate(ctx, g, core.Request{Path: g.path("merge_requests"), Query: query}, func(m glMergeRequest) bool {
		prs = append(prs, m.toPullRequest())
		return q.Limit <= 0 || len(prs) < q.Limit
	})
	if err != nil {
		return nil, core.Explain(err, "list the merge requests of %s", g.Slug())
	}
	return prs, nil
}

// Commits returns commits on a ref, newest first.
func (g *GitLab) Commits(ctx context.Context, q core.CommitQuery) ([]core.Commit, error) {
	query := g.page()
	if q.Ref != "" {
		query.Set("ref_name", q.Ref)
	}
	if !q.Since.IsZero() {
		query.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		query.Set("until", q.Until.UTC().Format(time.RFC3339))
	}

	var commits []core.Commit
	req := core.Request{Path: g.path("repository", "commits"), Query: query}
	err := paginate(ctx, g, req, func(c glCommit) bool {
		commits = append(commits, c.toCommit())
		return q.Limit <= 0 || len(commits) < q.Limit
	})
	if err != nil {
		return nil, core.Explain(err, "list the commits of %s", g.Slug())
	}
	return commits, nil
}

// Compare reports the ancestry between two commits, which is how flow proves a
// merge reached an environment (spec 13 §3).
//
// GitLab answers a comparison with the commits the head has and the base does
// not; it never names the relation. One list cannot tell `identical` from
// `behind` or `ahead` from `diverged`, so the question is asked both ways
// round and flow names the relation itself, as it does for Gitea.
func (g *GitLab) Compare(ctx context.Context, base, head string) (core.Comparison, error) {
	if base == "" || head == "" {
		return core.Comparison{}, core.Errorf(core.ErrValidation, "compare %q with %q", base, head).
			WithWhy("both a base and a head are needed")
	}
	ahead, same, err := g.distance(ctx, base, head)
	if err != nil {
		return core.Comparison{}, err
	}
	if same {
		return core.Comparison{Base: base, Head: head, Status: core.CompareIdentical}, nil
	}
	behind, _, err := g.distance(ctx, head, base)
	if err != nil {
		return core.Comparison{}, err
	}
	return core.Comparison{
		Base:     base,
		Head:     head,
		Status:   compareStatus(ahead, behind),
		AheadBy:  ahead,
		BehindBy: behind,
	}, nil
}

// distance returns how many commits head has that base does not, and whether
// GitLab recognised the two refs as the same commit.
//
// GitLab caps the commits it lists in a comparison, so a count far beyond the
// cap is the cap. Nothing flow asks turns on the number: containment is
// "nothing is missing", which is the difference between none and some.
func (g *GitLab) distance(ctx context.Context, base, head string) (int, bool, error) {
	var payload glCompare
	query := url.Values{}
	query.Set("from", base)
	query.Set("to", head)
	// A straight comparison is base..head rather than base...head, which is
	// what "is base contained in head" means: a merge base would answer a
	// different question, and answering it would make a deploy audit claim
	// a merge shipped when the trunk had moved on.
	query.Set("straight", "true")
	req := core.Request{Path: g.path("repository", "compare"), Query: query, Out: &payload}
	if _, err := g.do(ctx, req); err != nil {
		return 0, false, core.Explain(err, "compare %s with %s in %s", base, head, g.Slug())
	}
	return len(payload.Commits), payload.CompareSameRef, nil
}

// File returns the content of a file at a ref, so that doctor can read the
// workflow that is actually on the trunk.
func (g *GitLab) File(ctx context.Context, filePath, ref string) ([]byte, error) {
	if filePath == "" {
		return nil, core.Errorf(core.ErrValidation, "read a file from %s", g.Slug()).
			WithWhy("no path was given")
	}
	query := url.Values{}
	if ref != "" {
		query.Set("ref", ref)
	}

	var content []byte
	// GitLab serves the bytes themselves from an endpoint of their own,
	// rather than from a media type on the file endpoint.
	req := core.Request{
		Path:  g.path("repository", "files", filePath, "raw"),
		Query: query,
		Out:   &content,
	}
	if _, err := g.do(ctx, req); err != nil {
		return nil, core.Explain(err, "read %s from %s at %s", filePath, g.Slug(), refOrDefault(ref))
	}
	return content, nil
}

// Protection returns the effective protection of a branch.
//
// GitLab keeps it in four places and all four are read. The protected branch
// says whether the branch is protected at all and whether it may be rewritten.
// The project says whether a merge waits on a pipeline and whether a history
// stays linear, because GitLab has no per-branch setting for either. Approval
// rules say how many reviews a merge request needs, and external status checks
// name the verdicts it must carry (spec 10 §4).
func (g *GitLab) Protection(ctx context.Context, branch string) (core.Protection, error) {
	if branch == "" {
		return core.Protection{}, core.Errorf(core.ErrValidation, "read the protection of %s", g.Slug()).
			WithWhy("no branch was named")
	}
	project, err := g.project(ctx)
	if err != nil {
		return core.Protection{}, err
	}
	protected, err := g.protectedBranch(ctx, branch)
	if err != nil {
		return core.Protection{}, err
	}

	p := core.Protection{Branch: branch}
	if protected != nil {
		p = protected.toProtection(branch)
	}
	// Both of these are the project's, not the branch's. A project that
	// refuses anything but a fast-forward or a rebase keeps a linear
	// history on every branch it has, and a project that waits for its
	// pipelines waits on every merge request.
	p.RequireLinearHistory = project.MergeMethod != gitLabMergeCommit
	p.StrictChecks = project.PipelineMustSucceed
	if !p.Enabled {
		return p, nil
	}

	approvals, err := g.approvalRules(ctx)
	if err != nil {
		return core.Protection{}, err
	}
	for _, rule := range approvals {
		if rule.covers(branch) {
			p.RequiredApprovals = max(p.RequiredApprovals, rule.ApprovalsRequired)
		}
	}

	checks, _, err := g.externalChecks(ctx)
	if err != nil {
		return core.Protection{}, err
	}
	for _, check := range checks {
		if check.covers(branch) && check.Name != "" {
			p.RequiredChecks = append(p.RequiredChecks, check.Name)
		}
	}
	slices.Sort(p.RequiredChecks)
	return p, nil
}

// protectedBranch reads a branch's protection, or nil when the branch is not
// protected. A branch nobody protected is an answer rather than a failure:
// flow asks about the trunk a configuration names, and an unprotected one is
// the finding.
func (g *GitLab) protectedBranch(ctx context.Context, branch string) (*glProtectedBranch, error) {
	var payload glProtectedBranch
	req := core.Request{Path: g.path("protected_branches", branch), Out: &payload}
	if _, err := g.do(ctx, req); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return nil, nil
		}
		return nil, core.Explain(err, "read the protection of %s in %s", branch, g.Slug())
	}
	return &payload, nil
}

// approvalRules reads the project's approval rules, and none when this GitLab
// has no such feature.
//
// Approval rules are a paid feature. An instance without them answers 404, and
// a credential without the access answers 403; both mean the same thing here —
// flow cannot see a rule, so there is no rule it can report. Failing instead
// would make every read of a protected branch fail on the tier most people
// have (spec 05 §3).
func (g *GitLab) approvalRules(ctx context.Context) ([]glApprovalRule, error) {
	var rules []glApprovalRule
	req := core.Request{Path: g.path("approval_rules"), Query: g.page(), Out: &rules}
	if _, err := g.do(ctx, req); err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrPermission) {
			return nil, nil
		}
		return nil, core.Explain(err, "read the approval rules of %s", g.Slug())
	}
	return rules, nil
}

// externalChecks reads the project's external status checks, and reports
// whether this GitLab has them at all.
//
// The difference matters where the rules do not. An instance with no external
// status checks cannot require a named verdict of a merge request, which is
// what flow's gate is; an instance that has them and has none configured can.
// Both read as "no check is required", so the second answer is what tells a
// refusal which sentence to say (spec 10 §4).
func (g *GitLab) externalChecks(ctx context.Context) ([]glExternalCheck, bool, error) {
	var checks []glExternalCheck
	req := core.Request{Path: g.path("external_status_checks"), Query: g.page(), Out: &checks}
	if _, err := g.do(ctx, req); err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrPermission) {
			return nil, false, nil
		}
		return nil, false, core.Explain(err, "read the external status checks of %s", g.Slug())
	}
	return checks, true, nil
}

// SetProtection applies protection additively: it never removes a required
// check or a setting flow did not ask for (spec 06 §4).
func (g *GitLab) SetProtection(ctx context.Context, branch string, want core.Protection) error {
	switch {
	case branch == "":
		return core.Errorf(core.ErrValidation, "protect a branch of %s", g.Slug()).
			WithWhy("no branch was named")
	case !want.Enabled:
		return core.Errorf(core.ErrValidation, "protect %s of %s", branch, g.Slug()).
			WithWhy("the protection asked for is not enabled, which would mean removing it").
			WithFix("flow only ever adds protection; take the branch's protection off at the forge yourself")
	}

	g.writing.Lock()
	defer g.writing.Unlock()

	current, err := g.Protection(ctx, branch)
	if err != nil {
		return err
	}
	merged := current.Merge(want)
	if sameProtection(current, merged) {
		return nil
	}
	if err := g.protectBranch(ctx, branch, current, merged); err != nil {
		return err
	}
	if err := g.setProjectProtection(ctx, current, merged); err != nil {
		return err
	}
	if err := g.setApprovals(ctx, current, merged); err != nil {
		return err
	}
	return g.requireChecks(ctx, branch, current, merged)
}

// protectBranch creates the branch's protection, or narrows the one it has.
//
// A branch flow protects takes no pushes at all: work lands through a merge
// request, which is the whole of the workflow spec 00 describes, and GitLab
// says that by giving nobody push access. Merging stays open to developers,
// because the rules a merge must pass are the required checks rather than the
// rank of whoever presses the button (spec 10 §4).
func (g *GitLab) protectBranch(ctx context.Context, branch string, current, merged core.Protection) error {
	if !current.Enabled {
		req := core.Request{
			Method: http.MethodPost,
			Path:   g.path("protected_branches"),
			Body: glProtectedBranchRequest{
				Name:             branch,
				PushAccessLevel:  gitLabNobody,
				MergeAccessLevel: gitLabDeveloper,
				AllowForcePush:   merged.AllowForcePush,
			},
		}
		if _, err := g.do(ctx, req); err != nil {
			return core.Explain(err, "protect %s of %s", branch, g.Slug())
		}
		return nil
	}
	if current.AllowForcePush == merged.AllowForcePush {
		return nil
	}
	allow := merged.AllowForcePush
	req := core.Request{
		Method: http.MethodPatch,
		Path:   g.path("protected_branches", branch),
		Body:   glProtectedBranchPatch{AllowForcePush: &allow},
		// Writing the same whole state twice leaves the same state, so a
		// retry cannot do anything the first attempt did not.
		Retry: core.RetryAlways,
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "stop %s of %s being rewritten", branch, g.Slug())
	}
	return nil
}

// setProjectProtection writes the two settings GitLab keeps on the project
// rather than on the branch.
//
// A linear history is the merge method, which `forge.merge` also writes. They
// cannot disagree — both come from `commits.merge_strategy` — but they can
// arrive in either order, so this one only acts when the project allows a
// merge commit and asks for the least it can: a semi-linear merge keeps the
// history linear and leaves a later, stricter write alone (spec 10 §4).
func (g *GitLab) setProjectProtection(ctx context.Context, current, merged core.Protection) error {
	body := glProjectRequest{}
	if merged.StrictChecks && !current.StrictChecks {
		pipelines := true
		body.PipelineMustSucceed = &pipelines
	}
	if merged.RequireLinearHistory && !current.RequireLinearHistory {
		method := gitLabMergeSemiLinear
		body.MergeMethod = &method
	}
	if body.PipelineMustSucceed == nil && body.MergeMethod == nil {
		return nil
	}
	req := core.Request{
		Method: http.MethodPut,
		Path:   g.path(),
		Body:   body,
		Retry:  core.RetryAlways,
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "set how %s lands its merge requests", g.Slug())
	}
	return nil
}

// setApprovals raises the approvals a merge request needs, when a caller asked
// for more than the project already requires.
//
// It never lowers them. A rule somebody else wrote is theirs, and flow's
// protection changes are additive, so the rule flow writes is one of its own
// and the greater of the two is what applies.
func (g *GitLab) setApprovals(ctx context.Context, current, merged core.Protection) error {
	if merged.RequiredApprovals <= current.RequiredApprovals {
		return nil
	}
	rules, err := g.approvalRules(ctx)
	if err != nil {
		return err
	}
	body := glApprovalRuleRequest{
		Name:                          gitLabApprovalRule,
		RuleType:                      gitLabAnyApprover,
		ApprovalsRequired:             merged.RequiredApprovals,
		AppliesToAllProtectedBranches: true,
	}
	req := core.Request{Method: http.MethodPost, Path: g.path("approval_rules"), Body: body, Retry: core.RetryAlways}
	for _, rule := range rules {
		if rule.Name == gitLabApprovalRule {
			req.Method = http.MethodPut
			req.Path = g.path("approval_rules", strconv.FormatInt(rule.ID, 10))
			break
		}
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "ask for %d approvals in %s", merged.RequiredApprovals, g.Slug())
	}
	return nil
}

// requireChecks makes GitLab wait for the verdicts a merge must carry.
//
// A named verdict is an external status check, which not every GitLab has. On
// an instance without them the nearest thing is the project waiting for its
// pipelines, which `setProjectProtection` has already asked for — but that is
// not the same promise, because it names no check and a merge request with no
// pipeline at all satisfies it. So the checks that could not be required are
// reported as unavailable, and the capability's remediation turns that into a
// follow-up for somebody who can (spec 05 §2, spec 10 §4).
func (g *GitLab) requireChecks(ctx context.Context, branch string, current, merged core.Protection) error {
	var missing []string
	for _, context := range merged.RequiredChecks {
		if !current.RequiresCheck(context) {
			missing = append(missing, context)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	_, available, err := g.externalChecks(ctx)
	if err != nil {
		return err
	}
	if !available {
		return core.Errorf(core.ErrUnavailable, "require %s of %s in %s",
			strings.Join(missing, ", "), branch, g.Slug()).
			WithWhy("this GitLab has no external status checks, which is where a named verdict is required").
			WithFix("require the check by hand, or on a tier that has external status checks")
	}

	protected, err := g.protectedBranch(ctx, branch)
	if err != nil {
		return err
	}
	if protected == nil {
		return core.Errorf(core.ErrConflict, "require %s of %s in %s",
			strings.Join(missing, ", "), branch, g.Slug()).
			WithWhy("the branch is not protected, so there is nothing to require the checks of").
			WithFix("protect the branch first")
	}
	project, err := g.project(ctx)
	if err != nil {
		return err
	}
	for _, context := range missing {
		body := glExternalCheckRequest{
			Name: context,
			// GitLab calls the check's URL when a merge request changes.
			// Flow serves nothing: it publishes the verdict through the
			// API, so the address is the project's own page, which is
			// somewhere a person reading the check can usefully go.
			ExternalURL:        project.WebURL,
			ProtectedBranchIDs: []int{protected.ID},
		}
		req := core.Request{Method: http.MethodPost, Path: g.path("external_status_checks"), Body: body}
		if _, err := g.do(ctx, req); err != nil {
			return core.Explain(err, "require %s of %s in %s", context, branch, g.Slug())
		}
	}
	return nil
}

// SetMergeSettings sets how merge requests may land. They are one setting
// written whole (spec 06 §4): the strategies a team did not ask for are the
// ones it wants turned off, so the call carries all of them.
//
// Where a squash commit takes its words from goes with them, because GitLab
// keeps it on the same project and writes it with the same call. A wanted text
// GitLab has no template for is left unwritten rather than approximated: the
// whole plan is built on reading back what was written (spec 10 §4).
func (g *GitLab) SetMergeSettings(ctx context.Context, s core.MergeSettings) error {
	if !s.AllowSquash && !s.AllowMerge && !s.AllowRebase {
		return core.Errorf(core.ErrValidation, "set the merge settings of %s", g.Slug()).
			WithWhy("no way of merging would be left").
			WithFix("allow at least one of squash, merge commit or rebase")
	}

	g.writing.Lock()
	defer g.writing.Unlock()

	want := mergeRequestGitLab(s)
	template, templated := squashTemplateGitLab(s)
	if templated {
		want.SquashCommitTemplate = &template
	}

	var payload glProject
	req := core.Request{
		Method: http.MethodPut,
		Path:   g.path(),
		Body:   want,
		Out:    &payload,
		Retry:  core.RetryAlways,
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "set the merge settings of %s", g.Slug())
	}

	got := payload.toRepoInfo().Merge
	if !templated {
		got.SquashTitle, got.SquashMessage = s.SquashTitle, s.SquashMessage
	}
	if got != s {
		return core.Errorf(core.ErrConflict, "set the merge settings of %s", g.Slug()).
			WithWhy("GitLab kept %+v rather than %+v", got, s).
			WithFix("check whether a group policy fixes how this project merges")
	}
	return nil
}

// CommitStatuses returns the latest verdict from each context on a commit,
// newest first. GitLab's endpoint is a history — every status ever published,
// including the ones since replaced — and a history is not what a caller
// asking "what do the checks say" means (spec 09 §2).
func (g *GitLab) CommitStatuses(ctx context.Context, sha string) ([]core.CommitStatus, error) {
	if sha == "" {
		return nil, core.Errorf(core.ErrValidation, "read the statuses on a commit of %s", g.Slug()).
			WithWhy("no commit was named")
	}
	var (
		statuses []core.CommitStatus
		seen     = map[string]bool{}
	)
	err := paginate(ctx, g, g.statusRequest(sha), func(s glStatus) bool {
		if seen[s.Name] {
			return true
		}
		seen[s.Name] = true
		statuses = append(statuses, s.toCommitStatus())
		return true
	})
	if err != nil {
		return nil, core.Explain(err, "read the statuses on %s of %s", sha, g.Slug())
	}
	return statuses, nil
}

// statusRequest asks for a commit's statuses newest first.
//
// GitLab orders them by id, newest first, and an id is the order they were
// received in. Ordering by the time they were written can tie: two runs of the
// same workflow second finish in the same second, and the walk would then keep
// whichever of them the database happened to answer with.
func (g *GitLab) statusRequest(sha string) core.Request {
	query := g.page()
	query.Set("order_by", "id")
	query.Set("sort", "desc")
	return core.Request{Path: g.path("repository", "commits", sha, "statuses"), Query: query}
}

// SetCommitStatus publishes a status on a commit, unless that context already
// says exactly this — GitLab keeps every status it is told, so publishing the
// same verdict on every run would fill a commit's history with repetitions of
// one sentence (spec 09 §2).
func (g *GitLab) SetCommitStatus(ctx context.Context, sha string, s core.CommitStatus) error {
	s.Context = strings.TrimSpace(s.Context)
	s.Description = strings.TrimSpace(s.Description)
	s.TargetURL = strings.TrimSpace(s.TargetURL)

	switch {
	case sha == "":
		return core.Errorf(core.ErrValidation, "publish a status on %s", g.Slug()).
			WithWhy("no commit was named")
	case s.Context == "":
		return core.Errorf(core.ErrValidation, "publish a status on %s of %s", sha, g.Slug()).
			WithWhy("the status names no context, so nothing could replace it later").
			WithFix("give the status a context, such as flow/policy")
	case !slices.Contains(gitLabStatusStates, s.State):
		return core.Errorf(core.ErrValidation, "publish the %s status on %s of %s", s.Context, sha, g.Slug()).
			WithWhy("%q is not a state a commit status can be in", s.State).
			WithFix("use one of %s", strings.Join(gitLabStatusStates, ", "))
	}

	g.writing.Lock()
	defer g.writing.Unlock()

	latest, err := g.latestStatus(ctx, sha, s.Context)
	if err != nil {
		return err
	}
	// The state is compared as GitLab will store it rather than as flow
	// said it: `error` and `failure` are one word there, so a run that
	// alternated between them would otherwise publish a status GitLab
	// cannot tell from the last one, every time.
	if latest != nil && latest.State == statusStateGitLab(statusRequestStateGitLab(s.State)) &&
		latest.Description == s.Description && latest.TargetURL == s.TargetURL {
		return nil
	}

	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("statuses", sha),
		Body: glStatusRequest{
			State:       statusRequestStateGitLab(s.State),
			Name:        s.Context,
			Description: s.Description,
			TargetURL:   s.TargetURL,
		},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "publish the %s status on %s of %s", s.Context, sha, g.Slug())
	}
	return nil
}

// latestStatus returns the newest status from one context, or nil.
func (g *GitLab) latestStatus(ctx context.Context, sha, want string) (*core.CommitStatus, error) {
	var found *core.CommitStatus
	err := paginate(ctx, g, g.statusRequest(sha), func(s glStatus) bool {
		if s.Name != want {
			return true
		}
		status := s.toCommitStatus()
		found = &status
		return false
	})
	if err != nil {
		return nil, core.Explain(err, "read the %s status on %s of %s", want, sha, g.Slug())
	}
	return found, nil
}

// UpsertComment writes flow's comment on a merge request, editing the one
// carrying the marker rather than writing a second one.
func (g *GitLab) UpsertComment(ctx context.Context, number int, m core.Marker, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "comment on merge request !%d of %s", number, g.Slug()).
			WithWhy("the comment carries no marker, so flow could never find it again").
			WithFix("mark the comment with the kind of writing it is")
	}
	// GitLab renders notes as markdown and shows nobody an HTML comment,
	// so the marker goes in one rather than on a visible line (spec 11 §1).
	marked := core.MarkBody(body, m, true)

	g.writing.Lock()
	defer g.writing.Unlock()

	existing, err := g.findComment(ctx, number, m)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Body == marked {
			return nil
		}
		req := core.Request{
			Method: http.MethodPut,
			Path:   g.path("merge_requests", strconv.Itoa(number), "notes", existing.ID),
			Body:   glNoteRequest{Body: marked},
		}
		if _, err := g.do(ctx, req); err != nil {
			return core.Explain(err, "update flow's comment on merge request !%d of %s", number, g.Slug())
		}
		return nil
	}

	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("merge_requests", strconv.Itoa(number), "notes"),
		Body:   glNoteRequest{Body: marked},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "comment on merge request !%d of %s", number, g.Slug())
	}
	return nil
}

// Comment returns flow's marked comment on a merge request, or nil when there
// is none.
func (g *GitLab) Comment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	return g.findComment(ctx, number, m)
}

// DeleteComment removes flow's marked comment. Deleting one that is not there
// succeeds: the caller asked for it to be gone, and it is.
func (g *GitLab) DeleteComment(ctx context.Context, number int, m core.Marker) error {
	g.writing.Lock()
	defer g.writing.Unlock()

	existing, err := g.findComment(ctx, number, m)
	if err != nil || existing == nil {
		return err
	}
	req := core.Request{
		Method: http.MethodDelete,
		Path:   g.path("merge_requests", strconv.Itoa(number), "notes", existing.ID),
	}
	if _, err := g.do(ctx, req); err != nil {
		// Somebody else deleting it first is the outcome that was wanted.
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		return core.Explain(err, "delete flow's comment on merge request !%d of %s", number, g.Slug())
	}
	return nil
}

// findComment returns the first note on a merge request whose body carries a
// marker the wanted one matches.
func (g *GitLab) findComment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	if m.IsZero() {
		return nil, core.Errorf(core.ErrValidation, "find flow's comment on merge request !%d of %s", number, g.Slug()).
			WithWhy("no marker was given, and flow does not read comments it did not write")
	}
	var payload glMergeRequest
	path := g.path("merge_requests", strconv.Itoa(number))
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload, NoCache: true}); err != nil {
		return nil, core.Explain(err, "read merge request !%d of %s", number, g.Slug())
	}

	query := g.page()
	// Oldest first, so that a merge request somehow carrying two of flow's
	// comments is answered with the one flow wrote first — the same one,
	// run after run, rather than whichever is newest today.
	query.Set("order_by", "created_at")
	query.Set("sort", "asc")
	req := core.Request{
		Path:    g.path("merge_requests", strconv.Itoa(number), "notes"),
		Query:   query,
		NoCache: true,
	}
	var found *core.Comment
	err := paginate(ctx, g, req, func(n glNote) bool {
		comment, ok := n.toComment(m, payload.WebURL)
		if !ok {
			return true
		}
		found = &comment
		return false
	})
	if err != nil {
		return nil, core.Explain(err, "read flow's comment on merge request !%d of %s", number, g.Slug())
	}
	return found, nil
}

// ProposeFiles offers a set of files as a merge request from the proposal's own
// branch. flow never writes the default branch (spec 06 §4): the files are
// committed to flow/setup-<id> and offered, and proposing the same id again
// updates that branch and that merge request rather than opening another.
func (g *GitLab) ProposeFiles(ctx context.Context, p core.FileProposal) (core.PullRequest, error) {
	switch {
	case p.ID == "":
		return core.PullRequest{}, core.Errorf(core.ErrValidation, "propose files to %s", g.Slug()).
			WithWhy("the proposal has no id, so it has no branch").
			WithFix("give the proposal an id, such as files")
	case p.Title == "":
		return core.PullRequest{}, core.Errorf(core.ErrValidation, "propose files to %s", g.Slug()).
			WithWhy("the proposal has no title").
			WithFix("title it as a conventional commit, such as chore(flow): add the policy workflow")
	case len(p.Files) == 0:
		return core.PullRequest{}, core.Errorf(core.ErrValidation, "propose files to %s", g.Slug()).
			WithWhy("the proposal carries no files").
			WithFix("name the files to propose")
	}
	for _, file := range p.Files {
		if file.Path == "" {
			return core.PullRequest{}, core.Errorf(core.ErrValidation, "propose files to %s", g.Slug()).
				WithWhy("a file in the proposal has no path")
		}
	}

	g.writing.Lock()
	defer g.writing.Unlock()

	project, err := g.project(ctx)
	if err != nil {
		return core.PullRequest{}, err
	}
	branch := p.Branch()
	if err := g.ensureBranch(ctx, branch, project.DefaultBranch); err != nil {
		return core.PullRequest{}, err
	}
	for _, file := range p.Files {
		if err := g.writeFile(ctx, branch, p.Title, file); err != nil {
			return core.PullRequest{}, err
		}
	}
	return g.offer(ctx, p, branch, project.DefaultBranch)
}

// ensureBranch creates the proposal's branch at the tip of base when it is not
// there. A branch that already exists is left where it is: it carries the
// previous proposal, and resetting it would throw away a commit somebody may
// have added to the merge request.
func (g *GitLab) ensureBranch(ctx context.Context, branch, base string) error {
	read := core.Request{Path: g.path("repository", "branches", branch)}
	switch _, err := g.do(ctx, read); {
	case err == nil:
		return nil
	case !errors.Is(err, core.ErrNotFound):
		return core.Explain(err, "read the branch %s of %s", branch, g.Slug())
	}
	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("repository", "branches"),
		Body:   glBranchRequest{Branch: branch, Ref: base},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "branch %s off %s of %s", branch, base, g.Slug())
	}
	return nil
}

// writeFile commits one file to the proposal's branch, and writes nothing when
// the branch already holds exactly those bytes — a proposal re-run every hour
// should not be a commit every hour.
func (g *GitLab) writeFile(ctx context.Context, branch, message string, file core.ProposedFile) error {
	held, err := g.File(ctx, file.Path, branch)
	switch {
	case err == nil:
		if bytes.Equal(held, file.Content) {
			return nil
		}
	case !errors.Is(err, core.ErrNotFound):
		return err
	}

	// GitLab creates and updates a file at the same address with different
	// methods, and takes the content base64-encoded so that a file which is
	// not text survives the round trip.
	write := core.Request{
		Method: http.MethodPut,
		Path:   g.path("repository", "files", file.Path),
		Body: glFileRequest{
			Branch:        branch,
			Content:       base64.StdEncoding.EncodeToString(file.Content),
			Encoding:      "base64",
			CommitMessage: message,
		},
	}
	if errors.Is(err, core.ErrNotFound) {
		write.Method = http.MethodPost
	}
	if _, err := g.do(ctx, write); err != nil {
		return core.Explain(err, "write %s on %s of %s", file.Path, branch, g.Slug())
	}
	return nil
}

// offer opens the merge request for a proposal, or updates the one that is
// already open for its branch.
func (g *GitLab) offer(ctx context.Context, p core.FileProposal, branch, base string) (core.PullRequest, error) {
	open, err := g.mergeRequestFromBranch(ctx, branch)
	if err != nil {
		return core.PullRequest{}, err
	}
	var payload glMergeRequest
	req := core.Request{Method: http.MethodPost, Path: g.path("merge_requests"), Out: &payload}
	if open > 0 {
		req.Method = http.MethodPut
		req.Path = g.path("merge_requests", strconv.Itoa(open))
		req.Body = glMergeRequestUpdate{Title: p.Title, Description: p.Body}
	} else {
		req.Body = glMergeRequestCreate{
			SourceBranch:       branch,
			TargetBranch:       base,
			Title:              p.Title,
			Description:        p.Body,
			RemoveSourceBranch: true,
		}
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.PullRequest{}, core.Explain(err, "offer %s to %s of %s", branch, base, g.Slug())
	}
	return payload.toPullRequest(), nil
}

// mergeRequestFromBranch returns the number of the open merge request coming
// from a branch, or zero when there is none. GitLab narrows a listing to a
// source branch itself, so this costs one page however many are open.
func (g *GitLab) mergeRequestFromBranch(ctx context.Context, branch string) (int, error) {
	query := g.page()
	query.Set("state", "opened")
	query.Set("source_branch", branch)
	query.Set("order_by", "updated_at")
	query.Set("sort", "desc")

	var found int
	err := paginate(ctx, g, core.Request{Path: g.path("merge_requests"), Query: query}, func(m glMergeRequest) bool {
		found = m.IID
		return false
	})
	if err != nil {
		return 0, core.Explain(err, "look for the merge request from %s of %s", branch, g.Slug())
	}
	return found, nil
}

// sender is the client this adapter speaks through.
func (g *GitLab) sender() *core.Client { return g.client }

// do sends one request with GitLab's media type.
func (g *GitLab) do(ctx context.Context, r core.Request) (*core.Response, error) {
	return g.client.Do(ctx, g.decorate(r))
}

// decorate returns the request with GitLab's media type on it, leaving an
// Accept the caller chose alone.
func (g *GitLab) decorate(r core.Request) core.Request {
	header := http.Header{}
	for name, values := range r.Header {
		header[name] = slices.Clone(values)
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", gitLabAccept)
	}
	r.Header = header
	return r
}

// page is the query of a request that walks a collection.
func (g *GitLab) page() url.Values {
	return url.Values{"per_page": []string{strconv.Itoa(gitLabPageSize)}}
}

// path builds a path under this project.
//
// Every segment is escaped whole, slashes included. That is the opposite of
// GitHub's rule and it is GitLab's: a project is addressed by its full path as
// one segment — `acme%2Fplatform%2Fwidget` — and so are a branch with a slash
// in its name and a file in a directory. A slash left alone would make GitLab
// look for an endpoint rather than for a name.
func (g *GitLab) path(segments ...string) string {
	parts := make([]string, 0, len(segments)+2)
	parts = append(parts, "projects", url.PathEscape(g.Slug()))
	for _, segment := range segments {
		parts = append(parts, url.PathEscape(segment))
	}
	return strings.Join(parts, "/")
}

// gitLabListState maps a query's state onto the one GitLab's list endpoint
// understands. GitLab is the one forge of the three with a merged state of its
// own, so nothing has to be filtered back out.
func gitLabListState(state string) string {
	switch state {
	case core.PRStateOpen:
		return "opened"
	case core.PRStateMerged:
		return "merged"
	case core.PRStateClosed:
		return "closed"
	default:
		return "all"
	}
}
