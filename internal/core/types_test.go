package core_test

import (
	"slices"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

func TestRepoInfoSlug(t *testing.T) {
	tests := []struct {
		repo core.RepoInfo
		want string
	}{
		{core.RepoInfo{Owner: "acme", Name: "widget"}, "acme/widget"},
		{core.RepoInfo{Owner: "acme/platform", Name: "widget"}, "acme/platform/widget"},
		{core.RepoInfo{Name: "widget"}, "widget"},
	}
	for _, test := range tests {
		if got := test.repo.Slug(); got != test.want {
			t.Errorf("Slug() = %q, want %q", got, test.want)
		}
	}
}

// Protection is only ever added to: flow is not the only thing with an
// opinion about the trunk (spec 06 §4).
func TestProtectionMergeIsAdditive(t *testing.T) {
	current := core.Protection{
		Branch:            "main",
		Enabled:           true,
		RequiredChecks:    []string{"ci/test", "security/scan"},
		RequiredApprovals: 2,
		EnforceAdmins:     true,
		AllowForcePush:    false,
	}
	want := core.Protection{
		Enabled:             true,
		RequiredChecks:      []string{"flow/policy", "ci/test"},
		RequiredApprovals:   1,
		DismissStaleReviews: true,
		AllowForcePush:      true,
	}

	merged := current.Merge(want)
	if !slices.Equal(merged.RequiredChecks, []string{"ci/test", "flow/policy", "security/scan"}) {
		t.Errorf("RequiredChecks = %v, want the union, sorted", merged.RequiredChecks)
	}
	if merged.RequiredApprovals != 2 {
		t.Errorf("RequiredApprovals = %d, want the stricter 2", merged.RequiredApprovals)
	}
	if !merged.DismissStaleReviews {
		t.Error("a setting flow asked for was dropped")
	}
	if !merged.EnforceAdmins {
		t.Error("a setting flow did not ask about was dropped")
	}
	if merged.AllowForcePush {
		t.Error("merging re-allowed force pushes; a permission is never widened")
	}
	if !merged.RequiresCheck("flow/policy") {
		t.Error("RequiresCheck missed a check that is required")
	}
}

func TestPullRequestApprovals(t *testing.T) {
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		reviews []core.Review
		want    []string
	}{
		{name: "nobody has looked"},
		{
			name:    "an approval counts",
			reviews: []core.Review{{Author: "dev", State: core.ReviewApproved, SubmittedAt: at}},
			want:    []string{"dev"},
		},
		{
			name: "a later refusal withdraws an earlier approval",
			reviews: []core.Review{
				{Author: "dev", State: core.ReviewApproved, SubmittedAt: at},
				{Author: "dev", State: core.ReviewChangesRequested, SubmittedAt: at.Add(time.Hour)},
			},
		},
		{
			name: "a comment in between changes nothing",
			reviews: []core.Review{
				{Author: "dev", State: core.ReviewApproved, SubmittedAt: at},
				{Author: "dev", State: core.ReviewCommented, SubmittedAt: at.Add(time.Hour)},
			},
			want: []string{"dev"},
		},
		{
			name: "a dismissed approval does not count",
			reviews: []core.Review{
				{Author: "dev", State: core.ReviewApproved, SubmittedAt: at},
				{Author: "dev", State: core.ReviewDismissed, SubmittedAt: at.Add(time.Hour)},
			},
		},
		{
			name: "two people, sorted",
			reviews: []core.Review{
				{Author: "zoe", State: core.ReviewApproved, SubmittedAt: at},
				{Author: "alex", State: core.ReviewApproved, SubmittedAt: at.Add(time.Hour)},
			},
			want: []string{"alex", "zoe"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pr := core.PullRequest{Reviews: test.reviews}
			if got := pr.Approvals(); !slices.Equal(got, test.want) {
				t.Errorf("Approvals() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCommitHeader(t *testing.T) {
	commit := core.Commit{Message: "fix(core): stop the leak\n\nIt leaked.\n"}
	if got := commit.Header(); got != "fix(core): stop the leak" {
		t.Errorf("Header() = %q", got)
	}
	if got := (core.Commit{Message: "one line"}).Header(); got != "one line" {
		t.Errorf("Header() of a one-line message = %q", got)
	}
}

func TestComparisonContains(t *testing.T) {
	for status, want := range map[string]bool{
		core.CompareIdentical: true,
		core.CompareAhead:     true,
		core.CompareBehind:    false,
		core.CompareDiverged:  false,
		"":                    false,
	} {
		if got := (core.Comparison{Status: status}).Contains(); got != want {
			t.Errorf("Comparison{%q}.Contains() = %v, want %v", status, got, want)
		}
	}
}

func TestFileProposalBranch(t *testing.T) {
	if got := (core.FileProposal{ID: "ci"}).Branch(); got != "flow/setup-ci" {
		t.Errorf("Branch() = %q, want flow/setup-ci", got)
	}
}

func TestTicketHasLabel(t *testing.T) {
	ticket := core.Ticket{Labels: []string{"User-Facing", "blocked"}}
	for _, label := range []string{"user-facing", "User-Facing", "BLOCKED"} {
		if !ticket.HasLabel(label) {
			t.Errorf("HasLabel(%q) = false, want true: trackers differ on case", label)
		}
	}
	for _, label := range []string{"ui", "user-facin", "user-facingg", ""} {
		if ticket.HasLabel(label) {
			t.Errorf("HasLabel(%q) = true", label)
		}
	}
}

func TestItem(t *testing.T) {
	item := core.Item{Name: "Todo"}.WithField("category", "todo")
	if item.Field("category") != "todo" {
		t.Errorf("WithField did not set the field: %+v", item)
	}
	if item.Field("colour") != "" {
		t.Errorf("Field of something unset = %q", item.Field("colour"))
	}

	// WithField copies: the desired state must not change under the planner.
	original := core.Item{Name: "Todo", Fields: map[string]string{"category": "todo"}}
	changed := original.WithField("category", "done")
	if original.Field("category") != "todo" {
		t.Error("WithField wrote through to the item it was called on")
	}
	if changed.Field("category") != "done" {
		t.Error("WithField did not set the field on the copy")
	}

	tests := []struct {
		name  string
		a, b  core.Item
		equal bool
	}{
		{name: "the same thing", a: original, b: original, equal: true},
		{name: "a different name", a: original, b: core.Item{Name: "Done", Fields: original.Fields}},
		{name: "a different field", a: original, b: changed},
		{
			name:  "a desired item has no id yet",
			a:     core.Item{Name: "Todo", Fields: original.Fields},
			b:     core.Item{ID: "opt-1", Name: "Todo", Fields: original.Fields},
			equal: true,
		},
		{
			name: "two different ids are two different things",
			a:    core.Item{ID: "opt-1", Name: "Todo"},
			b:    core.Item{ID: "opt-2", Name: "Todo"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.a.Equal(test.b); got != test.equal {
				t.Errorf("Equal = %v, want %v", got, test.equal)
			}
		})
	}
}

// An observed item is compared against a desired one by what the desired one
// asks for, not by everything the provider knows about it (spec 06 §2).
func TestItemMatches(t *testing.T) {
	observed := core.Item{
		ID:     "lbl-1",
		Name:   "blocked",
		Fields: map[string]string{"colour": "b60205", "description": "cannot move"},
	}
	tests := []struct {
		name  string
		want  core.Item
		match bool
	}{
		{name: "the name alone", want: core.Item{Name: "blocked"}, match: true},
		{name: "a field it has", want: core.Item{Name: "blocked", Fields: map[string]string{"colour": "b60205"}}, match: true},
		{name: "the id it has", want: core.Item{ID: "lbl-1", Name: "blocked"}, match: true},
		{name: "another name", want: core.Item{Name: "incident"}},
		{name: "another id", want: core.Item{ID: "lbl-2", Name: "blocked"}},
		{name: "a field it does not have", want: core.Item{Name: "blocked", Fields: map[string]string{"limit": "3"}}},
		{name: "a field with another value", want: core.Item{Name: "blocked", Fields: map[string]string{"colour": "ffffff"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := observed.Matches(test.want); got != test.match {
				t.Errorf("Matches = %v, want %v", got, test.match)
			}
		})
	}

	// Matching is not equality: the provider may know more than flow asked.
	if observed.Equal(core.Item{Name: "blocked"}) {
		t.Error("an item the provider knows more about is equal to the bare one")
	}
}

// Two observations of the same board must be the same observation, or a hash
// of one would mean nothing (spec 06 §2).
func TestObservationCanonical(t *testing.T) {
	scrambled := core.Observation{
		What:   "tracker.status_options",
		Exists: true,
		Items: []core.Item{
			{ID: "opt-3", Name: "Done"},
			{ID: "opt-1", Name: "Todo"},
			{ID: "opt-2", Name: "Done"},
		},
	}
	canonical := scrambled.Canonical()

	var order []string
	for _, item := range canonical.Items {
		order = append(order, item.Name+"/"+item.ID)
	}
	if !slices.Equal(order, []string{"Done/opt-2", "Done/opt-3", "Todo/opt-1"}) {
		t.Errorf("Canonical() ordered them %v", order)
	}
	if scrambled.Items[0].ID != "opt-3" {
		t.Error("Canonical() sorted the observation it was called on")
	}

	if _, ok := canonical.Find("Todo"); !ok {
		t.Error("Find missed an item that is there")
	}
	if _, ok := canonical.Find("In review"); ok {
		t.Error("Find invented an item")
	}
}

func TestDeploymentSucceeded(t *testing.T) {
	if !(core.Deployment{State: core.DeploySuccess}).Succeeded() {
		t.Error("a successful deployment did not succeed")
	}
	for _, state := range []string{core.DeployFailure, core.DeployPending, core.DeployInProgress, ""} {
		if (core.Deployment{State: state}).Succeeded() {
			t.Errorf("a %q deployment counted as success", state)
		}
	}
}

func TestCandidateRef(t *testing.T) {
	candidate := core.Candidate{
		What:   "tracker.project",
		ID:     "PVT_1",
		Name:   "Delivery",
		URL:    "https://tracker.test/projects/1",
		Detail: "12 open items",
	}
	want := core.Ref{What: "tracker.project", ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1"}
	if got := candidate.Ref(); got != want {
		t.Errorf("Ref() = %+v, want %+v", got, want)
	}
}
