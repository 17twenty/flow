// Package forge holds the Git-host adapters: GitHub in v1, Gitea/Forgejo and
// GitLab in phase 7. Each is a thin REST client over [core.Client] and depends
// on core alone, so that nothing about a provider leaks into the engine
// (spec 01 §2, spec 10).
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

// GitHub adapter constants (spec 10 §2).
const (
	// GitHubKind is the adapter name, matching assets/capabilities/github.yaml
	// and forge.type in configuration.
	GitHubKind = "github"
	// GitHubAPI is the API root of github.com. GitHub Enterprise Server uses
	// https://<host>/api/v3.
	GitHubAPI = "https://api.github.com"
	// gitHubAccept is the media type every REST call asks for.
	gitHubAccept = "application/vnd.github+json"
	// gitHubRaw returns a file's bytes rather than a JSON envelope.
	gitHubRaw = "application/vnd.github.raw"
	// gitHubAPIVersion pins the REST API's dated version.
	gitHubAPIVersion = "2022-11-28"
	// gitHubPageSize is the largest page GitHub serves.
	gitHubPageSize = 100
	// gitHubLinkPageSize is how many linked issues one GraphQL page carries
	// (spec 10 §2). It is a page size and not a ceiling: the walk follows
	// the cursor, because a pull request that closes a twenty-first ticket
	// is still closing it.
	gitHubLinkPageSize = 20
	// gitHubStatusDescription is the longest description GitHub keeps on a
	// commit status.
	gitHubStatusDescription = 140
	// gitHubScopeHeader is the header a classic token's scopes come back
	// in. Nothing else sends it, which is how flow tells a classic token
	// from a fine-grained one, a GitHub App or the Actions token
	// (spec 05 §3).
	gitHubScopeHeader = "X-OAuth-Scopes"
	// gitHubScopeRepo is the classic scope that permits writing to any
	// repository, and gitHubScopePublicRepo the one that permits writing
	// only to public ones.
	gitHubScopeRepo       = "repo"
	gitHubScopePublicRepo = "public_repo"
)

// gitHubWriteTokens are the permission tokens a credential that may write to
// the repository holds. They are the words assets/capabilities/github.yaml
// requires, because a probe and a declaration that do not share a vocabulary
// can never agree (spec 05 §1).
var gitHubWriteTokens = []string{"statuses_write", "contents_write", "pulls_write"}

// gitHubStatusStates are the states GitHub accepts on a commit status, which
// are core's own (spec 10 §2).
var gitHubStatusStates = []string{
	core.StatusSuccess, core.StatusFailure, core.StatusPending, core.StatusError,
}

// GitHubHeader returns the headers every GitHub REST call carries. The root
// passes it to [core.ClientOptions] so that the adapter and anything else
// sharing the client speak the same API version.
func GitHubHeader() http.Header {
	h := http.Header{}
	h.Set("Accept", gitHubAccept)
	h.Set("X-GitHub-Api-Version", gitHubAPIVersion)
	return h
}

// GitHubOptions configures a [GitHub] adapter.
type GitHubOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Owner and Repo name the repository the adapter speaks for.
	Owner string
	Repo  string
	// Capabilities is the declaration loaded from
	// assets/capabilities/github.yaml. The adapter never invents it: what
	// flow believes it can do comes from the declaration, not from the code
	// (spec 05 §1).
	Capabilities core.CapabilitySet
}

// GitHub is the GitHub forge adapter.
type GitHub struct {
	client *core.Client
	owner  string
	repo   string
	caps   core.CapabilitySet
	// graphQL is the endpoint the few GraphQL queries go to, worked out
	// once from the REST root the client was built with.
	graphQL string
	// writing serialises the calls that change the repository. GitHub's
	// secondary rate limit counts concurrent mutations of one repository,
	// and answers a burst of them with a refusal that costs more than the
	// concurrency ever bought (spec 10 §2).
	writing sync.Mutex
}

// NewGitHub returns an adapter for one repository.
func NewGitHub(o GitHubOptions) (*GitHub, error) {
	switch {
	case o.Client == nil:
		return nil, core.Errorf(core.ErrValidation, "build the github adapter").
			WithWhy("no HTTP client was given")
	case o.Owner == "" || o.Repo == "":
		return nil, core.Errorf(core.ErrValidation, "build the github adapter").
			WithWhy("the repository was given as %q/%q", o.Owner, o.Repo).
			WithFix("name the repository as owner/name")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the github adapter").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/github.yaml and pass the declaration")
	}
	return &GitHub{
		client:  o.Client,
		owner:   o.Owner,
		repo:    o.Repo,
		caps:    o.Capabilities,
		graphQL: gitHubGraphQL(o.Client.BaseURL()),
	}, nil
}

// gitHubGraphQL returns the GraphQL endpoint belonging to a REST root.
// github.com answers REST at the root and GraphQL at /graphql beside it;
// GitHub Enterprise Server puts REST under /api/v3 and GraphQL at /api/graphql
// — a sibling, not a child, so the endpoint is derived rather than resolved
// against the base like every other path (spec 10 §2).
func gitHubGraphQL(base string) string {
	trimmed := strings.TrimSuffix(base, "/")
	if host, ok := strings.CutSuffix(trimmed, "/api/v3"); ok {
		return host + "/api/graphql"
	}
	return trimmed + "/graphql"
}

// Kind is the adapter's name.
func (g *GitHub) Kind() string { return GitHubKind }

// Capabilities is what this adapter declares it can do.
func (g *GitHub) Capabilities() core.CapabilitySet { return g.caps }

// Slug is the repository as people write it.
func (g *GitHub) Slug() string { return g.owner + "/" + g.repo }

// Repo describes the repository: its trunk and how pull requests may land.
func (g *GitHub) Repo(ctx context.Context) (core.RepoInfo, error) {
	var payload ghRepo
	if _, err := g.do(ctx, core.Request{Path: g.path(), Out: &payload}); err != nil {
		return core.RepoInfo{}, core.Explain(err, "read the repository %s", g.Slug())
	}
	return payload.toRepoInfo(), nil
}

// PullRequest returns one pull request with the reviews, the file summary and
// the linked tickets the policy gate reads.
func (g *GitHub) PullRequest(ctx context.Context, number int) (core.PullRequest, error) {
	var payload ghPull
	path := g.path("pulls", strconv.Itoa(number))
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		return core.PullRequest{}, core.Explain(err, "read pull request #%d of %s", number, g.Slug())
	}
	pr := payload.toPullRequest()

	reviews, err := g.reviews(ctx, number)
	if err != nil {
		return core.PullRequest{}, err
	}
	pr.Reviews = reviews

	paths, err := g.changedPaths(ctx, number)
	if err != nil {
		return core.PullRequest{}, err
	}
	if len(paths) > 0 {
		pr.Files.Paths = paths
	}

	closing, err := g.closingIssues(ctx, number)
	if err != nil {
		return core.PullRequest{}, err
	}
	pr.ClosingIssues = closing
	return pr, nil
}

// gitHubClosingIssues asks for the tickets a pull request closes: the links
// GitHub made from the closing keywords in the body and from the Development
// sidebar, which are the only linkage flow trusts (P12, spec 04 §3).
const gitHubClosingIssues = `query($owner:String!,$repo:String!,$number:Int!,$first:Int!,$after:String){` +
	`repository(owner:$owner,name:$repo){` +
	`pullRequest(number:$number){` +
	`closingIssuesReferences(first:$first,after:$after){` +
	`nodes{number repository{nameWithOwner}}` +
	`pageInfo{hasNextPage endCursor}` +
	`}}}}`

// closingIssues returns the tickets the forge itself says a pull request
// closes. An adapter whose declaration cannot verify the link returns none, so
// that the tracker falls back to its own convention instead of being told a
// pull request closes nothing (spec 04 §3, spec 05 §1).
func (g *GitHub) closingIssues(ctx context.Context, number int) ([]string, error) {
	if !g.caps.CanVerify(core.CapLinkedIssuesRead) {
		return nil, nil
	}
	variables := map[string]any{
		"owner":  g.owner,
		"repo":   g.repo,
		"number": number,
		"first":  gitHubLinkPageSize,
	}
	var keys []string
	for page := 0; page < core.MaxPages; page++ {
		data, err := graphQL[ghClosingIssues](ctx, g, gitHubClosingIssues, variables)
		if err != nil {
			return nil, core.Explain(err, "read the tickets pull request #%d of %s closes", number, g.Slug())
		}
		keys = append(keys, data.keys(g.owner, g.repo)...)
		pull := data.Repository.PullRequest
		if pull == nil {
			return keys, nil
		}
		info := pull.ClosingIssuesReferences.PageInfo
		if !info.HasNextPage || info.EndCursor == "" {
			return keys, nil
		}
		variables["after"] = info.EndCursor
	}
	return nil, core.Errorf(core.ErrUnavailable, "read the tickets pull request #%d of %s closes", number, g.Slug()).
		WithWhy("the provider offered more than %d pages of linked tickets", core.MaxPages).
		WithFix("link the pull request to the tickets it really closes")
}

// reviews returns the submitted reviews on a pull request, oldest first.
func (g *GitHub) reviews(ctx context.Context, number int) ([]core.Review, error) {
	var reviews []core.Review
	req := core.Request{
		Path:  g.path("pulls", strconv.Itoa(number), "reviews"),
		Query: url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}},
	}
	err := paginate(ctx, g, req, func(r ghReview) bool {
		if review, ok := r.toReview(); ok {
			reviews = append(reviews, review)
		}
		return true
	})
	if err != nil {
		return nil, core.Explain(err, "read the reviews on pull request #%d of %s", number, g.Slug())
	}
	return reviews, nil
}

// changedPaths returns the first page of files a pull request touches, sorted.
// GitHub caps the list at 3,000 files and flow never needs more than a summary,
// so one page is read and the counts come from the pull request itself.
func (g *GitHub) changedPaths(ctx context.Context, number int) ([]string, error) {
	var files []ghFile
	req := core.Request{
		Path:  g.path("pulls", strconv.Itoa(number), "files"),
		Query: url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}},
		Out:   &files,
	}
	if _, err := g.do(ctx, req); err != nil {
		return nil, core.Explain(err, "read the files of pull request #%d of %s", number, g.Slug())
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.Filename != "" {
			paths = append(paths, file.Filename)
		}
	}
	slices.Sort(paths)
	return paths, nil
}

// PullRequests returns the pull requests matching a query, newest first by
// update. Reviews and the file summary are not on GitHub's list payload; a
// caller that needs them asks for the pull request itself.
func (g *GitHub) PullRequests(ctx context.Context, q core.PRQuery) ([]core.PullRequest, error) {
	query := url.Values{
		"state":     []string{gitHubListState(q.State)},
		"sort":      []string{"updated"},
		"direction": []string{"desc"},
		"per_page":  []string{strconv.Itoa(gitHubPageSize)},
	}
	if q.Base != "" {
		query.Set("base", q.Base)
	}

	var prs []core.PullRequest
	err := paginate(ctx, g, core.Request{Path: g.path("pulls"), Query: query}, func(p ghPull) bool {
		pr := p.toPullRequest()
		// The list is newest first by update, so the first one older than
		// the window ends the walk rather than being skipped.
		if !q.UpdatedSince.IsZero() && pr.UpdatedAt.Before(q.UpdatedSince) {
			return false
		}
		if q.State != "" && pr.State != q.State {
			return true
		}
		prs = append(prs, listed(pr))
		return q.Limit <= 0 || len(prs) < q.Limit
	})
	if err != nil {
		return nil, core.Explain(err, "list the pull requests of %s", g.Slug())
	}
	return prs, nil
}

// Commits returns commits on a ref, newest first.
func (g *GitHub) Commits(ctx context.Context, q core.CommitQuery) ([]core.Commit, error) {
	query := url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}}
	if q.Ref != "" {
		query.Set("sha", q.Ref)
	}
	if !q.Since.IsZero() {
		query.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		query.Set("until", q.Until.UTC().Format(time.RFC3339))
	}

	var commits []core.Commit
	err := paginate(ctx, g, core.Request{Path: g.path("commits"), Query: query}, func(c ghCommit) bool {
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
func (g *GitHub) Compare(ctx context.Context, base, head string) (core.Comparison, error) {
	if base == "" || head == "" {
		return core.Comparison{}, core.Errorf(core.ErrValidation, "compare %q with %q", base, head).
			WithWhy("both a base and a head are needed")
	}
	basehead := base + "..." + head
	var payload ghCompare
	if _, err := g.do(ctx, core.Request{Path: g.path("compare", basehead), Out: &payload}); err != nil {
		return core.Comparison{}, core.Explain(err, "compare %s with %s in %s", base, head, g.Slug())
	}
	if !payload.known() {
		return core.Comparison{}, core.Errorf(core.ErrUnavailable, "compare %s with %s in %s", base, head, g.Slug()).
			WithWhy("the provider called the ancestry %q, which flow does not understand", payload.Status).
			WithFix("this is a provider or flow bug; report it with the command you ran")
	}
	return core.Comparison{
		Base:     base,
		Head:     head,
		Status:   payload.Status,
		AheadBy:  payload.AheadBy,
		BehindBy: payload.BehindBy,
	}, nil
}

// File returns the content of a file at a ref, so that doctor can read the
// workflow that is actually on the trunk.
func (g *GitHub) File(ctx context.Context, filePath, ref string) ([]byte, error) {
	if filePath == "" {
		return nil, core.Errorf(core.ErrValidation, "read a file from %s", g.Slug()).
			WithWhy("no path was given")
	}
	query := url.Values{}
	if ref != "" {
		query.Set("ref", ref)
	}
	header := http.Header{}
	header.Set("Accept", gitHubRaw)

	var content []byte
	req := core.Request{
		Path:   g.path("contents", filePath),
		Query:  query,
		Header: header,
		Out:    &content,
	}
	if _, err := g.do(ctx, req); err != nil {
		return nil, core.Explain(err, "read %s from %s at %s", filePath, g.Slug(), refOrDefault(ref))
	}
	return content, nil
}

// Probe reports what the credential may do with this repository, in one
// request: the role the account holds, and — for a classic token, which says
// so in a response header — the scopes the token may use that role through
// (spec 05 §3).
//
// A role and a scope are two different limits and both apply. A maintainer
// whose token carries no scope that permits writing cannot write, so the
// permissions flow reports are the role's, less whatever the token withholds.
// A credential that reports no scopes at all — a fine-grained token, a GitHub
// App, the Actions token — is limited by its role alone, because its real
// limits are not in any header and a guess would only ever be wrong.
func (g *GitHub) Probe(ctx context.Context) (core.Permissions, error) {
	var payload ghProbe
	res, err := g.do(ctx, core.Request{Path: g.path(), Out: &payload})
	if err != nil {
		return core.Permissions{}, core.Explain(err, "find out what the credential may do in %s", g.Slug())
	}

	role := payload.Permissions
	permissions := core.Permissions{
		Evidence: []string{"repository permissions: " + role.String()},
	}
	permissions.Tokens = append(permissions.Tokens, role.tokens()...)

	scopes, classic := gitHubScopes(res.Header)
	if classic {
		permissions.Evidence = append(permissions.Evidence,
			gitHubScopeHeader+": "+strings.Join(scopes, ", "))
		permissions.Tokens = append(permissions.Tokens, scopes...)
	}
	if role.writes() && (!classic || gitHubScopesWrite(scopes, payload.public())) {
		permissions.Tokens = append(permissions.Tokens, gitHubWriteTokens...)
	}
	return permissions, nil
}

// gitHubScopes returns the scopes a classic token's response reported, and
// whether the header was there at all. An empty header is a classic token
// with no scopes, which is not the same thing as no header.
func gitHubScopes(header http.Header) ([]string, bool) {
	raw, ok := header[http.CanonicalHeaderKey(gitHubScopeHeader)]
	if !ok {
		return nil, false
	}
	var scopes []string
	for _, scope := range strings.Split(strings.Join(raw, ","), ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes, true
}

// gitHubScopesWrite reports whether a classic token's scopes let it write to
// this repository. `repo` covers every repository; `public_repo` covers only
// the public ones, so what the repository is matters to the answer.
func gitHubScopesWrite(scopes []string, public bool) bool {
	return slices.Contains(scopes, gitHubScopeRepo) ||
		(public && slices.Contains(scopes, gitHubScopePublicRepo))
}

// Protection returns the effective protection of a branch: what the classic
// mechanism says and what the rulesets say, together. GitHub has both and a
// repository may use either or neither, so a branch is as protected as the
// strictest thing said about it (spec 10 §2).
func (g *GitHub) Protection(ctx context.Context, branch string) (core.Protection, error) {
	if branch == "" {
		return core.Protection{}, core.Errorf(core.ErrValidation, "read the protection of %s", g.Slug()).
			WithWhy("no branch was named")
	}
	classic, protected, err := g.classicProtection(ctx, branch)
	if err != nil {
		return core.Protection{}, err
	}
	rules, ruled, err := g.ruleProtection(ctx, branch)
	if err != nil {
		return core.Protection{}, err
	}
	dormant, err := g.dormantRules(ctx, branch)
	if err != nil {
		return core.Protection{}, err
	}
	var effective core.Protection
	switch {
	case protected && ruled:
		effective = classic.Merge(rules)
	case protected:
		effective = classic
	case ruled:
		effective = rules
	default:
		effective = core.Protection{Branch: branch}
	}
	effective.Dormant = dormant
	return effective, nil
}

// classicProtection reads branch protection, reporting whether the branch has
// any. GitHub answers 404 for a branch that is not protected, which is an
// answer rather than a failure.
func (g *GitHub) classicProtection(ctx context.Context, branch string) (core.Protection, bool, error) {
	var payload ghProtection
	path := g.path("branches", branch, "protection")
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return core.Protection{Branch: branch}, false, nil
		}
		return core.Protection{}, false, core.Explain(err, "read the protection of %s on %s", branch, g.Slug())
	}
	return payload.toProtection(branch), true, nil
}

// ruleProtection reads the rulesets in force on a branch, reporting whether
// any apply. An empty list is the usual answer: rulesets are newer than
// classic protection and most repositories have none.
func (g *GitHub) ruleProtection(ctx context.Context, branch string) (core.Protection, bool, error) {
	var rules []ghRule
	req := core.Request{
		Path:  g.path("rules", "branches", branch),
		Query: url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}},
	}
	err := paginate(ctx, g, req, func(rule ghRule) bool {
		rules = append(rules, rule)
		return true
	})
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return core.Protection{Branch: branch}, false, nil
		}
		return core.Protection{}, false, core.Explain(err, "read the rules in force on %s of %s", branch, g.Slug())
	}
	if len(rules) == 0 {
		return core.Protection{Branch: branch}, false, nil
	}
	return rulesToProtection(branch, rules), true, nil
}

// dormantRules are the rulesets that cover this branch and are not being
// enforced: GitHub creates every ruleset with its enforcement `disabled`, and
// `evaluate` reports without blocking, so a repository can hold a complete,
// correct ruleset that does nothing at all.
//
// None of it is protection and none of it is reported as such. It is read
// because the remedy differs entirely: a branch with no ruleset needs one
// written, and a branch with a dormant one needs a switch flicked, and a
// report that says the first when the second is true sends somebody to build
// what they already have.
//
// Only the ones that are not in force are fetched in full — the enforced ones
// have already been answered by the effective-rules endpoint — so the usual
// repository pays for one list call and nothing else.
func (g *GitHub) dormantRules(ctx context.Context, branch string) ([]core.DormantRule, error) {
	var summaries []ghRulesetSummary
	req := core.Request{
		Path:  g.path("rulesets"),
		Query: url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}},
	}
	err := paginate(ctx, g, req, func(summary ghRulesetSummary) bool {
		if summary.Enforcement != ghEnforcementActive {
			summaries = append(summaries, summary)
		}
		return true
	})
	if err != nil {
		// Reading rulesets is a nicety: it sharpens a remedy and decides
		// nothing. A credential that may not look is not a failed run.
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrPermission) {
			return nil, nil
		}
		return nil, core.Explain(err, "read the rulesets of %s", g.Slug())
	}
	if len(summaries) == 0 {
		return nil, nil
	}

	trunk, err := g.Repo(ctx)
	if err != nil {
		return nil, err
	}
	var dormant []core.DormantRule
	for _, summary := range summaries {
		var ruleset ghRuleset
		path := g.path("rulesets", strconv.FormatInt(summary.ID, 10))
		if _, err := g.do(ctx, core.Request{Path: path, Out: &ruleset}); err != nil {
			if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrPermission) {
				continue
			}
			return nil, core.Explain(err, "read ruleset %q of %s", summary.Name, g.Slug())
		}
		if !ruleset.covers(branch, trunk.DefaultBranch) {
			continue
		}
		dormant = append(dormant, core.DormantRule{
			Name:  ruleset.Name,
			State: ruleset.Enforcement,
			URL:   ruleset.Links.HTML.Href,
		})
	}
	return dormant, nil
}

// SetProtection applies protection additively: what the branch already
// requires it goes on requiring, and what it already forbids stays forbidden
// (spec 06 §4). GitHub's PUT replaces the whole of classic protection, so the
// merge happens here, against what the branch says now.
func (g *GitHub) SetProtection(ctx context.Context, branch string, want core.Protection) error {
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

	effective, err := g.Protection(ctx, branch)
	if err != nil {
		return err
	}
	if sameProtection(effective, effective.Merge(want)) {
		return nil
	}
	classic, _, err := g.classicProtection(ctx, branch)
	if err != nil {
		return err
	}
	merged := classic.Merge(want)
	req := core.Request{
		Method: http.MethodPut,
		Path:   g.path("branches", branch, "protection"),
		Body:   protectionRequest(merged),
		// Writing the same whole state twice leaves the same state, so
		// a retry cannot do anything the first attempt did not.
		Retry: core.RetryAlways,
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "protect %s of %s", branch, g.Slug())
	}
	return nil
}

// sameProtection reports whether two protections say the same thing. Every
// field is named rather than compared whole, because [core.Protection] holds a
// slice; a test walks the type and fails if a field is ever added here without
// being added below.
func sameProtection(a, b core.Protection) bool {
	return a.Branch == b.Branch &&
		a.Enabled == b.Enabled &&
		slices.Equal(a.RequiredChecks, b.RequiredChecks) &&
		a.StrictChecks == b.StrictChecks &&
		a.RequiredApprovals == b.RequiredApprovals &&
		a.DismissStaleReviews == b.DismissStaleReviews &&
		a.RequireLinearHistory == b.RequireLinearHistory &&
		a.RequireConversationResolution == b.RequireConversationResolution &&
		a.EnforceAdmins == b.EnforceAdmins &&
		a.AllowForcePush == b.AllowForcePush &&
		a.AllowDeletions == b.AllowDeletions &&
		slices.Equal(a.Dormant, b.Dormant)
}

// SetMergeSettings sets the repository's merge strategies. They are one
// setting written whole (spec 06 §4): the strategies a team did not ask for
// are the ones it wants turned off, so the call carries all of them.
func (g *GitHub) SetMergeSettings(ctx context.Context, s core.MergeSettings) error {
	if !s.AllowSquash && !s.AllowMerge && !s.AllowRebase {
		return core.Errorf(core.ErrValidation, "set the merge settings of %s", g.Slug()).
			WithWhy("no way of merging would be left, which GitHub refuses").
			WithFix("allow at least one of squash, merge commit or rebase")
	}

	g.writing.Lock()
	defer g.writing.Unlock()

	var payload ghRepo
	req := core.Request{
		Method: http.MethodPatch,
		Path:   g.path(),
		Body:   mergeRequest(s),
		Out:    &payload,
		Retry:  core.RetryAlways,
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "set the merge settings of %s", g.Slug())
	}
	if got := payload.toRepoInfo().Merge; got != s {
		return core.Errorf(core.ErrConflict, "set the merge settings of %s", g.Slug()).
			WithWhy("GitHub kept %+v rather than %+v", got, s).
			WithFix("check whether an organisation policy fixes the merge strategies")
	}
	return nil
}

// CommitStatuses returns the latest verdict from each context on a commit,
// newest first. GitHub's endpoint is a history — every status ever published,
// including the ones since replaced — and a history is not what a caller
// asking "what do the checks say" means (spec 09 §2).
func (g *GitHub) CommitStatuses(ctx context.Context, sha string) ([]core.CommitStatus, error) {
	if sha == "" {
		return nil, core.Errorf(core.ErrValidation, "read the statuses on a commit of %s", g.Slug()).
			WithWhy("no commit was named")
	}
	var (
		statuses []core.CommitStatus
		seen     = map[string]bool{}
	)
	req := core.Request{
		Path:  g.path("commits", sha, "statuses"),
		Query: url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}},
	}
	err := paginate(ctx, g, req, func(s ghStatus) bool {
		if seen[s.Context] {
			return true
		}
		seen[s.Context] = true
		statuses = append(statuses, s.toCommitStatus())
		return true
	})
	if err != nil {
		return nil, core.Explain(err, "read the statuses on %s of %s", sha, g.Slug())
	}
	return statuses, nil
}

// SetCommitStatus publishes a status on a commit, unless that context already
// says exactly this — GitHub keeps every status it is told, so publishing the
// same verdict on every run would fill a commit's history with repetitions of
// one sentence (spec 09 §2).
func (g *GitHub) SetCommitStatus(ctx context.Context, sha string, s core.CommitStatus) error {
	switch {
	case sha == "":
		return core.Errorf(core.ErrValidation, "publish a status on %s", g.Slug()).
			WithWhy("no commit was named")
	case s.Context == "":
		return core.Errorf(core.ErrValidation, "publish a status on %s of %s", sha, g.Slug()).
			WithWhy("the status names no context, so nothing could replace it later").
			WithFix("give the status a context, such as flow/policy")
	case !slices.Contains(gitHubStatusStates, s.State):
		return core.Errorf(core.ErrValidation, "publish the %s status on %s of %s", s.Context, sha, g.Slug()).
			WithWhy("%q is not a state a commit status can be in", s.State).
			WithFix("use one of %s", strings.Join(gitHubStatusStates, ", "))
	}
	s.Description = truncate(s.Description, gitHubStatusDescription)

	g.writing.Lock()
	defer g.writing.Unlock()

	latest, err := g.latestStatus(ctx, sha, s.Context)
	if err != nil {
		return err
	}
	if latest != nil && latest.State == s.State && latest.Description == s.Description && latest.TargetURL == s.TargetURL {
		return nil
	}

	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("statuses", sha),
		Body: ghStatusRequest{
			State:       s.State,
			Context:     s.Context,
			Description: s.Description,
			TargetURL:   s.TargetURL,
		},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "publish the %s status on %s of %s", s.Context, sha, g.Slug())
	}
	return nil
}

// latestStatus returns the newest status from one context, or nil. The walk
// stops at the first one it finds, because GitHub serves them newest first.
func (g *GitHub) latestStatus(ctx context.Context, sha, want string) (*core.CommitStatus, error) {
	var found *core.CommitStatus
	req := core.Request{
		Path:    g.path("commits", sha, "statuses"),
		Query:   url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}},
		NoCache: true,
	}
	err := paginate(ctx, g, req, func(s ghStatus) bool {
		if s.Context != want {
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

// UpsertComment writes flow's comment on a pull request, editing the one
// carrying the marker rather than writing a second one. A pull request is an
// issue to GitHub's comment endpoints, which is why they are addressed as one.
func (g *GitHub) UpsertComment(ctx context.Context, number int, m core.Marker, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "comment on pull request #%d of %s", number, g.Slug()).
			WithWhy("the comment carries no marker, so flow could never find it again").
			WithFix("mark the comment with the kind of writing it is")
	}
	// GitHub keeps an HTML comment in a body and shows nobody, so the
	// marker goes in one rather than on a visible line (spec 11 §1).
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
			Method: http.MethodPatch,
			Path:   g.path("issues", "comments", existing.ID),
			Body:   ghCommentRequest{Body: marked},
		}
		if _, err := g.do(ctx, req); err != nil {
			return core.Explain(err, "update flow's comment on pull request #%d of %s", number, g.Slug())
		}
		return nil
	}

	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("issues", strconv.Itoa(number), "comments"),
		Body:   ghCommentRequest{Body: marked},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "comment on pull request #%d of %s", number, g.Slug())
	}
	return nil
}

// Comment returns flow's marked comment on a pull request, or nil when there
// is none. A pull request that is not there is an error; a comment that is not
// there is an answer.
func (g *GitHub) Comment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	return g.findComment(ctx, number, m)
}

// DeleteComment removes flow's marked comment. Deleting one that is not there
// succeeds: the caller asked for it to be gone, and it is.
func (g *GitHub) DeleteComment(ctx context.Context, number int, m core.Marker) error {
	g.writing.Lock()
	defer g.writing.Unlock()

	existing, err := g.findComment(ctx, number, m)
	if err != nil || existing == nil {
		return err
	}
	req := core.Request{Method: http.MethodDelete, Path: g.path("issues", "comments", existing.ID)}
	if _, err := g.do(ctx, req); err != nil {
		// Somebody else deleting it first is the outcome that was wanted.
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		return core.Explain(err, "delete flow's comment on pull request #%d of %s", number, g.Slug())
	}
	return nil
}

// findComment returns the first comment on a pull request whose body carries a
// marker the wanted one matches.
func (g *GitHub) findComment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	if m.IsZero() {
		return nil, core.Errorf(core.ErrValidation, "find flow's comment on pull request #%d of %s", number, g.Slug()).
			WithWhy("no marker was given, and flow does not read comments it did not write")
	}
	var found *core.Comment
	req := core.Request{
		Path:    g.path("issues", strconv.Itoa(number), "comments"),
		Query:   url.Values{"per_page": []string{strconv.Itoa(gitHubPageSize)}},
		NoCache: true,
	}
	err := paginate(ctx, g, req, func(c ghComment) bool {
		comment, ok := c.toComment(m)
		if !ok {
			return true
		}
		found = &comment
		return false
	})
	if err != nil {
		return nil, core.Explain(err, "read flow's comment on pull request #%d of %s", number, g.Slug())
	}
	return found, nil
}

// ProposeFiles offers a set of files as a pull request from the proposal's own
// branch. flow never writes the default branch (spec 06 §4): the files are
// committed to flow/setup-<id> and offered, and proposing the same id again
// updates that branch and that pull request rather than opening another.
func (g *GitHub) ProposeFiles(ctx context.Context, p core.FileProposal) (core.PullRequest, error) {
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

	repo, err := g.Repo(ctx)
	if err != nil {
		return core.PullRequest{}, err
	}
	branch := p.Branch()
	if err := g.ensureBranch(ctx, branch, repo.DefaultBranch); err != nil {
		return core.PullRequest{}, err
	}
	for _, file := range p.Files {
		if err := g.writeFile(ctx, branch, p.Title, file); err != nil {
			return core.PullRequest{}, err
		}
	}
	return g.offer(ctx, p, branch, repo.DefaultBranch)
}

// ensureBranch creates the proposal's branch at the tip of base when it is not
// there. A branch that already exists is left where it is: it carries the
// previous proposal, and resetting it would throw away a commit somebody may
// have added to the pull request.
func (g *GitHub) ensureBranch(ctx context.Context, branch, base string) error {
	switch _, err := g.ref(ctx, branch); {
	case err == nil:
		return nil
	case !errors.Is(err, core.ErrNotFound):
		return err
	}
	from, err := g.ref(ctx, base)
	if err != nil {
		return err
	}
	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("git", "refs"),
		Body:   ghRefRequest{Ref: "refs/heads/" + branch, SHA: from},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "branch %s off %s of %s", branch, base, g.Slug())
	}
	return nil
}

// ref returns the commit a branch points at.
func (g *GitHub) ref(ctx context.Context, branch string) (string, error) {
	var payload ghRef
	path := g.path("git", "ref", "heads/"+branch)
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		return "", core.Explain(err, "read the branch %s of %s", branch, g.Slug())
	}
	if payload.Object.SHA == "" {
		return "", core.Errorf(core.ErrUnavailable, "read the branch %s of %s", branch, g.Slug()).
			WithWhy("the forge named no commit for it")
	}
	return payload.Object.SHA, nil
}

// writeFile commits one file to the proposal's branch, and writes nothing when
// the branch already holds exactly those bytes — a proposal re-run every hour
// should not be a commit every hour.
func (g *GitHub) writeFile(ctx context.Context, branch, message string, file core.ProposedFile) error {
	var current ghContents
	req := core.Request{
		Path:  g.path("contents", file.Path),
		Query: url.Values{"ref": []string{branch}},
		Out:   &current,
	}
	switch _, err := g.do(ctx, req); {
	case err == nil:
		if held, ok := current.decoded(); ok && bytes.Equal(held, file.Content) {
			return nil
		}
	case !errors.Is(err, core.ErrNotFound):
		return core.Explain(err, "read %s on %s of %s", file.Path, branch, g.Slug())
	default:
		current.SHA = ""
	}

	write := core.Request{
		Method: http.MethodPut,
		Path:   g.path("contents", file.Path),
		Body: ghContentsRequest{
			Message: message,
			Content: base64.StdEncoding.EncodeToString(file.Content),
			Branch:  branch,
			SHA:     current.SHA,
		},
	}
	if _, err := g.do(ctx, write); err != nil {
		return core.Explain(err, "write %s on %s of %s", file.Path, branch, g.Slug())
	}
	return nil
}

// offer opens the pull request for a proposal, or updates the one that is
// already open for its branch.
func (g *GitHub) offer(ctx context.Context, p core.FileProposal, branch, base string) (core.PullRequest, error) {
	open, err := g.pullFromBranch(ctx, branch)
	if err != nil {
		return core.PullRequest{}, err
	}
	var payload ghPull
	req := core.Request{Method: http.MethodPost, Path: g.path("pulls"), Out: &payload}
	if open > 0 {
		req.Method = http.MethodPatch
		req.Path = g.path("pulls", strconv.Itoa(open))
		req.Body = ghPullUpdate{Title: p.Title, Body: p.Body}
	} else {
		req.Body = ghPullCreate{Title: p.Title, Body: p.Body, Head: branch, Base: base}
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.PullRequest{}, core.Explain(err, "offer %s to %s of %s", branch, base, g.Slug())
	}
	return payload.toPullRequest(), nil
}

// pullFromBranch returns the number of the open pull request coming from a
// branch, or zero when there is none.
func (g *GitHub) pullFromBranch(ctx context.Context, branch string) (int, error) {
	var found int
	req := core.Request{
		Path: g.path("pulls"),
		Query: url.Values{
			"state":     []string{"open"},
			"head":      []string{g.owner + ":" + branch},
			"sort":      []string{"updated"},
			"direction": []string{"desc"},
			"per_page":  []string{strconv.Itoa(gitHubPageSize)},
		},
	}
	err := paginate(ctx, g, req, func(pull ghPull) bool {
		if pull.Head.Ref == branch {
			found = pull.Number
			return false
		}
		return true
	})
	if err != nil {
		return 0, core.Explain(err, "look for the pull request from %s of %s", branch, g.Slug())
	}
	return found, nil
}

// sender is the client this adapter speaks through.
func (g *GitHub) sender() *core.Client { return g.client }

// do sends one request with GitHub's media type and API version.
func (g *GitHub) do(ctx context.Context, r core.Request) (*core.Response, error) {
	return g.client.Do(ctx, g.decorate(r))
}

// decorate returns the request with GitHub's media type and API version on it,
// leaving an Accept the caller chose — a raw file, say — alone.
func (g *GitHub) decorate(r core.Request) core.Request {
	header := http.Header{}
	for name, values := range r.Header {
		header[name] = slices.Clone(values)
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", gitHubAccept)
	}
	header.Set("X-GitHub-Api-Version", gitHubAPIVersion)
	r.Header = header
	return r
}

// path builds a path under this repository. A segment may hold slashes of its
// own — a ref and a file path are written with theirs, as GitHub's own URLs do
// — and everything else that would change the URL's shape is escaped.
func (g *GitHub) path(segments ...string) string {
	parts := make([]string, 0, len(segments)+3)
	parts = append(parts, "repos", url.PathEscape(g.owner), url.PathEscape(g.repo))
	for _, segment := range segments {
		parts = append(parts, escapePath(segment))
	}
	return strings.Join(parts, "/")
}

// escapePath escapes a path segment, keeping the slashes inside it. Escaping
// them instead would send %2F, which GitHub reads as part of a name rather
// than as the separator in release/1.0 or in a file's path.
func escapePath(segment string) string {
	parts := strings.Split(segment, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// restAdapter is what a paginated walk needs of an adapter: the client to send
// through, and the decoration every request to that provider carries. Both
// forges in this package answer a listing the same way — a page of JSON and a
// Link header pointing at the next — so the walk is written once.
type restAdapter interface {
	sender() *core.Client
	decorate(core.Request) core.Request
}

// paginate walks a paginated collection. The request is decorated once, up
// front, because the walk follows the Link header's absolute URLs and carries
// the same header to each of them.
func paginate[T any](ctx context.Context, a restAdapter, req core.Request, visit func(T) bool) error {
	return core.Paginate(ctx, a.sender(), a.decorate(req), visit)
}

// listed is one pull request as a listing is allowed to describe it.
//
// Mergeability is dropped on every forge, including the two whose list
// endpoint happens to carry it. GitHub's does not, so an engine written
// against a listing would answer "nothing is stuck" there and be right
// everywhere else — a difference between providers that nothing would catch,
// since each adapter's tests only ever meet their own. Spec 10 §2 asks it of
// [core.Forge.PullRequest] and of nothing else.
func listed(pr core.PullRequest) core.PullRequest {
	pr.Mergeability = core.MergeabilityUnknown
	return pr
}

// graphQL sends one GraphQL query and returns its data, through the shared
// unwrapping in core with GitHub's own reading of what an error type means.
func graphQL[T any](ctx context.Context, g *GitHub, query string, variables map[string]any) (T, error) {
	return core.GraphQL[T](ctx, g.client,
		g.decorate(core.Request{Path: g.graphQL}),
		core.GraphQLQuery{Query: query, Variables: variables},
		gitHubGraphQLSentinel)
}

// gitHubListState maps a query's state onto the one GitHub's list endpoint
// understands. GitHub has no "merged" state: a merged pull request is a closed
// one with a merge time, so the caller filters what comes back.
func gitHubListState(state string) string {
	switch state {
	case core.PRStateOpen:
		return "open"
	case core.PRStateMerged, core.PRStateClosed:
		return "closed"
	default:
		return "all"
	}
}

// truncate shortens text to at most limit characters, marking that it did so.
// A provider's own ceiling is the adapter's to keep: another forge has another
// one, and a caller that overruns should still publish something readable
// rather than be refused.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimRight(string(runes[:limit-1]), " ") + "…"
}

// refOrDefault names the ref in a message when one was given.
func refOrDefault(ref string) string {
	if ref == "" {
		return "the default branch"
	}
	return ref
}
