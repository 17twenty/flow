package git

import (
	"context"
	"path/filepath"
	"strings"
)

// HooksPath returns the directory git runs hooks from, as an absolute path.
// It honours core.hooksPath, so a repository that has handed its hooks to
// lefthook or husky reports that manager's directory rather than .git/hooks.
func (r *Repo) HooksPath(ctx context.Context) (string, error) {
	out, err := output(ctx, r.dir, "", "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.dir, path)
	}
	return filepath.Clean(path), nil
}

// HooksPathConfigured returns the repository's core.hooksPath setting and
// whether it has one. [Repo.HooksPath] already honours it; this is for
// deciding whether somebody else is in charge of the hooks (spec 02 §2).
func (r *Repo) HooksPathConfigured(ctx context.Context) (string, bool, error) {
	return r.ConfigValue(ctx, "core.hooksPath")
}

// Tracked reports whether path is under version control. A path outside the
// working tree is not tracked rather than an error: the question "is this file
// shared with the team?" has an answer for any path (spec 03 §4).
func (r *Repo) Tracked(ctx context.Context, path string) (bool, error) {
	res, err := run(ctx, r.dir, "", "ls-files", "-z", "--", path)
	if err != nil {
		return false, err
	}
	if res.code != 0 {
		if strings.Contains(res.stderr, "outside repository") {
			return false, nil
		}
		return false, failure(res, "ls-files")
	}
	return res.stdout != "", nil
}

// CommentChar returns the character git strips commit message lines by, and
// whether the repository chose one. core.commentChar set to "auto" — where git
// picks a character per message — reports as unchosen, because flow cannot
// know which one git will pick; the caller then uses git's default.
func (r *Repo) CommentChar(ctx context.Context) (byte, bool, error) {
	value, set, err := r.ConfigValue(ctx, "core.commentChar")
	if err != nil || !set {
		return 0, false, err
	}
	if len(value) != 1 {
		return 0, false, nil
	}
	return value[0], true, nil
}
