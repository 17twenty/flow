package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// presetsFS is the shipped presets directory, read from the tree so that this
// package does not import the assets package it is read from.
func presetsFS(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(filepath.Join("..", "..", "assets", "presets"))
	if err != nil {
		t.Fatalf("open the presets directory: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// TestEveryPresetIsNamedAndEveryNameIsAPreset keeps the shipped files and the
// names flow offers in step: an unlisted preset is unreachable, and a listed
// one that does not exist fails at the worst moment, during flow init.
func TestEveryPresetIsNamedAndEveryNameIsAPreset(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "assets", "presets", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var shipped []string
	for _, path := range paths {
		shipped = append(shipped, strings.TrimSuffix(filepath.Base(path), ".yaml"))
	}
	for _, name := range PresetNames {
		if !contains(shipped, name) {
			t.Errorf("flow offers the %q preset but ships no %s", name, PresetFile(name))
		}
	}
	for _, name := range shipped {
		if !contains(PresetNames, name) {
			t.Errorf("flow ships %s but never offers it; add it to PresetNames", PresetFile(name))
		}
	}
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// TestPresetsDecodeAndValidate is the promise a preset makes: it is a working
// workflow on the day it is copied, not a sketch.
func TestPresetsDecodeAndValidate(t *testing.T) {
	registry := testRegistry(t)

	for _, name := range PresetNames {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadPreset(presetsFS(t).FS(), name)
			if err != nil {
				t.Fatalf("LoadPreset: %v", err)
			}
			if cfg.Preset != name {
				t.Errorf("preset key is %q, want %q: a copied file should say where it came from", cfg.Preset, name)
			}
			if cfg.Version != Version {
				t.Errorf("version is %d, want %d", cfg.Version, Version)
			}
			if err := Validate(cfg, PresetFile(name), registry); err != nil {
				t.Errorf("Validate:\n%v", err)
			}
		})
	}
}

// TestPresetsAreWhatTheySay holds each preset to the one sentence spec 03 §7
// writes about it. These are the differences a team chooses between.
func TestPresetsAreWhatTheySay(t *testing.T) {
	lean, err := LoadPreset(presetsFS(t).FS(), PresetLeanKanban)
	if err != nil {
		t.Fatal(err)
	}
	if lean.Workflow.DoneOn != DoneOnMerged {
		t.Errorf("lean-kanban is done on %q, want %q", lean.Workflow.DoneOn, DoneOnMerged)
	}
	if lean.Deploys.Source == DeploySourceNone || lean.Deploys.MaxLag == 0 {
		t.Error("lean-kanban still holds the team to a merged-to-deployed objective, so it needs a deploys source and a max_lag")
	}
	if _, ok := lean.Workflow.Transitions[EventDeployed]; ok {
		t.Error("under done_on: merged a deployment is verified as lag, not tracked as a status")
	}

	deployed, err := LoadPreset(presetsFS(t).FS(), PresetDeployDone)
	if err != nil {
		t.Fatal(err)
	}
	if deployed.Workflow.DoneOn != DoneOnDeployed {
		t.Errorf("deploy-done is done on %q, want %q", deployed.Workflow.DoneOn, DoneOnDeployed)
	}
	transition, ok := deployed.Workflow.Transitions[EventDeployed]
	if !ok {
		t.Fatal("deploy-done has no deployed transition, so nothing would ever finish")
	}
	if transition.Owner != OwnerFlow {
		t.Errorf("the deployed transition is owned by %q; no tracker automates a deployment, so flow owns it", transition.Owner)
	}
	if column, _ := deployed.Workflow.Column(transition.To); column.Category != CategoryDone {
		t.Errorf("deployed moves to %q, which is not the done column", transition.To)
	}

	minimal, err := LoadPreset(presetsFS(t).FS(), PresetMinimal)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range minimal.Workflow.Columns {
		if column.Gate != "" {
			t.Errorf("minimal gates %q on %q; the minimal preset has no Ready gate", column.ID, column.Gate)
		}
	}
	if minimal.Policy.Strictness != StrictnessRelaxed {
		t.Errorf("minimal is %q, want %q", minimal.Policy.Strictness, StrictnessRelaxed)
	}
}

// TestPresetsShareTheirShape keeps the three files from drifting apart in the
// ways that are not the point of them: the same keys, the same placeholders,
// the same editor hint.
func TestPresetsShareTheirShape(t *testing.T) {
	want := "# yaml-language-server: $schema=" + SchemaID

	for _, name := range PresetNames {
		t.Run(name, func(t *testing.T) {
			source, err := PresetSource(presetsFS(t).FS(), name)
			if err != nil {
				t.Fatal(err)
			}
			if first, _, _ := strings.Cut(string(source), "\n"); first != want {
				t.Errorf("first line is %q, want the schema hint %q", first, want)
			}
			if !strings.Contains(string(source), "#") {
				t.Error("a preset is read as much as it is run: keep the comments")
			}

			cfg, err := LoadPreset(presetsFS(t).FS(), name)
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.Tracker.Project.IsPending() {
				t.Error("tracker.project must still be a placeholder: nobody's board number can ship in a preset")
			}
			for key, owner := range map[string]Pending{
				"default":       cfg.FollowUps.Owners.Default,
				"tracker_admin": cfg.FollowUps.Owners.TrackerAdmin,
				"forge_admin":   cfg.FollowUps.Owners.ForgeAdmin,
				"platform":      cfg.FollowUps.Owners.Platform,
			} {
				if !owner.IsPending() {
					t.Errorf("followups.owners.%s is %q, want a placeholder", key, owner)
				}
			}
			if cfg.Forge.Repo != "owner/name" {
				t.Errorf("forge.repo is %q; a preset cannot know the repository, and init fills it in from the git remote", cfg.Forge.Repo)
			}
		})
	}
}

// TestPresetGolden pins what each preset decodes to. A preset is a promise
// about behaviour, so a change to one should be a change to a golden file
// somebody had to look at.
//
// Run go test ./internal/config -run TestPresetGolden -update to rewrite them.
func TestPresetGolden(t *testing.T) {
	for _, name := range PresetNames {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadPreset(presetsFS(t).FS(), name)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			golden := filepath.Join("testdata", "presets", name+".golden.yaml")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, encoded, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update to write it)", err)
			}
			if string(encoded) != string(want) {
				t.Errorf("the %s preset decodes differently than the golden file:\n--- got\n%s\n--- want\n%s", name, encoded, want)
			}
		})
	}
}

func TestLoadPresetRefusesWhatItDoesNotShip(t *testing.T) {
	if _, err := LoadPreset(presetsFS(t).FS(), "kanban"); err == nil {
		t.Fatal("LoadPreset invented a preset")
	} else if !strings.Contains(err.Error(), "ships no preset") {
		t.Errorf("error %q does not say the preset does not exist", err)
	}
}
