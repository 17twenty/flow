package forge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
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

// giteaFake serves Gitea's documented /api/v1 shapes over a
// [coretest.ForgeFixture], so that the adapter is asked the same questions
// about the same repository as the in-memory fake (spec 14).
//
// It is a second fake rather than a parameterisation of the GitHub one
// because almost nothing about the two is the same: Gitea pages with `limit`
// and `page`, keeps a status history ordered by its own index, answers a
// branch's effective protection in one place and its rule in another, serves
// a file's bytes from an endpoint of their own, and has no GraphQL at all. A
// fake that tried to be both would be a third forge, and passing it would
// prove nothing about either.
type giteaFake struct {
	fx coretest.ForgeFixture
	// pageSize is deliberately smaller than anything the fixture holds, so
	// that every walk is a walk and a reader of only the first page is
	// found out.
	pageSize int

	mu       sync.Mutex
	requests []string
	methods  []string
	// armed are the pull requests scheduled to merge themselves. Gitea
	// keeps the schedule in a queue of its own and shows it on no
	// endpoint, so nothing here is ever read back — the only use the fake
	// has for it is to refuse the second arming the way Gitea does.
	armed map[int]bool
	// repo is the repository as it stands, because the merge settings and
	// the archive flag are both written during the contract.
	repo core.RepoInfo
	// external makes the repository send its issues to another tracker,
	// which changes the character Gitea writes in front of a pull
	// request's number.
	external string
	// prs are the pull requests, fixture and created alike, and nextPR the
	// number the next one gets.
	prs    []core.PullRequest
	nextPR int
	// reviews are the verdicts on each pull request. They are held apart
	// from the pull requests because Gitea's list endpoint carries none of
	// them, which is the whole point of the summary contract.
	reviews map[int][]core.Review
	// statuses is a history, as Gitea's is: publishing a status appends to
	// it and replaces nothing, and nextStatus is the index the next one
	// gets.
	statuses   map[string][]core.CommitStatus
	nextStatus int64
	// comments are the comments on each issue, in the order they were
	// written, and nextComment the identity the next one gets.
	comments    map[int][]gtComment
	nextComment int64
	// rules are the branch protection rules by name. A branch's effective
	// protection is derived from the rule of its own name, which is what
	// the web interface creates.
	rules map[string]core.Protection
	// admin says whether the credential may read a rule at all. Without it
	// Gitea refuses the branch_protections endpoint and answers the branch
	// itself as usual, which is the ordinary state of a bot account.
	admin bool
	// branches holds the files on each branch, and refs which branches
	// exist at all.
	branches map[string]map[string][]byte
	refs     map[string]string
	// effective names the rule that governs a branch, where that is not a
	// rule of the branch's own name. A glob rule is the usual reason.
	effective map[string]string
	// whitelists are who may push to the branches each rule covers. flow
	// never writes them and Gitea's edit leaves alone what it is not sent,
	// so a rule keeps them across every edit and a rule created beside it
	// has none — which is what shadowing a glob rule loses (issue #293).
	whitelists map[string][]string
	// unnamed answers a branch without naming the rule that governs it, as
	// a Gitea from before `effective_branch_protection_name` does. A branch
	// a glob rule covers is then looked for under its own name, and 404s.
	unnamed bool
	// blobs is the identity Gitea gave each file it holds, so that an
	// update quotes the blob it replaces.
	blobs map[string]string
	// fixedMerge makes the instance ignore a change to the merge
	// strategies, as a site administrator who pins them does.
	fixedMerge bool
	// notModified counts the conditional requests answered 304, and
	// userAgents what identified itself.
	notModified int
	userAgents  map[string]int
}

// newGiteaServer stands a fake Gitea up in front of a fixture.
func newGiteaServer(t *testing.T, fx coretest.ForgeFixture) (*giteaFake, *httptest.Server) {
	t.Helper()
	fake := &giteaFake{
		fx:         fx,
		pageSize:   2,
		repo:       fx.Repo,
		reviews:    map[int][]core.Review{},
		armed:      map[int]bool{},
		statuses:   map[string][]core.CommitStatus{},
		comments:   map[int][]gtComment{},
		rules:      map[string]core.Protection{},
		admin:      slices.Contains(fx.Permissions.Tokens, "admin"),
		branches:   map[string]map[string][]byte{},
		refs:       map[string]string{},
		effective:  map[string]string{},
		whitelists: map[string][]string{},
		blobs:      map[string]string{},
		userAgents: map[string]int{},
	}
	fake.branches[fx.Repo.DefaultBranch] = maps.Clone(fx.Files)
	if fake.branches[fx.Repo.DefaultBranch] == nil {
		fake.branches[fx.Repo.DefaultBranch] = map[string][]byte{}
	}
	for path, content := range fake.branches[fx.Repo.DefaultBranch] {
		fake.blobs[fx.Repo.DefaultBranch+":"+path] = blobSHA(content)
	}
	fake.refs[fx.Repo.DefaultBranch] = coretest.SHADeployed
	for _, pr := range fx.PullRequests {
		fake.prs = append(fake.prs, pr)
		fake.nextPR = max(fake.nextPR, pr.Number)
		if len(pr.Reviews) > 0 {
			fake.reviews[pr.Number] = slices.Clone(pr.Reviews)
		}
	}
	for branch, protection := range fx.Protections {
		fake.rules[branch] = protection
	}
	for sha, statuses := range fx.Statuses {
		seeded := slices.Clone(statuses)
		for i := range seeded {
			fake.nextStatus++
			seeded[i].ID = strconv.FormatInt(fake.nextStatus, 10)
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

// Requests returns the paths asked for, in order.
func (f *giteaFake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Methods returns the methods asked for, in order.
func (f *giteaFake) Methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.methods)
}

// NotModified returns how many conditional requests were answered 304.
func (f *giteaFake) NotModified() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notModified
}

// statusHistory returns every status published on a commit, oldest first.
func (f *giteaFake) statusHistory(sha string) []core.CommitStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.statuses[sha])
}

// publish appends a status to a commit as somebody other than the adapter
// under test, which is how a reading taken earlier is made out of date.
func (f *giteaFake) publish(sha string, status core.CommitStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextStatus++
	status.ID = strconv.FormatInt(f.nextStatus, 10)
	status.CreatedAt = coretest.ContractNow
	status.UpdatedAt = coretest.ContractNow
	f.statuses[sha] = append(f.statuses[sha], status)
}

// commentsOn returns the comments on a pull request as the fake holds them.
func (f *giteaFake) commentsOn(number int) []gtComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.comments[number])
}

// addComment seeds a comment written by somebody else.
func (f *giteaFake) addComment(number int, author, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextComment++
	comment := gtComment{
		ID:        f.nextComment,
		Body:      body,
		CreatedAt: coretest.ContractNow,
		UpdatedAt: coretest.ContractNow,
	}
	comment.User.Login = author
	f.comments[number] = append(f.comments[number], comment)
}

// ruleFor returns the protection rule of a name, and whether there is one.
func (f *giteaFake) ruleFor(name string) (core.Protection, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rule, ok := f.rules[name]
	return rule, ok
}

// prefix is the repository's path prefix under the API root.
func (f *giteaFake) prefix() string {
	return GiteaAPIPath + "/repos/" + f.fx.Repo.Owner + "/" + f.fx.Repo.Name
}

// ServeHTTP implements http.Handler.
func (f *giteaFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	// The escaped path, not the decoded one: an adapter that sends %2F
	// inside a ref has asked about a different branch, and a decoded path
	// hides exactly that mistake.
	f.requests = append(f.requests, r.URL.EscapedPath())
	f.methods = append(f.methods, r.Method)
	f.userAgents[r.Header.Get("User-Agent")]++
	f.mu.Unlock()

	// Gitea has no dated API version and no vendor media type: the Accept
	// is the whole of what a call declares about itself, so it is the whole
	// of what the fake insists on.
	if accept := r.Header.Get("Accept"); accept != giteaAccept {
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
		f.writeJSON(w, r, f.repoJSON())
	case segments[0] == "pulls" && len(segments) == 1 && r.Method == http.MethodPost:
		f.openPull(w, r)
	case segments[0] == "pulls" && len(segments) == 1:
		f.pulls(w, r)
	case segments[0] == "pulls" && len(segments) == 2 && r.Method == http.MethodPatch:
		f.editPull(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 2:
		f.pull(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 3 && segments[2] == "merge" && r.Method == http.MethodPost:
		f.mergePull(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 3 && segments[2] == "reviews":
		f.pullReviews(w, r, segments[1])
	case segments[0] == "pulls" && len(segments) == 3 && segments[2] == "files":
		f.pullFiles(w, r, segments[1])
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
	case segments[0] == "branches" && len(segments) == 1 && r.Method == http.MethodPost:
		f.createBranch(w, r)
	case segments[0] == "branches" && len(segments) > 1:
		f.branch(w, r, strings.Join(segments[1:], "/"))
	case segments[0] == "branch_protections" && len(segments) == 1 && r.Method == http.MethodPost:
		f.createRule(w, r)
	case segments[0] == "branch_protections" && len(segments) > 1:
		f.rule(w, r, strings.Join(segments[1:], "/"))
	case segments[0] == "compare" && len(segments) > 1:
		f.compare(w, r, strings.Join(segments[1:], "/"))
	case segments[0] == "raw" && len(segments) > 1:
		f.raw(w, r, strings.Join(segments[1:], "/"))
	case segments[0] == "contents" && len(segments) > 1:
		f.contents(w, r, strings.Join(segments[1:], "/"))
	default:
		f.fail(w, http.StatusNotFound, "Not Found")
	}
}

// repoJSON renders the repository as Gitea does, with the permissions the
// credential holds on it.
func (f *giteaFake) repoJSON() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	doc := map[string]any{
		"name":                              f.repo.Name,
		"owner":                             map[string]any{"login": f.repo.Owner},
		"default_branch":                    f.repo.DefaultBranch,
		"html_url":                          f.repo.URL,
		"archived":                          f.repo.Archived,
		"allow_squash_merge":                f.repo.Merge.AllowSquash,
		"allow_merge_commits":               f.repo.Merge.AllowMerge,
		"allow_rebase":                      f.repo.Merge.AllowRebase,
		"default_delete_branch_after_merge": f.repo.Merge.DeleteBranchOnMerge,
		"permissions": map[string]any{
			"admin": f.admin,
			"push":  true,
			"pull":  true,
		},
	}
	if f.external != "" {
		doc["external_tracker"] = map[string]any{"external_tracker_url": f.external}
	}
	return doc
}

// patchRepo writes the merge strategies, which is the only part of a
// repository flow ever changes.
func (f *giteaFake) patchRepo(w http.ResponseWriter, r *http.Request) {
	var body gtRepoRequest
	if !f.decode(w, r, &body) {
		return
	}
	f.mu.Lock()
	if !f.fixedMerge {
		f.repo.Merge.AllowSquash = body.AllowSquashMerge
		f.repo.Merge.AllowMerge = body.AllowMergeCommit
		f.repo.Merge.AllowRebase = body.AllowRebase
		f.repo.Merge.DeleteBranchOnMerge = body.DeleteBranch
	}
	f.mu.Unlock()
	f.writeJSON(w, r, f.repoJSON())
}

// pulls serves the list endpoint. Gitea takes the direction as part of the
// sort's name and has no merged state of its own, so a caller asking for
// merged pull requests is asking for closed ones.
func (f *giteaFake) pulls(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("sort") != "recentupdate" {
		f.fail(w, http.StatusBadRequest, "the list must be sorted by recentupdate, which is newest first")
		return
	}
	state := query.Get("state")
	base := query.Get("base_branch")

	f.mu.Lock()
	prs := slices.Clone(f.prs)
	f.mu.Unlock()
	slices.SortStableFunc(prs, func(a, b core.PullRequest) int { return b.UpdatedAt.Compare(a.UpdatedAt) })

	items := make([]any, 0, len(prs))
	for _, pr := range prs {
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
		if base != "" && pr.BaseRef != base {
			continue
		}
		items = append(items, gtPullJSON(pr, false))
	}
	f.writePage(w, r, items)
}

// pull serves one pull request.
func (f *giteaFake) pull(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "pull request does not exist")
		return
	}
	f.writeJSON(w, r, gtPullJSON(pr, true))
}

// mergePull is the one endpoint that both merges a pull request and promises
// to merge it later, told apart by `merge_when_checks_succeed`.
//
// Gitea refuses with 405 for every condition the pull request has not met and
// with 409 when it is already waiting, and the two statuses mean quite
// different things to a caller: the first is "not like this", the second is
// "already done". A fake that answered one status for both would let an
// adapter treat an arming it had already made as a failure.
//
// Nothing is served back. Gitea answers 201 with no body, and keeps the
// schedule where no endpoint can see it.
func (f *giteaFake) mergePull(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "pull request does not exist")
		return
	}
	var body gtMergeRequest
	if !f.decode(w, r, &body) {
		return
	}
	if !body.MergeWhenChecksSucceed {
		f.fail(w, http.StatusMethodNotAllowed, "the fake only ever promises to merge, never merges")
		return
	}
	f.mu.Lock()
	armed := f.armed[pr.Number]
	f.mu.Unlock()
	switch {
	case pr.State == core.PRStateMerged:
		f.fail(w, http.StatusMethodNotAllowed, "The PR is already merged")
	case pr.State != core.PRStateOpen:
		f.fail(w, http.StatusNotFound, "pull request does not exist")
	case pr.Draft:
		f.fail(w, http.StatusMethodNotAllowed, "Work in progress PRs cannot be merged")
	case !slices.Contains([]string{"squash", "merge", "rebase"}, body.Do):
		f.fail(w, http.StatusMethodNotAllowed, body.Do+" is not allowed an allowed merge style for this repository")
	case armed:
		f.fail(w, http.StatusConflict,
			fmt.Sprintf("pull request is already scheduled to auto merge when checks succeed [pull_id: %d]", pr.Number))
	default:
		f.mu.Lock()
		f.armed[pr.Number] = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}
}

// pullReviews serves the verdicts on a pull request, including the two Gitea
// has that are not verdicts at all.
func (f *giteaFake) pullReviews(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "pull request does not exist")
		return
	}
	f.mu.Lock()
	reviews := slices.Clone(f.reviews[pr.Number])
	f.mu.Unlock()

	items := make([]any, 0, len(reviews))
	for _, review := range reviews {
		items = append(items, gtReviewJSON(review))
	}
	f.writePage(w, r, items)
}

// pullFiles serves the paths a pull request touches.
func (f *giteaFake) pullFiles(w http.ResponseWriter, r *http.Request, raw string) {
	pr, ok := f.findPull(raw)
	if !ok {
		f.fail(w, http.StatusNotFound, "pull request does not exist")
		return
	}
	items := make([]any, 0, len(pr.Files.Paths))
	for _, path := range pr.Files.Paths {
		items = append(items, map[string]any{"filename": path})
	}
	f.writePage(w, r, items)
}

// commits serves the history of a ref, newest first.
func (f *giteaFake) commits(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	// Gitea computes a diff stat, a signature verification and a file list
	// for every commit unless it is told not to, and on a busy trunk they
	// are most of the answer's cost. A caller that stops saying so is
	// asking for work nothing reads.
	for _, name := range []string{"stat", "verification", "files"} {
		if query.Get(name) != "false" {
			f.fail(w, http.StatusBadRequest, "the walk did not turn "+name+" off")
			return
		}
	}

	items := make([]any, 0, len(f.fx.Commits))
	for _, commit := range f.fx.Commits {
		if raw := query.Get("since"); raw != "" {
			if since := parseStamp(raw); !since.IsZero() && commit.CommittedAt.Before(since) {
				continue
			}
		}
		if raw := query.Get("until"); raw != "" {
			if until := parseStamp(raw); !until.IsZero() && commit.CommittedAt.After(until) {
				continue
			}
		}
		items = append(items, gtCommitJSON(commit))
	}
	f.writePage(w, r, items)
}

// commitStatuses serves the whole history of a commit's statuses, newest
// first by the index Gitea gave each of them.
func (f *giteaFake) commitStatuses(w http.ResponseWriter, r *http.Request, sha string) {
	if sort := r.URL.Query().Get("sort"); sort != "highestindex" {
		f.fail(w, http.StatusBadRequest, "the history must be ordered by highestindex: a time can tie")
		return
	}
	f.mu.Lock()
	history := slices.Clone(f.statuses[sha])
	f.mu.Unlock()
	slices.Reverse(history)

	items := make([]any, 0, len(history))
	for _, status := range history {
		items = append(items, gtStatusJSON(status))
	}
	f.writePage(w, r, items)
}

// publishStatus appends a status to a commit's history, as Gitea does:
// nothing is ever replaced.
func (f *giteaFake) publishStatus(w http.ResponseWriter, r *http.Request, sha string) {
	var body gtStatusRequest
	if !f.decode(w, r, &body) {
		return
	}
	if body.Context == "" || body.State == "" {
		f.fail(w, http.StatusUnprocessableEntity, "a status needs a context and a state")
		return
	}
	f.mu.Lock()
	f.nextStatus++
	status := core.CommitStatus{
		ID: strconv.FormatInt(f.nextStatus, 10),
		// Gitea trims the three strings of a status before storing them.
		Context:     strings.TrimSpace(body.Context),
		State:       strings.TrimSpace(body.State),
		Description: strings.TrimSpace(body.Description),
		TargetURL:   strings.TrimSpace(body.TargetURL),
		CreatedAt:   coretest.ContractNow,
		UpdatedAt:   coretest.ContractNow,
	}
	f.statuses[sha] = append(f.statuses[sha], status)
	f.mu.Unlock()
	f.write(w, r, http.StatusCreated, mustJSON(gtStatusJSON(status)), "application/json")
}

// issueComments lists and writes the comments on a pull request, which is an
// issue to Gitea's comment endpoints.
func (f *giteaFake) issueComments(w http.ResponseWriter, r *http.Request, raw string) {
	number, err := strconv.Atoi(raw)
	if err != nil {
		f.fail(w, http.StatusNotFound, "no such issue")
		return
	}
	if r.Method == http.MethodPost {
		var body gtCommentRequest
		if !f.decode(w, r, &body) {
			return
		}
		f.mu.Lock()
		f.nextComment++
		comment := gtComment{
			ID:        f.nextComment,
			Body:      body.Body,
			CreatedAt: coretest.ContractNow,
			UpdatedAt: coretest.ContractNow,
			HTMLURL:   fmt.Sprintf("%s/pulls/%d#issuecomment-%d", f.fx.Repo.URL, number, f.nextComment),
		}
		comment.User.Login = "flow-bot"
		f.comments[number] = append(f.comments[number], comment)
		f.mu.Unlock()
		f.write(w, r, http.StatusCreated, mustJSON(comment), "application/json")
		return
	}
	f.mu.Lock()
	held := slices.Clone(f.comments[number])
	f.mu.Unlock()
	items := make([]any, 0, len(held))
	for _, comment := range held {
		items = append(items, comment)
	}
	f.writePage(w, r, items)
}

// issueComment edits and deletes one comment by its identity.
func (f *giteaFake) issueComment(w http.ResponseWriter, r *http.Request, raw string) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		f.fail(w, http.StatusNotFound, "no such comment")
		return
	}
	var body gtCommentRequest
	if r.Method == http.MethodPatch && !f.decode(w, r, &body) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for number, held := range f.comments {
		for i, comment := range held {
			if comment.ID != id {
				continue
			}
			switch r.Method {
			case http.MethodDelete:
				f.comments[number] = slices.Delete(held, i, i+1)
				w.WriteHeader(http.StatusNoContent)
			case http.MethodPatch:
				held[i].Body = body.Body
				// Gitea stamps an edit, which is what makes a
				// redundant rewrite visible to a test.
				held[i].UpdatedAt = comment.UpdatedAt.Add(time.Minute)
				f.write(w, r, http.StatusOK, mustJSON(held[i]), "application/json")
			default:
				f.write(w, r, http.StatusOK, mustJSON(comment), "application/json")
			}
			return
		}
	}
	f.fail(w, http.StatusNotFound, "comment does not exist")
}

// branch serves a branch and the protection it comes to once every rule
// covering it has been applied. Any credential that can see the branch can
// see that much.
func (f *giteaFake) branch(w http.ResponseWriter, r *http.Request, name string) {
	f.mu.Lock()
	_, exists := f.refs[name]
	// The rule that governs this branch is one of its own name unless a
	// wider rule covers it, which is what Gitea reports here and the only
	// way to find out where the rest of the settings are kept.
	governing := name
	if named, ok := f.effective[name]; ok {
		governing = named
	}
	rule, protected := f.rules[governing]
	f.mu.Unlock()
	if !exists && !protected {
		f.fail(w, http.StatusNotFound, "branch does not exist")
		return
	}
	doc := map[string]any{
		"name":      name,
		"protected": protected,
	}
	if protected && !f.unnamed {
		doc["effective_branch_protection_name"] = governing
	}
	if protected {
		doc["enable_status_check"] = len(rule.RequiredChecks) > 0
		doc["status_check_contexts"] = rule.RequiredChecks
		doc["required_approvals"] = rule.RequiredApprovals
	}
	f.writeJSON(w, r, doc)
}

// createBranch branches off another one.
func (f *giteaFake) createBranch(w http.ResponseWriter, r *http.Request) {
	var body gtBranchRequest
	if !f.decode(w, r, &body) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.refs[body.NewBranchName]; ok {
		f.fail(w, http.StatusConflict, "branch already exists")
		return
	}
	if _, ok := f.refs[body.OldBranchName]; !ok {
		f.fail(w, http.StatusNotFound, "the branch to copy does not exist")
		return
	}
	f.refs[body.NewBranchName] = f.refs[body.OldBranchName]
	f.branches[body.NewBranchName] = maps.Clone(f.branches[body.OldBranchName])
	for path, content := range f.branches[body.NewBranchName] {
		f.blobs[body.NewBranchName+":"+path] = blobSHA(content)
	}
	f.write(w, r, http.StatusCreated, mustJSON(map[string]any{"name": body.NewBranchName}), "application/json")
}

// rule reads and edits one protection rule. Only an administrator may read
// one at all, which is the ordinary state of a bot account and the reason the
// branch itself carries the part the gate depends on.
func (f *giteaFake) rule(w http.ResponseWriter, r *http.Request, name string) {
	if !f.admin {
		f.fail(w, http.StatusForbidden, "only an administrator may read a branch protection")
		return
	}
	f.mu.Lock()
	held, ok := f.rules[name]
	f.mu.Unlock()
	if !ok {
		f.fail(w, http.StatusNotFound, "branch protection does not exist")
		return
	}
	if r.Method != http.MethodPatch {
		doc := gtProtectionJSON(name, held)
		f.mu.Lock()
		doc["push_whitelist_usernames"] = f.whitelists[name]
		f.mu.Unlock()
		f.writeJSON(w, r, doc)
		return
	}

	var body gtProtectionRequest
	if !f.decode(w, r, &body) {
		return
	}
	f.mu.Lock()
	f.rules[name] = protectionFromGitea(name, body)
	updated := f.rules[name]
	f.mu.Unlock()
	f.writeJSON(w, r, gtProtectionJSON(name, updated))
}

// createRule writes a rule where there was none, which is the other address
// Gitea keeps the same thing at.
func (f *giteaFake) createRule(w http.ResponseWriter, r *http.Request) {
	if !f.admin {
		f.fail(w, http.StatusForbidden, "only an administrator may write a branch protection")
		return
	}
	var body gtProtectionRequest
	if !f.decode(w, r, &body) {
		return
	}
	name := body.RuleName
	if name == "" {
		name = body.BranchName
	}
	if name == "" {
		f.fail(w, http.StatusUnprocessableEntity, "a rule needs a name or a branch")
		return
	}
	f.mu.Lock()
	if _, ok := f.rules[name]; ok {
		f.mu.Unlock()
		f.fail(w, http.StatusConflict, "branch protection already exists")
		return
	}
	f.rules[name] = protectionFromGitea(name, body)
	created := f.rules[name]
	f.mu.Unlock()
	f.write(w, r, http.StatusCreated, mustJSON(gtProtectionJSON(name, created)), "application/json")
}

// compare answers with the commits head has that base does not, and counts
// them. It never names the relation, which is why the adapter asks twice.
func (f *giteaFake) compare(w http.ResponseWriter, r *http.Request, basehead string) {
	base, head, ok := strings.Cut(basehead, "...")
	if !ok {
		f.fail(w, http.StatusBadRequest, "a comparison is base...head")
		return
	}
	position := func(sha string) int {
		for i, commit := range f.fx.Commits {
			if commit.SHA == sha {
				return i
			}
		}
		return -1
	}
	from, to := position(base), position(head)
	if from < 0 || to < 0 {
		f.fail(w, http.StatusNotFound, "no such revision")
		return
	}
	// The fixture's history is newest first, so a smaller index is a later
	// commit and the distance is how many of them lie between.
	f.writeJSON(w, r, map[string]any{"total_commits": max(from-to, 0)})
}

// raw serves a file's bytes, which Gitea keeps at an endpoint of their own
// rather than behind a media type on the contents endpoint.
func (f *giteaFake) raw(w http.ResponseWriter, r *http.Request, path string) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = f.fx.Repo.DefaultBranch
	}
	f.mu.Lock()
	content, ok := f.branches[ref][path]
	f.mu.Unlock()
	if !ok {
		f.fail(w, http.StatusNotFound, "object does not exist")
		return
	}
	f.write(w, r, http.StatusOK, content, "text/plain; charset=utf-8")
}

// contents reads the blob identity of a file and writes a new version of it.
func (f *giteaFake) contents(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method == http.MethodGet {
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = f.fx.Repo.DefaultBranch
		}
		f.mu.Lock()
		content, ok := f.branches[ref][path]
		identity := f.blobs[ref+":"+path]
		f.mu.Unlock()
		if !ok {
			f.fail(w, http.StatusNotFound, "object does not exist")
			return
		}
		f.writeJSON(w, r, map[string]any{
			"sha":      identity,
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString(content),
		})
		return
	}

	var body gtContentsRequest
	if !f.decode(w, r, &body) {
		return
	}
	content, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		f.fail(w, http.StatusUnprocessableEntity, "the content is not base64")
		return
	}
	branch := body.Branch
	if branch == "" {
		branch = f.fx.Repo.DefaultBranch
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.refs[branch]; !ok {
		f.fail(w, http.StatusNotFound, "branch does not exist")
		return
	}
	held, exists := f.branches[branch][path]
	switch {
	case r.Method == http.MethodPost && exists:
		f.fail(w, http.StatusUnprocessableEntity, "the file already exists")
		return
	case r.Method == http.MethodPut && !exists:
		f.fail(w, http.StatusNotFound, "object does not exist")
		return
	case r.Method == http.MethodPut && body.SHA != f.blobs[branch+":"+path]:
		// Gitea refuses an update that quotes a blob it no longer
		// holds, which is what stops two writers overwriting one
		// another.
		f.fail(w, http.StatusConflict, "the blob quoted is not the one held")
		return
	}
	_ = held
	if f.branches[branch] == nil {
		f.branches[branch] = map[string][]byte{}
	}
	f.branches[branch][path] = content
	f.blobs[branch+":"+path] = blobSHA(content)
	f.write(w, r, http.StatusCreated, mustJSON(map[string]any{
		"content": map[string]any{"sha": f.blobs[branch+":"+path]},
	}), "application/json")
}

// openPull opens a pull request from a branch.
func (f *giteaFake) openPull(w http.ResponseWriter, r *http.Request) {
	var body gtPullCreate
	if !f.decode(w, r, &body) {
		return
	}
	f.mu.Lock()
	head, ok := f.refs[body.Head]
	if !ok {
		f.mu.Unlock()
		f.fail(w, http.StatusNotFound, "the head branch does not exist")
		return
	}
	f.nextPR++
	pr := core.PullRequest{
		Number:    f.nextPR,
		Title:     body.Title,
		Body:      body.Body,
		Author:    "flow-bot",
		State:     core.PRStateOpen,
		HeadSHA:   head,
		HeadRef:   body.Head,
		BaseRef:   body.Base,
		CreatedAt: coretest.ContractNow,
		UpdatedAt: coretest.ContractNow,
		URL:       fmt.Sprintf("%s/pulls/%d", f.fx.Repo.URL, f.nextPR),
	}
	f.prs = append(f.prs, pr)
	f.mu.Unlock()
	f.write(w, r, http.StatusCreated, mustJSON(gtPullJSON(pr, true)), "application/json")
}

// editPull rewrites a pull request's title and body.
func (f *giteaFake) editPull(w http.ResponseWriter, r *http.Request, raw string) {
	var body gtPullUpdate
	if !f.decode(w, r, &body) {
		return
	}
	number, err := strconv.Atoi(raw)
	if err != nil {
		f.fail(w, http.StatusNotFound, "pull request does not exist")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.prs {
		if f.prs[i].Number != number {
			continue
		}
		f.prs[i].Title = body.Title
		f.prs[i].Body = body.Body
		f.prs[i].UpdatedAt = coretest.ContractNow
		f.write(w, r, http.StatusOK, mustJSON(gtPullJSON(f.prs[i], true)), "application/json")
		return
	}
	f.fail(w, http.StatusNotFound, "pull request does not exist")
}

// findPull returns a pull request by its number as a string.
func (f *giteaFake) findPull(raw string) (core.PullRequest, bool) {
	number, err := strconv.Atoi(raw)
	if err != nil {
		return core.PullRequest{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, pr := range f.prs {
		if pr.Number == number {
			return pr, true
		}
	}
	return core.PullRequest{}, false
}

// decode reads a JSON body, failing the request when it is not one.
func (f *giteaFake) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		f.fail(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// writePage serves one page of a collection, pointing at the next with a Link
// header. Gitea pages with `page` and `limit`, and caps the limit at an
// instance setting rather than refusing it.
func (f *giteaFake) writePage(w http.ResponseWriter, r *http.Request, items []any) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	size := f.pageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			size = min(size, n)
		}
	}
	start := min((page-1)*size, len(items))
	end := min(start+size, len(items))
	slice := items[start:end]
	if slice == nil {
		slice = []any{}
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(items)))
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

// writeJSON encodes a document and serves it with an ETag.
func (f *giteaFake) writeJSON(w http.ResponseWriter, r *http.Request, doc any) {
	raw, err := json.Marshal(doc)
	if err != nil {
		f.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	f.write(w, r, http.StatusOK, raw, "application/json")
}

// write serves a body, answering a conditional request with 304 when the
// caller already has it.
func (f *giteaFake) write(w http.ResponseWriter, r *http.Request, status int, body []byte, contentType string) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", contentType)
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

// fail answers in Gitea's error shape, which names the message and nothing
// else.
func (f *giteaFake) fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"message":%q,"url":"https://gitea.test/api/swagger"}`, message)
}

// gtPullJSON renders a pull request as Gitea's REST does. A listing carries
// no counted diff: Gitea sends the fields and leaves them at zero, because
// counting them is a read of the diff itself.
func gtPullJSON(pr core.PullRequest, detail bool) map[string]any {
	doc := map[string]any{
		"number":           pr.Number,
		"title":            pr.Title,
		"body":             pr.Body,
		"user":             map[string]any{"login": pr.Author},
		"state":            giteaWireState(pr.State),
		"draft":            pr.Draft,
		"merged":           pr.State == core.PRStateMerged,
		"merge_commit_sha": pr.MergeSHA,
		"created_at":       pr.CreatedAt,
		"updated_at":       pr.UpdatedAt,
		"merged_at":        stampOrNil(pr.MergedAt),
		"closed_at":        stampOrNil(pr.ClosedAt),
		"head":             map[string]any{"ref": pr.HeadRef, "sha": pr.HeadSHA},
		"base":             map[string]any{"ref": pr.BaseRef, "sha": coretest.SHADeployed},
		"html_url":         pr.URL,
		// Gitea sends one bool, in the listing as well as in the pull
		// request's own payload, and goes on sending it after the pull
		// request is merged — where it is false, because a merged
		// branch no longer merges. The adapter has to know not to read
		// it then.
		//
		// It is computed from conflicts alone, so a branch that has
		// only fallen behind its base is still mergeable here: Gitea
		// applies `block_on_outdated_branch` when somebody presses
		// merge and says nothing about it beforehand.
		"mergeable": pr.State == core.PRStateOpen && pr.Mergeability != core.MergeabilityConflicted,
	}
	labels := make([]any, 0, len(pr.Labels))
	for _, label := range pr.Labels {
		labels = append(labels, map[string]any{"name": label})
	}
	doc["labels"] = labels
	if detail {
		doc["changed_files"] = pr.Files.Changed
	}
	return doc
}

// giteaWireState is the state Gitea reports. It has no merged state: a merged
// pull request is a closed one that says it was merged.
func giteaWireState(state string) string {
	if state == core.PRStateOpen {
		return "open"
	}
	return "closed"
}

// gtReviewJSON renders a review as Gitea's REST does, in its own vocabulary.
func gtReviewJSON(review core.Review) map[string]any {
	state, dismissed := "COMMENT", false
	switch review.State {
	case core.ReviewApproved:
		state = "APPROVED"
	case core.ReviewChangesRequested:
		state = "REQUEST_CHANGES"
	case core.ReviewDismissed:
		state, dismissed = "APPROVED", true
	}
	doc := map[string]any{
		"user":      map[string]any{"login": review.Author},
		"state":     state,
		"dismissed": dismissed,
	}
	doc["submitted_at"] = stampOrNil(review.SubmittedAt)
	return doc
}

// gtCommitJSON renders a commit as Gitea's REST does.
func gtCommitJSON(commit core.Commit) map[string]any {
	authored := commit.AuthoredAt
	if authored.IsZero() {
		authored = commit.CommittedAt
	}
	return map[string]any{
		"sha":      commit.SHA,
		"html_url": commit.URL,
		"author":   map[string]any{"login": commit.Author},
		"commit": map[string]any{
			"message":   commit.Message,
			"author":    map[string]any{"name": commit.Author, "date": authored},
			"committer": map[string]any{"date": commit.CommittedAt},
		},
	}
}

// gtStatusJSON renders a commit status as Gitea's REST does, which answers
// with `status` where it is told with `state`.
func gtStatusJSON(status core.CommitStatus) map[string]any {
	id, _ := strconv.ParseInt(status.ID, 10, 64)
	return map[string]any{
		"id":          id,
		"context":     status.Context,
		"status":      status.State,
		"description": status.Description,
		"target_url":  status.TargetURL,
		"created_at":  status.CreatedAt,
		"updated_at":  status.UpdatedAt,
	}
}

// gtProtectionJSON renders a rule as Gitea's REST does.
func gtProtectionJSON(name string, p core.Protection) map[string]any {
	contexts := p.RequiredChecks
	if contexts == nil {
		contexts = []string{}
	}
	return map[string]any{
		"rule_name":                name,
		"enable_status_check":      len(contexts) > 0,
		"status_check_contexts":    contexts,
		"required_approvals":       p.RequiredApprovals,
		"dismiss_stale_approvals":  p.DismissStaleReviews,
		"block_on_outdated_branch": p.StrictChecks,
		"apply_to_admins":          p.EnforceAdmins,
	}
}

// protectionFromGitea is the rule a write leaves behind.
func protectionFromGitea(name string, body gtProtectionRequest) core.Protection {
	p := core.Protection{
		Branch:              name,
		Enabled:             true,
		RequiredApprovals:   body.RequiredApprovals,
		DismissStaleReviews: body.DismissStale,
		StrictChecks:        body.BlockOutdated,
		EnforceAdmins:       body.ApplyToAdmins,
	}
	if body.EnableStatusCheck {
		p.RequiredChecks = slices.Clone(body.StatusCheckContexts)
	}
	return p
}
