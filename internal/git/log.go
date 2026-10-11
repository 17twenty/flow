package git

import (
	"context"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// Identity is who did something, and when.
type Identity struct {
	// Name is the person's name as git recorded it.
	Name string
	// Email is the address git recorded.
	Email string
	// When is the timestamp, in the zone the author was in.
	When time.Time
}

// Commit is one commit, in as much detail as flow reads.
type Commit struct {
	// Hash is the full object name.
	Hash string
	// Parents are the full object names of the commit's parents.
	Parents []string
	// Author wrote the change; Committer applied it. They differ after a
	// rebase, a cherry-pick, or a squash merge performed by a forge.
	Author    Identity
	Committer Identity
	// Subject is the first line, as git itself computes it.
	Subject string
	// Message is the whole commit message, subject and body, with trailing
	// blank lines removed.
	Message string
}

// Merge reports whether the commit has more than one parent. Merges are
// excluded from the commit convention: nobody chose their wording (spec 08 §2).
func (c Commit) Merge() bool { return len(c.Parents) > 1 }

// Short returns the first seven characters of the hash, for display.
func (c Commit) Short() string {
	if len(c.Hash) < 7 {
		return c.Hash
	}
	return c.Hash[:7]
}

// fieldSeparator separates the fields of one commit in git's output, and
// recordSeparator separates commits. git -z terminates each commit with a NUL,
// so the unit separator is left to divide the fields within one.
const (
	fieldSeparator  = "\x1f"
	recordSeparator = "\x00"
)

// logFormat asks for exactly the fields [Commit] holds, in order. The message
// is last because it is the only field that can contain anything at all.
const logFormat = "%H" + fieldSeparator +
	"%P" + fieldSeparator +
	"%an" + fieldSeparator +
	"%ae" + fieldSeparator +
	"%aI" + fieldSeparator +
	"%cn" + fieldSeparator +
	"%ce" + fieldSeparator +
	"%cI" + fieldSeparator +
	"%s" + fieldSeparator +
	"%B"

// logFields is how many fields logFormat asks for.
const logFields = 10

// Log returns the commits the given revisions select, newest first. The
// revisions are git's own syntax: "main..HEAD" for a range, "HEAD" for a
// history, "--since" style options are not accepted.
//
// A repository with no commits yet returns no commits and no error: having
// just been created is not a failure.
func (r *Repo) Log(ctx context.Context, revisions ...string) ([]Commit, error) {
	for _, rev := range revisions {
		if strings.HasPrefix(rev, "-") {
			return nil, core.Errorf(core.ErrValidation, "read the commit log").
				WithWhy("%q is an option, and Log takes only revisions", rev).
				WithFix("pass a revision such as main..HEAD")
		}
	}

	args := append([]string{"log", "-z", "--no-show-signature", "--pretty=tformat:" + logFormat}, revisions...)
	args = append(args, "--")
	res, err := run(ctx, r.dir, "", args...)
	if err != nil {
		return nil, err
	}
	if res.code != 0 {
		// A repository with no commits has no revision to name, so git refuses
		// the range rather than returning nothing. Having just been created is
		// not a failure, and only this case may be read as an empty history:
		// anywhere else a revision git does not recognise is a mistake worth
		// reporting.
		if any, hasErr := r.HasCommits(ctx); hasErr == nil && !any {
			return nil, nil
		}
		return nil, failure(res, "log")
	}
	return parseLog(res.stdout)
}

// parseLog turns git's NUL-separated records into commits.
func parseLog(out string) ([]Commit, error) {
	var commits []Commit
	for record := range strings.SplitSeq(out, recordSeparator) {
		// git -z terminates rather than separates, so the text after the last
		// NUL is empty. A newline can follow it when git pages its output.
		if strings.TrimSpace(record) == "" {
			continue
		}
		fields := strings.SplitN(strings.TrimLeft(record, "\n"), fieldSeparator, logFields)
		if len(fields) != logFields {
			return nil, core.Errorf(core.ErrValidation, "read the commit log").
				WithWhy("git described a commit in %d fields, not %d", len(fields), logFields).
				WithFix("report this: flow cannot read this version of git's output")
		}

		authored, err := parseTime(fields[4])
		if err != nil {
			return nil, err
		}
		committed, err := parseTime(fields[7])
		if err != nil {
			return nil, err
		}
		commits = append(commits, Commit{
			Hash:      fields[0],
			Parents:   strings.Fields(fields[1]),
			Author:    Identity{Name: fields[2], Email: fields[3], When: authored},
			Committer: Identity{Name: fields[5], Email: fields[6], When: committed},
			Subject:   fields[8],
			Message:   strings.TrimRight(fields[9], "\n"),
		})
	}
	return commits, nil
}

// parseTime reads one of git's strict ISO 8601 timestamps, keeping the offset
// the commit was made in.
func parseTime(s string) (time.Time, error) {
	when, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, core.Errorf(core.ErrValidation, "read the commit log").
			WithWhy("git gave %q as a timestamp, which is not ISO 8601", s).
			WithFix("report this: flow cannot read this version of git's output")
	}
	return when, nil
}
