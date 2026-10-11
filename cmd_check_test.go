package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// execIn runs the CLI in-process with stdin wired to in.
func execIn(t *testing.T, in string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, strings.NewReader(in), &out, &errb)
	return code, out.String(), errb.String()
}

// writeMsg puts a message in a temp file and returns its path.
func writeMsg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write message: %v", err)
	}
	return path
}

func TestCheckCommitExitCodes(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want int
	}{
		{"a conventional header passes", "feat(cli): add --json\n", core.ExitOK},
		{"a body separated properly passes", "feat(cli): add --json\n\nbody\n", core.ExitOK},
		{"warnings alone do not fail", "feat(cli): Add --json.\n", core.ExitOK},
		{"a non-conventional header fails", "add some json support\n", core.ExitFail},
		{"an unknown type fails", "wibble(cli): add --json\n", core.ExitFail},
		{"an uppercase type fails", "Feat(cli): add --json\n", core.ExitFail},
		{"a missing blank line fails", "feat(cli): add --json\nbody\n", core.ExitFail},
		{"an unexplained break fails", "feat(cli)!: drop --legacy\n", core.ExitFail},
		{"an explained break passes", "feat(cli)!: drop --legacy\n\nBREAKING CHANGE: gone\n", core.ExitOK},
		{"a merge commit is skipped", "Merge branch 'main' into feature\n", core.ExitOK},
		{"a fixup commit is skipped", "fixup! feat(cli): add --json\n", core.ExitOK},
		{"an over-long header fails", "feat(cli): " + strings.Repeat("x", 80) + "\n", core.ExitFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, _ := execIn(t, tt.msg, "check", "commit")
			if code != tt.want {
				t.Errorf("exit = %d, want %d", code, tt.want)
			}
		})
	}
}

// A clean message must say nothing: this backs a commit-msg hook.
func TestCheckCommitIsSilentWhenClean(t *testing.T) {
	code, stdout, stderr := execIn(t, "feat(cli): add --json\n", "check", "commit")
	if code != core.ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, core.ExitOK, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestCheckCommitReportsRuleAndFix(t *testing.T) {
	code, stdout, stderr := execIn(t, "Feat(cli): add --json\n", "check", "commit")
	if code != core.ExitFail {
		t.Fatalf("exit = %d, want %d", code, core.ExitFail)
	}
	if !strings.Contains(stdout, "cc.type.allowed") {
		t.Errorf("stdout missing the rule id:\n%s", stdout)
	}
	if !strings.Contains(stdout, `fix: write "feat"`) {
		t.Errorf("stdout missing the fix hint:\n%s", stdout)
	}
	// The P10 explanation belongs on stderr, the violations on stdout.
	for _, want := range []string{"error:", "why:", "fix:"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q line:\n%s", want, stderr)
		}
	}
}

func TestCheckCommitReadsAFile(t *testing.T) {
	path := writeMsg(t, "feat(cli): add --json\n")
	code, _, stderr := execIn(t, "", "check", "commit", path)
	if code != core.ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, core.ExitOK, stderr)
	}
}

func TestCheckCommitMissingFileIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	code, _, stderr := execIn(t, "", "check", "commit", missing)
	if code != core.ExitFail {
		t.Errorf("exit = %d, want %d", code, core.ExitFail)
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("stderr should explain the failure:\n%s", stderr)
	}
}

// --fix must rewrite the file in place, and the result must then pass.
func TestCheckCommitFixRewritesTheFile(t *testing.T) {
	path := writeMsg(t, "  Feat(cli): add --json.  \nbody text\n")

	code, stdout, _ := execIn(t, "", "check", "commit", "--fix", path)
	if code != core.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout:\n%s", code, core.ExitOK, stdout)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	const want = "feat(cli): add --json\n\nbody text\n"
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if !strings.Contains(stdout, "fixed:") {
		t.Errorf("stdout should name the rules fixed:\n%s", stdout)
	}
}

// Running --fix twice must leave the file untouched the second time.
func TestCheckCommitFixIsIdempotent(t *testing.T) {
	path := writeMsg(t, "  Feat(cli): add --json.  \nbody text\n")

	if code, _, _ := execIn(t, "", "check", "commit", "--fix", path); code != core.ExitOK {
		t.Fatalf("first pass exited %d", code)
	}
	first, _ := os.ReadFile(path)

	code, stdout, _ := execIn(t, "", "check", "commit", "--fix", path)
	if code != core.ExitOK {
		t.Fatalf("second pass exited %d", code)
	}
	second, _ := os.ReadFile(path)

	if string(first) != string(second) {
		t.Errorf("second pass changed the file:\n first %q\nsecond %q", first, second)
	}
	if strings.Contains(stdout, "fixed:") {
		t.Errorf("second pass claimed fixes:\n%s", stdout)
	}
}

// A fix that cannot make the message legal must still fail the check.
func TestCheckCommitFixDoesNotMaskRealErrors(t *testing.T) {
	path := writeMsg(t, "Wibble(cli): add --json.\n")
	code, _, _ := execIn(t, "", "check", "commit", "--fix", path)
	if code != core.ExitFail {
		t.Errorf("exit = %d, want %d: the type is still not allowed", code, core.ExitFail)
	}
	got, _ := os.ReadFile(path)
	if want := "wibble(cli): add --json\n"; string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

// --fix on stdin has no file to write, so the message goes to stdout.
func TestCheckCommitFixOnStdinWritesToStdout(t *testing.T) {
	code, stdout, _ := execIn(t, "  Feat(cli): add --json.  \n", "check", "commit", "--fix")
	if code != core.ExitOK {
		t.Fatalf("exit = %d, want %d", code, core.ExitOK)
	}
	if !strings.Contains(stdout, "feat(cli): add --json") {
		t.Errorf("stdout should carry the fixed message:\n%s", stdout)
	}
}

func TestCheckCommitFixPreservesFileMode(t *testing.T) {
	path := writeMsg(t, "Feat(cli): add --json\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if code, _, _ := execIn(t, "", "check", "commit", "--fix", path); code != core.ExitOK {
		t.Fatalf("fix failed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestCheckCommitJSON(t *testing.T) {
	code, stdout, _ := execIn(t, "Feat(cli): add --json.\n", "--json", "check", "commit")
	if code != core.ExitFail {
		t.Fatalf("exit = %d, want %d", code, core.ExitFail)
	}

	var doc struct {
		Schema       string `json:"schema"`
		File         string `json:"file"`
		Header       string `json:"header"`
		Kind         string `json:"kind"`
		Conventional bool   `json:"conventional"`
		Skipped      bool   `json:"skipped"`
		Fixed        []string
		Violations   []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Column   int    `json:"column"`
			Fix      string `json:"fix"`
		} `json:"violations"`
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}

	if want := core.SchemaID("commit"); doc.Schema != want {
		t.Errorf("schema = %q, want %q", doc.Schema, want)
	}
	if doc.File != "-" {
		t.Errorf("file = %q, want %q", doc.File, "-")
	}
	if !doc.Conventional || doc.Skipped || doc.Kind != "normal" {
		t.Errorf("header classification wrong: %+v", doc)
	}
	if doc.Errors != 1 {
		t.Errorf("errors = %d, want 1 (the uppercase type)", doc.Errors)
	}
	if doc.Warnings != 1 {
		t.Errorf("warnings = %d, want 1 (the trailing stop)", doc.Warnings)
	}
	if len(doc.Violations) != doc.Errors+doc.Warnings {
		t.Errorf("violations = %d, want %d", len(doc.Violations), doc.Errors+doc.Warnings)
	}
	for _, v := range doc.Violations {
		if v.Rule == "" || v.Severity == "" || v.Fix == "" || v.Column < 1 {
			t.Errorf("violation is under-populated: %+v", v)
		}
	}
}

// --json must emit exactly one document even when a fix was applied, so the
// fixed message cannot be interleaved onto stdout.
func TestCheckCommitJSONStaysOneDocumentWithFix(t *testing.T) {
	code, stdout, _ := execIn(t, "  Feat(cli): add --json.  \n", "--json", "check", "commit", "--fix")
	if code != core.ExitOK {
		t.Fatalf("exit = %d, want %d:\n%s", code, core.ExitOK, stdout)
	}

	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc struct {
		Fixed []string `json:"fixed"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("first document: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout carries more than one JSON document:\n%s", stdout)
	}
	if len(doc.Fixed) == 0 {
		t.Errorf("fixed should name the rules applied:\n%s", stdout)
	}
}

// A message git wrote itself is reported as skipped rather than judged.
func TestCheckCommitJSONMarksGeneratedMessagesSkipped(t *testing.T) {
	code, stdout, _ := execIn(t, "Merge branch 'main'\n", "--json", "check", "commit")
	if code != core.ExitOK {
		t.Fatalf("exit = %d, want %d", code, core.ExitOK)
	}
	var doc struct {
		Kind       string `json:"kind"`
		Skipped    bool   `json:"skipped"`
		Violations []any  `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if doc.Kind != "merge" || !doc.Skipped {
		t.Errorf("kind = %q skipped = %v, want merge/true", doc.Kind, doc.Skipped)
	}
	if len(doc.Violations) != 0 {
		t.Errorf("a generated message must not be judged: %+v", doc.Violations)
	}
}
