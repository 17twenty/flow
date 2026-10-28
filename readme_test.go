package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
)

// The README's demo section (spec 17 §7, plan item 8.2) is a claim about a
// particular repository and a particular board, and a claim in prose is the
// kind that goes quietly wrong: flow is pointed somewhere else, the
// configuration follows, and the demo goes on naming what used to be true.
// These tests read the section against `.flow.yaml` and `.flow.lock` so that
// the demo and the workflow it demonstrates cannot come apart.

// demoHeading opens the section this file is about.
const demoHeading = "## Demo — flow runs on flow"

// demoLinkRe matches every link in the section to a page on the forge.
var demoLinkRe = regexp.MustCompile(`https://github\.com/[^\s)>]+`)

// demoSection is the README from the demo heading to the next one.
func demoSection(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("the README is what this test reads: %v", err)
	}
	text := string(source)
	start := strings.Index(text, demoHeading)
	if start < 0 {
		t.Fatalf("the README has no %q section, so nothing points at the live demo", demoHeading)
	}
	rest := text[start+len(demoHeading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// TestTheDemoPointsAtTheRepositoryFlowIsConfiguredFor fails when a link in the
// section names a repository or a board the configuration does not.
func TestTheDemoPointsAtTheRepositoryFlowIsConfiguredFor(t *testing.T) {
	cfg, err := config.Load(".flow.yaml")
	if err != nil {
		t.Fatalf("flow's own configuration is what the demo is a demo of: %v", err)
	}
	lock, err := config.LoadLock(".flow.lock")
	if err != nil {
		t.Fatalf("the board's number is resolved in the lock: %v", err)
	}
	project, ok := lock.Lookup("tracker.project")
	if !ok {
		t.Fatalf("%s has no resolved tracker.project, so the board has no number to check against", ".flow.lock")
	}

	repo := "https://github.com/" + cfg.Forge.Repo
	board := fmt.Sprintf("https://github.com/users/%s/projects/%s", cfg.Tracker.Owner, project)

	links := demoLinkRe.FindAllString(demoSection(t), -1)
	if len(links) == 0 {
		t.Fatalf("the demo section links to nothing, so there is no demo to see")
	}
	var sawRepo, sawBoard bool
	for _, link := range links {
		switch {
		case link == board:
			sawBoard = true
		case strings.HasPrefix(link, repo+"/"):
			sawRepo = true
		default:
			t.Errorf("the demo links to %s, which is neither %s nor the board %s", link, repo, board)
		}
	}
	if !sawRepo {
		t.Errorf("the demo links nowhere in %s, which is the repository flow is configured for", repo)
	}
	if !sawBoard {
		t.Errorf("the demo does not link %s, which is the board flow is configured for", board)
	}
}

// TestTheDemoSaysWhereTheStreakIsKept: plan item 8.2 asks the demo to say
// where the fourteen-day streak stands. It once did so with a number, which
// this test required to exist and to carry a date — and which nothing but a
// person ever changed, so the README led with a count that was stale by the
// next morning. The record is the health issue the daily run rewrites; the
// demo points there, and a hand-kept count is refused rather than required.
func TestTheDemoSaysWhereTheStreakIsKept(t *testing.T) {
	section := demoSection(t)
	if !strings.Contains(section, "fourteen consecutive green daily runs") {
		t.Errorf("the demo section does not say what v1.0.0 waits for:\n%s", section)
	}
	if !regexp.MustCompile(`\[health issue\]\(https://github\.com/[^)]+/issues/\d+\)`).MatchString(section) {
		t.Errorf("the demo section does not link the health issue as where the count is kept:\n%s", section)
	}
	if regexp.MustCompile(`(?i)\d+ of 14`).MatchString(section) {
		t.Errorf("the demo section keeps a count by hand, which only a person updates:\n%s", section)
	}
}
