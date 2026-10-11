package git_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/git"
)

func TestParseRemote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		url    string
		want   git.Remote
		wantID string // Slug()
	}{
		{
			name:   "scp form",
			url:    "git@github.com:17twenty/flow.git",
			want:   git.Remote{URL: "git@github.com:17twenty/flow.git", Scheme: "ssh", User: "git", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "scp form without a user",
			url:    "github.com:17twenty/flow.git",
			want:   git.Remote{URL: "github.com:17twenty/flow.git", Scheme: "ssh", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "scp form without the git suffix",
			url:    "git@github.com:17twenty/flow",
			want:   git.Remote{URL: "git@github.com:17twenty/flow", Scheme: "ssh", User: "git", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "scp form with an absolute path",
			url:    "git@git.example.com:/srv/repos/flow.git",
			want:   git.Remote{URL: "git@git.example.com:/srv/repos/flow.git", Scheme: "ssh", User: "git", Host: "git.example.com", Path: "srv/repos/flow", Owner: "srv/repos", Repo: "flow"},
			wantID: "srv/repos/flow",
		},
		{
			name:   "https",
			url:    "https://github.com/17twenty/flow.git",
			want:   git.Remote{URL: "https://github.com/17twenty/flow.git", Scheme: "https", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "https without the git suffix",
			url:    "https://github.com/17twenty/flow",
			want:   git.Remote{URL: "https://github.com/17twenty/flow", Scheme: "https", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "https with a trailing slash",
			url:    "https://github.com/17twenty/flow/",
			want:   git.Remote{URL: "https://github.com/17twenty/flow/", Scheme: "https", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "ssh URL",
			url:    "ssh://git@github.com/17twenty/flow.git",
			want:   git.Remote{URL: "ssh://git@github.com/17twenty/flow.git", Scheme: "ssh", User: "git", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "ssh URL with a port",
			url:    "ssh://git@git.example.com:2222/17twenty/flow.git",
			want:   git.Remote{URL: "ssh://git@git.example.com:2222/17twenty/flow.git", Scheme: "ssh", User: "git", Host: "git.example.com", Port: "2222", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "git protocol",
			url:    "git://github.com/17twenty/flow.git",
			want:   git.Remote{URL: "git://github.com/17twenty/flow.git", Scheme: "git", Host: "github.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "http",
			url:    "http://git.example.com/17twenty/flow.git",
			want:   git.Remote{URL: "http://git.example.com/17twenty/flow.git", Scheme: "http", Host: "git.example.com", Path: "17twenty/flow", Owner: "17twenty", Repo: "flow"},
			wantID: "17twenty/flow",
		},
		{
			name:   "gitlab subgroups",
			url:    "git@gitlab.com:acme/platform/tooling/flow.git",
			want:   git.Remote{URL: "git@gitlab.com:acme/platform/tooling/flow.git", Scheme: "ssh", User: "git", Host: "gitlab.com", Path: "acme/platform/tooling/flow", Owner: "acme/platform/tooling", Repo: "flow"},
			wantID: "acme/platform/tooling/flow",
		},
		{
			name:   "gitlab subgroups over https",
			url:    "https://gitlab.com/acme/platform/tooling/flow.git",
			want:   git.Remote{URL: "https://gitlab.com/acme/platform/tooling/flow.git", Scheme: "https", Host: "gitlab.com", Path: "acme/platform/tooling/flow", Owner: "acme/platform/tooling", Repo: "flow"},
			wantID: "acme/platform/tooling/flow",
		},
		{
			name:   "host case is not significant",
			url:    "https://GitHub.COM/17twenty/Flow.git",
			want:   git.Remote{URL: "https://GitHub.COM/17twenty/Flow.git", Scheme: "https", Host: "github.com", Path: "17twenty/Flow", Owner: "17twenty", Repo: "Flow"},
			wantID: "17twenty/Flow",
		},
		{
			name:   "a repository named like the suffix keeps its name",
			url:    "git@github.com:17twenty/flow.github.io.git",
			want:   git.Remote{URL: "git@github.com:17twenty/flow.github.io.git", Scheme: "ssh", User: "git", Host: "github.com", Path: "17twenty/flow.github.io", Owner: "17twenty", Repo: "flow.github.io"},
			wantID: "17twenty/flow.github.io",
		},
		{
			name: "local path",
			url:  "/srv/git/flow.git",
			want: git.Remote{URL: "/srv/git/flow.git", Scheme: "file", Path: "/srv/git/flow"},
		},
		{
			name: "relative local path",
			url:  "../mirror/flow.git",
			want: git.Remote{URL: "../mirror/flow.git", Scheme: "file", Path: "../mirror/flow"},
		},
		{
			name: "file URL",
			url:  "file:///srv/git/flow.git",
			want: git.Remote{URL: "file:///srv/git/flow.git", Scheme: "file", Path: "/srv/git/flow"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := git.ParseRemote(tt.url)
			if err != nil {
				t.Fatalf("ParseRemote(%q): %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("ParseRemote(%q)\n got %+v\nwant %+v", tt.url, got, tt.want)
			}
			if tt.wantID != "" && got.Slug() != tt.wantID {
				t.Errorf("Slug() = %q, want %q", got.Slug(), tt.wantID)
			}
			if got.Local() != (tt.want.Host == "") {
				t.Errorf("Local() = %v for host %q", got.Local(), got.Host)
			}
		})
	}
}

// A clone URL is a place a token can hide, because https://<token>@host/… is a
// URL git will happily use. Nothing parsed out of one may carry it, or the
// first thing flow init prints is somebody's credential (spec 15 §2).
func TestParseRemoteDropsCredentials(t *testing.T) {
	t.Parallel()
	secret := "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	for _, url := range []string{
		"https://x-access-token:" + secret + "@github.com/17twenty/flow.git",
		"https://" + secret + "@github.com/17twenty/flow.git",
		"https://ada:hunter2@github.com/17twenty/flow.git",
		"ssh://git:" + secret + "@github.com/17twenty/flow.git",
	} {
		t.Run(url[:22], func(t *testing.T) {
			t.Parallel()
			got, err := git.ParseRemote(url)
			if err != nil {
				t.Fatalf("ParseRemote: %v", err)
			}
			if strings.Contains(got.URL, secret) || strings.Contains(got.URL, "hunter2") || strings.Contains(got.URL, "@") {
				t.Errorf("URL still carries a credential: %q", got.URL)
			}
			if got.User != "" {
				t.Errorf("User = %q, want it dropped", got.User)
			}
			if got.Slug() != "17twenty/flow" {
				t.Errorf("Slug() = %q, want 17twenty/flow", got.Slug())
			}
		})
	}
}

func TestParseRemoteRejections(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, url string }{
		{"empty", ""},
		{"blank", "   "},
		{"host only", "https://github.com"},
		{"host and owner only", "https://github.com/17twenty"},
		{"scp with no path", "git@github.com:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := git.ParseRemote(tt.url)
			if err == nil {
				t.Fatalf("ParseRemote(%q) = %+v, want an error", tt.url, got)
			}
			if !errors.Is(err, core.ErrValidation) {
				t.Errorf("error is %v, want it to wrap ErrValidation", err)
			}
			var user *core.UserError
			if !errors.As(err, &user) || user.Fix == "" {
				t.Errorf("the error does not say how to fix it: %v", err)
			}
		})
	}
}

func TestRepoRemote(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "add", "origin", "git@github.com:17twenty/flow.git")
	f.git("remote", "add", "upstream", "https://gitlab.com/acme/platform/flow.git")
	repo := open(t, f)

	origin, err := repo.Remote(ctx(t), "")
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	if origin.Name != "origin" || origin.Slug() != "17twenty/flow" || origin.Host != "github.com" {
		t.Errorf("origin = %+v", origin)
	}

	upstream, err := repo.Remote(ctx(t), "upstream")
	if err != nil {
		t.Fatalf("Remote(upstream): %v", err)
	}
	if upstream.Slug() != "acme/platform/flow" {
		t.Errorf("upstream slug = %q", upstream.Slug())
	}

	names, err := repo.RemoteNames(ctx(t))
	if err != nil {
		t.Fatalf("RemoteNames: %v", err)
	}
	if len(names) != 2 || names[0] != "origin" || names[1] != "upstream" {
		t.Errorf("names = %v, want [origin upstream]", names)
	}
}

func TestRepoRemoteMissing(t *testing.T) {
	f := newFixture(t)
	repo := open(t, f)

	_, err := repo.Remote(ctx(t), "origin")
	if err == nil {
		t.Fatal("found an origin in a repository with no remotes")
	}
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("error is %v, want it to wrap ErrNotFound", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "git remote add origin") {
		t.Errorf("the error does not say how to add one: %v", err)
	}
}
