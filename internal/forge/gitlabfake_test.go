package forge

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// gitLabTestToken is the credential the fake expects. A call that does not
// carry it is refused rather than answered, because a fake that serves
// anybody proves nothing about what the adapter sends.
const gitLabTestToken = "test-token"

// gitLabFake serves GitLab's documented /api/v4 shapes over a
// [coretest.ForgeFixture].
//
// It is a third fake rather than a parameterisation of the other two because
// almost nothing about GitLab is the same: a project is addressed by its
// URL-encoded path rather than two segments, a merge request's reviewers and
// its approvals are two endpoints, the tickets it closes are a third, a
// branch's protection is in one place and the approvals and named checks that
// gate it are in two others, pages are walked with `page` and `per_page`, and
// a commit status is a row in a history rather than a replaceable verdict.
//
// It answers only what the adapter asks for, and only what the query selected:
// a listing that ignored `state`, `target_branch` or `updated_after` would let
// the adapter pass by filtering what GitLab would never have sent.
type gitLabFake struct {
	fx coretest.ForgeFixture
	// base is the server's own URL, so that a page's Link header points
	// back at it as GitLab's does.
	base string
	// pageSize is deliberately smaller than anything the fixture holds, so
	// that every walk is a walk and a reader of only the first page is
	// found out.
	pageSize int

	mu       sync.Mutex
	requests []string
	methods  []string
	queries  []string
	// anonymous counts calls refused for carrying no credential, and
	// unacceptable those refused for asking for something other than JSON.
	anonymous    int
	unacceptable int
	// agents counts what each caller identified itself as.
	agents map[string]int
	// fail refuses a path with a status, so that a test can ask what the
	// adapter makes of a forge that says no.
	fail map[string]int

	// project is the project as it stands: the merge settings and the
	// pipeline gate are both written during the contract.
	project glProject

	// prs are the merge requests, fixture and created alike, and nextMR
	// the internal id the next one gets.
	prs    []core.PullRequest
	nextMR int
	// reviews are the verdicts on each merge request, held apart from the
	// merge requests because GitLab's list endpoint carries none of them.
	reviews map[int][]core.Review
	// closes are the issues each merge request closes, which GitLab serves
	// from an endpoint of its own and never from the merge request.
	closes map[int][]string
	// external marks the merge requests whose closing issues live in an
	// external tracker. GitLab answers those with an id and a title and
	// nothing else — no `references`, so no name flow can use.
	external map[int]bool
	// squashCommit says whether a merged merge request was squashed. A
	// squashed one carries both commits and an unsquashed one only the
	// merge commit.
	squashCommit bool
	// armed are the merge requests set to land themselves. GitLab answers
	// the arrangement on a listing as well as on the merge request, which
	// is a reading the adapter has to drop rather than pass on.
	armed map[int]bool
	// legacyAutoMerge answers only `merge_when_pipeline_succeeds`, as an
	// installation from before 17.11 renamed it does.
	legacyAutoMerge bool
	// approvers are logins that approved a merge request without ever
	// being one of its reviewers, which GitLab allows and reports only
	// from the approvals endpoint.
	approvers map[int][]string
	// paths are the files each merge request touches.
	paths map[int][]string

	// statuses is a history per commit, as GitLab's is: publishing appends
	// and replaces nothing, and nextStatus is the identity the next one
	// gets.
	statuses   map[string][]glStatus
	nextStatus int64

	// notes are the comments on each merge request, oldest first.
	notes map[int][]glNote
	// systemNote is the body GitLab's own bot wrote on every merge
	// request. flow must never mistake one for its own comment.
	systemNote string
	nextNote   int64
	noteGone   map[int64]bool
	nextRule   int64
	nextCheck  int64

	// protected are the protected branches by name, rules the approval
	// rules and checks the external status checks. GitLab keeps all three
	// apart, and a branch's effective protection is read from all three.
	protected     map[string]*glProtectedBranch
	nextProtected int
	rules         []glApprovalRule
	checks        []glExternalCheck
	// hasChecks says whether this instance has external status checks at
	// all. They are an Ultimate feature, and an instance without them
	// answers 404 — which is the ordinary state of a GitLab.
	hasChecks bool
	// fixedMerge makes the project ignore a change to how it merges, as a
	// group policy that pins them does: GitLab answers 200 with the
	// settings it kept.
	fixedMerge bool
	// hasApprovals says whether approval rules can be read. They are a
	// paid feature too, and a free instance refuses them.
	hasApprovals bool
	// hiddenAs is what this GitLab answers a credential below maintainer
	// that asks for a protected branch, the approval rules or the external
	// status checks: 403 where `authorize!` refuses it, 404 where the route
	// hides what the caller may not know of. Zero answers it as it would a
	// maintainer.
	hiddenAs int

	// branches holds the files on each branch, tips the commit each branch
	// points at, and ancestry the commits each commit contains, oldest
	// first. Ancestry is the whole of what a straight compare answers.
	branches map[string]map[string][]byte
	tips     map[string]string
	ancestry map[string][]string
	commits  map[string]core.Commit
}

// newGitLabServer stands a fake GitLab up in front of a fixture.
func newGitLabServer(t *testing.T, fx coretest.ForgeFixture) (*gitLabFake, *httptest.Server) {
	t.Helper()
	fake := &gitLabFake{
		fx:           fx,
		pageSize:     2,
		agents:       map[string]int{},
		fail:         map[string]int{},
		reviews:      map[int][]core.Review{},
		closes:       map[int][]string{},
		external:     map[int]bool{},
		squashCommit: true,
		armed:        map[int]bool{},
		approvers:    map[int][]string{},
		paths:        map[int][]string{},
		statuses:     map[string][]glStatus{},
		notes:        map[int][]glNote{},
		systemNote:   "changed target branch from master to main",
		noteGone:     map[int64]bool{},
		protected:    map[string]*glProtectedBranch{},
		hasChecks:    true,
		hasApprovals: true,
		branches:     map[string]map[string][]byte{},
		tips:         map[string]string{},
		ancestry:     map[string][]string{},
		commits:      map[string]core.Commit{},
	}

	settings := mergeRequestGitLab(fx.Repo.Merge)
	template, _ := squashTemplateGitLab(fx.Repo.Merge)
	level := gitLabDeveloper
	if slices.Contains(fx.Permissions.Tokens, "admin") {
		level = gitLabMaintainer
	}
	fake.project = glProject{
		ID:                   42,
		Path:                 fx.Repo.Name,
		PathWithNamespace:    fx.Repo.Owner + "/" + fx.Repo.Name,
		Namespace:            glNamespace{FullPath: fx.Repo.Owner},
		DefaultBranch:        fx.Repo.DefaultBranch,
		WebURL:               fx.Repo.URL,
		Archived:             fx.Repo.Archived,
		MergeMethod:          *settings.MergeMethod,
		SquashOption:         *settings.SquashOption,
		SquashCommitTemplate: template,
		RemoveSourceBranch:   *settings.RemoveSourceBranch,
		Permissions:          glPermissions{Project: &glAccess{AccessLevel: level}},
		// GitLab holds "the trunk's checks must pass" on the project
		// rather than on the branch, which is where the adapter reads
		// it back from as well.
		PipelineMustSucceed: fx.Protections[fx.Repo.DefaultBranch].StrictChecks,
	}

	// The trunk's history, oldest first, and a commit's ancestry with it.
	var chain []string
	for i := len(fx.Commits) - 1; i >= 0; i-- {
		commit := fx.Commits[i]
		fake.commits[commit.SHA] = commit
		chain = append(chain, commit.SHA)
		fake.ancestry[commit.SHA] = slices.Clone(chain)
	}
	if len(chain) > 0 {
		fake.tips[fx.Repo.DefaultBranch] = chain[len(chain)-1]
	}
	fake.branches[fx.Repo.DefaultBranch] = maps.Clone(fx.Files)
	if fake.branches[fx.Repo.DefaultBranch] == nil {
		fake.branches[fx.Repo.DefaultBranch] = map[string][]byte{}
	}

	for _, pr := range fx.PullRequests {
		fake.prs = append(fake.prs, pr)
		fake.nextMR = max(fake.nextMR, pr.Number)
		if len(pr.Reviews) > 0 {
			fake.reviews[pr.Number] = slices.Clone(pr.Reviews)
		}
		if len(pr.ClosingIssues) > 0 {
			fake.closes[pr.Number] = slices.Clone(pr.ClosingIssues)
		}
		if len(pr.Files.Paths) > 0 {
			fake.paths[pr.Number] = slices.Clone(pr.Files.Paths)
		}
		if pr.HeadSHA == "" {
			continue
		}
		if _, known := fake.ancestry[pr.HeadSHA]; !known {
			// A branch off the trunk before its tip, so that the head
			// of an open merge request has diverged from the trunk
			// rather than trailing it.
			forked := slices.Clone(chain[:max(len(chain)-1, 0)])
			fake.ancestry[pr.HeadSHA] = append(forked, pr.HeadSHA)
		}
		if pr.State == core.PRStateOpen && pr.HeadRef != "" {
			fake.tips[pr.HeadRef] = pr.HeadSHA
			fake.branches[pr.HeadRef] = maps.Clone(fake.branches[fx.Repo.DefaultBranch])
		}
	}

	for branch, protection := range fx.Protections {
		fake.nextProtected++
		fake.protected[branch] = &glProtectedBranch{
			ID:                fake.nextProtected,
			Name:              branch,
			AllowForcePush:    protection.AllowForcePush,
			PushAccessLevels:  []glAccessLevel{{AccessLevel: gitLabNobody}},
			MergeAccessLevels: []glAccessLevel{{AccessLevel: gitLabDeveloper}},
		}
		if protection.RequiredApprovals > 0 {
			fake.nextRule++
			fake.rules = append(fake.rules, glApprovalRule{
				ID:                            fake.nextRule,
				Name:                          "default",
				RuleType:                      gitLabAnyApprover,
				ApprovalsRequired:             protection.RequiredApprovals,
				AppliesToAllProtectedBranches: true,
			})
		}
		for _, context := range protection.RequiredChecks {
			fake.nextCheck++
			check := glExternalCheck{ID: fake.nextCheck, Name: context, ExternalURL: fx.Repo.URL}
			check.ProtectedBranches = append(check.ProtectedBranches, struct {
				Name string `json:"name"`
			}{Name: branch})
			fake.checks = append(fake.checks, check)
		}
	}

	for sha, statuses := range fx.Statuses {
		for _, status := range statuses {
			fake.nextStatus++
			at := status.CreatedAt
			finished := at
			fake.statuses[sha] = append(fake.statuses[sha], glStatus{
				ID:          fake.nextStatus,
				Name:        status.Context,
				Status:      statusRequestStateGitLab(status.State),
				Description: status.Description,
				TargetURL:   status.TargetURL,
				CreatedAt:   at,
				FinishedAt:  &finished,
			})
		}
	}

	srv := httptest.NewServer(fake)
	fake.base = srv.URL
	t.Cleanup(srv.Close)
	return fake, srv
}

// Requests returns the paths asked for, in order, as they went over the wire:
// a project is `acme%2Fwidget` and not `acme/widget`.
func (f *gitLabFake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Methods returns the methods asked for, in order.
func (f *gitLabFake) Methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.methods)
}

// Query returns the query the nth call to a path carried, and says whether
// there was one.
func (f *gitLabFake) Query(path string, nth int) (url.Values, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var seen int
	for i, asked := range f.requests {
		if asked != path {
			continue
		}
		seen++
		if seen <= nth {
			continue
		}
		query, err := url.ParseQuery(f.queries[i])
		if err != nil {
			return nil, false
		}
		return query, true
	}
	return nil, false
}

// Anonymous returns how many calls were refused for carrying no credential.
func (f *gitLabFake) Anonymous() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.anonymous
}

// Unacceptable returns how many calls were refused for asking for something
// other than JSON.
func (f *gitLabFake) Unacceptable() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unacceptable
}

// Agents returns what the callers identified themselves as.
func (f *gitLabFake) Agents() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.agents)
}

// refuse makes a path answer a status rather than its usual payload.
func (f *gitLabFake) refuse(path string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[path] = status
}

// statusHistory returns every status published on a commit, oldest first.
func (f *gitLabFake) statusHistory(sha string) []glStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.statuses[sha])
}

// ServeHTTP answers as GitLab does, or refuses as GitLab does.
func (f *gitLabFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	f.mu.Lock()
	f.requests = append(f.requests, escaped)
	f.methods = append(f.methods, r.Method)
	f.queries = append(f.queries, r.URL.RawQuery)
	f.agents[r.Header.Get("User-Agent")]++
	failure, failing := f.fail[escaped]
	f.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+gitLabTestToken {
		f.mu.Lock()
		f.anonymous++
		f.mu.Unlock()
		glRefuse(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}
	if accept := r.Header.Get("Accept"); !strings.Contains(accept, gitLabAccept) {
		f.mu.Lock()
		f.unacceptable++
		f.mu.Unlock()
		glRefuse(w, http.StatusNotAcceptable, "406 Not Acceptable")
		return
	}
	if r.Header.Get("User-Agent") == "" {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: no user agent")
		return
	}
	if failing {
		glRefuse(w, failure, strconv.Itoa(failure)+" refused")
		return
	}

	rest, found := strings.CutPrefix(escaped, GitLabAPIPath+"/")
	if !found {
		glRefuse(w, http.StatusNotFound, "404 API Version Not Found")
		return
	}
	segments, err := gitLabSegments(rest)
	if err != nil {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: "+err.Error())
		return
	}
	if len(segments) < 2 || segments[0] != "projects" {
		glRefuse(w, http.StatusNotFound, "404 Not Found")
		return
	}
	if segments[1] != f.project.PathWithNamespace {
		glRefuse(w, http.StatusNotFound, "404 Project Not Found")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.route(w, r, segments[2:])
}

// route answers one call, the project itself having been found.
func (f *gitLabFake) route(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0:
		f.serveProject(w, r)
	case rest[0] == "merge_requests":
		f.serveMergeRequests(w, r, rest[1:])
	case rest[0] == "repository":
		f.serveRepository(w, r, rest[1:])
	case rest[0] == "statuses" && len(rest) == 2 && r.Method == http.MethodPost:
		f.servePublishStatus(w, r, rest[1])
	case f.hiddenAs != 0 && f.project.Permissions.level() < gitLabMaintainer &&
		slices.Contains([]string{"protected_branches", "approval_rules", "external_status_checks"}, rest[0]):
		glRefuse(w, f.hiddenAs, strconv.Itoa(f.hiddenAs)+" refused")
	case rest[0] == "protected_branches":
		f.serveProtectedBranches(w, r, rest[1:])
	case rest[0] == "approval_rules":
		f.serveApprovalRules(w, r, rest[1:])
	case rest[0] == "external_status_checks":
		f.serveExternalChecks(w, r, rest[1:])
	default:
		glRefuse(w, http.StatusNotFound, "404 Not Found")
	}
}

// serveProject answers GET and PUT of the project itself.
func (f *gitLabFake) serveProject(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		glWrite(w, http.StatusOK, f.project)
	case http.MethodPut:
		body, ok := glBody[glProjectRequest](w, r)
		if !ok {
			return
		}
		if f.fixedMerge {
			glWrite(w, http.StatusOK, f.project)
			return
		}
		if body.MergeMethod != nil {
			f.project.MergeMethod = *body.MergeMethod
		}
		if body.SquashOption != nil {
			f.project.SquashOption = *body.SquashOption
		}
		if body.SquashCommitTemplate != nil {
			f.project.SquashCommitTemplate = *body.SquashCommitTemplate
		}
		if body.RemoveSourceBranch != nil {
			f.project.RemoveSourceBranch = *body.RemoveSourceBranch
		}
		if body.PipelineMustSucceed != nil {
			f.project.PipelineMustSucceed = *body.PipelineMustSucceed
		}
		glWrite(w, http.StatusOK, f.project)
	default:
		glRefuse(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
	}
}

// mergeNow is PUT /merge_requests/:iid/merge, which merges a merge request
// and also promises to merge one, told apart by the flag.
//
// GitLab refuses with 405 whenever it will not make the promise — a draft, an
// approval still owing — and that is the only answer a caller gets for any of
// them. Arming again is not a refusal at all: GitLab happily re-arms what is
// armed, so the fake accepts it rather than inventing a conflict GitLab does
// not have.
func (f *gitLabFake) mergeNow(w http.ResponseWriter, r *http.Request, pr *core.PullRequest) {
	body, ok := glBody[glMergeNow](w, r)
	if !ok {
		return
	}
	if !body.MergeWhenPipelineSucceeds {
		glRefuse(w, http.StatusMethodNotAllowed, "the fake only ever promises to merge, never merges")
		return
	}
	switch {
	case pr.State != core.PRStateOpen:
		glRefuse(w, http.StatusNotFound, "404 Not found")
	case pr.Draft:
		glRefuse(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
	case body.SHA != "" && body.SHA != pr.HeadSHA:
		glRefuse(w, http.StatusConflict, "409 Conflict: SHA does not match HEAD of source branch")
	default:
		f.armed[pr.Number] = true
		glWrite(w, http.StatusOK, f.mergeRequest(*pr))
	}
}

// serveMergeRequests answers the merge request endpoints: the listing, one
// merge request, its reviewers, its approvals, the issues it closes, its
// diffs and its notes.
func (f *gitLabFake) serveMergeRequests(w http.ResponseWriter, r *http.Request, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			f.listMergeRequests(w, r)
		case http.MethodPost:
			f.createMergeRequest(w, r)
		default:
			glRefuse(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
		}
		return
	}
	iid, err := strconv.Atoi(rest[0])
	if err != nil {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: iid is not a number")
		return
	}
	pr := f.pullRequest(iid)
	if pr == nil {
		glRefuse(w, http.StatusNotFound, "404 Not found")
		return
	}
	switch {
	case len(rest) == 1 && r.Method == http.MethodGet:
		glWrite(w, http.StatusOK, f.mergeRequest(*pr))
	case len(rest) == 1 && r.Method == http.MethodPut:
		body, ok := glBody[glMergeRequestUpdate](w, r)
		if !ok {
			return
		}
		pr.Title, pr.Body = body.Title, body.Description
		pr.UpdatedAt = f.now()
		glWrite(w, http.StatusOK, f.mergeRequest(*pr))
	case len(rest) == 2 && rest[1] == "merge" && r.Method == http.MethodPut:
		f.mergeNow(w, r, pr)
	case len(rest) == 2 && rest[1] == "reviewers" && r.Method == http.MethodGet:
		glPage(f, w, r, f.reviewers(iid))
	case len(rest) == 2 && rest[1] == "approvals" && r.Method == http.MethodGet:
		glWrite(w, http.StatusOK, f.approvals(iid))
	case len(rest) == 2 && rest[1] == "closes_issues" && r.Method == http.MethodGet:
		glPage(f, w, r, f.closesIssues(iid))
	case len(rest) == 2 && rest[1] == "diffs" && r.Method == http.MethodGet:
		glPage(f, w, r, f.diffs(iid))
	case len(rest) == 2 && rest[1] == "notes" && r.Method == http.MethodGet:
		glPage(f, w, r, f.listNotes(r, iid))
	case len(rest) == 2 && rest[1] == "notes" && r.Method == http.MethodPost:
		f.createNote(w, r, iid)
	case len(rest) == 3 && rest[1] == "notes":
		f.serveNote(w, r, iid, rest[2])
	default:
		glRefuse(w, http.StatusNotFound, "404 Not found")
	}
}

// listMergeRequests answers the listing, honouring every filter the query
// carries. A fake that ignored them would let an adapter pass on filtering
// what GitLab had never sent.
func (f *gitLabFake) listMergeRequests(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var out []glMergeRequest
	for _, pr := range f.prs {
		if state := query.Get("state"); state != "" && state != "all" {
			if state != gitLabListState(pr.State) {
				continue
			}
		}
		if base := query.Get("target_branch"); base != "" && base != pr.BaseRef {
			continue
		}
		if head := query.Get("source_branch"); head != "" && head != pr.HeadRef {
			continue
		}
		if raw := query.Get("updated_after"); raw != "" {
			since, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				glRefuse(w, http.StatusBadRequest, "400 Bad Request: updated_after is not a time")
				return
			}
			if !pr.UpdatedAt.After(since) {
				continue
			}
		}
		out = append(out, f.mergeRequest(pr))
	}
	ascending := query.Get("sort") == "asc"
	slices.SortStableFunc(out, func(a, b glMergeRequest) int {
		if query.Get("order_by") == "created_at" {
			if ascending {
				return a.CreatedAt.Compare(b.CreatedAt)
			}
			return b.CreatedAt.Compare(a.CreatedAt)
		}
		if ascending {
			return a.UpdatedAt.Compare(b.UpdatedAt)
		}
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	glPage(f, w, r, out)
}

// createMergeRequest opens one, as POST /merge_requests does.
func (f *gitLabFake) createMergeRequest(w http.ResponseWriter, r *http.Request) {
	body, ok := glBody[glMergeRequestCreate](w, r)
	if !ok {
		return
	}
	if _, known := f.branches[body.SourceBranch]; !known {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: source_branch does not exist")
		return
	}
	f.nextMR++
	now := f.now()
	pr := core.PullRequest{
		Number:    f.nextMR,
		Title:     body.Title,
		Body:      body.Description,
		Author:    "flow-bot",
		State:     core.PRStateOpen,
		HeadSHA:   f.tips[body.SourceBranch],
		HeadRef:   body.SourceBranch,
		BaseRef:   body.TargetBranch,
		CreatedAt: now,
		UpdatedAt: now,
		URL:       f.project.WebURL + "/-/merge_requests/" + strconv.Itoa(f.nextMR),
	}
	f.prs = append(f.prs, pr)
	glWrite(w, http.StatusCreated, f.mergeRequest(pr))
}

// serveNote answers an edit or a deletion of one note.
func (f *gitLabFake) serveNote(w http.ResponseWriter, r *http.Request, iid int, raw string) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: note id is not a number")
		return
	}
	held := f.notes[iid]
	index := slices.IndexFunc(held, func(n glNote) bool { return n.ID == id })
	if index < 0 {
		glRefuse(w, http.StatusNotFound, "404 Note Not Found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, ok := glBody[glNoteRequest](w, r)
		if !ok {
			return
		}
		held[index].Body = body.Body
		held[index].UpdatedAt = f.now()
		glWrite(w, http.StatusOK, held[index])
	case http.MethodDelete:
		f.notes[iid] = slices.Delete(held, index, index+1)
		f.noteGone[id] = true
		w.WriteHeader(http.StatusNoContent)
	default:
		glRefuse(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
	}
}

// createNote writes a comment on a merge request.
func (f *gitLabFake) createNote(w http.ResponseWriter, r *http.Request, iid int) {
	body, ok := glBody[glNoteRequest](w, r)
	if !ok {
		return
	}
	f.nextNote++
	now := f.now()
	note := glNote{
		ID:        f.nextNote,
		Body:      body.Body,
		Author:    glUser{Username: "flow-bot"},
		CreatedAt: now,
		UpdatedAt: now,
	}
	f.notes[iid] = append(f.notes[iid], note)
	glWrite(w, http.StatusCreated, note)
}

// listNotes answers the notes of a merge request in the order asked for, with
// a system note among them: GitLab writes those itself, and flow must not
// mistake one for its own.
func (f *gitLabFake) listNotes(r *http.Request, iid int) []glNote {
	out := []glNote{{
		ID:        -int64(iid),
		Body:      f.systemNote,
		Author:    glUser{Username: "gitlab-bot"},
		System:    true,
		CreatedAt: f.now().Add(-time.Hour),
		UpdatedAt: f.now().Add(-time.Hour),
	}}
	out = append(out, f.notes[iid]...)
	if r.URL.Query().Get("sort") == "desc" {
		slices.Reverse(out)
	}
	return out
}

// serveRepository answers the repository endpoints: commits, their statuses,
// a comparison, files and branches.
func (f *gitLabFake) serveRepository(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 1 && rest[0] == "commits" && r.Method == http.MethodGet:
		f.listCommits(w, r)
	case len(rest) == 3 && rest[0] == "commits" && rest[2] == "statuses" && r.Method == http.MethodGet:
		f.listStatuses(w, r, rest[1])
	case len(rest) == 1 && rest[0] == "compare" && r.Method == http.MethodGet:
		f.compare(w, r)
	case len(rest) == 3 && rest[0] == "files" && rest[2] == "raw" && r.Method == http.MethodGet:
		f.rawFile(w, r, rest[1])
	case len(rest) == 2 && rest[0] == "files":
		f.writeFile(w, r, rest[1])
	case len(rest) == 1 && rest[0] == "branches" && r.Method == http.MethodPost:
		f.createBranch(w, r)
	case len(rest) == 2 && rest[0] == "branches" && r.Method == http.MethodGet:
		f.readBranch(w, rest[1])
	default:
		glRefuse(w, http.StatusNotFound, "404 Not Found")
	}
}

// listCommits answers the commits a ref contains, newest first, within the
// window the query named.
func (f *gitLabFake) listCommits(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	ref := query.Get("ref_name")
	if ref == "" {
		ref = f.project.DefaultBranch
	}
	sha := f.resolve(ref)
	if sha == "" {
		glRefuse(w, http.StatusNotFound, "404 Reference Not Found")
		return
	}
	var out []glCommit
	history := f.ancestry[sha]
	for i := len(history) - 1; i >= 0; i-- {
		commit, known := f.commits[history[i]]
		if !known {
			continue
		}
		if raw := query.Get("since"); raw != "" {
			since, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				glRefuse(w, http.StatusBadRequest, "400 Bad Request: since is not a time")
				return
			}
			if !commit.CommittedAt.After(since) {
				continue
			}
		}
		if raw := query.Get("until"); raw != "" {
			until, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				glRefuse(w, http.StatusBadRequest, "400 Bad Request: until is not a time")
				return
			}
			if commit.CommittedAt.After(until) {
				continue
			}
		}
		out = append(out, f.commit(commit))
	}
	glPage(f, w, r, out)
}

// compare answers a straight comparison: the commits the `to` ref contains
// and the `from` ref does not, which is empty when `to` is an ancestor.
func (f *gitLabFake) compare(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("straight") != "true" {
		// flow asks for a straight comparison because a merge base
		// would answer a different question (spec 13 §3).
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: this fake only answers straight comparisons")
		return
	}
	from, to := f.resolve(query.Get("from")), f.resolve(query.Get("to"))
	if from == "" || to == "" {
		glRefuse(w, http.StatusNotFound, "404 Ref Not Found")
		return
	}
	payload := glCompare{CompareSameRef: from == to}
	held := f.ancestry[from]
	for _, sha := range f.ancestry[to] {
		if slices.Contains(held, sha) {
			continue
		}
		commit, known := f.commits[sha]
		if !known {
			commit = core.Commit{SHA: sha}
		}
		payload.Commits = append(payload.Commits, f.commit(commit))
	}
	glWrite(w, http.StatusOK, payload)
}

// rawFile answers a file's bytes at a ref, as GitLab's raw endpoint does:
// the bytes themselves, not a JSON envelope.
func (f *gitLabFake) rawFile(w http.ResponseWriter, r *http.Request, path string) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = f.project.DefaultBranch
	}
	files, known := f.branches[ref]
	if !known {
		glRefuse(w, http.StatusNotFound, "404 Branch Not Found")
		return
	}
	content, held := files[path]
	if !held {
		glRefuse(w, http.StatusNotFound, "404 File Not Found")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write(content)
}

// writeFile creates or updates a file on a branch.
func (f *gitLabFake) writeFile(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		glRefuse(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
		return
	}
	body, ok := glBody[glFileRequest](w, r)
	if !ok {
		return
	}
	files, known := f.branches[body.Branch]
	if !known {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: branch does not exist")
		return
	}
	_, held := files[path]
	switch {
	case held && r.Method == http.MethodPost:
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: file already exists")
		return
	case !held && r.Method == http.MethodPut:
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: file does not exist")
		return
	}
	if body.Encoding != "base64" {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: unexpected encoding")
		return
	}
	content, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: content is not base64")
		return
	}
	if strings.TrimSpace(body.CommitMessage) == "" {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: commit_message is required")
		return
	}
	files[path] = content
	glWrite(w, http.StatusCreated, map[string]string{"file_path": path, "branch": body.Branch})
}

// createBranch branches off a ref.
func (f *gitLabFake) createBranch(w http.ResponseWriter, r *http.Request) {
	body, ok := glBody[glBranchRequest](w, r)
	if !ok {
		return
	}
	if _, taken := f.branches[body.Branch]; taken {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: branch already exists")
		return
	}
	sha := f.resolve(body.Ref)
	if sha == "" {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: ref does not exist")
		return
	}
	f.branches[body.Branch] = maps.Clone(f.branches[body.Ref])
	if f.branches[body.Branch] == nil {
		f.branches[body.Branch] = map[string][]byte{}
	}
	f.tips[body.Branch] = sha
	glWrite(w, http.StatusCreated, map[string]any{
		"name":   body.Branch,
		"commit": map[string]string{"id": sha},
	})
}

// readBranch answers one branch, or says it is not there.
func (f *gitLabFake) readBranch(w http.ResponseWriter, branch string) {
	sha, known := f.tips[branch]
	if !known {
		glRefuse(w, http.StatusNotFound, "404 Branch Not Found")
		return
	}
	// Whether a branch is protected is answered to anybody who can read
	// the code, however little of the protection itself they may read.
	_, protected := f.protected[branch]
	glWrite(w, http.StatusOK, map[string]any{
		"name":      branch,
		"protected": protected,
		"commit":    map[string]string{"id": sha},
	})
}

// listStatuses answers a commit's status history, newest first by default.
func (f *gitLabFake) listStatuses(w http.ResponseWriter, r *http.Request, sha string) {
	if _, known := f.ancestry[sha]; !known {
		glRefuse(w, http.StatusNotFound, "404 Commit Not Found")
		return
	}
	out := slices.Clone(f.statuses[sha])
	if r.URL.Query().Get("sort") != "asc" {
		slices.Reverse(out)
	}
	glPage(f, w, r, out)
}

// servePublishStatus appends a status to a commit's history, as GitLab does:
// nothing is replaced, and the newest row is the verdict that counts.
func (f *gitLabFake) servePublishStatus(w http.ResponseWriter, r *http.Request, sha string) {
	if _, known := f.ancestry[sha]; !known {
		glRefuse(w, http.StatusNotFound, "404 Commit Not Found")
		return
	}
	body, ok := glBody[glStatusRequest](w, r)
	if !ok {
		return
	}
	if body.Name == "" || body.State == "" {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: state and name are required")
		return
	}
	if !slices.Contains([]string{"pending", "running", "success", "failed", "canceled"}, body.State) {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: state is not a state")
		return
	}
	f.nextStatus++
	now := f.now()
	status := glStatus{
		ID:          f.nextStatus,
		Name:        body.Name,
		Status:      body.State,
		Description: body.Description,
		TargetURL:   body.TargetURL,
		CreatedAt:   now,
		FinishedAt:  &now,
	}
	f.statuses[sha] = append(f.statuses[sha], status)
	glWrite(w, http.StatusCreated, status)
}

// serveProtectedBranches answers the protected branch endpoints.
func (f *gitLabFake) serveProtectedBranches(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		branches := make([]glProtectedBranch, 0, len(f.protected))
		for _, branch := range f.protected {
			branches = append(branches, *branch)
		}
		slices.SortFunc(branches, func(a, b glProtectedBranch) int { return a.ID - b.ID })
		glPage(f, w, r, branches)
	case len(rest) == 0 && r.Method == http.MethodPost:
		body, ok := glBody[glProtectedBranchRequest](w, r)
		if !ok {
			return
		}
		if _, taken := f.protected[body.Name]; taken {
			glRefuse(w, http.StatusConflict, "409 Protected branch already exists")
			return
		}
		f.nextProtected++
		branch := &glProtectedBranch{
			ID:                f.nextProtected,
			Name:              body.Name,
			AllowForcePush:    body.AllowForcePush,
			PushAccessLevels:  []glAccessLevel{{AccessLevel: body.PushAccessLevel}},
			MergeAccessLevels: []glAccessLevel{{AccessLevel: body.MergeAccessLevel}},
		}
		f.protected[body.Name] = branch
		glWrite(w, http.StatusCreated, branch)
	case len(rest) == 1 && r.Method == http.MethodGet:
		branch, known := f.protected[rest[0]]
		if !known {
			glRefuse(w, http.StatusNotFound, "404 Not found")
			return
		}
		glWrite(w, http.StatusOK, branch)
	case len(rest) == 1 && r.Method == http.MethodPatch:
		branch, known := f.protected[rest[0]]
		if !known {
			glRefuse(w, http.StatusNotFound, "404 Not found")
			return
		}
		body, ok := glBody[glProtectedBranchPatch](w, r)
		if !ok {
			return
		}
		if body.AllowForcePush != nil {
			branch.AllowForcePush = *body.AllowForcePush
		}
		glWrite(w, http.StatusOK, branch)
	default:
		glRefuse(w, http.StatusNotFound, "404 Not found")
	}
}

// serveApprovalRules answers the approval rules, which a free instance has
// not got at all.
func (f *gitLabFake) serveApprovalRules(w http.ResponseWriter, r *http.Request, rest []string) {
	if !f.hasApprovals {
		glRefuse(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		glWrite(w, http.StatusOK, f.rules)
	case len(rest) == 0 && r.Method == http.MethodPost:
		body, ok := glBody[glApprovalRuleRequest](w, r)
		if !ok {
			return
		}
		f.nextRule++
		rule := glApprovalRule{
			ID:                            f.nextRule,
			Name:                          body.Name,
			RuleType:                      body.RuleType,
			ApprovalsRequired:             body.ApprovalsRequired,
			AppliesToAllProtectedBranches: body.AppliesToAllProtectedBranches,
		}
		f.rules = append(f.rules, rule)
		glWrite(w, http.StatusCreated, rule)
	case len(rest) == 1 && r.Method == http.MethodPut:
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			glRefuse(w, http.StatusBadRequest, "400 Bad Request: rule id is not a number")
			return
		}
		index := slices.IndexFunc(f.rules, func(rule glApprovalRule) bool { return rule.ID == id })
		if index < 0 {
			glRefuse(w, http.StatusNotFound, "404 Rule Not Found")
			return
		}
		body, ok := glBody[glApprovalRuleRequest](w, r)
		if !ok {
			return
		}
		f.rules[index].ApprovalsRequired = body.ApprovalsRequired
		f.rules[index].AppliesToAllProtectedBranches = body.AppliesToAllProtectedBranches
		glWrite(w, http.StatusOK, f.rules[index])
	default:
		glRefuse(w, http.StatusNotFound, "404 Not found")
	}
}

// serveExternalChecks answers the external status checks, which an instance
// below Ultimate has not got at all.
func (f *gitLabFake) serveExternalChecks(w http.ResponseWriter, r *http.Request, rest []string) {
	if !f.hasChecks {
		glRefuse(w, http.StatusNotFound, "404 Not found")
		return
	}
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		glWrite(w, http.StatusOK, f.checks)
	case len(rest) == 0 && r.Method == http.MethodPost:
		body, ok := glBody[glExternalCheckRequest](w, r)
		if !ok {
			return
		}
		if body.Name == "" || body.ExternalURL == "" {
			glRefuse(w, http.StatusBadRequest, "400 Bad Request: name and external_url are required")
			return
		}
		f.nextCheck++
		check := glExternalCheck{ID: f.nextCheck, Name: body.Name, ExternalURL: body.ExternalURL}
		for _, id := range body.ProtectedBranchIDs {
			for _, branch := range f.protected {
				if branch.ID != id {
					continue
				}
				check.ProtectedBranches = append(check.ProtectedBranches, struct {
					Name string `json:"name"`
				}{Name: branch.Name})
			}
		}
		if len(check.ProtectedBranches) != len(body.ProtectedBranchIDs) {
			glRefuse(w, http.StatusBadRequest, "400 Bad Request: protected_branch_ids are not all protected branches")
			return
		}
		f.checks = append(f.checks, check)
		glWrite(w, http.StatusCreated, check)
	default:
		glRefuse(w, http.StatusNotFound, "404 Not found")
	}
}

// pullRequest returns the merge request with an internal id, as it stands.
func (f *gitLabFake) pullRequest(iid int) *core.PullRequest {
	index := slices.IndexFunc(f.prs, func(pr core.PullRequest) bool { return pr.Number == iid })
	if index < 0 {
		return nil
	}
	return &f.prs[index]
}

// mergeRequest is one merge request in GitLab's own shape.
func (f *gitLabFake) mergeRequest(pr core.PullRequest) glMergeRequest {
	out := glMergeRequest{
		IID:          pr.Number,
		Title:        pr.Title,
		Description:  pr.Body,
		State:        gitLabListState(pr.State),
		Draft:        pr.Draft,
		Author:       glUser{Username: pr.Author},
		Labels:       slices.Clone(pr.Labels),
		SHA:          pr.HeadSHA,
		SourceBranch: pr.HeadRef,
		TargetBranch: pr.BaseRef,
		ChangesCount: gitLabChangesCount(pr.Files.Changed),
		WebURL:       pr.URL,
		CreatedAt:    pr.CreatedAt,
		UpdatedAt:    pr.UpdatedAt,
	}
	if !pr.MergedAt.IsZero() {
		at := pr.MergedAt
		out.MergedAt = &at
		// A squashed merge request carries two commits: the merge commit
		// GitLab made and the one commit the change was squashed into.
		// The squash is the one that is on the trunk.
		out.MergeCommitSHA = pr.MergeSHA
		if f.squashCommit {
			out.SquashCommitSHA = pr.MergeSHA
			out.MergeCommitSHA = "merge-" + pr.MergeSHA
		}
	}
	if !pr.ClosedAt.IsZero() {
		at := pr.ClosedAt
		out.ClosedAt = &at
	}
	out.DetailedMergeStatus, out.MergeStatus = gitLabMergeStatus(pr)
	if f.armed[pr.Number] {
		// 17.11 renamed the field and went on sending the old one, so
		// an installation at that release answers both. An older one
		// answers only the old name, which is what legacyAutoMerge is
		// for.
		out.AutoMergeEnabled = !f.legacyAutoMerge
		out.MergeWhenPipelineSucceeds = true
	}
	return out
}

// gitLabMergeStatus is the pair GitLab answers mergeability with, in both the
// listing and the merge request's own payload — unlike GitHub, GitLab sends
// it in a list, which is what makes the contract's "a listing says nothing"
// a test of the adapter rather than of the fake.
//
// A merge request that is no longer open answers `not_open`, a value flow has
// no name for and would otherwise read as "nothing in the way".
func gitLabMergeStatus(pr core.PullRequest) (detailed, coarse string) {
	if pr.State != core.PRStateOpen {
		return "not_open", glMergeStatusCan
	}
	switch pr.Mergeability {
	case core.MergeabilityClean:
		return glMergeMergeable, glMergeStatusCan
	case core.MergeabilityConflicted:
		return glMergeConflict, glMergeStatusCannot
	case core.MergeabilityBehind:
		// Nothing conflicts: the merge method wants it rebased, so the
		// coarse field GitLab deprecated still says it can be merged.
		return glMergeNeedRebase, glMergeStatusCan
	default:
		return "checking", "checking"
	}
}

// gitLabChangesCount is `changes_count`, which GitLab sends as text and caps
// at a thousand: a merge request touching more than that reads "1000+".
func gitLabChangesCount(changed int) string {
	switch {
	case changed <= 0:
		return ""
	case changed >= 1000:
		return "1000+"
	}
	return strconv.Itoa(changed)
}

// reviewers is GET /merge_requests/:iid/reviewers: one row per reviewer
// carrying their latest verdict, which is all GitLab keeps.
func (f *gitLabFake) reviewers(iid int) []glReviewer {
	latest := map[string]core.Review{}
	var order []string
	for _, review := range f.reviews[iid] {
		if _, seen := latest[review.Author]; !seen {
			order = append(order, review.Author)
		}
		latest[review.Author] = review
	}
	out := make([]glReviewer, 0, len(order))
	for _, author := range order {
		review := latest[author]
		out = append(out, glReviewer{
			User:      glUser{Username: author},
			State:     glReviewState(review.State),
			CreatedAt: review.SubmittedAt,
			UpdatedAt: review.SubmittedAt,
		})
	}
	return out
}

// approvals is GET /merge_requests/:iid/approvals, which names the approvers
// and nobody else.
func (f *gitLabFake) approvals(iid int) glApprovals {
	var out glApprovals
	for _, reviewer := range f.reviewers(iid) {
		if reviewer.State != "approved" {
			continue
		}
		out.ApprovedBy = append(out.ApprovedBy, struct {
			User glUser `json:"user"`
		}{User: reviewer.User})
	}
	for _, login := range f.approvers[iid] {
		out.ApprovedBy = append(out.ApprovedBy, struct {
			User glUser `json:"user"`
		}{User: glUser{Username: login}})
	}
	return out
}

// closesIssues is GET /merge_requests/:iid/closes_issues: the forge's own
// link between a merge request and the tickets it closes.
func (f *gitLabFake) closesIssues(iid int) []glIssue {
	slug := f.project.PathWithNamespace
	var out []glIssue
	if f.external[iid] {
		// What an external tracker's issue looks like here: an id and a
		// title, and no reference of any kind.
		return []glIssue{{IID: 0}}
	}
	for _, key := range f.closes[iid] {
		project, number, found := strings.Cut(key, "#")
		if !found {
			continue
		}
		if project == "" {
			project = slug
		}
		iid, err := strconv.Atoi(number)
		if err != nil {
			continue
		}
		out = append(out, glIssue{
			IID: iid,
			References: glReferences{
				Short:    "#" + number,
				Relative: "#" + number,
				Full:     project + "#" + number,
			},
			WebURL: "https://gitlab.test/" + project + "/-/issues/" + number,
		})
	}
	return out
}

// diffs is GET /merge_requests/:iid/diffs, which names the paths a merge
// request touches.
func (f *gitLabFake) diffs(iid int) []glDiff {
	out := make([]glDiff, 0, len(f.paths[iid]))
	for _, path := range f.paths[iid] {
		out = append(out, glDiff{OldPath: path, NewPath: path})
	}
	return out
}

// commit is one commit in GitLab's own shape.
func (f *gitLabFake) commit(c core.Commit) glCommit {
	title, _, _ := strings.Cut(c.Message, "\n")
	authored := c.AuthoredAt
	if authored.IsZero() {
		authored = c.CommittedAt
	}
	return glCommit{
		ID:            c.SHA,
		Title:         title,
		Message:       c.Message,
		AuthorName:    c.Author,
		AuthorEmail:   c.Author + "@example.test",
		AuthoredDate:  authored,
		CommittedDate: c.CommittedAt,
		WebURL:        f.project.WebURL + "/-/commit/" + c.SHA,
	}
}

// resolve turns a ref — a branch or a commit — into the commit it names.
func (f *gitLabFake) resolve(ref string) string {
	if _, known := f.ancestry[ref]; known {
		return ref
	}
	if sha, known := f.tips[ref]; known {
		return sha
	}
	return ""
}

// now is the fixture's instant, so that nothing in the fake needs a clock.
func (f *gitLabFake) now() time.Time { return coretest.ContractNow }

// glReviewState is a verdict as GitLab's reviewers endpoint words it.
func glReviewState(state string) string {
	switch state {
	case core.ReviewApproved:
		return "approved"
	case core.ReviewChangesRequested:
		return "requested_changes"
	case core.ReviewCommented:
		return "reviewed"
	default:
		return "unreviewed"
	}
}

// gitLabSegments splits an escaped path into its decoded segments, so that a
// project is one segment and not two: GitLab addresses a project by its path
// with the slash percent-encoded, and a fake that split the decoded path
// would never see the encoding the adapter is meant to send.
func gitLabSegments(escaped string) ([]string, error) {
	raw := strings.Split(strings.Trim(escaped, "/"), "/")
	out := make([]string, 0, len(raw))
	for _, segment := range raw {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded)
	}
	return out, nil
}

// glWrite answers with a JSON payload.
func glWrite(w http.ResponseWriter, status int, doc any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if doc == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(doc)
}

// glRefuse answers with GitLab's own refusal shape, which carries a message
// and nothing else.
func glRefuse(w http.ResponseWriter, status int, message string) {
	glWrite(w, status, map[string]string{"message": message})
}

// glBody decodes a request body, refusing the call when it cannot.
func glBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var body T
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		glRefuse(w, http.StatusBadRequest, "400 Bad Request: "+err.Error())
		var zero T
		return zero, false
	}
	return body, true
}

// glPage answers one page of a collection and links to the next, as GitLab
// does. The page is smaller than the fixture so that a walk is a walk.
func glPage[T any](f *gitLabFake, w http.ResponseWriter, r *http.Request, items []T) {
	query := r.URL.Query()
	perPage := f.pageSize
	if raw := query.Get("per_page"); raw != "" {
		if asked, err := strconv.Atoi(raw); err == nil && asked > 0 && asked < perPage {
			perPage = asked
		}
	}
	page := 1
	if raw := query.Get("page"); raw != "" {
		asked, err := strconv.Atoi(raw)
		if err != nil || asked < 1 {
			glRefuse(w, http.StatusBadRequest, "400 Bad Request: page is not a page")
			return
		}
		page = asked
	}
	start := min((page-1)*perPage, len(items))
	end := min(start+perPage, len(items))
	window := items[start:end]

	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Per-Page", strconv.Itoa(perPage))
	w.Header().Set("X-Total", strconv.Itoa(len(items)))
	if end < len(items) {
		next := *r.URL
		forward := next.Query()
		forward.Set("page", strconv.Itoa(page+1))
		next.RawQuery = forward.Encode()
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		w.Header().Set("Link", "<"+f.base+next.RequestURI()+">; rel=\"next\"")
	}
	if window == nil {
		window = []T{}
	}
	glWrite(w, http.StatusOK, window)
}
