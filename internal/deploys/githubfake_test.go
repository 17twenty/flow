package deploys

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// The repository the fake speaks for, the same one the forge fixtures use.
const (
	fakeOwner = "acme"
	fakeRepo  = "widget"
	// ghStatusDelay is how long after a deployment was asked for that the
	// deployer reported on it, so that a test can tell the two times
	// apart.
	ghStatusDelay = 90 * time.Second
)

// gitHubFake serves GitHub's documented deployment shapes over a
// [coretest.DeployFixture], so that the adapter is asked the same questions
// about the same history as the in-memory source (spec 14).
//
// It paginates at pageSize, filters by environment and commit the way the
// provider does, and keeps the state in a separate status history, because
// those are the three things the adapter has to get right that an in-memory
// fake can never exercise.
type gitHubFake struct {
	pageSize int
	// records are the deployments, newest first, as GitHub orders them.
	records []ghDeployment
	// statuses are each record's status history, newest first, in GitHub's
	// own vocabulary.
	statuses map[int64][]map[string]any

	mu       sync.Mutex
	requests []string
	queries  []string
	// agents counts the User-Agent of every request, so that a call going
	// out anonymously is visible.
	agents map[string]int
}

// newGitHubServer stands the fake up and returns it with its server.
func newGitHubServer(t *testing.T, fx coretest.DeployFixture) (*gitHubFake, *httptest.Server) {
	t.Helper()
	fake := &gitHubFake{
		pageSize: 2,
		statuses: map[int64][]map[string]any{},
		agents:   map[string]int{},
	}
	records := slices.Clone(fx.Deployments)
	slices.SortStableFunc(records, func(a, b core.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) })
	for i, record := range records {
		id := int64(i + 1)
		fake.records = append(fake.records, ghDeployment{
			ID:          id,
			SHA:         record.SHA,
			Ref:         record.SHA,
			Environment: record.Environment,
			Description: "deployed by the release workflow",
			URL:         "https://api.github.test/repos/acme/widget/deployments/" + strconv.FormatInt(id, 10),
			CreatedAt:   record.CreatedAt,
			// The record's own timestamps are when GitHub was told
			// to deploy. What became of it is on the status, and
			// landed later.
			UpdatedAt: record.CreatedAt,
		})
		fake.setStatus(id, ghStateOf(record.State), record.UpdatedAt.Add(ghStatusDelay))
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

// ghStateOf is the GitHub state a core state came from.
func ghStateOf(state string) string {
	switch state {
	case core.DeploySuccess:
		return ghDeploySuccess
	case core.DeployFailure:
		return ghDeployFailure
	case core.DeployInProgress:
		return ghDeployInProgress
	default:
		return ghDeployPending
	}
}

// setStatus replaces a record's status history with one status.
func (f *gitHubFake) setStatus(id int64, state string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = []map[string]any{ghStatusJSON(state, at)}
}

// pushStatus adds a status to a record, as GitHub does: newest first, nothing
// replaced.
func (f *gitHubFake) pushStatus(id int64, state string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = append([]map[string]any{ghStatusJSON(state, at)}, f.statuses[id]...)
}

// clearStatuses leaves a record with no status at all, which is a deployment
// nothing has reported on yet.
func (f *gitHubFake) clearStatuses(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.statuses, id)
}

// ghStatusJSON renders a deployment status as GitHub does.
func ghStatusJSON(state string, at time.Time) map[string]any {
	return map[string]any{
		"id":          1,
		"state":       state,
		"description": "the deployer said " + state,
		"log_url":     "https://ci.test/runs/" + state,
		"target_url":  "https://ci.test/runs/" + state,
		"created_at":  at.Format(time.RFC3339),
		"updated_at":  at.Format(time.RFC3339),
	}
}

// Requests is every path the adapter asked for, and Queries the raw query of
// each, so that a test can prove a filter was the provider's work.
func (f *gitHubFake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *gitHubFake) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

func (f *gitHubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.EscapedPath())
	f.queries = append(f.queries, r.URL.RawQuery)
	f.agents[r.Header.Get("User-Agent")]++
	f.mu.Unlock()

	// A dated API and a media type are asked for on everything. The fake
	// refuses a request that pins neither, so a call built without them
	// fails every test it is in rather than only the one that looked.
	if r.Header.Get("X-GitHub-Api-Version") != gitHubAPIVersion {
		f.fail(w, http.StatusBadRequest, "no API version was pinned")
		return
	}
	if accept := r.Header.Get("Accept"); accept != gitHubAccept {
		f.fail(w, http.StatusNotAcceptable, "unexpected Accept "+accept)
		return
	}
	if r.Method != http.MethodGet {
		f.fail(w, http.StatusMethodNotAllowed, "a deployment source only reads")
		return
	}
	prefix := "/repos/" + fakeOwner + "/" + fakeRepo + "/deployments"
	path, under := strings.CutPrefix(r.URL.EscapedPath(), prefix)
	if !under {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	switch {
	case path == "":
		f.listDeployments(w, r)
	case strings.HasSuffix(path, "/statuses"):
		f.listStatuses(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/statuses"))
	default:
		f.fail(w, http.StatusNotFound, "Not Found")
	}
}

// listDeployments serves the records of one environment, filtered the way
// GitHub filters them and paginated the way GitHub paginates them.
func (f *gitHubFake) listDeployments(w http.ResponseWriter, r *http.Request) {
	environment := r.URL.Query().Get("environment")
	sha := r.URL.Query().Get("sha")
	var items []any
	f.mu.Lock()
	for _, record := range f.records {
		if environment != "" && record.Environment != environment {
			continue
		}
		if sha != "" && record.SHA != sha {
			continue
		}
		items = append(items, record)
	}
	f.mu.Unlock()
	f.writePage(w, r, items)
}

// listStatuses serves one record's status history.
func (f *gitHubFake) listStatuses(w http.ResponseWriter, r *http.Request, raw string) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	f.mu.Lock()
	statuses := slices.Clone(f.statuses[id])
	f.mu.Unlock()
	items := make([]any, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, status)
	}
	f.writePage(w, r, items)
}

// writePage serves one page and the Link header that leads to the next, so
// that the adapter has to follow it rather than count.
func (f *gitHubFake) writePage(w http.ResponseWriter, r *http.Request, items []any) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	start := min((page-1)*f.pageSize, len(items))
	end := min(start+f.pageSize, len(items))
	slice := items[start:end]
	if slice == nil {
		slice = []any{}
	}
	if end < len(items) {
		next := *r.URL
		query := next.Query()
		query.Set("page", strconv.Itoa(page+1))
		next.RawQuery = query.Encode()
		next.Scheme, next.Host = "http", r.Host
		w.Header().Set("Link", `<`+next.String()+`>; rel="next", <`+next.String()+`>; rel="last"`)
	}
	f.writeJSON(w, r, slice)
}

// writeJSON encodes a document and serves it with an ETag, so that a repeated
// read is a conditional request.
func (f *gitHubFake) writeJSON(w http.ResponseWriter, r *http.Request, doc any) {
	raw, err := json.Marshal(doc)
	if err != nil {
		f.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum := sha256.Sum256(raw)
	etag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4999")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// fail answers in GitHub's error shape.
func (f *gitHubFake) fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"message":%q}`, message)
}
