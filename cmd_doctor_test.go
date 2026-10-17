package main

import (
	"os"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/engine"
)

// This repository's own configuration has to let flow open the pull requests
// flow opens for itself. The daily audit proposed the refreshed lock every
// morning and flow's gate then refused it — `chore` was not in
// `commits.unlinked_types`, so the proposal was asked for a ticket it has no
// business having — which put a red cross on the trunk's gate on the one
// change nobody could act on (issue #152).
//
// The configuration is read from the tree rather than from a fixture: what is
// being asserted is a fact about this repository, and a fixture would go on
// agreeing with itself after somebody narrowed the real one.
func TestFlowCanOpenItsOwnPullRequestsHere(t *testing.T) {
	raw, err := os.ReadFile(config.DefaultPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw, config.DefaultPath)
	if err != nil {
		t.Fatal(err)
	}
	for what, proposal := range map[string]engine.Proposal{
		"the refreshed lock":           engine.LockProposal,
		"the files the workflow needs": engine.FilesProposal,
	} {
		title, err := proposal.Title(cfg)
		if err != nil {
			t.Errorf("flow cannot propose %s on this repository: %v", what, err)
			continue
		}
		t.Logf("%s would be proposed as %q", what, title)
	}
}
