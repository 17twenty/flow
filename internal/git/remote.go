package git

import (
	"context"
	neturl "net/url"
	"regexp"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// Remote is a git remote URL, parsed into the parts flow needs: which host to
// talk to, and which repository to talk about.
//
// A remote URL is one of the few places a credential can hide in a repository,
// because `https://<token>@host/…` is a working clone URL. Nothing in this
// type carries one: see [ParseRemote].
type Remote struct {
	// Name is the remote's name, such as origin. It is empty when a URL was
	// parsed on its own rather than read from a repository.
	Name string
	// URL is the remote's URL with any embedded credential removed.
	URL string
	// Scheme is ssh, https, http, git or file.
	Scheme string
	// User is the account named in an ssh URL, usually git. It is empty for
	// every other scheme.
	User string
	// Host is the lowercased host name, without a port. It is empty when the
	// remote is a path on this machine.
	Host string
	// Port is the port the URL named, or empty for the scheme's default.
	Port string
	// Path is the repository's path on the host, without a leading slash or a
	// .git suffix.
	Path string
	// Owner is the path above the repository: a user, an organisation, or a
	// nested group path such as group/subgroup.
	Owner string
	// Repo is the repository's own name.
	Repo string
}

// Slug returns the owner/name form flow uses to address a repository.
func (r Remote) Slug() string {
	if r.Owner == "" {
		return r.Repo
	}
	return r.Owner + "/" + r.Repo
}

// Local reports whether the remote is a path on this machine rather than a
// host flow could talk to.
func (r Remote) Local() bool { return r.Host == "" }

// Remote returns the named remote of r, parsed. An empty name means origin.
func (r *Repo) Remote(ctx context.Context, name string) (Remote, error) {
	if name == "" {
		name = "origin"
	}
	res, err := run(ctx, r.dir, "", "remote", "get-url", name)
	if err != nil {
		return Remote{}, err
	}
	if res.code != 0 {
		if strings.Contains(res.stderr, "No such remote") {
			return Remote{}, core.Errorf(core.ErrNotFound, "read the %s remote", name).
				WithWhy("this repository has no remote called %s", name).
				WithFix("add one with git remote add %s <url>", name)
		}
		return Remote{}, failure(res, "remote")
	}
	remote, err := ParseRemote(strings.TrimSpace(res.stdout))
	if err != nil {
		return Remote{}, err
	}
	remote.Name = name
	return remote, nil
}

// RemoteNames returns the names of every remote, in git's order.
func (r *Repo) RemoteNames(ctx context.Context) ([]string, error) {
	out, err := output(ctx, r.dir, "", "remote")
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// scpLike matches git's historic [user@]host:path remote syntax.
var scpLike = regexp.MustCompile(`^(?:([^@/]+)@)?([^@/:]+):(.*)$`)

// ParseRemote parses a git remote URL in any of the forms git itself accepts:
// scp-like (git@host:owner/repo.git), a URL (ssh, git, http, https, file), or a
// path on this machine. Owner may have several segments, as GitLab subgroups
// do.
//
// Any credential in the URL is dropped rather than returned: an https remote's
// user name is routinely a token, so neither [Remote.URL] nor [Remote.User]
// keeps one.
func ParseRemote(raw string) (Remote, error) {
	url := strings.TrimSpace(raw)
	if url == "" {
		return Remote{}, core.Errorf(core.ErrValidation, "read the remote URL").
			WithWhy("it is empty").
			WithFix("set one with git remote set-url origin <url>")
	}

	var remote Remote
	var err error
	switch {
	case strings.Contains(url, "://"):
		remote, err = parseRemoteURL(url)
	case isSCPLike(url):
		remote, err = parseSCPRemote(url)
	default:
		return Remote{Scheme: "file", URL: url, Path: strings.TrimSuffix(url, ".git")}, nil
	}
	if err != nil {
		return Remote{}, err
	}

	if remote.Host == "" {
		return remote, nil
	}
	parts := strings.Split(remote.Path, "/")
	if len(parts) < 2 || parts[len(parts)-1] == "" {
		return Remote{}, core.Errorf(core.ErrValidation, "read the remote URL").
			WithWhy("%s names a host but not an owner and repository", core.Redact(remote.URL)).
			WithFix("the URL should end in <owner>/<repository>")
	}
	remote.Repo = parts[len(parts)-1]
	remote.Owner = strings.Join(parts[:len(parts)-1], "/")
	return remote, nil
}

// isSCPLike applies git's own rule for the scp-like form: a colon that comes
// before any slash makes the text a host and a path rather than a local one.
func isSCPLike(url string) bool {
	colon := strings.Index(url, ":")
	if colon < 0 {
		return false
	}
	slash := strings.Index(url, "/")
	return (slash < 0 || colon < slash) && scpLike.MatchString(url)
}

// parseSCPRemote parses [user@]host:path. The form has no place to put a port
// or a password, which is why it is handled apart from the URL parser.
func parseSCPRemote(url string) (Remote, error) {
	m := scpLike.FindStringSubmatch(url)
	return Remote{
		URL:    url,
		Scheme: "ssh",
		User:   m[1],
		Host:   strings.ToLower(strings.TrimSuffix(m[2], ".")),
		Path:   cleanRemotePath(m[3]),
	}, nil
}

// parseRemoteURL parses a remote written as a URL.
func parseRemoteURL(url string) (Remote, error) {
	parsed, err := neturl.Parse(url)
	if err != nil {
		return Remote{}, core.Errorf(core.ErrValidation, "read the remote URL").
			WithWhy("it is not a URL git could use").
			WithFix("check git remote get-url origin")
	}

	scheme := strings.ToLower(parsed.Scheme)
	remote := Remote{
		Scheme: scheme,
		Host:   strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")),
		Port:   parsed.Port(),
		Path:   cleanRemotePath(parsed.Path),
	}
	if scheme == "file" {
		remote.Host = ""
		remote.Port = ""
		remote.Path = strings.TrimSuffix(parsed.Path, ".git")
	}

	// Userinfo is kept only where it names an account. On ssh it is "git" or a
	// login; on http it is how a token is carried, so it goes.
	clean := *parsed
	clean.User = nil
	if (scheme == "ssh" || scheme == "git") && parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); !hasPassword {
			remote.User = parsed.User.Username()
			clean.User = parsed.User
		}
	}
	remote.URL = clean.String()
	return remote, nil
}

// cleanRemotePath reduces a URL path to owner/repo: no leading or trailing
// slash, and no .git suffix.
func cleanRemotePath(path string) string {
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	return strings.Trim(path, "/")
}
