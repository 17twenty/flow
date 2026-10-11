package core_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/core"
)

// shippedRegistry loads the declarations flow actually ships.
func shippedRegistry(t *testing.T) core.Registry {
	t.Helper()
	capabilities, err := assets.Capabilities()
	if err != nil {
		t.Fatalf("assets.Capabilities: %v", err)
	}
	registry, err := core.LoadRegistry(capabilities)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return registry
}

func TestShippedDeclarationsLoad(t *testing.T) {
	registry := shippedRegistry(t)

	want := []string{"deploys", "gitea", "github", "github-projects", "gitlab", "jira-cloud", "linear"}
	if got := registry.Adapters(); !slices.Equal(got, want) {
		t.Errorf("adapters = %v, want %v", got, want)
	}

	for _, adapter := range registry.Adapters() {
		set, err := registry.Of(adapter)
		if err != nil {
			t.Fatalf("Of(%s): %v", adapter, err)
		}
		if set.Researched().IsZero() {
			t.Errorf("%s: researched_at %q did not parse", adapter, set.ResearchedAt)
		}
		if set.Kind != core.KindDeploys && len(set.Sources) == 0 {
			t.Errorf("%s: declares no sources, so nothing can be re-checked", adapter)
		}

		// Every name is a registry name, and every registry name is declared:
		// the loader enforces both, so this proves the shipped files are the
		// ones being checked.
		if got, want := set.Names(), core.CapabilityNames(set.Kind); !slices.Equal(got, want) {
			t.Errorf("%s: declares %v, want the whole %s registry %v", adapter, got, set.Kind, want)
		}

		for _, name := range set.Names() {
			capability := set.Capability(name)
			if capability.Remediation == core.RemediationCompensate && capability.CompensatedBy == "" {
				t.Errorf("%s %s: compensated by nothing", adapter, name)
			}
			if capability.Remediation != core.RemediationCompensate && capability.CompensatedBy != "" {
				t.Errorf("%s %s: compensated_by %q without compensate", adapter, name, capability.CompensatedBy)
			}
		}
	}
}

func TestShippedKindsAreWhatTheAdapterIs(t *testing.T) {
	registry := shippedRegistry(t)

	for adapter, kind := range map[string]string{
		"github":          core.KindForge,
		"gitea":           core.KindForge,
		"gitlab":          core.KindForge,
		"github-projects": core.KindTracker,
		"jira-cloud":      core.KindTracker,
		"linear":          core.KindTracker,
		"deploys":         core.KindDeploys,
	} {
		set, err := registry.Of(adapter)
		if err != nil {
			t.Fatalf("Of(%s): %v", adapter, err)
		}
		if set.Kind != kind {
			t.Errorf("%s is a %s, want a %s", adapter, set.Kind, kind)
		}
	}

	if got := len(registry.OfKind(core.KindForge)); got != 3 {
		t.Errorf("OfKind(forge) returned %d declarations, want 3", got)
	}
	if got := registry.OfKind(core.KindTracker)[0].Adapter; got != "github-projects" {
		t.Errorf("OfKind(tracker) starts at %s, want github-projects sorted first", got)
	}
	if got := registry.OfKind("nonsense"); got != nil {
		t.Errorf("OfKind(nonsense) = %v, want nothing", got)
	}
}

// TestGitHubProjectsDeclarationIsTheResearch spot-checks the one declaration
// flow's own workflow depends on, so that a careless edit to the file is a
// test failure rather than a changed plan.
func TestGitHubProjectsDeclarationIsTheResearch(t *testing.T) {
	set, err := shippedRegistry(t).Of("github-projects")
	if err != nil {
		t.Fatalf("Of: %v", err)
	}

	if !set.Can(core.CapStatusOptions) || !set.CanVerify(core.CapStatusOptions) {
		t.Error("status options are both settable and verifiable on GitHub Projects")
	}
	if set.Can(core.CapWIPLimits) {
		t.Error("GitHub Projects column limits are advisory and per-view: flow cannot set them")
	}
	if got := set.Capability(core.CapWIPLimits); got.Remediation != core.RemediationCompensate || got.CompensatedBy != core.CompensatePolicyWIP {
		t.Errorf("wip_limits remediation = %q by %q, want compensate by %q", got.Remediation, got.CompensatedBy, core.CompensatePolicyWIP)
	}
	if got := set.Capability(core.CapNativeTransitionPRMerged); got.Set || !got.Verify {
		t.Error("the built-in 'pull request merged' automation cannot be configured by API, only verified")
	}
	if got := set.Capability(core.CapNativeTransitionPROpenedDraft); got.Verify {
		t.Error("GitHub Projects has no draft-opened automation, so there is nothing to verify")
	}
	if len(set.Capability(core.CapStatusOptions).Hazards) == 0 {
		t.Error("updateProjectV2Field can clear item values: the hazard must be declared")
	}
}

func TestCapabilityNamesRegistry(t *testing.T) {
	tracker := core.CapabilityNames(core.KindTracker)
	forge := core.CapabilityNames(core.KindForge)
	deploys := core.CapabilityNames(core.KindDeploys)

	for _, test := range []struct {
		kind  string
		names []string
	}{{core.KindTracker, tracker}, {core.KindForge, forge}, {core.KindDeploys, deploys}} {
		for _, name := range test.names {
			if !core.KnownCapability(test.kind, name) {
				t.Errorf("KnownCapability(%s, %s) = false for a registered name", test.kind, name)
			}
		}
		if len(slices.Compact(slices.Sorted(slices.Values(test.names)))) != len(test.names) {
			t.Errorf("%s names repeat: %v", test.kind, test.names)
		}
	}

	// The constants code uses must be the names the registry holds. A typo
	// here would otherwise read as an undeclared capability, which resolves
	// to unsupported — silently, and at the worst moment.
	for _, name := range []string{
		core.CapProject, core.CapStatusOptions, core.CapStatusCategories, core.CapWIPLimits,
		core.CapExpediteView, core.CapLabels, core.CapFollowUpTickets, core.CapTicketMove,
		core.CapNativeTransitionPROpenedDraft, core.CapNativeTransitionPRReadyForReview,
		core.CapNativeTransitionChangesRequested, core.CapNativeTransitionPRMerged,
		core.CapNativeTransitionIssueClosed, core.CapNativeTransitionDeployed,
	} {
		if !slices.Contains(tracker, name) {
			t.Errorf("tracker capability %q is used in code but not registered", name)
		}
	}
	for _, name := range []string{
		core.CapCommitStatusWrite, core.CapBranchProtection, core.CapRequiredStatusChecks,
		core.CapMergeSettings, core.CapFileProposal, core.CapPRComments,
		core.CapDeploymentsRead, core.CapLinkedIssuesRead,
	} {
		if !slices.Contains(forge, name) {
			t.Errorf("forge capability %q is used in code but not registered", name)
		}
	}
	if !slices.Contains(deploys, core.CapDeployRecords) {
		t.Errorf("deploys capability %q is used in code but not registered", core.CapDeployRecords)
	}

	if core.KnownCapability(core.KindTracker, core.CapBranchProtection) {
		t.Error("a forge capability is not a tracker capability")
	}
	if core.CapabilityNames("nonsense") != nil {
		t.Error("an unknown kind has no capabilities")
	}
}

func TestNativeTransitionCapability(t *testing.T) {
	// Spec 04 §4's vocabulary is larger than the capability registry: an
	// event no tracker automates has no name here, and spec 04 §5 leaves it
	// to owner: flow.
	for event, want := range map[string]bool{
		"pr_opened_draft":       true,
		"pr_ready_for_review":   true,
		"changes_requested":     true,
		"pr_merged":             true,
		"issue_closed":          true,
		"deployed":              true,
		"pr_opened":             false,
		"pr_converted_to_draft": false,
		"approved":              false,
		"pr_closed_unmerged":    false,
	} {
		name, ok := core.NativeTransitionCapability(event)
		if ok != want {
			t.Errorf("NativeTransitionCapability(%q) known = %v, want %v", event, ok, want)
		}
		if got := core.NativeTransitionPrefix + event; name != got {
			t.Errorf("NativeTransitionCapability(%q) = %q, want %q", event, name, got)
		}
	}
}

// validDeploys is the smallest complete declaration: one capability, one kind.
const validDeploys = `adapter: deploys
kind: deploys
version: 1
researched_at: 2026-10-09
sources: []
capabilities:
  deploy_records: { set: false, verify: true, remediation: follow_up }
`

func TestParseCapabilitySetRejects(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		src    string
		wantIn string
	}{
		{
			name:   "an unknown key",
			src:    validDeploys + "future: true\n",
			wantIn: "future",
		},
		{
			name:   "a capability outside the registry",
			src:    strings.Replace(validDeploys, "deploy_records:", "deploy_recrods:", 1),
			wantIn: `"deploy_recrods" is not a capability of a deploys`,
		},
		{
			name:   "a capability of another kind",
			src:    validDeploys + "  branch_protection: { set: true, verify: true, remediation: none }\n",
			wantIn: `"branch_protection" is not a capability of a deploys`,
		},
		{
			name:   "an undeclared capability",
			src:    "adapter: deploys\nkind: deploys\nversion: 1\nresearched_at: 2026-10-09\nsources: []\ncapabilities: {}\n",
			wantIn: `"deploy_records" is not declared`,
		},
		{
			name:   "an unknown kind",
			src:    strings.Replace(validDeploys, "kind: deploys", "kind: ticketing", 1),
			wantIn: `kind is "ticketing"`,
		},
		{
			name:   "another format version",
			src:    strings.Replace(validDeploys, "version: 1", "version: 2", 1),
			wantIn: "flow reads version 1",
		},
		{
			name:   "research with no date",
			src:    strings.Replace(validDeploys, "researched_at: 2026-10-09", `researched_at: "soon"`, 1),
			wantIn: "it must be a day",
		},
		{
			name:   "a file named after something else",
			file:   "deployments.yaml",
			src:    validDeploys,
			wantIn: "name the file after the adapter",
		},
		{
			name:   "an unknown remediation",
			src:    strings.Replace(validDeploys, "remediation: follow_up", "remediation: shrug", 1),
			wantIn: `deploy_records has remediation "shrug"`,
		},
		{
			name:   "compensating with nothing",
			src:    strings.Replace(validDeploys, "remediation: follow_up", "remediation: compensate", 1),
			wantIn: "does not say by what",
		},
		{
			name:   "compensated by a mechanism flow does not have",
			src:    strings.Replace(validDeploys, "remediation: follow_up", "remediation: compensate, compensated_by: policy.vibes", 1),
			wantIn: `compensated by "policy.vibes"`,
		},
		{
			name:   "compensated by something without compensating",
			src:    strings.Replace(validDeploys, "remediation: follow_up", "remediation: none, compensated_by: policy.wip", 1),
			wantIn: "only a compensated capability is compensated by something",
		},
		{
			name:   "an empty permission token",
			src:    strings.Replace(validDeploys, "remediation: follow_up", `remediation: follow_up, requires: ["", "x"]`, 1),
			wantIn: "requires an empty permission token",
		},
		{
			name:   "no adapter",
			src:    strings.Replace(validDeploys, "adapter: deploys", `adapter: ""`, 1),
			wantIn: "no adapter is named",
		},
		{
			name:   "not a mapping at all",
			src:    "- deploys\n",
			wantIn: "cannot unmarshal",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := test.file
			if file == "" {
				file = "deploys.yaml"
			}
			_, err := core.ParseCapabilitySet([]byte(test.src), file)
			if err == nil {
				t.Fatal("ParseCapabilitySet accepted it")
			}
			if !strings.Contains(err.Error(), test.wantIn) {
				t.Errorf("error %q does not explain %q", err, test.wantIn)
			}
			if got := core.ExitCode(err); got != core.ExitUsage {
				t.Errorf("exit code = %d, want %d", got, core.ExitUsage)
			}
			if !strings.Contains(err.Error(), file) {
				t.Errorf("error %q does not name the file at fault", err)
			}
		})
	}
}

func TestParseCapabilitySetReportsEveryProblemAtOnce(t *testing.T) {
	src := strings.Replace(validDeploys, "version: 1", "version: 9", 1)
	src = strings.Replace(src, "researched_at: 2026-10-09", `researched_at: "никогда"`, 1)

	_, err := core.ParseCapabilitySet([]byte(src), "deploys.yaml")
	if err == nil {
		t.Fatal("ParseCapabilitySet accepted it")
	}
	for _, want := range []string{"version is 9", "it must be a day"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q; fixing a file one problem per build is friction", err, want)
		}
	}
}

func TestParseCapabilitySetAccepts(t *testing.T) {
	set, err := core.ParseCapabilitySet([]byte(validDeploys), "deploys.yaml")
	if err != nil {
		t.Fatalf("ParseCapabilitySet: %v", err)
	}
	if set.Adapter != "deploys" || set.Kind != core.KindDeploys {
		t.Errorf("parsed %q/%q, want deploys/deploys", set.Adapter, set.Kind)
	}
	if want := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC); !set.Researched().Equal(want) {
		t.Errorf("Researched() = %v, want %v", set.Researched(), want)
	}
	if !set.CanVerify(core.CapDeployRecords) || set.Can(core.CapDeployRecords) {
		t.Error("deploy records are written by the deployer: readable, not settable")
	}

	// An undeclared capability is zero, not a guess: it cannot be set, cannot
	// be verified and has no remediation, which spec 05 §4 resolves to
	// unsupported.
	undeclared := set.Capability("something_nobody_researched")
	if !reflect.DeepEqual(undeclared, core.Capability{}) {
		t.Errorf("an undeclared capability came back as %+v, want the zero value", undeclared)
	}
}

func TestLoadRegistryFromAFileSystem(t *testing.T) {
	fsys := fstest.MapFS{
		"deploys.yaml": {Data: []byte(validDeploys)},
		"README.md":    {Data: []byte("not a declaration")},
	}

	registry, err := core.LoadRegistry(fsys)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if got := registry.Adapters(); !slices.Equal(got, []string{"deploys"}) {
		t.Errorf("adapters = %v, want just deploys: anything that is not a .yaml is not a declaration", got)
	}

	_, err = registry.Of("jira-server")
	if err == nil {
		t.Fatal("Of accepted an adapter flow does not ship")
	}
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("error %v is not a UserError, so it cannot explain itself", err)
	}
	if !strings.Contains(user.Fix, "deploys") {
		t.Errorf("fix %q does not name the adapters flow does have", user.Fix)
	}
	if got := core.ExitCode(err); got != core.ExitFail {
		t.Errorf("exit code = %d, want %d", got, core.ExitFail)
	}
}

func TestLoadRegistryRefusesBrokenDeclarations(t *testing.T) {
	t.Run("an empty directory", func(t *testing.T) {
		if _, err := core.LoadRegistry(fstest.MapFS{}); err == nil {
			t.Fatal("LoadRegistry accepted a build with no declarations at all")
		}
	})

	t.Run("one bad file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"deploys.yaml": {Data: []byte(validDeploys)},
			"github.yaml":  {Data: []byte("adapter: github\nkind: forge\nversion: 1\n")},
		}
		_, err := core.LoadRegistry(fsys)
		if err == nil {
			t.Fatal("LoadRegistry accepted an incomplete declaration")
		}
		if !strings.Contains(err.Error(), "github.yaml") {
			t.Errorf("error %q does not name the file at fault", err)
		}
	})
}
