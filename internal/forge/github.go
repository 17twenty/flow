// Package forge holds the Git-host adapters: GitHub in v1, Gitea/Forgejo and
// GitLab in phase 7. Each is a thin REST client over [core.Client] and depends
// on core alone, so that nothing about a provider leaks into the engine
// (spec 01 §2, spec 10).
package forge

import (
	"context"
	"errors"
	"fmt"
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
	// maxPages bounds a paginated read, so that a provider looping its own
	// Link header cannot make flow walk forever.
	maxPages = 500
	// gitHubStatusDescription is the longest description GitHub keeps on a
	// commit status.
	gitHubStatusDescription = 140
)

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
		return core.RepoInfo{}, explain(err, "read the repository %s", g.Slug())
	}
	return payload.toRepoInfo(), nil
}

// PullRequest returns one pull request with the reviews, the file summary and
// the linked tickets the policy gate reads.
func (g *GitHub) PullRequest(ctx context.Context, number int) (core.PullRequest, error) {
	var payload ghPull
	path := g.path("pulls", strconv.Itoa(number))
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		return core.PullRequest{}, explain(err, "read pull request #%d of %s", number, g.Slug())
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
	for page := 0; page < maxPages; page++ {
		data, err := graphQL[ghClosingIssues](ctx, g, gitHubClosingIssues, variables)
		if err != nil {
			return nil, explain(err, "read the tickets pull request #%d of %s closes", number, g.Slug())
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
		WithWhy("the provider offered more than %d pages of linked tickets", maxPages).
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
		return nil, explain(err, "read the reviews on pull request #%d of %s", number, g.Slug())
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
		return nil, explain(err, "read the files of pull request #%d of %s", number, g.Slug())
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
		prs = append(prs, pr)
		return q.Limit <= 0 || len(prs) < q.Limit
	})
	if err != nil {
		return nil, explain(err, "list the pull requests of %s", g.Slug())
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
		return nil, explain(err, "list the commits of %s", g.Slug())
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
		return core.Comparison{}, explain(err, "compare %s with %s in %s", base, head, g.Slug())
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
		return nil, explain(err, "read %s from %s at %s", filePath, g.Slug(), refOrDefault(ref))
	}
	return content, nil
}

// Probe reports what the credential may do. Plan item 3.7.
func (g *GitHub) Probe(ctx context.Context) (core.Permissions, error) {
	return core.Permissions{}, notYet("probe the credential")
}

// Protection returns the effective protection of a branch. Plan item 3.4.
func (g *GitHub) Protection(ctx context.Context, branch string) (core.Protection, error) {
	return core.Protection{}, notYet("read the protection of %s", branch)
}

// SetProtection applies protection additively. Plan item 3.4.
func (g *GitHub) SetProtection(ctx context.Context, branch string, p core.Protection) error {
	return notYet("protect %s", branch)
}

// SetMergeSettings sets the repository's merge strategies. Plan item 3.4.
func (g *GitHub) SetMergeSettings(ctx context.Context, s core.MergeSettings) error {
	return notYet("set the merge settings of %s", g.Slug())
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
		return nil, explain(err, "read the statuses on %s of %s", sha, g.Slug())
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
		return explain(err, "publish the %s status on %s of %s", s.Context, sha, g.Slug())
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
		return nil, explain(err, "read the %s status on %s of %s", want, sha, g.Slug())
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
			return explain(err, "update flow's comment on pull request #%d of %s", number, g.Slug())
		}
		return nil
	}

	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("issues", strconv.Itoa(number), "comments"),
		Body:   ghCommentRequest{Body: marked},
	}
	if _, err := g.do(ctx, req); err != nil {
		return explain(err, "comment on pull request #%d of %s", number, g.Slug())
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
		return explain(err, "delete flow's comment on pull request #%d of %s", number, g.Slug())
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
		return nil, explain(err, "read flow's comment on pull request #%d of %s", number, g.Slug())
	}
	return found, nil
}

// ProposeFiles offers a set of files as a pull request. Plan item 3.5.
func (g *GitHub) ProposeFiles(ctx context.Context, p core.FileProposal) (core.PullRequest, error) {
	return core.PullRequest{}, notYet("propose %d files", len(p.Files))
}

// do sends one request with GitHub's media type and API version.
func (g *GitHub) do(ctx context.Context, r core.Request) (*core.Response, error) {
	header := http.Header{}
	for name, values := range r.Header {
		header[name] = slices.Clone(values)
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", gitHubAccept)
	}
	header.Set("X-GitHub-Api-Version", gitHubAPIVersion)
	r.Header = header
	return g.client.Do(ctx, r)
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

// paginate walks a paginated collection, following the Link header's "next"
// relation rather than counting pages, and stops as soon as visit says so.
func paginate[T any](ctx context.Context, g *GitHub, req core.Request, visit func(T) bool) error {
	for page := 0; ; page++ {
		if page >= maxPages {
			return core.Errorf(core.ErrUnavailable, "read a page of results").
				WithWhy("the provider offered more than %d pages", maxPages).
				WithFix("narrow the query's window or limit")
		}
		var items []T
		req.Out = &items
		res, err := g.do(ctx, req)
		if err != nil {
			return err
		}
		for _, item := range items {
			if !visit(item) {
				return nil
			}
		}
		next := res.Links["next"]
		if next == "" || len(items) == 0 {
			return nil
		}
		// The next URL is absolute and carries the whole query — the
		// window, the page size and the cursor — so the walk follows it
		// as it stands.
		req = core.Request{Path: next, Header: req.Header}
	}
}

// graphQL sends one GraphQL query and returns its data. GitHub answers 200
// whatever happened and says what went wrong beside the data, so the envelope
// is unwrapped here: an answer carrying errors is a refusal, with the sentinel
// its type means, whatever came back alongside (spec 10 §2).
func graphQL[T any](ctx context.Context, g *GitHub, query string, variables map[string]any) (T, error) {
	var envelope ghGraphQLResponse[T]
	_, err := g.do(ctx, core.Request{
		Method: http.MethodPost,
		Path:   g.graphQL,
		Body:   ghGraphQLRequest{Query: query, Variables: variables},
		Out:    &envelope,
		// A query is a read however it is sent, so trying it again
		// cannot do anything twice — which POST's default policy, which
		// knows only the method, has to assume it could.
		Retry: core.RetryAlways,
	})
	if err != nil {
		var zero T
		return zero, err
	}
	if len(envelope.Errors) > 0 {
		var zero T
		return zero, graphQLError(envelope.Errors)
	}
	return envelope.Data, nil
}

// graphQLError states a GraphQL errors array as one refusal, keeping every
// message: a query that failed for two reasons has two things to fix.
func graphQLError(errs []ghGraphQLError) error {
	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		if message := strings.TrimSpace(e.Message); message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		messages = append(messages, "the provider gave no reason")
	}
	return core.Errorf(errs[0].sentinel(), "ask GitHub's GraphQL API").
		WithWhy("%s", strings.Join(messages, "; "))
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

// notYet is the refusal of a method whose plan item has not landed. It is the
// only stub the plan permits, and it names itself so that a caller reaching it
// says what it wanted rather than failing blankly.
func notYet(format string, args ...any) error {
	return core.Errorf(core.ErrUnsupported, format, args...).
		WithWhy("the github adapter does not do that yet").
		WithFix("this is a gap in flow, not in your configuration; see the plan")
}

// explain re-states a provider error as the thing flow was trying to do,
// keeping the sentinel so that the exit code and the fix do not change.
func explain(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	var user *core.UserError
	if errors.As(err, &user) {
		restated := *user
		restated.What = fmt.Sprintf(format, args...)
		return &restated
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}
