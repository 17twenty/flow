//go:build flowtest

package main

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// Fake providers for the CLI tests (spec 14). A build carrying the flowtest
// tag may be told to act against the in-memory fakes instead of a network,
// which is what lets `flow plan` and `flow apply` be exercised whole — kong
// parsing, configuration, lock, rendering and exit code — with no credential
// and nothing to reach.
//
// It is one switch, read once per run and only here: the shipped binary does
// not contain this file, so FLOW_TEST_FAKES means nothing to it and cannot be
// made to mean anything (spec 14's "test asserts release builds can't").

// fakeEnv is the variable that selects the fakes. Only the exact value "1"
// counts: a variable somebody exported to an empty string, or to the name of
// a fixture flow does not have, must not quietly produce a fabricated world.
const fakeEnv = "FLOW_TEST_FAKES"

func init() { newAdapters = fakeAdapters }

// fakeAdapters hands a command the fake providers for its repository, or the
// real ones when nobody asked for fakes.
func fakeAdapters(a *Context, s *session) (engine.Adapters, error) {
	if os.Getenv(fakeEnv) != "1" {
		return liveAdapters(a, s)
	}
	world, err := fakeWorldFor(s)
	if err != nil {
		return engine.Adapters{}, err
	}
	return engine.Adapters{Forge: world.forge, Tracker: world.tracker, Deploys: world.deploys}, nil
}

// fakeWorld is one repository's providers.
type fakeWorld struct {
	forge   *coretest.FakeForge
	tracker *coretest.FakeTracker
	deploys *coretest.FakeDeploys
}

// fakeWorlds remembers a world per repository, so that a second command in
// the same process sees what the first one wrote.
//
// Idempotence is the property these fakes exist to demonstrate (scenario
// A04), and an apply whose providers are rebuilt from a fixture each time
// would converge for ever and prove nothing. The key is the working tree's
// path, which is unique per test, so two tests cannot see each other's board.
var fakeWorlds sync.Map

// fakeSeeds are the pull requests and tickets a test wants its world to hold
// before the first command looks at it, keyed by working tree.
//
// A gate has nothing to judge in a repository with no pull requests, and the
// world is built lazily inside the first command, so a test cannot reach in
// and populate it beforehand. It leaves the population here instead and the
// world applies it to itself as it is born.
var fakeSeeds sync.Map

// seedFakeWorld registers what a test wants its repository's providers to
// hold. It must be called before the first command runs against dir.
func seedFakeWorld(dir string, seed func(*fakeWorld)) { fakeSeeds.Store(dir, seed) }

// fakeWorldFor returns the world behind one working tree, building it the
// first time it is asked for.
func fakeWorldFor(s *session) (*fakeWorld, error) {
	if existing, ok := fakeWorlds.Load(s.Root); ok {
		return existing.(*fakeWorld), nil
	}
	world, err := newFakeWorld(s)
	if err != nil {
		return nil, err
	}
	if seed, ok := fakeSeeds.Load(s.Root); ok {
		seed.(func(*fakeWorld))(world)
	}
	actual, _ := fakeWorlds.LoadOrStore(s.Root, world)
	return actual.(*fakeWorld), nil
}

// newFakeWorld builds a repository nobody has configured yet: a trunk with no
// protection, a forge that allows every merge strategy, and a board holding
// the three statuses GitHub creates a project with (scenario A03).
//
// The capability declarations are the shipped ones, read through the
// session's registry, so a fake answers for exactly what the real adapter of
// that kind declares. The permissions are an administrator's: what a plan
// does when a token is too small is the follow-up path, which the engine's
// own tests hold, and a CLI test that could not reach a native outcome could
// not show an apply converging.
func newFakeWorld(s *session) (*fakeWorld, error) {
	owner, name, err := splitRepo(s.Config.Forge.Repo)
	if err != nil {
		return nil, err
	}
	forgeCaps, err := s.Registry.Of(s.Config.Forge.Type)
	if err != nil {
		return nil, err
	}
	trackerCaps, err := s.Registry.Of(s.Config.Tracker.Type)
	if err != nil {
		return nil, err
	}

	world := &fakeWorld{
		forge: coretest.NewFakeForge(coretest.ForgeFixture{
			Kind:         s.Config.Forge.Type,
			Capabilities: forgeCaps,
			Permissions: core.Permissions{
				Tokens:   []string{"admin", "statuses_write", "contents_write", "pulls_write"},
				Evidence: []string{"repository permission: admin"},
			},
			Repo: core.RepoInfo{
				Owner:         owner,
				Name:          name,
				DefaultBranch: "main",
				URL:           "https://github.test/" + s.Config.Forge.Repo,
				Merge: core.MergeSettings{
					AllowSquash:   true,
					AllowMerge:    true,
					AllowRebase:   true,
					SquashTitle:   core.SquashTitleCommit,
					SquashMessage: core.SquashMessageCommits,
				},
			},
			Files: trunkFiles(s),
			Clock: s.Clock,
		}),
		tracker: coretest.NewFakeTracker(coretest.TrackerFixture{
			Kind:         s.Config.Tracker.Type,
			Capabilities: trackerCaps,
			Permissions: core.Permissions{
				Tokens:   []string{"project_read", "project_write", "issues_write"},
				Evidence: []string{"X-OAuth-Scopes: repo, project"},
			},
			Observations: map[string]core.Observation{
				core.ItemTrackerProject: {Exists: true},
				core.ItemTrackerStatusOptions: {Exists: true, Items: []core.Item{
					{ID: "opt-todo", Name: "Todo"},
					{ID: "opt-in-progress", Name: "In Progress"},
					{ID: "opt-done", Name: "Done"},
				}},
				core.ItemTrackerLabels: {Exists: true},
			},
			Clock: s.Clock,
		}),
	}
	// One declaration covers every deployment source, keyed by the kind
	// rather than by the source, exactly as [liveAdapters] reads it: a fake
	// that looked the source up would ask the registry for a name it has
	// never held and fail where the real build succeeds.
	switch s.Config.Deploys.Source {
	case "", config.DeploySourceNone:
	default:
		deployCaps, err := s.Registry.Of(core.KindDeploys)
		if err != nil {
			return nil, err
		}
		world.deploys = coretest.NewFakeDeploys(coretest.DeployFixture{
			Kind:         s.Config.Deploys.Source,
			Capabilities: deployCaps,
		})
	}
	return world, nil
}

// trunkFiles is what the fake forge's default branch holds: the files the
// working tree holds, of the ones flow has an opinion about.
//
// A fake whose trunk were always empty would make every run propose the
// files again, and an apply that can never converge is not one scenario A04
// can be written against. The repository and its remote being in step is the
// ordinary case — somebody ran `flow init` and pushed what it wrote — so that
// is what the fake models, and a test that wants the proposal path deletes
// the file from the working tree to get it.
func trunkFiles(s *session) map[string][]byte {
	wanted, ok := s.Desired.Find(core.ItemForgeFiles)
	if !ok {
		return nil
	}
	files := make(map[string][]byte)
	for _, file := range wanted.Files {
		content, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(file.Path)))
		if err != nil {
			continue
		}
		files[file.Path] = content
	}
	return files
}
