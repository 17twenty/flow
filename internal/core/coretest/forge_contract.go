package coretest

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// NewForge builds the forge under test from a fixture. A real adapter's test
// stands an httptest server up in front of the same world; the in-memory fake
// is built straight from it.
type NewForge func(t *testing.T, fx ForgeFixture) core.Forge

// ForgeContract is every behaviour the engine is entitled to assume of a
// forge. An adapter that passes it can be swapped for any other, which is the
// whole point of there being an interface (spec 14).
func ForgeContract(t *testing.T, newForge NewForge) {
	t.Helper()

	t.Run("identity", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		if forge.Kind() == "" {
			t.Error("the forge has no kind, so no capability declaration can be found for it")
		}
		capabilities := forge.Capabilities()
		if capabilities.Kind != core.KindForge {
			t.Errorf("Capabilities().Kind = %q, want %q", capabilities.Kind, core.KindForge)
		}
		for _, name := range core.CapabilityNames(core.KindForge) {
			if _, ok := capabilities.Capabilities[name]; !ok {
				t.Errorf("the declaration says nothing about %q; silence is not a finding", name)
			}
		}
	})

	t.Run("probe reports what the credential may do", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		permissions, err := forge.Probe(t.Context())
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if len(permissions.Tokens) == 0 {
			t.Error("Probe reported no permissions for a credential the fixture grants everything to")
		}
		if len(permissions.Evidence) == 0 {
			t.Error("Probe reported no evidence, so a refusal could not say what flow saw")
		}
	})

	t.Run("repo", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		repo, err := forge.Repo(t.Context())
		if err != nil {
			t.Fatalf("Repo: %v", err)
		}
		if repo.Slug() != "acme/widget" {
			t.Errorf("Repo().Slug() = %q, want acme/widget", repo.Slug())
		}
		if repo.DefaultBranch != "main" {
			t.Errorf("DefaultBranch = %q, want main", repo.DefaultBranch)
		}
	})

	t.Run("one pull request, with everything the gate reads", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		pr, err := forge.PullRequest(t.Context(), 2)
		if err != nil {
			t.Fatalf("PullRequest(2): %v", err)
		}
		if !pr.Merged() {
			t.Errorf("pull request 2 is %q, want merged", pr.State)
		}
		if pr.HeadSHA == "" {
			t.Error("the pull request has no head SHA, so no status could be published on it")
		}
		if got := pr.Approvals(); !slices.Equal(got, []string{"dev"}) {
			t.Errorf("Approvals() = %v, want [dev]: a later approval replaces an earlier refusal", got)
		}
		if !slices.Contains(pr.ClosingIssues, "#11") {
			t.Errorf("ClosingIssues = %v, want it to carry #11", pr.ClosingIssues)
		}
		if pr.Files.Changed == 0 {
			t.Error("the pull request summarises no files")
		}
	})

	t.Run("a pull request that is not there", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		if _, err := forge.PullRequest(t.Context(), 4040); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("PullRequest(4040) = %v, want it to wrap ErrNotFound", err)
		}
	})

	t.Run("pull requests in a window, newest first", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		since := ContractNow.Add(-14 * 24 * time.Hour)
		prs, err := forge.PullRequests(t.Context(), core.PRQuery{Base: "main", UpdatedSince: since})
		if err != nil {
			t.Fatalf("PullRequests: %v", err)
		}
		if len(prs) != 2 {
			t.Fatalf("got %d pull requests in the window, want 2 (the third closed 39 days ago)", len(prs))
		}
		if !slices.IsSortedFunc(prs, func(a, b core.PullRequest) int { return b.UpdatedAt.Compare(a.UpdatedAt) }) {
			t.Error("pull requests came back oldest first; the audit stops at the window's edge and needs newest first")
		}
		merged, err := forge.PullRequests(t.Context(), core.PRQuery{State: core.PRStateMerged, UpdatedSince: since})
		if err != nil {
			t.Fatalf("PullRequests(merged): %v", err)
		}
		for _, pr := range merged {
			if !pr.Merged() {
				t.Errorf("a query for merged pull requests returned #%d, which is %q", pr.Number, pr.State)
			}
		}
		one, err := forge.PullRequests(t.Context(), core.PRQuery{Limit: 1})
		if err != nil {
			t.Fatalf("PullRequests(limit 1): %v", err)
		}
		if len(one) != 1 {
			t.Errorf("a limit of 1 returned %d pull requests", len(one))
		}
	})

	t.Run("commits on the trunk, newest first", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		commits, err := forge.Commits(t.Context(), core.CommitQuery{Ref: "main", Limit: 2})
		if err != nil {
			t.Fatalf("Commits: %v", err)
		}
		if len(commits) != 2 {
			t.Fatalf("got %d commits, want 2", len(commits))
		}
		if commits[0].SHA != SHADeployed {
			t.Errorf("first commit is %q, want the tip %q", commits[0].SHA, SHADeployed)
		}
		if commits[0].Header() != "chore(release): v1.2.0" {
			t.Errorf("Header() = %q, want the first line only", commits[0].Header())
		}
	})

	t.Run("ancestry proves a merge reached an environment", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		ahead, err := forge.Compare(t.Context(), SHAMerged, SHADeployed)
		if err != nil {
			t.Fatalf("Compare: %v", err)
		}
		if !ahead.Contains() {
			t.Errorf("Compare(merged, deployed) = %q, want the deployed commit to contain the merged one", ahead.Status)
		}
		same, err := forge.Compare(t.Context(), SHADeployed, SHADeployed)
		if err != nil {
			t.Fatalf("Compare(same, same): %v", err)
		}
		if !same.Contains() || same.Status != core.CompareIdentical {
			t.Errorf("Compare of a commit with itself = %q, want identical", same.Status)
		}
		behind, err := forge.Compare(t.Context(), SHADeployed, SHAMerged)
		if err != nil {
			t.Fatalf("Compare(deployed, merged): %v", err)
		}
		if behind.Contains() {
			t.Error("Compare said an older commit contains a newer one")
		}
	})

	t.Run("files on the trunk", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		content, err := forge.File(t.Context(), ".github/workflows/flow.yml", "main")
		if err != nil {
			t.Fatalf("File: %v", err)
		}
		if !strings.Contains(string(content), "flow check pr") {
			t.Errorf("File returned %q, which is not the workflow the fixture holds", content)
		}
		if _, err := forge.File(t.Context(), "nothing/here.yml", "main"); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("File of a missing path = %v, want it to wrap ErrNotFound", err)
		}
	})

	t.Run("protection is read effectively and written additively", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		before, err := forge.Protection(t.Context(), "main")
		if err != nil {
			t.Fatalf("Protection: %v", err)
		}
		if !before.Enabled || !before.RequiresCheck("ci/test") {
			t.Fatalf("Protection(main) = %+v, want the fixture's protection", before)
		}

		if err := forge.SetProtection(t.Context(), "main", core.Protection{
			Enabled:        true,
			RequiredChecks: []string{"flow/policy"},
		}); err != nil {
			t.Fatalf("SetProtection: %v", err)
		}
		after, err := forge.Protection(t.Context(), "main")
		if err != nil {
			t.Fatalf("Protection after SetProtection: %v", err)
		}
		if !after.RequiresCheck("flow/policy") {
			t.Error("SetProtection did not add the required check")
		}
		if !after.RequiresCheck("ci/test") {
			t.Error("SetProtection removed a check flow did not ask about; protection changes are additive")
		}
		if after.RequiredApprovals < before.RequiredApprovals {
			t.Errorf("SetProtection lowered the required approvals from %d to %d", before.RequiredApprovals, after.RequiredApprovals)
		}

		unprotected, err := forge.Protection(t.Context(), "some-branch")
		if err != nil {
			t.Fatalf("Protection of an unprotected branch: %v", err)
		}
		if unprotected.Enabled {
			t.Error("an unprotected branch came back protected")
		}
	})

	t.Run("merge settings", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		want := core.MergeSettings{
			AllowSquash:         true,
			SquashTitle:         core.SquashTitlePR,
			SquashMessage:       core.SquashMessagePRBody,
			DeleteBranchOnMerge: true,
		}
		if err := forge.SetMergeSettings(t.Context(), want); err != nil {
			t.Fatalf("SetMergeSettings: %v", err)
		}
		repo, err := forge.Repo(t.Context())
		if err != nil {
			t.Fatalf("Repo: %v", err)
		}
		if repo.Merge != want {
			t.Errorf("Repo().Merge = %+v, want %+v", repo.Merge, want)
		}
	})

	t.Run("a commit status replaces the last verdict from the same context", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		first := core.CommitStatus{Context: "flow/policy", State: core.StatusFailure, Description: "✗ 1 error"}
		if err := forge.SetCommitStatus(t.Context(), SHAHead, first); err != nil {
			t.Fatalf("SetCommitStatus: %v", err)
		}
		second := core.CommitStatus{Context: "flow/policy", State: core.StatusSuccess, Description: "✓ all rules pass"}
		if err := forge.SetCommitStatus(t.Context(), SHAHead, second); err != nil {
			t.Fatalf("SetCommitStatus again: %v", err)
		}

		statuses, err := forge.CommitStatuses(t.Context(), SHAHead)
		if err != nil {
			t.Fatalf("CommitStatuses: %v", err)
		}
		var ours []core.CommitStatus
		for _, status := range statuses {
			if status.Context == "flow/policy" {
				ours = append(ours, status)
			}
		}
		if len(ours) != 1 {
			t.Fatalf("got %d flow/policy statuses, want the latest verdict only", len(ours))
		}
		if ours[0].State != core.StatusSuccess {
			t.Errorf("the surviving status is %q, want the second verdict", ours[0].State)
		}
	})

	t.Run("a comment is edited in place, found by its marker, and deleted once", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		marker := core.NewMarker(core.MarkerPolicy, "pr", "1")

		if err := forge.UpsertComment(t.Context(), 1, marker, "two rules failed"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
		if err := forge.UpsertComment(t.Context(), 1, marker, "one rule failed"); err != nil {
			t.Fatalf("UpsertComment again: %v", err)
		}

		comment, err := forge.Comment(t.Context(), 1, marker)
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if comment == nil {
			t.Fatal("Comment found nothing flow had just written")
		}
		if !strings.Contains(comment.Body, "one rule failed") {
			t.Errorf("the comment says %q, want the second body", comment.Body)
		}
		if !core.HasMarker(comment.Body, marker) {
			t.Errorf("the comment carries no marker: %q", comment.Body)
		}

		if err := forge.DeleteComment(t.Context(), 1, marker); err != nil {
			t.Fatalf("DeleteComment: %v", err)
		}
		gone, err := forge.Comment(t.Context(), 1, marker)
		if err != nil {
			t.Fatalf("Comment after delete: %v", err)
		}
		if gone != nil {
			t.Error("the comment survived being deleted")
		}
		if err := forge.DeleteComment(t.Context(), 1, marker); err != nil {
			t.Errorf("deleting a comment that is not there failed: %v", err)
		}
	})

	t.Run("files are proposed on their own branch and updated in place", func(t *testing.T) {
		forge := newForge(t, ContractForge())
		proposal := core.FileProposal{
			ID:    "ci",
			Title: "chore(flow): add the policy workflow",
			Body:  "flow proposes this.",
			Files: []core.ProposedFile{{Path: ".github/workflows/flow.yml", Content: []byte("name: flow\n")}},
		}
		first, err := forge.ProposeFiles(t.Context(), proposal)
		if err != nil {
			t.Fatalf("ProposeFiles: %v", err)
		}
		if first.BaseRef != "main" {
			t.Errorf("the proposal targets %q, want the default branch", first.BaseRef)
		}
		if first.HeadRef != proposal.Branch() {
			t.Errorf("the proposal comes from %q, want %q — flow never writes the trunk", first.HeadRef, proposal.Branch())
		}
		if first.State != core.PRStateOpen {
			t.Errorf("the proposal is %q, want open", first.State)
		}

		proposal.Body = "flow proposes this, again."
		second, err := forge.ProposeFiles(t.Context(), proposal)
		if err != nil {
			t.Fatalf("ProposeFiles again: %v", err)
		}
		if second.Number != first.Number {
			t.Errorf("proposing the same files again opened #%d as well as #%d", second.Number, first.Number)
		}
		if second.Body != proposal.Body {
			t.Errorf("the second proposal did not update the body: %q", second.Body)
		}
	})
}
