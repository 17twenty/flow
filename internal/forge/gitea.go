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

// Gitea adapter constants (spec 10 §3).
const (
	// GiteaKind is the adapter name, matching assets/capabilities/gitea.yaml
	// and forge.type in configuration. Forgejo is Gitea's fork and serves
	// the same API under the same path, so one adapter speaks for both and
	// the flavour is a detail of detection rather than of the kind.
	GiteaKind = "gitea"
	// GiteaAPIPath is where every Gitea and Forgejo instance answers. There
	// is no canonical host: a Gitea lives wherever somebody put it, which is
	// why forge.url is not optional for this adapter.
	GiteaAPIPath = "/api/v1"
	// giteaAccept is the media type every call asks for. Gitea has no dated
	// API version and no vendor media type.
	giteaAccept = "application/json"
	// giteaPageSize is the largest page a Gitea serves without being
	// reconfigured: api.MAX_RESPONSE_ITEMS defaults to 50, and asking for
	// more is silently reduced to it.
	giteaPageSize = 50
	// giteaReference is the character Gitea writes in front of a pull
	// request's number in the header a squash lands, and
	// giteaExternalReference the one it writes when the repository sends
	// its issues to another tracker (services/pull/merge.go).
	giteaReference         = "#"
	giteaExternalReference = "!"
)

// Gitea's permission vocabulary, which is the one assets/capabilities/
// gitea.yaml is written in. A probe and a declaration that do not share a
// vocabulary can never agree (spec 05 §1).
const (
	giteaTokenRead  = "read"
	giteaTokenWrite = "write"
	giteaTokenAdmin = "admin"
)

// giteaStatusStates are the states flow publishes. Gitea accepts two more —
// `warning` and `skipped` — and both are read back faithfully, but flow has
// no verdict that means either, so it never writes one.
var giteaStatusStates = []string{
	core.StatusSuccess, core.StatusFailure, core.StatusPending, core.StatusError,
}

// GiteaHeader returns the headers every Gitea call carries. The root passes it
// to [core.ClientOptions], as it does GitHub's.
func GiteaHeader() http.Header {
	h := http.Header{}
	h.Set("Accept", giteaAccept)
	return h
}

// GiteaOptions configures a [Gitea] adapter.
type GiteaOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Owner and Repo name the repository the adapter speaks for.
	Owner string
	Repo  string
	// Capabilities is the declaration loaded from
	// assets/capabilities/gitea.yaml.
	Capabilities core.CapabilitySet
}

// Gitea is the Gitea and Forgejo forge adapter.
type Gitea struct {
	client *core.Client
	owner  string
	repo   string
	caps   core.CapabilitySet

	// writing serialises the calls that change the repository, as GitHub's
	// does: a Gitea is usually one process in front of one database, and a
	// burst of concurrent writes to one repository buys nothing.
	writing sync.Mutex

	// referencing guards reference, the character Gitea writes in front of
	// a pull request's number.
	referencing sync.Mutex
	reference   string
}

// NewGitea returns an adapter for one repository.
func NewGitea(o GiteaOptions) (*Gitea, error) {
	switch {
	case o.Client == nil:
		return nil, core.Errorf(core.ErrValidation, "build the gitea adapter").
			WithWhy("no HTTP client was given")
	case o.Owner == "" || o.Repo == "":
		return nil, core.Errorf(core.ErrValidation, "build the gitea adapter").
			WithWhy("the repository was given as %q/%q", o.Owner, o.Repo).
			WithFix("name the repository as owner/name")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the gitea adapter").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/gitea.yaml and pass the declaration")
	}
	return &Gitea{client: o.Client, owner: o.Owner, repo: o.Repo, caps: o.Capabilities}, nil
}

// Kind is the adapter's name.
func (g *Gitea) Kind() string { return GiteaKind }

// Capabilities is what this adapter declares it can do.
func (g *Gitea) Capabilities() core.CapabilitySet { return g.caps }

// Slug is the repository as people write it.
func (g *Gitea) Slug() string { return g.owner + "/" + g.repo }

// Repo describes the repository: its trunk and how pull requests may land.
func (g *Gitea) Repo(ctx context.Context) (core.RepoInfo, error) {
	payload, err := g.repository(ctx)
	if err != nil {
		return core.RepoInfo{}, err
	}
	return payload.toRepoInfo(), nil
}

// repository reads the repository itself.
func (g *Gitea) repository(ctx context.Context) (gtRepo, error) {
	var payload gtRepo
	if _, err := g.do(ctx, core.Request{Path: g.path(), Out: &payload}); err != nil {
		return gtRepo{}, core.Explain(err, "read the repository %s", g.Slug())
	}
	return payload, nil
}

// squashReference returns the character Gitea will write in front of a pull
// request's number in the header a squash lands, reading the repository for it
// once a run.
//
// Where the issues of a repository live is a setting, and a setting does not
// change halfway through a run. Reading it for every pull request would double
// the cost of an audit that walks a fortnight of them, so the answer is kept —
// only when it was an answer, since a failed read must not become the one this
// adapter gives for the rest of the run.
func (g *Gitea) squashReference(ctx context.Context) (string, error) {
	g.referencing.Lock()
	defer g.referencing.Unlock()
	if g.reference != "" {
		return g.reference, nil
	}
	payload, err := g.repository(ctx)
	if err != nil {
		return "", err
	}
	g.reference = payload.reference()
	return g.reference, nil
}

// PullRequest returns one pull request with the reviews and the file summary
// the policy gate reads.
//
// The tickets it closes are not among them. Gitea stores the links it makes
// from the closing keywords in a body but publishes none of them: no field of
// the pull request payload names an issue, and no endpoint lists them. So
// `linked_issues_read` is declared unsupported and the engine falls back to
// reading the branch and the title (spec 05 §2, spec 10 §3).
func (g *Gitea) PullRequest(ctx context.Context, number int) (core.PullRequest, error) {
	reference, err := g.squashReference(ctx)
	if err != nil {
		return core.PullRequest{}, err
	}
	var payload gtPull
	path := g.path("pulls", strconv.Itoa(number))
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		return core.PullRequest{}, core.Explain(err, "read pull request #%d of %s", number, g.Slug())
	}
	pr := payload.toPullRequest(reference)

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

// reviews returns the submitted verdicts on a pull request.
func (g *Gitea) reviews(ctx context.Context, number int) ([]core.Review, error) {
	var reviews []core.Review
	req := core.Request{
		Path:  g.path("pulls", strconv.Itoa(number), "reviews"),
		Query: g.page(),
	}
	err := paginate(ctx, g, req, func(r gtReview) bool {
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
// Flow needs a summary rather than a manifest, and the counts come from the
// pull request itself.
func (g *Gitea) changedPaths(ctx context.Context, number int) ([]string, error) {
	var files []gtChangedFile
	req := core.Request{
		Path:  g.path("pulls", strconv.Itoa(number), "files"),
		Query: g.page(),
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
// update. Reviews and the file summary are not on Gitea's list payload — it
// sends the fields and leaves them null — so a caller that needs either asks
// for the pull request itself (spec 10 §2).
func (g *Gitea) PullRequests(ctx context.Context, q core.PRQuery) ([]core.PullRequest, error) {
	reference, err := g.squashReference(ctx)
	if err != nil {
		return nil, err
	}
	query := g.page()
	query.Set("state", giteaListState(q.State))
	// Gitea has no direction of its own: the order is part of the sort's
	// name, and `recentupdate` is updated-descending.
	query.Set("sort", "recentupdate")
	if q.Base != "" {
		query.Set("base_branch", q.Base)
	}

	var prs []core.PullRequest
	err = paginate(ctx, g, core.Request{Path: g.path("pulls"), Query: query}, func(p gtPull) bool {
		pr := p.toPullRequest(reference)
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
func (g *Gitea) Commits(ctx context.Context, q core.CommitQuery) ([]core.Commit, error) {
	query := g.page()
	// Gitea computes a diff stat, a signature verification and a file list
	// for every commit unless it is told not to. Flow reads none of the
	// three, and on a busy trunk they are most of the answer's cost.
	query.Set("stat", "false")
	query.Set("verification", "false")
	query.Set("files", "false")
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
	err := paginate(ctx, g, core.Request{Path: g.path("commits"), Query: query}, func(c gtCommit) bool {
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
// Gitea answers a comparison with the commits the head has and the base does
// not, and counts them; it never names the relation. One count cannot tell
// `identical` from `behind` or `ahead` from `diverged`, so the question is
// asked both ways round and flow names the relation itself.
func (g *Gitea) Compare(ctx context.Context, base, head string) (core.Comparison, error) {
	if base == "" || head == "" {
		return core.Comparison{}, core.Errorf(core.ErrValidation, "compare %q with %q", base, head).
			WithWhy("both a base and a head are needed")
	}
	ahead, err := g.distance(ctx, base, head)
	if err != nil {
		return core.Comparison{}, err
	}
	behind, err := g.distance(ctx, head, base)
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

// distance returns how many commits head has that base does not.
func (g *Gitea) distance(ctx context.Context, base, head string) (int, error) {
	var payload gtCompare
	path := g.path("compare", base+"..."+head)
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		return 0, core.Explain(err, "compare %s with %s in %s", base, head, g.Slug())
	}
	return payload.TotalCommits, nil
}

// File returns the content of a file at a ref, so that doctor can read the
// workflow that is actually on the trunk.
func (g *Gitea) File(ctx context.Context, filePath, ref string) ([]byte, error) {
	if filePath == "" {
		return nil, core.Errorf(core.ErrValidation, "read a file from %s", g.Slug()).
			WithWhy("no path was given")
	}
	query := url.Values{}
	if ref != "" {
		query.Set("ref", ref)
	}

	var content []byte
	// Gitea serves the bytes themselves from an endpoint of their own,
	// rather than from a media type on the contents endpoint.
	req := core.Request{Path: g.path("raw", filePath), Query: query, Out: &content}
	if _, err := g.do(ctx, req); err != nil {
		return nil, core.Explain(err, "read %s from %s at %s", filePath, g.Slug(), refOrDefault(ref))
	}
	return content, nil
}

// Probe reports what the credential may do with this repository.
//
// Gitea answers the whole question in the repository payload: the permissions
// object is the credential's own, worked out from the account's role and the
// token's scopes together. There is no second limit to apply and no header to
// read, which is the difference from GitHub (spec 05 §3).
func (g *Gitea) Probe(ctx context.Context) (core.Permissions, error) {
	var payload gtRepo
	if _, err := g.do(ctx, core.Request{Path: g.path(), Out: &payload}); err != nil {
		return core.Permissions{}, core.Explain(err, "find out what the credential may do in %s", g.Slug())
	}
	return core.Permissions{
		Tokens:   payload.Permissions.tokens(),
		Evidence: []string{"repository permissions: " + payload.Permissions.String()},
	}, nil
}

// Protection returns the effective protection of a branch.
//
// Gitea answers it in two places and both are read. The branch itself carries
// what its protection comes to once every rule that covers it has been applied,
// and any credential that can see the branch can see that. The rule carries the
// settings the branch does not mention, and only an administrator may read it —
// so a credential without admin still learns what is required of the branch,
// which is the part the gate depends on (spec 10 §3).
func (g *Gitea) Protection(ctx context.Context, branch string) (core.Protection, error) {
	if branch == "" {
		return core.Protection{}, core.Errorf(core.ErrValidation, "read the protection of %s", g.Slug()).
			WithWhy("no branch was named")
	}
	payload, err := g.branch(ctx, branch)
	if err != nil {
		return core.Protection{}, err
	}
	if payload == nil {
		return core.Protection{Branch: branch}, nil
	}
	effective := payload.toProtection(branch)
	if !effective.Enabled {
		return effective, nil
	}
	rule, err := g.rule(ctx, payload.ruleName(branch))
	if err != nil {
		return core.Protection{}, err
	}
	if rule == nil {
		return effective, nil
	}
	return rule.refine(effective), nil
}

// branch reads a branch, or nil when the repository has no such branch. A
// branch that is not there is an answer rather than a failure: flow asks about
// the trunk a configuration names, and a name nobody pushed is a finding.
func (g *Gitea) branch(ctx context.Context, name string) (*gtBranch, error) {
	var payload gtBranch
	path := g.path("branches", name)
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return nil, nil
		}
		return nil, core.Explain(err, "read the branch %s of %s", name, g.Slug())
	}
	return &payload, nil
}

// rule reads one branch protection rule, or nil when there is none to read.
//
// A credential without admin is refused rather than told the rule is absent,
// and both answers mean the same thing here: flow cannot see the rule, so what
// the branch said about itself stands. Reporting the refusal instead would make
// every read of a protected branch fail for a credential that can do everything
// flow asks of it (spec 05 §3).
func (g *Gitea) rule(ctx context.Context, name string) (*gtProtection, error) {
	var payload gtProtection
	path := g.path("branch_protections", name)
	if _, err := g.do(ctx, core.Request{Path: path, Out: &payload}); err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrPermission) {
			return nil, nil
		}
		return nil, core.Explain(err, "read the protection rule %s of %s", name, g.Slug())
	}
	return &payload, nil
}

// SetProtection applies protection additively: it never removes a required
// check or a setting flow did not ask for (spec 06 §4).
func (g *Gitea) SetProtection(ctx context.Context, branch string, want core.Protection) error {
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
	merged := effective.Merge(want)
	body := protectionRequestGitea(merged)

	req := core.Request{
		Method: http.MethodPatch,
		Path:   g.path("branch_protections", branch),
		Body:   body,
		// Writing the same whole state twice leaves the same state, so a
		// retry cannot do anything the first attempt did not.
		Retry: core.RetryAlways,
	}
	// A rule may not exist yet, and Gitea creates one at a different
	// address from the one that edits it. The edit is tried first because
	// it is the ordinary case; a repository with no rule answers 404 and
	// gets the create.
	if _, err := g.do(ctx, req); err == nil {
		return nil
	} else if !errors.Is(err, core.ErrNotFound) {
		return core.Explain(err, "protect %s of %s", branch, g.Slug())
	}

	body.RuleName = branch
	body.BranchName = branch
	create := core.Request{
		Method: http.MethodPost,
		Path:   g.path("branch_protections"),
		Body:   body,
	}
	if _, err := g.do(ctx, create); err != nil {
		return core.Explain(err, "protect %s of %s", branch, g.Slug())
	}
	return nil
}

// SetMergeSettings sets the repository's merge strategies. They are one setting
// written whole (spec 06 §4): the strategies a team did not ask for are the
// ones it wants turned off, so the call carries all of them.
//
// What a squash commit says is not among them. Gitea composes the message
// itself and offers no setting for it, which is what `squash_text.set: false`
// in the declaration records — so the write is judged on the four strategies it
// was about rather than on the whole of [core.MergeSettings] (spec 10 §3).
func (g *Gitea) SetMergeSettings(ctx context.Context, s core.MergeSettings) error {
	if !s.AllowSquash && !s.AllowMerge && !s.AllowRebase {
		return core.Errorf(core.ErrValidation, "set the merge settings of %s", g.Slug()).
			WithWhy("no way of merging would be left").
			WithFix("allow at least one of squash, merge commit or rebase")
	}

	g.writing.Lock()
	defer g.writing.Unlock()

	want := mergeRequestGitea(s)
	var payload gtRepo
	req := core.Request{
		Method: http.MethodPatch,
		Path:   g.path(),
		Body:   want,
		Out:    &payload,
		Retry:  core.RetryAlways,
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "set the merge settings of %s", g.Slug())
	}
	if got := mergeRequestGitea(payload.toRepoInfo().Merge); got != want {
		return core.Errorf(core.ErrConflict, "set the merge settings of %s", g.Slug()).
			WithWhy("the forge kept %+v rather than %+v", got, want).
			WithFix("check whether the instance fixes the merge strategies for this repository")
	}
	return nil
}

// CommitStatuses returns the latest verdict from each context on a commit,
// newest first. Gitea's endpoint is a history — every status ever published,
// including the ones since replaced — and a history is not what a caller asking
// "what do the checks say" means (spec 09 §2).
func (g *Gitea) CommitStatuses(ctx context.Context, sha string) ([]core.CommitStatus, error) {
	if sha == "" {
		return nil, core.Errorf(core.ErrValidation, "read the statuses on a commit of %s", g.Slug()).
			WithWhy("no commit was named")
	}
	var (
		statuses []core.CommitStatus
		seen     = map[string]bool{}
	)
	err := paginate(ctx, g, g.statusRequest(sha), func(s gtStatus) bool {
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

// statusRequest asks for a commit's statuses newest first.
//
// Gitea numbers the statuses of one commit as it receives them, so ordering by
// that number is the only ordering that cannot tie. Ordering by the time they
// were written can: two runs of the same workflow second finish in the same
// second, and the walk would then keep whichever of them the database happened
// to answer with.
func (g *Gitea) statusRequest(sha string) core.Request {
	query := g.page()
	query.Set("sort", "highestindex")
	return core.Request{Path: g.path("commits", sha, "statuses"), Query: query}
}

// SetCommitStatus publishes a status on a commit, unless that context already
// says exactly this — Gitea keeps every status it is told, so publishing the
// same verdict on every run would fill a commit's history with repetitions of
// one sentence (spec 09 §2).
func (g *Gitea) SetCommitStatus(ctx context.Context, sha string, s core.CommitStatus) error {
	// Gitea trims the three strings of a status before storing them, so
	// flow trims them before comparing. Otherwise a description written
	// with a trailing space would differ from the one read back, every run,
	// for ever.
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
	case !slices.Contains(giteaStatusStates, s.State):
		return core.Errorf(core.ErrValidation, "publish the %s status on %s of %s", s.Context, sha, g.Slug()).
			WithWhy("%q is not a state a commit status can be in", s.State).
			WithFix("use one of %s", strings.Join(giteaStatusStates, ", "))
	}

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
		Body: gtStatusRequest{
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

// latestStatus returns the newest status from one context, or nil.
//
// It reads through the ordinary cache rather than around it. A cached answer
// is never believed without asking — [core.Client] sends the tag it holds and
// takes a 304 as permission to reuse the body — so the reading is of the
// commit as it stands either way, and a second way of saying "do not use the
// cache" would be a second thing to keep true.
func (g *Gitea) latestStatus(ctx context.Context, sha, want string) (*core.CommitStatus, error) {
	var found *core.CommitStatus
	err := paginate(ctx, g, g.statusRequest(sha), func(s gtStatus) bool {
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
// issue to Gitea's comment endpoints, which is why they are addressed as one.
func (g *Gitea) UpsertComment(ctx context.Context, number int, m core.Marker, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "comment on pull request #%d of %s", number, g.Slug()).
			WithWhy("the comment carries no marker, so flow could never find it again").
			WithFix("mark the comment with the kind of writing it is")
	}
	// Gitea renders comments as markdown and shows nobody an HTML comment,
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
			Method: http.MethodPatch,
			Path:   g.path("issues", "comments", existing.ID),
			Body:   gtCommentRequest{Body: marked},
		}
		if _, err := g.do(ctx, req); err != nil {
			return core.Explain(err, "update flow's comment on pull request #%d of %s", number, g.Slug())
		}
		return nil
	}

	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("issues", strconv.Itoa(number), "comments"),
		Body:   gtCommentRequest{Body: marked},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "comment on pull request #%d of %s", number, g.Slug())
	}
	return nil
}

// Comment returns flow's marked comment on a pull request, or nil when there is
// none.
func (g *Gitea) Comment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	return g.findComment(ctx, number, m)
}

// DeleteComment removes flow's marked comment. Deleting one that is not there
// succeeds: the caller asked for it to be gone, and it is.
func (g *Gitea) DeleteComment(ctx context.Context, number int, m core.Marker) error {
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
func (g *Gitea) findComment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	if m.IsZero() {
		return nil, core.Errorf(core.ErrValidation, "find flow's comment on pull request #%d of %s", number, g.Slug()).
			WithWhy("no marker was given, and flow does not read comments it did not write")
	}
	var found *core.Comment
	req := core.Request{
		Path:    g.path("issues", strconv.Itoa(number), "comments"),
		Query:   g.page(),
		NoCache: true,
	}
	err := paginate(ctx, g, req, func(c gtComment) bool {
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
func (g *Gitea) ProposeFiles(ctx context.Context, p core.FileProposal) (core.PullRequest, error) {
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

	repo, err := g.repository(ctx)
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
	return g.offer(ctx, p, branch, repo.DefaultBranch, repo.reference())
}

// ensureBranch creates the proposal's branch at the tip of base when it is not
// there. A branch that already exists is left where it is: it carries the
// previous proposal, and resetting it would throw away a commit somebody may
// have added to the pull request.
func (g *Gitea) ensureBranch(ctx context.Context, branch, base string) error {
	switch existing, err := g.branch(ctx, branch); {
	case err != nil:
		return err
	case existing != nil:
		return nil
	}
	req := core.Request{
		Method: http.MethodPost,
		Path:   g.path("branches"),
		Body:   gtBranchRequest{NewBranchName: branch, OldBranchName: base},
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.Explain(err, "branch %s off %s of %s", branch, base, g.Slug())
	}
	return nil
}

// writeFile commits one file to the proposal's branch, and writes nothing when
// the branch already holds exactly those bytes — a proposal re-run every hour
// should not be a commit every hour.
func (g *Gitea) writeFile(ctx context.Context, branch, message string, file core.ProposedFile) error {
	var current gtContents
	read := core.Request{
		Path:  g.path("contents", file.Path),
		Query: url.Values{"ref": []string{branch}},
		Out:   &current,
	}
	switch _, err := g.do(ctx, read); {
	case err == nil:
		if held, ok := current.decoded(); ok && bytes.Equal(held, file.Content) {
			return nil
		}
	case !errors.Is(err, core.ErrNotFound):
		return core.Explain(err, "read %s on %s of %s", file.Path, branch, g.Slug())
	default:
		current.SHA = ""
	}

	// Gitea creates and updates a file at different methods, and the update
	// quotes the blob it replaces so that two writers cannot silently
	// overwrite one another.
	write := core.Request{
		Method: http.MethodPost,
		Path:   g.path("contents", file.Path),
		Body: gtContentsRequest{
			Message: message,
			Content: base64.StdEncoding.EncodeToString(file.Content),
			Branch:  branch,
			SHA:     current.SHA,
		},
	}
	if current.SHA != "" {
		write.Method = http.MethodPut
	}
	if _, err := g.do(ctx, write); err != nil {
		return core.Explain(err, "write %s on %s of %s", file.Path, branch, g.Slug())
	}
	return nil
}

// offer opens the pull request for a proposal, or updates the one that is
// already open for its branch.
func (g *Gitea) offer(ctx context.Context, p core.FileProposal, branch, base, reference string) (core.PullRequest, error) {
	open, err := g.pullFromBranch(ctx, branch)
	if err != nil {
		return core.PullRequest{}, err
	}
	var payload gtPull
	req := core.Request{Method: http.MethodPost, Path: g.path("pulls"), Out: &payload}
	if open > 0 {
		req.Method = http.MethodPatch
		req.Path = g.path("pulls", strconv.Itoa(open))
		req.Body = gtPullUpdate{Title: p.Title, Body: p.Body}
	} else {
		req.Body = gtPullCreate{Title: p.Title, Body: p.Body, Head: branch, Base: base}
	}
	if _, err := g.do(ctx, req); err != nil {
		return core.PullRequest{}, core.Explain(err, "offer %s to %s of %s", branch, base, g.Slug())
	}
	return payload.toPullRequest(reference), nil
}

// pullFromBranch returns the number of the open pull request coming from a
// branch, or zero when there is none.
//
// Gitea's listing cannot be narrowed to a head branch, so the open pull
// requests are walked and matched. They are walked newest-updated first, which
// is the order a branch reused for a second proposal wants.
func (g *Gitea) pullFromBranch(ctx context.Context, branch string) (int, error) {
	query := g.page()
	query.Set("state", "open")
	query.Set("sort", "recentupdate")

	var found int
	err := paginate(ctx, g, core.Request{Path: g.path("pulls"), Query: query}, func(pull gtPull) bool {
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
func (g *Gitea) sender() *core.Client { return g.client }

// do sends one request with Gitea's media type.
func (g *Gitea) do(ctx context.Context, r core.Request) (*core.Response, error) {
	return g.client.Do(ctx, g.decorate(r))
}

// decorate returns the request with Gitea's media type on it, leaving an Accept
// the caller chose alone.
func (g *Gitea) decorate(r core.Request) core.Request {
	header := http.Header{}
	for name, values := range r.Header {
		header[name] = slices.Clone(values)
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", giteaAccept)
	}
	r.Header = header
	return r
}

// page is the query of a request that walks a collection.
func (g *Gitea) page() url.Values {
	return url.Values{"limit": []string{strconv.Itoa(giteaPageSize)}}
}

// path builds a path under this repository.
func (g *Gitea) path(segments ...string) string {
	parts := make([]string, 0, len(segments)+3)
	parts = append(parts, "repos", url.PathEscape(g.owner), url.PathEscape(g.repo))
	for _, segment := range segments {
		parts = append(parts, escapePath(segment))
	}
	return strings.Join(parts, "/")
}

// giteaListState maps a query's state onto the one Gitea's list endpoint
// understands. Gitea has no "merged" state: a merged pull request is a closed
// one that says it was merged, so the caller filters what comes back.
func giteaListState(state string) string {
	switch state {
	case core.PRStateOpen:
		return "open"
	case core.PRStateMerged, core.PRStateClosed:
		return "closed"
	default:
		return "all"
	}
}
