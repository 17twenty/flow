package forge

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
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
	// statuses is a history, as GitHub's is: publishing a status appends to
	// it and replaces nothing, and nextStatus is the identity the next one
	// gets.
	statuses   map[string][]core.CommitStatus
	nextStatus int
	// comments are the comments on each issue, in the order they were
	// written, and nextID is the identity the next one gets.
	comments map[int][]ghComment
	nextID   int64
	// loop makes every page point at another one, which is what a provider
	// with a bug in its own Link header does to a client that trusts it.
	loop bool
	// mergeableState, when set, is the `mergeable_state` every pull
	// request answers with, alongside `mergeable: false`. GitHub grows
	// this vocabulary with its features, so a fake that could only speak
	// the words flow has names for could never hand the adapter one it
	// has not.
	mergeableState string
	// mergeableNull sends `mergeable: null` alongside it, which is what
	// GitHub answers while the background job that works it out has not
	// run — including, for a moment, next to a state that is not
	// `unknown`.
	mergeableNull bool
	// armed is the merge method each armed pull request was promised to
	// land with. GitHub holds the arrangement on the pull request rather
	// than anywhere a listing would carry it, which is why only the single
	// read says so — and it holds the method with it, because "merge this
	// when the checks pass" has to say how.
	armed map[int]string
	// armings counts the mutations sent, which the request log cannot: a
	// GraphQL query and a GraphQL mutation are the same path.
	armings int
	// refusalKind and refusalMessage, when set, make every GraphQL query
	// fail the way GraphQL fails: 200, no data, and a reason.
	refusalKind    string
	refusalMessage string
	// protections are the branches with classic protection, as GitHub
	// holds them: a PUT replaces the whole of one, which is what makes an
	// adapter that does not merge lose what was there.
	protections map[string]core.Protection
	// rules are the effective rulesets per branch, GitHub's other
	// mechanism. Most repositories have none.
	rules map[string][]any
	// rulesets are the repository's rulesets as objects, whether or not
	// they are being enforced, and nextRuleset the identity the next one
	// gets. A ruleset that is not enforced appears here and in no branch's
	// effective rules, which is exactly the repository that reads as
	// unprotected while holding the protection in full.
	rulesets    []ghFakeRuleset
	nextRuleset int64
	// rulesetStatus, when set, is the status every ruleset request is
	// refused with: a credential allowed to read a repository is not
	// necessarily allowed to read its rules.
	rulesetStatus int
	// fixedMerge makes the repository ignore a change to its merge
	// settings, as an organisation policy that pins them does.
	fixedMerge bool
	// role is what the credential is to this repository, scopes the
	// classic token scopes every response reports — nil for a credential
	// that is not a classic token and sends no such header — and public
	// what the repository is, which is what a public_repo scope turns on.
	role   ghRepoPermissions
	scopes *string
	public bool
	// branches holds each branch's files, keyed by branch then path, and
	// refs the commit each branch points at. A file is read and written on
	// a branch, which is how a proposal keeps off the trunk.
	branches map[string]map[string][]byte
	refs     map[string]string
	nextPR   int

	mu          sync.Mutex
	requests    []string
	methods     []string
	writing     int
	mostWriting int
	notModified int
	userAgents  map[string]int
}

// newGitHubServer stands the fake up and returns it with its server.
func newGitHubServer(t *testing.T, fx coretest.ForgeFixture) (*gitHubFake, *httptest.Server) {
	t.Helper()
	fake := &gitHubFake{
		fx:          fx,
		pageSize:    2,
		statuses:    map[string][]core.CommitStatus{},
		comments:    map[int][]ghComment{},
		userAgents:  map[string]int{},
		protections: map[string]core.Protection{},
		armed:       map[int]string{},
		rules:       map[string][]any{},
		branches:    map[string]map[string][]byte{},
		refs:        map[string]string{},
	}
	fake.role = ghRoleOf(fx.Permissions)
	fake.scopes = ghScopesOf(fx.Permissions)
	fake.public = true
	fake.branches[fx.Repo.DefaultBranch] = maps.Clone(fx.Files)
	if fake.branches[fx.Repo.DefaultBranch] == nil {
		fake.branches[fx.Repo.DefaultBranch] = map[string][]byte{}
	}
	fake.refs[fx.Repo.DefaultBranch] = coretest.SHADeployed
	for _, pr := range fx.PullRequests {
		fake.nextPR = max(fake.nextPR, pr.Number)
	}
	for branch, protection := range fx.Protections {
		fake.protections[branch] = protection
	}
	for sha, statuses := range fx.Statuses {
		seeded := slices.Clone(statuses)
		for i := range seeded {
			fake.nextStatus++
			seeded[i].ID = strconv.Itoa(fake.nextStatus)
			if seeded[i].UpdatedAt.IsZero() {
				seeded[i].UpdatedAt = seeded[i].CreatedAt
			}
		}
		fake.statuses[sha] = seeded
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

// addComment seeds a comment written by somebody else.
func (f *gitHubFake) addComment(number int, author, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	comment := ghComment{
		ID:        f.nextID,
		Body:      body,
		CreatedAt: coretest.ContractNow,
		UpdatedAt: coretest.ContractNow,
	}
	comment.User.Login = author
	f.comments[number] = append(f.comments[number], comment)
}

// comment returns the comments on a pull request as the fake holds them.
func (f *gitHubFake) commentsOn(number int) []ghComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.comments[number])
}

// statusHistory returns every status published on a commit, oldest first.
func (f *gitHubFake) statusHistory(sha string) []core.CommitStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.statuses[sha])
}

// Requests returns the paths asked for, in order.
func (f *gitHubFake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// enterWriting records that a mutation is in flight.
func (f *gitHubFake) enterWriting() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writing++
	f.mostWriting = max(f.mostWriting, f.writing)
}

// leaveWriting records that a mutation finished, after holding it open.
func (f *gitHubFake) leaveWriting(hold time.Duration) {
	time.Sleep(hold)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writing--
}

// MostWriting returns the most mutations that were ever in flight at once.
func (f *gitHubFake) MostWriting() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mostWriting
}

// Methods returns the methods asked for, in order.
func (f *gitHubFake) Methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.methods)
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
	f.methods = append(f.methods, r.Method)
	f.userAgents[r.Header.Get("User-Agent")]++
	f.mu.Unlock()

	// GraphQL is answered before anything else: it is one endpoint off the
	// root rather than under the repository, and a query is a read however
	// GitHub makes you send it, so it is not counted as a mutation.
	if r.URL.EscapedPath() == "/graphql" {
		f.graphQL(w, r)
		return
	}

	if r.Method != http.MethodGet {
		f.enterWriting()
		// Long enough that two mutations arriving together would be seen
		// to overlap, rather than passing by being quick.
		defer f.leaveWriting(time.Millisecond)
	}

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
	case len(segments) == 0 && r.Method == http.MethodPatch:
		f.patchRepo(w, r)
	case len(segments) == 0:
		f.writeJSON(w, r, f.repoJSON(f.fx.Repo))
	case segments[0] == "pulls" && len(segments) == 1 && r.Method == http.MethodPost:
		f.openPull(w, r)
	case segments[0] == "pulls" && len(segments) == 1:
		f.pulls(w, r)
	case segments[0] == "pulls" && len(segments) == 2 && r.Method == http.MethodPatch:
		f.editPull(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 2:
		f.pull(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 3 && segments[2] == "reviews":
		f.reviews(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 3 && segments[2] == "files":
		f.files(w, r, segments[1])
	case segments[0] == "commits" && len(segments) == 1:
		f.commits(w, r)
	case segments[0] == "commits" && len(segments) == 3 && segments[2] == "statuses":
		f.commitStatuses(w, r, segments[1])
	case segments[0] == "statuses" && len(segments) == 2:
		f.publishStatus(w, r, segments[1])
	case segments[0] == "issues" && len(segments) == 3 && segments[2] == "comments":
		f.issueComments(w, r, segments[1])
	case segments[0] == "issues" && len(segments) == 3 && segments[1] == "comments":
		f.issueComment(w, r, segments[2])
	case segments[0] == "branches" && len(segments) > 2 && segments[len(segments)-1] == "protection":
		f.protection(w, r, strings.Join(segments[1:len(segments)-1], "/"))
	case segments[0] == "rules" && len(segments) > 2 && segments[1] == "branches":
		f.branchRules(w, r, strings.Join(segments[2:], "/"))
	case segments[0] == "rulesets" && len(segments) == 1:
		f.rulesetList(w, r)
	case segments[0] == "rulesets" && len(segments) == 2:
		f.ruleset(w, r, segments[1])
	case segments[0] == "git" && len(segments) == 2 && segments[1] == "refs":
		f.createRef(w, r)
	case segments[0] == "git" && len(segments) > 3 && segments[1] == "ref" && segments[2] == "heads":
		f.readRef(w, r, strings.Join(segments[3:], "/"))
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
	// head is owner-qualified, as GitHub insists.
	head := strings.TrimPrefix(query.Get("head"), f.fx.Repo.Owner+":")

	prs := slices.Clone(f.fx.PullRequests)
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].UpdatedAt.After(prs[j].UpdatedAt) })

	var page []any
	for _, pr := range prs {
		if base != "" && pr.BaseRef != base {
			continue
		}
		if head != "" && pr.HeadRef != head {
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
	pr.AutoMerge = f.armed[pr.Number] != ""
	doc := ghPullJSON(pr, true)
	if f.mergeableState != "" {
		doc["mergeable"], doc["mergeable_state"] = false, f.mergeableState
		if f.mergeableNull {
			doc["mergeable"] = nil
		}
	}
	f.writeJSON(w, r, doc)
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

// commitStatuses serves a commit's status history, newest first.
func (f *gitHubFake) commitStatuses(w http.ResponseWriter, r *http.Request, sha string) {
	if r.Method != http.MethodGet {
		f.fail(w, http.StatusMethodNotAllowed, "statuses are read, not written, here")
		return
	}
	f.mu.Lock()
	history := slices.Clone(f.statuses[sha])
	f.mu.Unlock()
	slices.Reverse(history)

	page := make([]any, 0, len(history))
	for _, status := range history {
		page = append(page, map[string]any{
			"id":          json.Number(status.ID),
			"context":     status.Context,
			"state":       status.State,
			"description": status.Description,
			"target_url":  status.TargetURL,
			"created_at":  status.CreatedAt,
			"updated_at":  status.UpdatedAt,
		})
	}
	f.writePage(w, r, page)
}

// publishStatus appends a status to a commit's history, as GitHub does: it
// replaces nothing, and the newest one from a context is the verdict.
func (f *gitHubFake) publishStatus(w http.ResponseWriter, r *http.Request, sha string) {
	if r.Method != http.MethodPost {
		f.fail(w, http.StatusMethodNotAllowed, "a status is published with POST")
		return
	}
	var body ghStatusRequest
	if !f.decode(w, r, &body) {
		return
	}
	if body.Context == "" || body.State == "" {
		f.fail(w, http.StatusUnprocessableEntity, "a status needs a context and a state")
		return
	}
	if len([]rune(body.Description)) > gitHubStatusDescription {
		f.fail(w, http.StatusUnprocessableEntity, "the description is too long")
		return
	}
	status := core.CommitStatus{
		Context:     body.Context,
		State:       body.State,
		Description: body.Description,
		TargetURL:   body.TargetURL,
		CreatedAt:   coretest.ContractNow,
		UpdatedAt:   coretest.ContractNow,
	}
	f.mu.Lock()
	f.nextStatus++
	status.ID = strconv.Itoa(f.nextStatus)
	f.statuses[sha] = append(f.statuses[sha], status)
	f.mu.Unlock()

	f.writeJSON(w, r, map[string]any{
		"id":          json.Number(status.ID),
		"context":     status.Context,
		"state":       status.State,
		"description": status.Description,
		"target_url":  status.TargetURL,
		"created_at":  status.CreatedAt,
		"updated_at":  status.UpdatedAt,
	})
}

// issueComments lists and writes the comments on a pull request, which GitHub
// addresses as an issue.
func (f *gitHubFake) issueComments(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		comments := slices.Clone(f.comments[pr.Number])
		f.mu.Unlock()
		page := make([]any, 0, len(comments))
		for _, comment := range comments {
			page = append(page, ghCommentJSON(comment))
		}
		f.writePage(w, r, page)
	case http.MethodPost:
		var body ghCommentRequest
		if !f.decode(w, r, &body) {
			return
		}
		f.mu.Lock()
		f.nextID++
		comment := ghComment{
			ID:        f.nextID,
			Body:      body.Body,
			CreatedAt: coretest.ContractNow,
			UpdatedAt: coretest.ContractNow,
			HTMLURL:   f.fx.Repo.URL + "/pull/" + raw + "#issuecomment-" + strconv.FormatInt(f.nextID, 10),
		}
		comment.User.Login = "flow"
		f.comments[pr.Number] = append(f.comments[pr.Number], comment)
		f.mu.Unlock()
		f.write(w, r, http.StatusCreated, mustJSON(ghCommentJSON(comment)), "application/json")
	default:
		f.fail(w, http.StatusMethodNotAllowed, "comments are listed and written here")
	}
}

// issueComment edits and deletes one comment by its identity.
func (f *gitHubFake) issueComment(w http.ResponseWriter, r *http.Request, raw string) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for number, comments := range f.comments {
		for i, comment := range comments {
			if comment.ID != id {
				continue
			}
			switch r.Method {
			case http.MethodPatch:
				var body ghCommentRequest
				if !f.decode(w, r, &body) {
					return
				}
				comment.Body, comment.UpdatedAt = body.Body, coretest.ContractNow
				f.comments[number][i] = comment
				f.writeJSON(w, r, ghCommentJSON(comment))
			case http.MethodDelete:
				f.comments[number] = slices.Delete(f.comments[number], i, i+1)
				w.WriteHeader(http.StatusNoContent)
			default:
				f.fail(w, http.StatusMethodNotAllowed, "a comment is edited or deleted here")
			}
			return
		}
	}
	f.fail(w, http.StatusNotFound, "Not Found")
}

// decode reads a JSON request body.
func (f *gitHubFake) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		f.fail(w, http.StatusBadRequest, "the body is not JSON: "+err.Error())
		return false
	}
	return true
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

// protection serves and replaces a branch's classic protection. A PUT
// replaces the whole of it, exactly as GitHub's does: an adapter that sends
// only what it was asked for loses everything else, which is the hazard spec
// 06 §4 is about.
func (f *gitHubFake) protection(w http.ResponseWriter, r *http.Request, branch string) {
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		protection, ok := f.protections[branch]
		f.mu.Unlock()
		if !ok {
			f.fail(w, http.StatusNotFound, "Branch not protected")
			return
		}
		f.writeJSON(w, r, ghProtectionJSON(protection))
	case http.MethodPut:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.fail(w, http.StatusBadRequest, "the protection did not decode: "+err.Error())
			return
		}
		for _, required := range []string{"required_status_checks", "enforce_admins", "required_pull_request_reviews", "restrictions"} {
			if _, ok := body[required]; !ok {
				f.fail(w, http.StatusUnprocessableEntity, "Invalid request: "+required+" is required and may be null")
				return
			}
		}
		protection := core.Protection{Branch: branch, Enabled: true}
		if checks, ok := body["required_status_checks"].(map[string]any); ok {
			protection.StrictChecks, _ = checks["strict"].(bool)
			for _, context := range checks["contexts"].([]any) {
				protection.RequiredChecks = append(protection.RequiredChecks, context.(string))
			}
		}
		if reviews, ok := body["required_pull_request_reviews"].(map[string]any); ok {
			protection.DismissStaleReviews, _ = reviews["dismiss_stale_reviews"].(bool)
			if count, ok := reviews["required_approving_review_count"].(float64); ok {
				protection.RequiredApprovals = int(count)
			}
		}
		protection.EnforceAdmins, _ = body["enforce_admins"].(bool)
		protection.RequireLinearHistory, _ = body["required_linear_history"].(bool)
		protection.AllowForcePush, _ = body["allow_force_pushes"].(bool)
		protection.AllowDeletions, _ = body["allow_deletions"].(bool)
		protection.RequireConversationResolution, _ = body["required_conversation_resolution"].(bool)
		f.mu.Lock()
		f.protections[branch] = protection
		f.mu.Unlock()
		f.writeJSON(w, r, ghProtectionJSON(protection))
	default:
		f.fail(w, http.StatusMethodNotAllowed, "no such method on protection")
	}
}

// branchRules serves the rulesets in force on a branch. An unruled branch
// gets an empty list, not a 404, which is what GitHub answers.
func (f *gitHubFake) branchRules(w http.ResponseWriter, r *http.Request, branch string) {
	f.mu.Lock()
	rules := slices.Clone(f.rules[branch])
	f.mu.Unlock()
	f.writePage(w, r, rules)
}

// addRule puts a ruleset rule in force on a branch.
func (f *gitHubFake) addRule(branch string, rule map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[branch] = append(f.rules[branch], rule)
}

// ghFakeRuleset is a ruleset as the repository holds it, in force or not.
type ghFakeRuleset struct {
	id          int64
	name        string
	target      string
	enforcement string
	include     []string
	exclude     []string
}

// addRuleset writes a ruleset on the repository without putting anything in
// force on any branch, which is what GitHub's own "create" does: every
// ruleset starts out disabled.
func (f *gitHubFake) addRuleset(ruleset ghFakeRuleset) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRuleset++
	ruleset.id = f.nextRuleset
	f.rulesets = append(f.rulesets, ruleset)
	return ruleset.id
}

// rulesetList serves the repository's rulesets. GitHub's list is a summary:
// it names each one and says whether it is enforced, and says nothing about
// what it covers or contains. Anything more costs a second request, which is
// what makes it worth fetching only the ones that matter.
func (f *gitHubFake) rulesetList(w http.ResponseWriter, r *http.Request) {
	if f.rulesetStatus != 0 {
		f.fail(w, f.rulesetStatus, "you may not read the rules of this repository")
		return
	}
	f.mu.Lock()
	summaries := make([]any, 0, len(f.rulesets))
	for _, ruleset := range f.rulesets {
		summaries = append(summaries, map[string]any{
			"id":          ruleset.id,
			"name":        ruleset.name,
			"target":      ruleset.target,
			"enforcement": ruleset.enforcement,
			"source_type": "Repository",
			"source":      f.fx.Repo.Owner + "/" + f.fx.Repo.Name,
		})
	}
	f.mu.Unlock()
	f.writePage(w, r, summaries)
}

// ruleset serves one ruleset in full, conditions and all.
func (f *gitHubFake) ruleset(w http.ResponseWriter, r *http.Request, raw string) {
	if f.rulesetStatus != 0 {
		f.fail(w, f.rulesetStatus, "you may not read the rules of this repository")
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		f.fail(w, http.StatusNotFound, "no such ruleset")
		return
	}
	f.mu.Lock()
	var found ghFakeRuleset
	for _, held := range f.rulesets {
		if held.id == id {
			found = held
		}
	}
	f.mu.Unlock()
	if found.id == 0 {
		f.fail(w, http.StatusNotFound, "no such ruleset")
		return
	}
	include, exclude := found.include, found.exclude
	if include == nil {
		include = []string{}
	}
	if exclude == nil {
		exclude = []string{}
	}
	f.writeJSON(w, r, map[string]any{
		"id":          found.id,
		"name":        found.name,
		"target":      found.target,
		"enforcement": found.enforcement,
		"conditions": map[string]any{
			"ref_name": map[string]any{"include": include, "exclude": exclude},
		},
		"_links": map[string]any{
			"html": map[string]any{
				"href": "https://github.com/" + f.fx.Repo.Owner + "/" + f.fx.Repo.Name + "/rules/" + raw,
			},
		},
	})
}

// patchRepo changes the repository's settings, replacing what it is given.
func (f *gitHubFake) patchRepo(w http.ResponseWriter, r *http.Request) {
	var body ghRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "the settings did not decode: "+err.Error())
		return
	}
	if !body.AllowSquashMerge && !body.AllowMergeCommit && !body.AllowRebaseMerge {
		f.fail(w, http.StatusUnprocessableEntity, "You cannot disable all merge types")
		return
	}
	f.mu.Lock()
	if f.fixedMerge {
		repo := f.fx.Repo
		f.mu.Unlock()
		f.writeJSON(w, r, f.repoJSON(repo))
		return
	}
	// A field the payload does not carry is a setting GitHub leaves exactly
	// as it found it, which is the whole of what makes an additive ask
	// additive. The fake has to keep that, or a test could not tell a flow
	// that asked for nothing from one that asked for a "no".
	autoMerge := f.fx.Repo.Merge.AllowAutoMerge
	if body.AllowAutoMerge != nil {
		autoMerge = *body.AllowAutoMerge
	}
	f.fx.Repo.Merge = core.MergeSettings{
		AllowSquash:         body.AllowSquashMerge,
		AllowMerge:          body.AllowMergeCommit,
		AllowRebase:         body.AllowRebaseMerge,
		SquashTitle:         squashTitleFromGitHub(body.SquashMergeCommitTitle),
		SquashMessage:       squashMessageFromGitHub(body.SquashMergeCommitMessage),
		DeleteBranchOnMerge: body.DeleteBranchOnMerge,
		AllowAutoMerge:      autoMerge,
	}
	repo := f.fx.Repo
	f.mu.Unlock()
	f.writeJSON(w, r, f.repoJSON(repo))
}

// ghProtectionJSON renders branch protection as GitHub does, with both the
// deprecated contexts list and the newer checks list.
func ghProtectionJSON(p core.Protection) map[string]any {
	doc := map[string]any{
		"enforce_admins":                   map[string]any{"enabled": p.EnforceAdmins},
		"required_linear_history":          map[string]any{"enabled": p.RequireLinearHistory},
		"allow_force_pushes":               map[string]any{"enabled": p.AllowForcePush},
		"allow_deletions":                  map[string]any{"enabled": p.AllowDeletions},
		"required_conversation_resolution": map[string]any{"enabled": p.RequireConversationResolution},
	}
	if len(p.RequiredChecks) > 0 || p.StrictChecks {
		checks := make([]any, 0, len(p.RequiredChecks))
		for _, context := range p.RequiredChecks {
			checks = append(checks, map[string]any{"context": context})
		}
		doc["required_status_checks"] = map[string]any{
			"strict":   p.StrictChecks,
			"contexts": p.RequiredChecks,
			"checks":   checks,
		}
	}
	if p.RequiredApprovals > 0 || p.DismissStaleReviews {
		doc["required_pull_request_reviews"] = map[string]any{
			"dismiss_stale_reviews":           p.DismissStaleReviews,
			"required_approving_review_count": p.RequiredApprovals,
		}
	}
	return doc
}

// graphQL answers the one GraphQL endpoint. GitHub returns 200 whatever
// happened and puts the failure in an errors array, which is the shape the
// adapter has to be held to — an HTTP status would have been noticed for it.
func (f *gitHubFake) graphQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		f.fail(w, http.StatusMethodNotAllowed, "a GraphQL query is sent with POST")
		return
	}
	var query core.GraphQLQuery
	if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
		f.fail(w, http.StatusBadRequest, "the query did not decode: "+err.Error())
		return
	}
	f.mu.Lock()
	kind, message := f.refusalKind, f.refusalMessage
	f.mu.Unlock()
	if kind != "" {
		f.graphQLError(w, r, kind, message)
		return
	}
	if strings.Contains(query.Query, "enablePullRequestAutoMerge") {
		f.enableAutoMerge(w, r, query)
		return
	}
	wantsAutoMerge := strings.Contains(query.Query, "autoMergeRequest")
	if !wantsAutoMerge && !strings.Contains(query.Query, "closingIssuesReferences") {
		f.graphQLError(w, r, "NOT_IMPLEMENTED", "the fake knows no such query")
		return
	}
	if query.Variables["owner"] != f.fx.Repo.Owner || query.Variables["repo"] != f.fx.Repo.Name {
		f.graphQLError(w, r, "NOT_FOUND", "Could not resolve to a Repository.")
		return
	}
	number, ok := query.Variables["number"].(float64)
	if !ok {
		f.graphQLError(w, r, "INVALID", "number is not an Int")
		return
	}
	pr, ok := f.findPull(strconv.Itoa(int(number)))
	if !ok {
		f.graphQLError(w, r, "NOT_FOUND", fmt.Sprintf("Could not resolve to a PullRequest with the number of %d.", int(number)))
		return
	}
	if wantsAutoMerge {
		f.writeJSON(w, r, map[string]any{"data": map[string]any{
			"repository": map[string]any{"pullRequest": f.autoMergeState(pr)},
		}})
		return
	}
	f.writeJSON(w, r, map[string]any{"data": f.closingIssuesPage(pr, query.Variables)})
}

// ghNodeID is the opaque identity GraphQL addresses a pull request by. The
// number is in it because the fake has to get back from one to the other, and
// GitHub's own ids are opaque in the same way: an adapter that took one apart
// would be reading something it was handed to pass on.
func ghNodeID(number int) string {
	return "PR_kwDO" + strconv.Itoa(number)
}

// autoMergeState renders the pull request as the arming query selects it:
// the node id the mutation takes, and the pending request if there is one.
func (f *gitHubFake) autoMergeState(pr core.PullRequest) map[string]any {
	state := map[string]any{"id": ghNodeID(pr.Number), "autoMergeRequest": nil}
	if f.armed[pr.Number] != "" {
		state["autoMergeRequest"] = map[string]any{"enabledAt": f.fx.Clock.Now()}
	}
	return state
}

// enableAutoMerge answers the arming mutation.
//
// Every refusal here is one only GitHub could have made, and each comes back
// as GitHub makes it: an error of type UNPROCESSABLE, with a message naming
// the condition. A repository with the setting switched off, a draft, a pull
// request with nothing left to wait for — the caller could not have known any
// of them without asking.
func (f *gitHubFake) enableAutoMerge(w http.ResponseWriter, r *http.Request, query core.GraphQLQuery) {
	id, _ := query.Variables["pr"].(string)
	number, err := strconv.Atoi(strings.TrimPrefix(id, "PR_kwDO"))
	if err != nil {
		f.graphQLError(w, r, "NOT_FOUND", "Could not resolve to a node with the global id of "+id)
		return
	}
	pr, ok := f.findPull(strconv.Itoa(number))
	if !ok {
		f.graphQLError(w, r, "NOT_FOUND", "Could not resolve to a node with the global id of "+id)
		return
	}
	method, _ := query.Variables["method"].(string)
	switch {
	case !f.fx.Repo.Merge.AllowAutoMerge:
		f.graphQLError(w, r, "UNPROCESSABLE", "Pull request Auto merge is not allowed for this repository")
	case pr.State != core.PRStateOpen:
		f.graphQLError(w, r, "UNPROCESSABLE", "Pull request is closed")
	case pr.Draft:
		f.graphQLError(w, r, "UNPROCESSABLE", "Pull request is in draft status")
	case method != "SQUASH" && method != "MERGE" && method != "REBASE":
		f.graphQLError(w, r, "UNPROCESSABLE", "Variable $method of type PullRequestMergeMethod! was provided invalid value")
	default:
		f.mu.Lock()
		f.armed[pr.Number], f.armings = method, f.armings+1
		f.mu.Unlock()
		f.writeJSON(w, r, map[string]any{"data": map[string]any{
			"enablePullRequestAutoMerge": map[string]any{
				"pullRequest": map[string]any{
					"autoMergeRequest": map[string]any{"enabledAt": f.fx.Clock.Now()},
				},
			},
		}})
	}
}

// graphQLRefusal makes every GraphQL query from now on fail with kind.
func (f *gitHubFake) graphQLRefusal(kind, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusalKind, f.refusalMessage = kind, message
}

// closingIssuesPage renders one page of a closingIssuesReferences connection.
// A connection may hand back fewer nodes than were asked for, so the fake
// pages at its own small size: an adapter that reads the first page and stops
// would pass against a fake that always answered in one.
func (f *gitHubFake) closingIssuesPage(pr core.PullRequest, variables map[string]any) map[string]any {
	size := f.pageSize
	if first, ok := variables["first"].(float64); ok && int(first) < size {
		size = int(first)
	}
	from := 0
	if after, ok := variables["after"].(string); ok {
		if n, err := strconv.Atoi(after); err == nil {
			from = n
		}
	}
	keys := pr.ClosingIssues[min(from, len(pr.ClosingIssues)):]
	more := len(keys) > size
	if more {
		keys = keys[:size]
	}
	nodes := make([]any, 0, len(keys))
	for _, key := range keys {
		slug, raw, _ := strings.Cut(key, "#")
		number, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		if slug == "" {
			slug = f.fx.Repo.Owner + "/" + f.fx.Repo.Name
		}
		nodes = append(nodes, map[string]any{
			"number":     number,
			"repository": map[string]any{"nameWithOwner": slug},
		})
	}
	cursor := ""
	if more {
		cursor = strconv.Itoa(from + len(keys))
	}
	return map[string]any{
		"repository": map[string]any{
			"pullRequest": map[string]any{
				"closingIssuesReferences": map[string]any{
					"nodes":    nodes,
					"pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor},
				},
			},
		},
	}
}

// graphQLError answers as GitHub does when a query fails: 200, no data, and
// the reason in the errors array.
func (f *gitHubFake) graphQLError(w http.ResponseWriter, r *http.Request, kind, message string) {
	f.writeJSON(w, r, map[string]any{
		"data":   map[string]any{"repository": map[string]any{"pullRequest": nil}},
		"errors": []any{map[string]any{"type": kind, "message": message}},
	})
}

// contents serves a file's bytes raw, its identity as JSON, and writes it
// back. A file belongs to a branch, which is what keeps a proposal off the
// trunk.
func (f *gitHubFake) contents(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method == http.MethodPut {
		f.writeContents(w, r, path)
		return
	}
	branch := cmp.Or(r.URL.Query().Get("ref"), f.fx.Repo.DefaultBranch)
	f.mu.Lock()
	files, known := f.branches[branch]
	content, ok := files[path]
	f.mu.Unlock()
	if !known {
		f.fail(w, http.StatusNotFound, "No commit found for the ref "+branch)
		return
	}
	if !ok {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	if r.Header.Get("Accept") == gitHubRaw {
		f.write(w, r, http.StatusOK, content, "text/plain")
		return
	}
	f.writeJSON(w, r, map[string]any{
		"path":     path,
		"sha":      blobSHA(content),
		"encoding": "base64",
		// GitHub wraps the base64 it sends, and a decoder that does not
		// expect the newlines fails on the real thing.
		"content": wrap(base64.StdEncoding.EncodeToString(content), 60),
		"type":    "file",
	})
}

// writeContents commits a file to a branch. GitHub refuses a write that does
// not quote the blob being replaced, which is what stops two writers from
// losing each other's work.
func (f *gitHubFake) writeContents(w http.ResponseWriter, r *http.Request, path string) {
	var body ghContentsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "the file did not decode: "+err.Error())
		return
	}
	content, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		f.fail(w, http.StatusBadRequest, "the content is not base64")
		return
	}
	if body.Message == "" {
		f.fail(w, http.StatusUnprocessableEntity, "Invalid request: message is required")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	files, known := f.branches[body.Branch]
	if !known {
		f.fail(w, http.StatusNotFound, "Branch "+body.Branch+" not found")
		return
	}
	if held, ok := files[path]; ok && body.SHA != blobSHA(held) {
		f.fail(w, http.StatusConflict, path+" does not match "+body.SHA)
		return
	} else if !ok && body.SHA != "" {
		f.fail(w, http.StatusUnprocessableEntity, path+" does not exist")
		return
	}
	files[path] = content
	f.refs[body.Branch] = blobSHA(content)
	f.writeJSON(w, r, map[string]any{
		"content": map[string]any{"path": path, "sha": blobSHA(content)},
		"commit":  map[string]any{"sha": f.refs[body.Branch]},
	})
}

// fileOn returns a file as a branch holds it.
func (f *gitHubFake) fileOn(branch, path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.branches[branch][path]
}

// readRef serves the commit a branch points at.
func (f *gitHubFake) readRef(w http.ResponseWriter, r *http.Request, branch string) {
	f.mu.Lock()
	sha, ok := f.refs[branch]
	f.mu.Unlock()
	if !ok {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	f.writeJSON(w, r, map[string]any{
		"ref":    "refs/heads/" + branch,
		"object": map[string]any{"sha": sha, "type": "commit"},
	})
}

// createRef branches off a commit, taking that commit's files with it.
func (f *gitHubFake) createRef(w http.ResponseWriter, r *http.Request) {
	var body ghRefRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "the ref did not decode: "+err.Error())
		return
	}
	branch, ok := strings.CutPrefix(body.Ref, "refs/heads/")
	if !ok || body.SHA == "" {
		f.fail(w, http.StatusUnprocessableEntity, "Invalid request: ref and sha are required")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.refs[branch]; exists {
		f.fail(w, http.StatusUnprocessableEntity, "Reference already exists")
		return
	}
	files := map[string][]byte{}
	for from, sha := range f.refs {
		if sha == body.SHA {
			files = maps.Clone(f.branches[from])
			break
		}
	}
	f.refs[branch] = body.SHA
	f.branches[branch] = files
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ref":    body.Ref,
		"object": map[string]any{"sha": body.SHA, "type": "commit"},
	})
}

// openPull opens a pull request, refusing one from a branch that is no
// different from its base, as GitHub does.
func (f *gitHubFake) openPull(w http.ResponseWriter, r *http.Request) {
	var body ghPullCreate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "the pull request did not decode: "+err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if body.Title == "" || body.Head == "" || body.Base == "" {
		f.fail(w, http.StatusUnprocessableEntity, "Invalid request: title, head and base are required")
		return
	}
	if _, ok := f.refs[body.Head]; !ok {
		f.fail(w, http.StatusUnprocessableEntity, "Field head is invalid")
		return
	}
	if maps.EqualFunc(f.branches[body.Head], f.branches[body.Base], bytes.Equal) {
		f.fail(w, http.StatusUnprocessableEntity, "No commits between "+body.Base+" and "+body.Head)
		return
	}
	f.nextPR++
	pr := core.PullRequest{
		Number:    f.nextPR,
		Title:     body.Title,
		Body:      body.Body,
		Author:    "flow",
		State:     core.PRStateOpen,
		HeadRef:   body.Head,
		HeadSHA:   f.refs[body.Head],
		BaseRef:   body.Base,
		CreatedAt: coretest.ContractNow,
		UpdatedAt: coretest.ContractNow,
		URL:       f.fx.Repo.URL + "/pull/" + strconv.Itoa(f.nextPR),
	}
	f.fx.PullRequests = append(f.fx.PullRequests, pr)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(ghPullJSON(pr, true))
}

// editPull changes an open pull request's title and body.
func (f *gitHubFake) editPull(w http.ResponseWriter, r *http.Request, raw string) {
	var body ghPullUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "the pull request did not decode: "+err.Error())
		return
	}
	number, err := strconv.Atoi(raw)
	if err != nil {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, pr := range f.fx.PullRequests {
		if pr.Number != number {
			continue
		}
		pr.Title, pr.Body, pr.UpdatedAt = body.Title, body.Body, coretest.ContractNow
		f.fx.PullRequests[i] = pr
		f.writeJSON(w, r, ghPullJSON(pr, true))
		return
	}
	f.fail(w, http.StatusNotFound, "Not Found")
}

// blobSHA is the identity a file's bytes have, as git gives one.
func blobSHA(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:20])
}

// wrap breaks a string into lines of at most n characters, as GitHub wraps
// the base64 of a file.
func wrap(text string, n int) string {
	var lines []string
	for len(text) > n {
		lines = append(lines, text[:n])
		text = text[n:]
	}
	return strings.Join(append(lines, text), "\n")
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
	if f.scopes != nil {
		w.Header().Set("X-OAuth-Scopes", *f.scopes)
	}
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

// repoJSON renders a repository as this fake's GitHub does: with the role the
// credential holds on it and what the repository is, which only the live API
// puts on the same document.
func (f *gitHubFake) repoJSON(repo core.RepoInfo) map[string]any {
	doc := ghRepoJSON(repo)
	f.mu.Lock()
	defer f.mu.Unlock()
	doc["permissions"] = map[string]any{
		"admin":    f.role.Admin,
		"maintain": f.role.Maintain,
		"push":     f.role.Push,
		"triage":   f.role.Triage,
		"pull":     f.role.Pull,
	}
	doc["private"] = !f.public
	doc["visibility"] = map[bool]string{true: "public", false: "private"}[f.public]
	return doc
}

// ghRoleOf is the repository role a fixture's permissions describe, with
// GitHub's own implication applied: an admin is also a maintainer.
func ghRoleOf(p core.Permissions) ghRepoPermissions {
	role := ghRepoPermissions{
		Admin:    p.Has("admin"),
		Maintain: p.Has("maintain"),
		Push:     p.Has("push"),
		Triage:   p.Has("triage"),
		Pull:     p.Has("pull"),
	}
	role.Maintain = role.Maintain || role.Admin
	role.Push = role.Push || role.Maintain
	role.Triage = role.Triage || role.Push
	role.Pull = role.Pull || role.Triage
	return role
}

// ghScopesOf is the classic token scopes a fixture's evidence reports, or nil
// when it names none.
func ghScopesOf(p core.Permissions) *string {
	for _, line := range p.Evidence {
		if rest, ok := strings.CutPrefix(line, "X-OAuth-Scopes: "); ok {
			return &rest
		}
	}
	return nil
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
		"allow_auto_merge":            repo.Merge.AllowAutoMerge,
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
		// GitHub sends the pending request as an object and sends
		// nothing at all when there is none, which is the difference
		// between a pull request that will land itself and one that
		// will not.
		doc["auto_merge"] = nil
		if pr.AutoMerge {
			doc["auto_merge"] = map[string]any{"merge_method": "squash"}
		}
		doc["changed_files"] = pr.Files.Changed
		mergeable, mergeableState := ghMergeabilityJSON(pr)
		doc["mergeable"] = mergeable
		doc["mergeable_state"] = mergeableState
	}
	return doc
}

// ghMergeabilityJSON renders the pair GitHub answers mergeability with.
//
// The list endpoint sends neither key, which is why they are behind `detail`,
// and GitHub answers `null` with the state `unknown` until the background job
// that works it out has run. A fake that always said true or false would
// never once hand the adapter the third state, and the adapter would then be
// free to read a pull request nobody has looked at yet as one that is fine.
func ghMergeabilityJSON(pr core.PullRequest) (any, string) {
	if pr.State != core.PRStateOpen {
		// GitHub does not stop answering once a pull request is closed
		// or merged; it answers with whatever it last computed against
		// a base that has moved on since, which for anything finished
		// is a refusal. The adapter has to drop it, and a fake that
		// went quiet here would let it keep it.
		return false, "dirty"
	}
	switch pr.Mergeability {
	case core.MergeabilityClean:
		return true, "clean"
	case core.MergeabilityConflicted:
		return false, "dirty"
	case core.MergeabilityBehind:
		// GitHub says a branch that has only fallen behind is still
		// mergeable: nothing conflicts, it is the protection that will
		// not take it until it is level. Rendering it as false here
		// would let an adapter that reads the bool and ignores the
		// state pass, and it would then call a stale branch a conflict
		// and send somebody to resolve one that is not there.
		return true, "behind"
	default:
		return nil, "unknown"
	}
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

// ghCommentJSON renders a comment as GitHub does.
func ghCommentJSON(comment ghComment) map[string]any {
	return map[string]any{
		"id":         comment.ID,
		"user":       map[string]any{"login": comment.User.Login},
		"body":       comment.Body,
		"created_at": comment.CreatedAt,
		"updated_at": comment.UpdatedAt,
		"html_url":   comment.HTMLURL,
	}
}

// mustJSON encodes a document the fake built itself.
func mustJSON(doc any) []byte {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
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
