package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// gitHubFake serves GitHub's documented REST shapes over a
// [coretest.ForgeFixture], so that the adapter is asked the same questions
// about the same repository as the in-memory fake (spec 14).
//
// It paginates at pageSize and answers conditional requests, because
// pagination and ETags are the two things the adapter has to get right that an
// in-memory fake can never exercise.
type gitHubFake struct {
	fx       coretest.ForgeFixture
	pageSize int
	// loop makes every page point at another one, which is what a provider
	// with a bug in its own Link header does to a client that trusts it.
	loop bool

	mu          sync.Mutex
	requests    []string
	notModified int
	userAgents  map[string]int
}

// newGitHubServer stands the fake up and returns it with its server.
func newGitHubServer(t *testing.T, fx coretest.ForgeFixture) (*gitHubFake, *httptest.Server) {
	t.Helper()
	fake := &gitHubFake{fx: fx, pageSize: 2, userAgents: map[string]int{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

// Requests returns the paths asked for, in order.
func (f *gitHubFake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// NotModified returns how many conditional requests were answered 304.
func (f *gitHubFake) NotModified() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notModified
}

// prefix is the repository's path prefix.
func (f *gitHubFake) prefix() string {
	return "/repos/" + f.fx.Repo.Owner + "/" + f.fx.Repo.Name
}

// ServeHTTP implements http.Handler.
func (f *gitHubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	// The escaped path, not the decoded one: an adapter that sends %2F
	// inside a ref has asked GitHub about a different branch, and a
	// decoded path hides exactly that mistake.
	f.requests = append(f.requests, r.URL.EscapedPath())
	f.userAgents[r.Header.Get("User-Agent")]++
	f.mu.Unlock()

	if r.Header.Get("X-GitHub-Api-Version") != gitHubAPIVersion {
		f.fail(w, http.StatusBadRequest, "no API version was pinned")
		return
	}
	if accept := r.Header.Get("Accept"); accept != gitHubAccept && accept != gitHubRaw {
		f.fail(w, http.StatusNotAcceptable, "unexpected Accept "+accept)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), f.prefix())
	if !ok {
		f.fail(w, http.StatusNotFound, "no such repository")
		return
	}
	segments := splitPath(rest)

	switch {
	case len(segments) == 0:
		f.writeJSON(w, r, ghRepoJSON(f.fx.Repo))
	case segments[0] == "pulls" && len(segments) == 1:
		f.pulls(w, r)
	case segments[0] == "pulls" && len(segments) == 2:
		f.pull(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 3 && segments[2] == "reviews":
		f.reviews(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 3 && segments[2] == "files":
		f.files(w, r, segments[1])
	case segments[0] == "commits" && len(segments) == 1:
		f.commits(w, r)
	case segments[0] == "compare" && len(segments) > 1:
		f.compare(w, r, strings.Join(segments[1:], "/"))
	case segments[0] == "contents" && len(segments) > 1:
		f.contents(w, r, strings.Join(segments[1:], "/"))
	default:
		f.fail(w, http.StatusNotFound, "Not Found")
	}
}

// pulls serves the list endpoint, paginated and sorted newest first.
func (f *gitHubFake) pulls(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("sort") != "updated" || query.Get("direction") != "desc" {
		f.fail(w, http.StatusBadRequest, "the list must be sorted by update, newest first")
		return
	}
	state := query.Get("state")
	base := query.Get("base")

	prs := slices.Clone(f.fx.PullRequests)
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].UpdatedAt.After(prs[j].UpdatedAt) })

	var page []any
	for _, pr := range prs {
		if base != "" && pr.BaseRef != base {
			continue
		}
		switch state {
		case "open":
			if pr.State != core.PRStateOpen {
				continue
			}
		case "closed":
			if pr.State == core.PRStateOpen {
				continue
			}
		}
		page = append(page, ghPullJSON(pr, false))
	}
	f.writePage(w, r, page)
}

// pull serves one pull request, with the detail only this endpoint carries.
func (f *gitHubFake) pull(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	f.writeJSON(w, r, ghPullJSON(pr, true))
}

// reviews serves a pull request's reviews, oldest first and paginated.
func (f *gitHubFake) reviews(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	page := make([]any, 0, len(pr.Reviews)+1)
	for _, review := range pr.Reviews {
		page = append(page, map[string]any{
			"user":         map[string]any{"login": review.Author},
			"state":        strings.ToUpper(review.State),
			"submitted_at": review.SubmittedAt,
		})
	}
	// A review still being written is not a verdict, and the adapter must
	// drop it rather than count it.
	page = append(page, map[string]any{
		"user":         map[string]any{"login": "someone"},
		"state":        "PENDING",
		"submitted_at": nil,
	})
	f.writePage(w, r, page)
}

// files serves the paths a pull request touches, deliberately out of order.
func (f *gitHubFake) files(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	paths := slices.Clone(pr.Files.Paths)
	slices.Reverse(paths)
	page := make([]any, 0, len(paths))
	for _, path := range paths {
		page = append(page, map[string]any{"filename": path})
	}
	f.writePage(w, r, page)
}

// commits serves the history, newest first, honouring since and until.
func (f *gitHubFake) commits(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	since, until := parseStamp(query.Get("since")), parseStamp(query.Get("until"))

	var page []any
	for _, commit := range f.fx.Commits {
		if !since.IsZero() && commit.CommittedAt.Before(since) {
			continue
		}
		if !until.IsZero() && commit.CommittedAt.After(until) {
			continue
		}
		page = append(page, ghCommitJSON(commit))
	}
	f.writePage(w, r, page)
}

// compare answers the ancestry question the fixture's history implies.
func (f *gitHubFake) compare(w http.ResponseWriter, r *http.Request, basehead string) {
	base, head, ok := strings.Cut(basehead, "...")
	if !ok {
		f.fail(w, http.StatusBadRequest, "expected base...head")
		return
	}
	comparison, err := coretest.NewFakeForge(f.fx).Compare(r.Context(), base, head)
	if err != nil {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	f.writeJSON(w, r, map[string]any{
		"status":    comparison.Status,
		"ahead_by":  comparison.AheadBy,
		"behind_by": comparison.BehindBy,
	})
}

// contents serves a file's bytes, as the raw media type does.
func (f *gitHubFake) contents(w http.ResponseWriter, r *http.Request, path string) {
	content, ok := f.fx.Files[path]
	if !ok {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	if r.Header.Get("Accept") != gitHubRaw {
		f.fail(w, http.StatusNotAcceptable, "a file must be asked for raw")
		return
	}
	f.write(w, r, http.StatusOK, content, "text/plain")
}

// findPull returns the fixture's pull request by number.
func (f *gitHubFake) findPull(raw string) (core.PullRequest, bool) {
	number, err := strconv.Atoi(raw)
	if err != nil {
		return core.PullRequest{}, false
	}
	for _, pr := range f.fx.PullRequests {
		if pr.Number == number {
			return pr, true
		}
	}
	return core.PullRequest{}, false
}

// writePage serves one page of a collection and the Link header that leads to
// the next, so that the adapter has to follow it rather than count.
func (f *gitHubFake) writePage(w http.ResponseWriter, r *http.Request, items []any) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	size := f.pageSize
	start := 0
	if !f.loop {
		start = (page - 1) * size
	}
	if start > len(items) {
		start = len(items)
	}
	end := min(start+size, len(items))
	slice := items[start:end]
	if slice == nil {
		slice = []any{}
	}
	if end < len(items) || f.loop {
		next := *r.URL
		query := next.Query()
		query.Set("page", strconv.Itoa(page+1))
		next.RawQuery = query.Encode()
		next.Scheme, next.Host = "http", r.Host
		w.Header().Set("Link", `<`+next.String()+`>; rel="next", <`+next.String()+`>; rel="last"`)
	}
	f.writeJSON(w, r, slice)
}

// writeJSON encodes a document and serves it with an ETag.
func (f *gitHubFake) writeJSON(w http.ResponseWriter, r *http.Request, doc any) {
	raw, err := json.Marshal(doc)
	if err != nil {
		f.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	f.write(w, r, http.StatusOK, raw, "application/json")
}

// write serves a body, answering a conditional request with 304 when the
// caller already has it.
func (f *gitHubFake) write(w http.ResponseWriter, r *http.Request, status int, body []byte, contentType string) {
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4999")
	if r.Header.Get("If-None-Match") == etag {
		f.mu.Lock()
		f.notModified++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// fail answers in GitHub's error shape.
func (f *gitHubFake) fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"message":%q}`, message)
}

// ghRepoJSON renders a repository as GitHub does.
func ghRepoJSON(repo core.RepoInfo) map[string]any {
	title := map[string]string{
		core.SquashTitlePR:     ghSquashTitlePR,
		core.SquashTitleCommit: ghSquashTitleCommit,
	}[repo.Merge.SquashTitle]
	message := map[string]string{
		core.SquashMessagePRBody:  ghSquashMessagePRBody,
		core.SquashMessageCommits: ghSquashMessageCommits,
		core.SquashMessageBlank:   ghSquashMessageBlank,
	}[repo.Merge.SquashMessage]
	return map[string]any{
		"name":                        repo.Name,
		"owner":                       map[string]any{"login": repo.Owner},
		"default_branch":              repo.DefaultBranch,
		"html_url":                    repo.URL,
		"archived":                    repo.Archived,
		"allow_squash_merge":          repo.Merge.AllowSquash,
		"allow_merge_commit":          repo.Merge.AllowMerge,
		"allow_rebase_merge":          repo.Merge.AllowRebase,
		"squash_merge_commit_title":   title,
		"squash_merge_commit_message": message,
		"delete_branch_on_merge":      repo.Merge.DeleteBranchOnMerge,
	}
}

// ghPullJSON renders a pull request as GitHub does. detail adds the fields
// only the single-pull-request endpoint carries.
func ghPullJSON(pr core.PullRequest, detail bool) map[string]any {
	labels := make([]any, 0, len(pr.Labels))
	for _, label := range pr.Labels {
		labels = append(labels, map[string]any{"name": label})
	}
	state := "open"
	if pr.State != core.PRStateOpen {
		state = "closed"
	}
	doc := map[string]any{
		"number":           pr.Number,
		"title":            pr.Title,
		"body":             pr.Body,
		"user":             map[string]any{"login": pr.Author},
		"state":            state,
		"draft":            pr.Draft,
		"merge_commit_sha": pr.MergeSHA,
		"created_at":       pr.CreatedAt,
		"updated_at":       pr.UpdatedAt,
		"merged_at":        stampOrNil(pr.MergedAt),
		"closed_at":        stampOrNil(pr.ClosedAt),
		"head":             map[string]any{"sha": pr.HeadSHA, "ref": pr.HeadRef},
		"base":             map[string]any{"ref": pr.BaseRef},
		"labels":           labels,
		"html_url":         pr.URL,
	}
	if detail {
		doc["changed_files"] = pr.Files.Changed
		doc["additions"] = pr.Files.Additions
		doc["deletions"] = pr.Files.Deletions
	}
	return doc
}

// ghCommitJSON renders a commit as GitHub does.
func ghCommitJSON(commit core.Commit) map[string]any {
	doc := map[string]any{
		"sha": commit.SHA,
		"commit": map[string]any{
			"message":   commit.Message,
			"author":    map[string]any{"name": commit.Author, "date": commit.AuthoredAt},
			"committer": map[string]any{"date": commit.CommittedAt},
		},
		"html_url": commit.URL,
	}
	if commit.Author != "" {
		doc["author"] = map[string]any{"login": commit.Author}
	}
	return doc
}

// stampOrNil renders a zero time as JSON null, as GitHub does.
func stampOrNil(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at
}

// parseStamp reads an RFC 3339 query parameter, or the zero time.
func parseStamp(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return at
}

// splitPath splits a URL path into unescaped segments.
func splitPath(path string) []string {
	var segments []string
	for _, raw := range strings.Split(strings.Trim(path, "/"), "/") {
		if raw == "" {
			continue
		}
		if decoded, err := url.PathUnescape(raw); err == nil {
			raw = decoded
		}
		segments = append(segments, raw)
	}
	return segments
}
