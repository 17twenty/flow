package forge

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// glUser is the `author`, `user` and `owner` object: a login and nothing else
// flow reads.
type glUser struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// login is the name to report. GitLab always has a username; the display name
// is a fallback for a payload that carried only that.
func (u glUser) login() string {
	if u.Username != "" {
		return u.Username
	}
	return u.Name
}

// glAccess is one half of a project's `permissions` object. GitLab answers
// both the member's own access and the access their group gives them, and
// either may be null.
type glAccess struct {
	AccessLevel int `json:"access_level"`
}

// glPermissions is the `permissions` object on a project.
type glPermissions struct {
	Project *glAccess `json:"project_access"`
	Group   *glAccess `json:"group_access"`
}

// level is the greater of the two accesses, because a member of a group with
// maintainer access is a maintainer of the project whatever their own
// membership says.
func (p glPermissions) level() int {
	var level int
	if p.Project != nil {
		level = p.Project.AccessLevel
	}
	if p.Group != nil {
		level = max(level, p.Group.AccessLevel)
	}
	return level
}

// tokens returns the permission words the capability declaration is written in
// (assets/capabilities/gitlab.yaml). GitLab's levels are cumulative — a
// maintainer may do everything a developer may — so the words are too, and a
// probe and a declaration that do not share a vocabulary can never agree
// (spec 05 §1).
func (p glPermissions) tokens() []string {
	level := p.level()
	var tokens []string
	for _, step := range gitLabAccessLevels {
		if level >= step.level {
			tokens = append(tokens, step.token)
		}
	}
	return tokens
}

// String names the access for the evidence line of a probe. The number is
// kept beside the word: GitLab's own API speaks in numbers, so an evidence
// line that drops it cannot be checked against what the provider answered.
func (p glPermissions) String() string {
	words := p.tokens()
	if len(words) == 0 {
		return "access_level " + strconv.Itoa(p.level()) + ": none"
	}
	return "access_level " + strconv.Itoa(p.level()) + ": " + strings.Join(words, ", ")
}

// glNamespace is the group or user a project belongs to. GitLab nests groups
// arbitrarily deep, so the owner is the whole path above the project.
type glNamespace struct {
	FullPath string `json:"full_path"`
}

// glProject is GET /projects/{id}.
type glProject struct {
	ID                int    `json:"id"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`

	Namespace     glNamespace `json:"namespace"`
	DefaultBranch string      `json:"default_branch"`
	WebURL        string      `json:"web_url"`
	Archived      bool        `json:"archived"`

	MergeMethod          string `json:"merge_method"`
	SquashOption         string `json:"squash_option"`
	SquashCommitTemplate string `json:"squash_commit_template"`
	RemoveSourceBranch   bool   `json:"remove_source_branch_after_merge"`

	PipelineMustSucceed bool `json:"only_allow_merge_if_pipeline_succeeds"`

	Permissions glPermissions `json:"permissions"`
}

// owner is the namespace the project sits in, which is everything above its
// own name.
func (p glProject) owner() string {
	if p.Namespace.FullPath != "" {
		return p.Namespace.FullPath
	}
	owner, _, found := strings.Cut(p.PathWithNamespace, "/")
	if !found {
		return ""
	}
	return owner
}

// name is the project's own path segment.
func (p glProject) name() string {
	if p.Path != "" {
		return p.Path
	}
	if index := strings.LastIndex(p.PathWithNamespace, "/"); index >= 0 {
		return p.PathWithNamespace[index+1:]
	}
	return p.PathWithNamespace
}

// toRepoInfo converts the payload.
func (p glProject) toRepoInfo() core.RepoInfo {
	title, message := squashTextGitLab(p.SquashCommitTemplate)
	return core.RepoInfo{
		Owner:         p.owner(),
		Name:          p.name(),
		DefaultBranch: p.DefaultBranch,
		URL:           p.WebURL,
		Archived:      p.Archived,
		Merge: core.MergeSettings{
			AllowSquash:         p.SquashOption != gitLabSquashNever,
			AllowMerge:          p.MergeMethod == gitLabMergeCommit && p.SquashOption != gitLabSquashAlways,
			AllowRebase:         p.MergeMethod != gitLabMergeCommit && p.SquashOption != gitLabSquashAlways,
			SquashTitle:         title,
			SquashMessage:       message,
			DeleteBranchOnMerge: p.RemoveSourceBranch,
			// GitLab has no project setting that forbids a merge
			// request from landing itself, so every project permits
			// it and anything else here would be an invention.
			AllowAutoMerge: true,
		},
	}
}

// glProjectRequest is PUT /projects/{id}, carrying only what flow manages.
// Every field is a pointer because GitLab reads an absent one as "leave it
// alone", and flow has no opinion about the rest of a project's settings.
type glProjectRequest struct {
	MergeMethod          *string `json:"merge_method,omitempty"`
	SquashOption         *string `json:"squash_option,omitempty"`
	SquashCommitTemplate *string `json:"squash_commit_template,omitempty"`
	RemoveSourceBranch   *bool   `json:"remove_source_branch_after_merge,omitempty"`
	PipelineMustSucceed  *bool   `json:"only_allow_merge_if_pipeline_succeeds,omitempty"`
}

// mergeRequestGitLab turns the wanted merge settings into GitLab's vocabulary.
//
// GitLab has no switch per strategy. It has one merge method, which decides
// what landing without a squash produces, and a squash option, which decides
// whether a squash is offered, defaulted or compulsory. "Squash and nothing
// else" is therefore `squash_option: always`, and a repository that allows a
// merge commit as well is `merge_method: merge` with the squash merely
// offered (spec 10 §4).
func mergeRequestGitLab(s core.MergeSettings) glProjectRequest {
	method := gitLabMergeFastForward
	if s.AllowMerge {
		method = gitLabMergeCommit
	} else if s.AllowRebase {
		method = gitLabMergeSemiLinear
	}
	squash := gitLabSquashDefaultOn
	switch {
	case !s.AllowSquash:
		squash = gitLabSquashNever
	case !s.AllowMerge && !s.AllowRebase:
		squash = gitLabSquashAlways
	}
	remove := s.DeleteBranchOnMerge
	return glProjectRequest{
		MergeMethod:        &method,
		SquashOption:       &squash,
		RemoveSourceBranch: &remove,
	}
}

// The squash commit templates flow reads and writes. GitLab composes a squash
// commit from one template holding its whole message, so where the header
// comes from and where the body comes from are two readings of one string
// (spec 10 §4).
const (
	// gitLabTemplateTitle takes the whole message from the merge request's
	// title, which is also what GitLab does when there is no template.
	gitLabTemplateTitle = "%{title}"
	// gitLabTemplateDescription is the title and then the description.
	gitLabTemplateDescription = "%{title}\n\n%{description}"
	// gitLabTemplateAllCommits is the title and then the branch's commit
	// messages.
	gitLabTemplateAllCommits = "%{title}\n\n%{all_commits}"
	// gitLabTemplateFirstCommit is the first commit on the branch, header
	// and body alike.
	gitLabTemplateFirstCommit = "%{first_multiline_commit}"
)

// squashTextGitLab reads a squash commit template as the two questions flow
// asks of it: where the header comes from, and where the body comes from.
//
// A template flow does not recognise is answered with two empty strings rather
// than a guess. Somebody wrote it on purpose, and reporting it as one of
// flow's four would make the plan propose replacing a template nobody asked
// about with one that says the same thing.
func squashTextGitLab(template string) (title, message string) {
	switch strings.ReplaceAll(template, "\r\n", "\n") {
	case "", gitLabTemplateTitle:
		return core.SquashTitlePR, core.SquashMessageBlank
	case gitLabTemplateDescription:
		return core.SquashTitlePR, core.SquashMessagePRBody
	case gitLabTemplateAllCommits:
		return core.SquashTitlePR, core.SquashMessageCommits
	case gitLabTemplateFirstCommit:
		return core.SquashTitleCommit, core.SquashMessageCommits
	default:
		return "", ""
	}
}

// squashTemplateGitLab is the template that says what the settings ask for,
// and false when GitLab has no way to say it. Writing nothing is better than
// writing a template that means something else: the whole plan is built on
// reading back what was written.
func squashTemplateGitLab(s core.MergeSettings) (string, bool) {
	switch {
	case s.SquashTitle == core.SquashTitleCommit:
		return gitLabTemplateFirstCommit, true
	case s.SquashTitle != core.SquashTitlePR:
		return "", false
	}
	switch s.SquashMessage {
	case core.SquashMessageBlank:
		return gitLabTemplateTitle, true
	case core.SquashMessagePRBody:
		return gitLabTemplateDescription, true
	case core.SquashMessageCommits:
		return gitLabTemplateAllCommits, true
	default:
		return "", false
	}
}

// glReferences is the `references` object: the same thing written three ways.
// `full` is the only one that is unambiguous from outside the project it
// belongs to, which is exactly flow's position (spec 04 §3).
type glReferences struct {
	Short    string `json:"short"`
	Relative string `json:"relative"`
	Full     string `json:"full"`
}

// glMergeRequest is GET /projects/{id}/merge_requests/{iid}.
type glMergeRequest struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Draft       bool   `json:"draft"`

	Author glUser   `json:"author"`
	Labels []string `json:"labels"`

	SHA             string `json:"sha"`
	MergeCommitSHA  string `json:"merge_commit_sha"`
	SquashCommitSHA string `json:"squash_commit_sha"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`

	ChangesCount string `json:"changes_count"`
	WebURL       string `json:"web_url"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	MergedAt  *time.Time `json:"merged_at"`
	ClosedAt  *time.Time `json:"closed_at"`

	// DetailedMergeStatus is the first merge check that is failing, and
	// MergeStatus is the coarse field it replaced in GitLab 15.6. Both are
	// read, because an installation older than that answers only the
	// second and a forge that has said something coarse has still said
	// more than nothing.
	DetailedMergeStatus string `json:"detailed_merge_status"`
	MergeStatus         string `json:"merge_status"`

	// AutoMergeEnabled is GitLab 17.11's name for a merge request already
	// set to land itself, and MergeWhenPipelineSucceeds the older one it
	// replaced. Both are read, where the arming sends only the older name,
	// because the two directions fail differently: a parameter an
	// installation does not know is a refusal flow sees, and a field it has
	// stopped answering is a false "nothing is armed" that nobody sees
	// (spec 10 §4).
	AutoMergeEnabled          bool `json:"auto_merge_enabled"`
	MergeWhenPipelineSucceeds bool `json:"merge_when_pipeline_succeeds"`
}

// The detailed_merge_status values flow has a use for.
//
// GitLab reports the *first* failing merge check, not all of them, so a merge
// request that conflicts and is also unapproved answers `not_approved`. That
// is not a problem for the question flow asks, because flow only asks it of
// work somebody has already approved: by then the approval check has passed
// and whatever is left is the real blocker.
const (
	// glMergeMergeable is every check passing.
	glMergeMergeable = "mergeable"
	// glMergeConflict is the source and target branches conflicting.
	glMergeConflict = "conflict"
	// glMergeBroken is GitLab unable to merge the source into the target,
	// which it reports for a conflict it could not describe.
	glMergeBroken = "broken_status"
	// glMergeNeedRebase is a merge method — fast-forward, semi-linear —
	// that requires the branch to be rebased whenever the target moves.
	glMergeNeedRebase = "need_rebase"
)

// The detailed_merge_status values that mean GitLab has not finished working
// it out. They are an answer about GitLab, not about the merge request.
var glMergeUnsettled = []string{"checking", "unchecked", "preparing", "approvals_syncing"}

// The merge_status values of installations too old for detailed_merge_status.
const (
	glMergeStatusCan    = "can_be_merged"
	glMergeStatusCannot = "cannot_be_merged"
)

// mergeabilityFromGitLab maps GitLab's answer onto flow's.
//
// Every value that is neither a conflict, a rebase nor a shrug reads as clean:
// an unapproved merge request, one whose pipeline is running and one with an
// open thread are all blocked on something, but on nothing that moving the
// branch would fix, which is the only distinction this is asked to make.
func mergeabilityFromGitLab(detailed, coarse string) string {
	switch {
	case detailed == "":
		return mergeabilityFromGitLabStatus(coarse)
	case slices.Contains(glMergeUnsettled, detailed):
		return core.MergeabilityUnknown
	case detailed == glMergeConflict, detailed == glMergeBroken:
		return core.MergeabilityConflicted
	case detailed == glMergeNeedRebase:
		return core.MergeabilityBehind
	default:
		return core.MergeabilityClean
	}
}

// mergeabilityFromGitLabStatus reads the deprecated field, which cannot tell a
// conflict from a branch that needs rebasing and so never answers `behind`.
func mergeabilityFromGitLabStatus(coarse string) string {
	switch coarse {
	case glMergeStatusCan:
		return core.MergeabilityClean
	case glMergeStatusCannot:
		return core.MergeabilityConflicted
	default:
		return core.MergeabilityUnknown
	}
}

// toPullRequest converts the payload.
//
// The header a squash lands is the title, unchanged: GitLab appends no
// reference to it, which is why spec 10 §2's question is put to the adapter
// rather than answered once in the engine.
func (m glMergeRequest) toPullRequest() core.PullRequest {
	pr := core.PullRequest{
		Number:       m.IID,
		Title:        m.Title,
		SquashHeader: m.Title,
		Body:         m.Description,
		Author:       m.Author.login(),
		State:        pullStateGitLab(m.State),
		Draft:        m.Draft,
		HeadSHA:      m.SHA,
		HeadRef:      m.SourceBranch,
		BaseRef:      m.TargetBranch,
		Labels:       slices.Clone(m.Labels),
		Files:        core.FileSummary{Changed: changedCountGitLab(m.ChangesCount)},
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		URL:          m.WebURL,
	}
	if m.MergedAt != nil {
		pr.MergedAt = *m.MergedAt
	}
	if m.ClosedAt != nil {
		pr.ClosedAt = *m.ClosedAt
	}
	// A merge request that was closed unmerged keeps neither commit, and a
	// squashed one keeps both: the squash is what landed on the trunk, so
	// it is what a deployment is proved to contain.
	if pr.Merged() {
		pr.MergeSHA = m.SquashCommitSHA
		if pr.MergeSHA == "" {
			pr.MergeSHA = m.MergeCommitSHA
		}
		if pr.ClosedAt.IsZero() {
			pr.ClosedAt = pr.MergedAt
		}
	}
	if pr.State == core.PRStateOpen {
		pr.Mergeability = mergeabilityFromGitLab(m.DetailedMergeStatus, m.MergeStatus)
		pr.AutoMerge = m.AutoMergeEnabled || m.MergeWhenPipelineSucceeds
	}
	return pr
}

// pullStateGitLab maps GitLab's four states onto flow's three. A locked merge
// request is one being merged, which is still open to everybody reading it.
func pullStateGitLab(state string) string {
	switch state {
	case "merged":
		return core.PRStateMerged
	case "closed":
		return core.PRStateClosed
	default:
		return core.PRStateOpen
	}
}

// changedCountGitLab reads `changes_count`, which GitLab sends as text and
// caps: a merge request touching more than a thousand files reads "1000+".
// The cap is kept as the number it qualifies, because a summary that says a
// thousand of something is not improved by saying none.
func changedCountGitLab(raw string) int {
	count, err := strconv.Atoi(strings.TrimSuffix(raw, "+"))
	if err != nil {
		return 0
	}
	return count
}

// glDiff is one file in GET /projects/{id}/merge_requests/{iid}/diffs.
type glDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	DeletedFile bool   `json:"deleted_file"`
}

// path is the file this diff is about: where it ended up, or where it was
// when it was deleted.
func (d glDiff) path() string {
	if d.DeletedFile || d.NewPath == "" {
		return d.OldPath
	}
	return d.NewPath
}

// glReviewer is one entry of GET
// /projects/{id}/merge_requests/{iid}/reviewers: somebody asked to review,
// and what they have said so far.
type glReviewer struct {
	User      glUser    `json:"user"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// toReview converts a reviewer's standing verdict, and reports false for one
// who has not given one. GitLab keeps a reviewer's current state rather than
// a history, so there is exactly one verdict per person to convert.
func (r glReviewer) toReview() (core.Review, bool) {
	var state string
	switch r.State {
	case "approved":
		state = core.ReviewApproved
	case "requested_changes":
		state = core.ReviewChangesRequested
	case "reviewed":
		state = core.ReviewCommented
	default:
		return core.Review{}, false
	}
	at := r.UpdatedAt
	if at.IsZero() {
		at = r.CreatedAt
	}
	return core.Review{Author: r.User.login(), State: state, SubmittedAt: at}, true
}

// glApprovals is GET /projects/{id}/merge_requests/{iid}/approvals.
type glApprovals struct {
	ApprovedBy []struct {
		User glUser `json:"user"`
	} `json:"approved_by"`
}

// glIssue is one entry of the closes_issues listing.
type glIssue struct {
	IID        int          `json:"iid"`
	References glReferences `json:"references"`
	WebURL     string       `json:"web_url"`
}

// key is how flow writes this ticket: as everybody writes it when it belongs
// to the project being spoken for, and qualified by its own project when it
// does not. "#7" there and "#7" here are different tickets (spec 04 §3).
func (i glIssue) key(slug string) string {
	full := i.References.Full
	if full == "" {
		return i.References.Short
	}
	if rest, found := strings.CutPrefix(full, slug+"#"); found {
		return "#" + rest
	}
	return full
}

// glCommit is one commit of the repository.
type glCommit struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Message       string    `json:"message"`
	AuthorName    string    `json:"author_name"`
	AuthorEmail   string    `json:"author_email"`
	AuthoredDate  time.Time `json:"authored_date"`
	CommittedDate time.Time `json:"committed_date"`
	WebURL        string    `json:"web_url"`
}

// toCommit converts the payload. GitLab sends the whole message and the first
// line of it separately; the message is the one flow reads, because a
// conventional commit is judged on its header and explained by its body.
func (c glCommit) toCommit() core.Commit {
	message := c.Message
	if message == "" {
		message = c.Title
	}
	return core.Commit{
		SHA:         c.ID,
		Message:     message,
		Author:      c.AuthorName,
		AuthoredAt:  c.AuthoredDate,
		CommittedAt: c.CommittedDate,
		URL:         c.WebURL,
	}
}

// glCompare is GET /projects/{id}/repository/compare.
type glCompare struct {
	Commits        []glCommit `json:"commits"`
	CompareSameRef bool       `json:"compare_same_ref"`
}

// glStatus is one entry of GET
// /projects/{id}/repository/commits/{sha}/statuses.
type glStatus struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Description string     `json:"description"`
	TargetURL   string     `json:"target_url"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// toCommitStatus converts the payload. A status's own progress is its history
// — created when it was first published, finished when it reached a verdict —
// so the later of the two is when it was last written.
func (s glStatus) toCommitStatus() core.CommitStatus {
	status := core.CommitStatus{
		ID:          strconv.FormatInt(s.ID, 10),
		Context:     s.Name,
		State:       statusStateGitLab(s.Status),
		Description: s.Description,
		TargetURL:   s.TargetURL,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.CreatedAt,
	}
	for _, at := range []*time.Time{s.StartedAt, s.FinishedAt} {
		if at != nil && at.After(status.UpdatedAt) {
			status.UpdatedAt = *at
		}
	}
	return status
}

// statusStateGitLab maps a GitLab job state onto one of flow's four. GitLab
// has eight, because a status of its own is a job in a pipeline and a job has
// a life before it has a verdict.
func statusStateGitLab(state string) string {
	switch state {
	case "success":
		return core.StatusSuccess
	case "failed":
		return core.StatusFailure
	case "canceled", "skipped":
		return core.StatusError
	default:
		return core.StatusPending
	}
}

// statusRequestStateGitLab maps one of flow's verdicts onto the state GitLab
// accepts. GitLab has no "error": a check that could not run has failed as
// far as the forge is concerned, and saying so is more honest than publishing
// a word the provider would refuse.
func statusRequestStateGitLab(state string) string {
	switch state {
	case core.StatusSuccess:
		return "success"
	case core.StatusFailure, core.StatusError:
		return "failed"
	default:
		return "pending"
	}
}

// glStatusRequest is POST /projects/{id}/statuses/{sha}.
type glStatusRequest struct {
	State       string `json:"state"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
	Ref         string `json:"ref,omitempty"`
	PipelineID  int    `json:"pipeline_id,omitempty"`
}

// glNote is one comment on a merge request.
type glNote struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	Author    glUser    `json:"author"`
	System    bool      `json:"system"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// toComment converts a note that carries the wanted marker, and reports false
// for every other note. A note GitLab wrote itself — "changed the description"
// — is system and is never flow's.
func (n glNote) toComment(want core.Marker, mrURL string) (core.Comment, bool) {
	if n.System || !core.HasMarker(n.Body, want) {
		return core.Comment{}, false
	}
	return core.Comment{
		ID:        strconv.FormatInt(n.ID, 10),
		Author:    n.Author.login(),
		Body:      n.Body,
		Marker:    want,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		URL:       mrURL + "#note_" + strconv.FormatInt(n.ID, 10),
	}, true
}

// glNoteRequest is the body of a note written or edited.
type glNoteRequest struct {
	Body string `json:"body"`
}

// glAccessLevel is one entry of a protected branch's push or merge access.
type glAccessLevel struct {
	AccessLevel int `json:"access_level"`
}

// glProtectedBranch is GET /projects/{id}/protected_branches/{name}.
type glProtectedBranch struct {
	ID                        int             `json:"id"`
	Name                      string          `json:"name"`
	AllowForcePush            bool            `json:"allow_force_push"`
	CodeOwnerApprovalRequired bool            `json:"code_owner_approval_required"`
	PushAccessLevels          []glAccessLevel `json:"push_access_levels"`
	MergeAccessLevels         []glAccessLevel `json:"merge_access_levels"`
}

// toProtection converts the payload.
//
// A protected branch cannot be deleted and cannot be pushed to by anybody
// without the access its push levels name, so `AllowDeletions` is false for
// every branch GitLab calls protected: the protection is what forbids it, and
// GitLab has no second switch to turn that part off.
func (b glProtectedBranch) toProtection(branch string) core.Protection {
	return core.Protection{
		Branch:         branch,
		Enabled:        true,
		AllowForcePush: b.AllowForcePush,
		AllowDeletions: false,
		// Nobody is exempt from a protected branch on GitLab: an owner who
		// may push is one the access levels name, so the rules apply to
		// everybody they do not.
		EnforceAdmins: true,
	}
}

// glProtectedBranchRequest is POST /projects/{id}/protected_branches.
type glProtectedBranchRequest struct {
	Name             string `json:"name"`
	PushAccessLevel  int    `json:"push_access_level"`
	MergeAccessLevel int    `json:"merge_access_level"`
	AllowForcePush   bool   `json:"allow_force_push"`
}

// glProtectedBranchPatch is PATCH /projects/{id}/protected_branches/{name},
// which is how an existing rule is narrowed without being replaced.
type glProtectedBranchPatch struct {
	AllowForcePush *bool `json:"allow_force_push,omitempty"`
}

// glExternalCheck is one entry of GET /projects/{id}/external_status_checks:
// a named verdict a merge request must carry before GitLab will merge it.
type glExternalCheck struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	ExternalURL       string `json:"external_url"`
	ProtectedBranches []struct {
		Name string `json:"name"`
	} `json:"protected_branches"`
}

// covers reports whether this check applies to a branch. A check with no
// protected branches at all applies to every merge request in the project,
// which is GitLab's own reading of an empty list.
func (c glExternalCheck) covers(branch string) bool {
	if len(c.ProtectedBranches) == 0 {
		return true
	}
	for _, protected := range c.ProtectedBranches {
		if protected.Name == branch {
			return true
		}
	}
	return false
}

// glExternalCheckRequest is POST /projects/{id}/external_status_checks.
type glExternalCheckRequest struct {
	Name               string `json:"name"`
	ExternalURL        string `json:"external_url"`
	ProtectedBranchIDs []int  `json:"protected_branch_ids,omitempty"`
}

// glApprovalRule is one entry of GET /projects/{id}/approval_rules.
type glApprovalRule struct {
	ID                            int64  `json:"id"`
	Name                          string `json:"name"`
	RuleType                      string `json:"rule_type"`
	ApprovalsRequired             int    `json:"approvals_required"`
	AppliesToAllProtectedBranches bool   `json:"applies_to_all_protected_branches"`
	ProtectedBranches             []struct {
		Name string `json:"name"`
	} `json:"protected_branches"`
}

// covers reports whether this rule asks for approvals on a branch.
func (r glApprovalRule) covers(branch string) bool {
	if r.AppliesToAllProtectedBranches || len(r.ProtectedBranches) == 0 {
		return true
	}
	for _, protected := range r.ProtectedBranches {
		if protected.Name == branch {
			return true
		}
	}
	return false
}

// glApprovalRuleRequest writes an approval rule.
type glApprovalRuleRequest struct {
	Name                          string `json:"name"`
	RuleType                      string `json:"rule_type,omitempty"`
	ApprovalsRequired             int    `json:"approvals_required"`
	AppliesToAllProtectedBranches bool   `json:"applies_to_all_protected_branches"`
}

// glBranchRequest is POST /projects/{id}/repository/branches.
type glBranchRequest struct {
	Branch string `json:"branch"`
	Ref    string `json:"ref"`
}

// glFileRequest writes one file on one branch.
type glFileRequest struct {
	Branch        string `json:"branch"`
	Content       string `json:"content"`
	Encoding      string `json:"encoding"`
	CommitMessage string `json:"commit_message"`
}

// glMergeNow is the body of PUT /projects/{id}/merge_requests/{iid}/merge.
// Flow only ever sends it to arm the merge request, never to merge it: the
// same endpoint does both, and which one it does turns on this one field.
//
// The field keeps its older spelling. 17.11 renamed it to `auto_merge` and
// deprecated this one, and a deprecated v4 parameter is removed when v4 is —
// so for as long as flow speaks `/api/v4` the old name reaches every
// installation and the new one reaches no installation the old one does not
// (spec 10 §4). Reading is the other way round, and [glMergeRequest] says
// why.
type glMergeNow struct {
	MergeWhenPipelineSucceeds bool   `json:"merge_when_pipeline_succeeds"`
	SHA                       string `json:"sha,omitempty"`
}

// glMergeRequestCreate is POST /projects/{id}/merge_requests.
type glMergeRequestCreate struct {
	SourceBranch       string `json:"source_branch"`
	TargetBranch       string `json:"target_branch"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	RemoveSourceBranch bool   `json:"remove_source_branch"`
}

// glMergeRequestUpdate is PUT /projects/{id}/merge_requests/{iid}.
type glMergeRequestUpdate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}
