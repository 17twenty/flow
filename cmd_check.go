package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/17twenty/flow/internal/commits"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// CheckCmd groups the CI entry points (spec 02 §2, specs 08, 09, 13).
type CheckCmd struct {
	PR     CheckPRCmd     `cmd:"" name:"pr" help:"Evaluate the policy gate and publish the flow/policy status."`
	Deploy CheckDeployCmd `cmd:"" help:"React to a deployment event; move tickets when done_on is deployed."`
	Commit CheckCommitCmd `cmd:"" help:"Lint one commit message."`
}

// CheckPRCmd is the CI gate. It evaluates policy, posts the flow/policy status,
// upserts its explanation comment and performs flow-owned transitions. It exits
// 0 whenever the status was posted: the status, not the job, carries the verdict.
type CheckPRCmd struct {
	PR            int  `name:"pr" help:"Pull request number. Defaults to the CI event payload." placeholder:"N"`
	DryRun        bool `name:"dry-run" help:"Evaluate and print; post nothing."`
	NoTransitions bool `name:"no-transitions" help:"Do not move tickets, only gate."`
	ExitCode      bool `name:"exit-code" help:"Exit 1 when policy fails. For local use."`
}

// Run implements the check pr command. It exits 0 whenever the status went
// out: the status carries the verdict, and a CI job that failed as well would
// report the same thing twice. `--exit-code` is the local exception.
func (c *CheckPRCmd) Run(app *Context) error {
	s, err := app.open()
	if err != nil {
		return err
	}
	number, err := c.number()
	if err != nil {
		return err
	}
	if err := s.connect(app); err != nil {
		return err
	}

	gate, err := engine.RunGate(app.Ctx, engine.GateInput{
		PR:            number,
		Config:        s.Config,
		Lock:          s.Lock,
		Adapters:      s.Adapters,
		Now:           s.Clock.Now(),
		DryRun:        c.DryRun,
		NoTransitions: c.NoTransitions,
		TargetURL:     ciRunURL(os.Getenv),
	})
	if err != nil {
		return err
	}
	if err := app.render(s, func(r ui.Renderer) (string, error) { return r.Policy(gate) }); err != nil {
		return err
	}
	if c.ExitCode && !gate.Pass() {
		app.exit(core.ExitFail)
	}
	return nil
}

// number is the pull request to judge: the flag, or the CI event that started
// this run (spec 02 §2).
func (c *CheckPRCmd) number() (int, error) {
	if c.PR > 0 {
		return c.PR, nil
	}
	if c.PR < 0 {
		return 0, core.Errorf(core.ErrValidation, "check pull request %d", c.PR).
			WithWhy("pull requests are numbered from 1").
			WithFix("pass --pr with the number of the pull request to check")
	}
	number, err := eventPR(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return 0, err
	}
	return number, nil
}

// eventPR reads the pull request number out of the CI event payload. GitHub
// writes the whole webhook body to a file and names it in the environment;
// `pull_request` events carry the number at the top level and
// `pull_request_review` only inside the pull request, so both are read.
func eventPR(path string) (int, error) {
	needs := core.Errorf(core.ErrNeedsInput, "find the pull request to check").
		WithWhy("no --pr was given and no CI event payload names one").
		WithFix("pass --pr with the number of the pull request to check")
	if path == "" {
		return 0, needs
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, needs.WithWhy("the event payload at %s could not be read: %v", path, err)
	}
	var event struct {
		Number      int `json:"number"`
		PullRequest *struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return 0, needs.WithWhy("the event payload at %s is not JSON: %v", path, err)
	}
	if event.PullRequest != nil && event.PullRequest.Number > 0 {
		return event.PullRequest.Number, nil
	}
	if event.Number > 0 {
		return event.Number, nil
	}
	return 0, needs.WithWhy("the event payload at %s is not about a pull request", path)
}

// ciRunURL is where to send somebody who wants to see why the status says what
// it says. An environment that does not name a run gets no link rather than a
// guess: a status pointing at the wrong page is worse than one pointing
// nowhere.
func ciRunURL(getenv func(string) string) string {
	server, repo, run := getenv("GITHUB_SERVER_URL"), getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || run == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%s", strings.TrimSuffix(server, "/"), repo, run)
}

// CheckDeployCmd reacts to a deployment event. With done_on: deployed it moves
// the tickets linked to PRs merged at or before the deployed SHA (spec 04 §6).
type CheckDeployCmd struct {
	SHA         string `name:"sha" help:"Deployed commit. Defaults to the CI event payload." placeholder:"SHA"`
	Environment string `help:"Deployment environment. Defaults to the CI event payload." placeholder:"ENV"`
	DryRun      bool   `name:"dry-run" help:"Evaluate and print; change nothing."`
}

// Run implements the check deploy command.
func (c *CheckDeployCmd) Run(app *Context) error { return app.unimplemented("check deploy") }

// CheckCommitCmd lints a single commit message (spec 08). It backs the optional
// commit-msg hook.
type CheckCommitCmd struct {
	File string `arg:"" optional:"" default:"-" help:"Message file, or - for stdin."`
	Fix  bool   `help:"Apply safe autofixes in place."`
}

// commitReport is the --json document for check commit, schema flow.commit/v1.
type commitReport struct {
	Schema string `json:"schema"`
	// File is the message's source, "-" when it came from stdin.
	File string `json:"file"`
	// Header is the subject line the rules were measured against.
	Header string `json:"header"`
	// Kind is the message form: normal, merge, revert, fixup, squash or amend.
	Kind string `json:"kind"`
	// Conventional reports whether the header parsed.
	Conventional bool `json:"conventional"`
	// Skipped reports that git wrote this message, so no rule was applied.
	Skipped bool `json:"skipped"`
	// Fixed lists the rule ids --fix resolved, in the order they were applied.
	Fixed []string `json:"fixed"`
	// Violations lists what is still wrong after any fixes.
	Violations []commitViolation `json:"violations"`
	// Errors and Warnings count Violations by severity.
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

// commitViolation is one violation rendered for JSON. It exists because
// commits.Violation keeps Severity unexported from JSON, the wire format
// wanting the name rather than the enum.
type commitViolation struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Column   int    `json:"column"`
	Fix      string `json:"fix"`
}

// Run implements the check commit command. It lints one message, optionally
// rewriting it with the safe autofixes first, and exits 1 if any error-severity
// rule is still violated (spec 02 §2).
func (c *CheckCommitCmd) Run(app *Context) error {
	raw, err := c.read(app)
	if err != nil {
		return err
	}

	var fixed []string
	if c.Fix {
		updated, rules := commits.Fix(raw)
		if updated != raw {
			if err := c.write(app, updated); err != nil {
				return err
			}
		}
		raw, fixed = updated, rules
	}

	// Configuration arrives with plan item 1.4; until then the defaults spec 03
	// §1 publishes are the contract.
	msg := commits.Parse(raw)
	violations := commits.Lint(msg, commits.LintOptions{
		Settings: commits.DefaultSettings(),
		Subject:  commits.SubjectCommit,
	})

	if app.CLI.JSON {
		if err := c.reportJSON(app, msg, fixed, violations); err != nil {
			return err
		}
	} else {
		c.reportText(app, fixed, violations)
	}

	if n := len(commits.Errors(violations)); n > 0 {
		return &core.UserError{
			What: "accept the commit message",
			Why:  plural(n, "rule") + " failed",
			Fix:  "correct the message, or run flow check commit --fix to apply the safe fixes",
		}
	}
	return nil
}

// read loads the message from the file argument, or stdin when it is "-".
func (c *CheckCommitCmd) read(app *Context) (string, error) {
	if c.File == "-" {
		b, err := io.ReadAll(app.Stdin)
		if err != nil {
			return "", core.Errorf(core.ErrValidation, "read the commit message from stdin").
				WithWhy("%v", err).
				WithFix("pass the message file as an argument instead")
		}
		return string(b), nil
	}
	b, err := os.ReadFile(c.File)
	if err != nil {
		return "", core.Errorf(core.ErrNotFound, "read the commit message from %s", c.File).
			WithWhy("%v", err).
			WithFix("check the path, or pass - to read the message from stdin")
	}
	return string(b), nil
}

// write stores a fixed message back where it came from. A message read from
// stdin has nowhere to go but stdout, so the hook can pipe it onward.
func (c *CheckCommitCmd) write(app *Context, message string) error {
	if c.File == "-" {
		if app.CLI.JSON {
			return nil // the JSON document carries the result instead
		}
		_, err := io.WriteString(app.Stdout, message)
		return err
	}
	// Preserve the file's existing mode: git creates COMMIT_EDITMSG at 0644,
	// and a hook has no business changing that.
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(c.File); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(c.File, []byte(message), mode); err != nil {
		return core.Errorf(core.ErrValidation, "write the fixed message to %s", c.File).
			WithWhy("%v", err).
			WithFix("check the file is writable")
	}
	return nil
}

// reportJSON writes the single flow.commit/v1 document.
func (c *CheckCommitCmd) reportJSON(app *Context, msg commits.Message, fixed []string, vs []commits.Violation) error {
	doc := commitReport{
		Schema:       core.SchemaID("commit"),
		File:         c.File,
		Header:       msg.Header,
		Kind:         msg.Kind.String(),
		Conventional: msg.Conventional(),
		Skipped:      msg.Kind.Generated(),
		Fixed:        fixed,
		Violations:   make([]commitViolation, 0, len(vs)),
	}
	if doc.Fixed == nil {
		doc.Fixed = []string{}
	}
	for _, v := range vs {
		doc.Violations = append(doc.Violations, commitViolation{
			Rule:     v.Rule,
			Severity: v.Severity.String(),
			Message:  v.Message,
			Column:   v.Column,
			Fix:      v.Fix,
		})
		if v.Severity == commits.SeverityError {
			doc.Errors++
		} else {
			doc.Warnings++
		}
	}
	return core.WriteJSON(app.Stdout, doc)
}

// reportText writes the human rendering. A clean message says nothing: this is
// a hook, and silence is the good case.
func (c *CheckCommitCmd) reportText(app *Context, fixed []string, vs []commits.Violation) {
	if len(fixed) > 0 {
		fmt.Fprintf(app.Stdout, "fixed: %s\n", strings.Join(fixed, ", "))
	}
	for _, v := range vs {
		fmt.Fprintf(app.Stdout, "%-5s col %-3d %-22s %s\n",
			v.Severity, v.Column, v.Rule, v.Message)
		if v.Fix != "" {
			fmt.Fprintf(app.Stdout, "      fix: %s\n", v.Fix)
		}
	}
}

// plural renders a count with its noun, pluralised the easy way.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
