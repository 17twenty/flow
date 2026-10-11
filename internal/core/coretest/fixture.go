package coretest

import (
	"time"

	"github.com/17twenty/flow/internal/core"
)

// ContractNow is the instant the contract fixtures are built around. Every
// time in them is relative to it, so a contract suite can reason about a
// window without a real clock.
var ContractNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// Commits in the contract forge's history, newest first. The order is the
// ancestry: SHAMerged contains SHAOld, and SHADeployed contains both.
const (
	// SHADeployed is the tip of the default branch, and what production runs.
	SHADeployed = "c3c3c3c3"
	// SHAMerged is the commit a merged pull request produced.
	SHAMerged = "c2c2c2c2"
	// SHAOld is the oldest commit in the fixture.
	SHAOld = "c1c1c1c1"
	// SHAHead is the tip of the open pull request's branch, which is not on
	// the default branch at all.
	SHAHead = "h1h1h1h1"
)

// ContractForge is the world every forge contract runs against: one
// repository, one open draft pull request, one merged pull request that was
// gated, reviewed and closes a ticket in another repository, and one closed
// long enough ago to fall outside a fortnight's window.
func ContractForge() ForgeFixture {
	return ForgeFixture{
		Kind:         "fake-forge",
		Capabilities: FullCapabilities("fake-forge", core.KindForge),
		Permissions: core.Permissions{
			Tokens:   []string{"repo", "statuses_write", "admin"},
			Evidence: []string{"X-OAuth-Scopes: repo, project"},
		},
		Repo: core.RepoInfo{
			Owner:         "acme",
			Name:          "widget",
			DefaultBranch: "main",
			URL:           "https://forge.test/acme/widget",
			Merge: core.MergeSettings{
				AllowSquash:   true,
				AllowMerge:    true,
				SquashTitle:   core.SquashTitleCommit,
				SquashMessage: core.SquashMessageCommits,
			},
		},
		PullRequests: []core.PullRequest{
			{
				Number:        1,
				Title:         "feat(ui): add the filter bar",
				Body:          "Closes #10",
				Author:        "dev",
				State:         core.PRStateOpen,
				Draft:         true,
				HeadSHA:       SHAHead,
				HeadRef:       "feature/filter-bar",
				BaseRef:       "main",
				Labels:        []string{"user-facing"},
				ClosingIssues: []string{"#10"},
				Files:         core.FileSummary{Changed: 2, Additions: 40, Deletions: 3, Paths: []string{"ui/filter.go", "ui/filter_test.go"}},
				CreatedAt:     ContractNow.Add(-2 * time.Hour),
				UpdatedAt:     ContractNow.Add(-1 * time.Hour),
				URL:           "https://forge.test/acme/widget/pull/1",
			},
			{
				Number:        2,
				Title:         "fix(core): stop losing the last event",
				Body:          "Closes #11",
				Author:        "flow-agent",
				State:         core.PRStateMerged,
				HeadSHA:       SHAMerged,
				HeadRef:       "fix/last-event",
				BaseRef:       "main",
				MergeSHA:      SHAMerged,
				Labels:        []string{"agent"},
				ClosingIssues: []string{"#11", "acme/other#7"},
				Reviews: []core.Review{
					{Author: "dev", State: core.ReviewChangesRequested, SubmittedAt: ContractNow.Add(-50 * time.Hour)},
					{Author: "dev", State: core.ReviewApproved, SubmittedAt: ContractNow.Add(-49 * time.Hour)},
				},
				Files:     core.FileSummary{Changed: 1, Additions: 4, Deletions: 1, Paths: []string{"core/events.go"}},
				CreatedAt: ContractNow.Add(-72 * time.Hour),
				UpdatedAt: ContractNow.Add(-48 * time.Hour),
				MergedAt:  ContractNow.Add(-48 * time.Hour),
				ClosedAt:  ContractNow.Add(-48 * time.Hour),
				URL:       "https://forge.test/acme/widget/pull/2",
			},
			{
				Number:    3,
				Title:     "chore(deps): bump everything",
				Author:    "dev",
				State:     core.PRStateClosed,
				HeadSHA:   "d4d4d4d4",
				HeadRef:   "chore/bump",
				BaseRef:   "main",
				CreatedAt: ContractNow.Add(-40 * 24 * time.Hour),
				UpdatedAt: ContractNow.Add(-39 * 24 * time.Hour),
				ClosedAt:  ContractNow.Add(-39 * 24 * time.Hour),
				URL:       "https://forge.test/acme/widget/pull/3",
			},
		},
		Commits: []core.Commit{
			{SHA: SHADeployed, Message: "chore(release): v1.2.0\n\nCut the release.", Author: "dev", CommittedAt: ContractNow.Add(-24 * time.Hour)},
			{SHA: SHAMerged, Message: "fix(core): stop losing the last event", Author: "flow-agent", CommittedAt: ContractNow.Add(-48 * time.Hour)},
			{SHA: SHAOld, Message: "feat(core): record events", Author: "dev", CommittedAt: ContractNow.Add(-96 * time.Hour)},
		},
		Statuses: map[string][]core.CommitStatus{
			SHAMerged: {
				{Context: "flow/policy", State: core.StatusSuccess, Description: "✓ all rules pass", CreatedAt: ContractNow.Add(-49 * time.Hour)},
				{Context: "ci/test", State: core.StatusSuccess, CreatedAt: ContractNow.Add(-49 * time.Hour)},
			},
			SHAHead: {
				{Context: "flow/policy", State: core.StatusPending, Description: "draft — not enforced", CreatedAt: ContractNow.Add(-1 * time.Hour)},
			},
		},
		Protections: map[string]core.Protection{
			"main": {
				Enabled:           true,
				RequiredChecks:    []string{"ci/test"},
				RequiredApprovals: 1,
			},
		},
		Files: map[string][]byte{
			".github/workflows/flow.yml": []byte("name: flow\njobs:\n  policy:\n    steps:\n      - run: flow check pr\n"),
		},
		Clock: NewClock(ContractNow),
	}
}

// Columns in the contract tracker's board, as the tracker identifies them.
const (
	// ColumnTodo is where new work waits.
	ColumnTodo core.ColumnID = "opt-todo"
	// ColumnReady is work that has passed the Definition of Ready.
	ColumnReady core.ColumnID = "opt-ready"
	// ColumnInProgress is work somebody is doing.
	ColumnInProgress core.ColumnID = "opt-in-progress"
	// ColumnInReview is work with a pull request open.
	ColumnInReview core.ColumnID = "opt-in-review"
	// ColumnDone is finished work.
	ColumnDone core.ColumnID = "opt-done"
)

// ContractTracker is the world every tracker contract runs against: a board
// with the lean columns, five tickets spread across them — one of them closed,
// one of them already carrying a follow-up marker — and two projects to
// discover between.
func ContractTracker() TrackerFixture {
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")
	return TrackerFixture{
		Kind:         "fake-tracker",
		Capabilities: FullCapabilities("fake-tracker", core.KindTracker),
		Permissions: core.Permissions{
			Tokens:   []string{"project_write", "repo"},
			Evidence: []string{"X-OAuth-Scopes: repo, project"},
		},
		Candidates: map[string][]core.Candidate{
			"tracker.project": {
				{ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1", Detail: "12 open items"},
				{ID: "PVT_2", Name: "Research", URL: "https://tracker.test/projects/2", Detail: "3 open items"},
			},
		},
		Observations: map[string]core.Observation{
			// Deliberately out of order: an adapter must canonicalise.
			"tracker.status_options": {
				Exists: true,
				Items: []core.Item{
					{ID: string(ColumnInReview), Name: "In review", Fields: map[string]string{"category": "in_progress"}},
					{ID: string(ColumnTodo), Name: "Todo", Fields: map[string]string{"category": "todo"}},
					{ID: string(ColumnDone), Name: "Done", Fields: map[string]string{"category": "done"}},
					{ID: string(ColumnInProgress), Name: "In progress", Fields: map[string]string{"category": "in_progress"}},
				},
			},
			"tracker.labels": {
				Exists: true,
				Items: []core.Item{
					{ID: "lbl-2", Name: "flow-followup", Fields: map[string]string{"color": "d73a4a"}},
					{ID: "lbl-1", Name: "blocked", Fields: map[string]string{"color": "b60205"}},
				},
			},
		},
		Columns: map[core.ColumnID]string{
			ColumnTodo:       "Todo",
			ColumnReady:      "Ready",
			ColumnInProgress: "In progress",
			ColumnInReview:   "In review",
			ColumnDone:       "Done",
		},
		Tickets: []core.Ticket{
			{
				Key: "#10", ID: "I_10", URL: "https://tracker.test/issues/10",
				Title:     "Add the filter bar",
				Body:      "## Acceptance criteria\n\n- the bar filters\n",
				Status:    core.Status{ID: string(ColumnInProgress), Name: "In progress"},
				ColumnID:  ColumnInProgress,
				Labels:    []string{"user-facing"},
				Assignees: []string{"dev"},
				Parent:    "#1",
				UpdatedAt: ContractNow.Add(-3 * time.Hour),
			},
			{
				Key: "#11", ID: "I_11", URL: "https://tracker.test/issues/11",
				Title:     "Last event is lost",
				Body:      "## Acceptance criteria\n\n- no event is lost\n",
				Status:    core.Status{ID: string(ColumnDone), Name: "Done"},
				ColumnID:  ColumnDone,
				Closed:    true,
				Parent:    "#1",
				UpdatedAt: ContractNow.Add(-48 * time.Hour),
				ClosedAt:  ContractNow.Add(-48 * time.Hour),
			},
			{
				Key: "#12", ID: "I_12", URL: "https://tracker.test/issues/12",
				Title:     "Rename the widget",
				Body:      "No criteria yet.",
				Status:    core.Status{ID: string(ColumnTodo), Name: "Todo"},
				ColumnID:  ColumnTodo,
				Blocked:   true,
				Labels:    []string{"blocked"},
				UpdatedAt: ContractNow.Add(-5 * 24 * time.Hour),
			},
			{
				Key: "#13", ID: "I_13", URL: "https://tracker.test/issues/13",
				Title:     "flow: set a WIP limit on In review (acme/widget)",
				Body:      core.MarkBody("The tracker cannot enforce it.", marker, true),
				Status:    core.Status{ID: string(ColumnTodo), Name: "Todo"},
				ColumnID:  ColumnTodo,
				Labels:    []string{"flow-followup"},
				UpdatedAt: ContractNow.Add(-24 * time.Hour),
			},
			{
				Key: "#14", ID: "I_14", URL: "https://tracker.test/issues/14",
				Title:     "Tidy the icons",
				Body:      "## Acceptance criteria\n\n- the icons are tidy\n",
				Status:    core.Status{ID: string(ColumnReady), Name: "Ready"},
				ColumnID:  ColumnReady,
				Size:      4 * time.Hour,
				UpdatedAt: ContractNow.Add(-2 * 24 * time.Hour),
			},
		},
		Clock: NewClock(ContractNow),
	}
}

// ContractFollowUpMarker is the marker the contract tracker's ticket #13
// already carries, so that a suite can prove filing it again finds it.
func ContractFollowUpMarker() core.Marker {
	return core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")
}

// ContractDeploys is the world every deployment contract runs against: three
// successful production deployments, one failure, and one to staging.
func ContractDeploys() DeployFixture {
	return DeployFixture{
		Kind:         core.SourceForgeDeployments,
		Capabilities: FullCapabilities(core.SourceForgeDeployments, core.KindDeploys),
		Deployments: []core.Deployment{
			{ID: "d1", Environment: "production", SHA: SHAOld, State: core.DeploySuccess, CreatedAt: ContractNow.Add(-96 * time.Hour), UpdatedAt: ContractNow.Add(-96 * time.Hour)},
			{ID: "d2", Environment: "production", SHA: SHAMerged, State: core.DeployFailure, CreatedAt: ContractNow.Add(-47 * time.Hour), UpdatedAt: ContractNow.Add(-47 * time.Hour)},
			{ID: "d3", Environment: "production", SHA: SHADeployed, State: core.DeploySuccess, CreatedAt: ContractNow.Add(-23 * time.Hour), UpdatedAt: ContractNow.Add(-23 * time.Hour)},
			{ID: "d4", Environment: "staging", SHA: SHADeployed, State: core.DeploySuccess, CreatedAt: ContractNow.Add(-22 * time.Hour), UpdatedAt: ContractNow.Add(-22 * time.Hour)},
		},
	}
}
