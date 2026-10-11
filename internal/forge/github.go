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
	// maxPages bounds a paginated read, so that a provider looping its own
	// Link header cannot make flow walk forever.
	maxPages = 500
)

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
	return &GitHub{client: o.Client, owner: o.Owner, repo: o.Repo, caps: o.Capabilities}, nil
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

// PullRequest returns one pull request with the reviews and file summary the
// policy gate reads. The tickets it closes come from GraphQL and are added by
// plan item 3.3.
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
	return pr, nil
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

// CommitStatuses returns the statuses on a commit. Plan item 3.2.
func (g *GitHub) CommitStatuses(ctx context.Context, sha string) ([]core.CommitStatus, error) {
	return nil, notYet("read the statuses on %s", sha)
}

// SetCommitStatus publishes a status on a commit. Plan item 3.2.
func (g *GitHub) SetCommitStatus(ctx context.Context, sha string, s core.CommitStatus) error {
	return notYet("publish the %s status on %s", s.Context, sha)
}

// UpsertComment writes flow's comment on a pull request. Plan item 3.2.
func (g *GitHub) UpsertComment(ctx context.Context, number int, m core.Marker, body string) error {
	return notYet("comment on pull request #%d", number)
}

// Comment returns flow's marked comment on a pull request. Plan item 3.2.
func (g *GitHub) Comment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	return nil, notYet("read flow's comment on pull request #%d", number)
}

// DeleteComment removes flow's marked comment. Plan item 3.2.
func (g *GitHub) DeleteComment(ctx context.Context, number int, m core.Marker) error {
	return notYet("delete flow's comment on pull request #%d", number)
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
