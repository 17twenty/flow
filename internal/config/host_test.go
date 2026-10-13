package config

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// hostAuth builds a resolver whose only configuration is one provider's
// approved host list.
func hostAuth(provider string, hosts ...string) *Credentials {
	return NewCredentials(env(nil), map[string]AuthSource{provider: {Hosts: hosts}})
}

func TestWhatApprovesAHost(t *testing.T) {
	tests := []struct {
		name     string
		hosts    []string
		host     string
		standard string
		want     bool
	}{
		{
			name:     "the provider's own API host needs nobody's permission",
			host:     "api.github.com",
			standard: "api.github.com",
			want:     true,
		},
		{
			name:     "another host does",
			host:     "ghe.example.com",
			standard: "api.github.com",
			want:     false,
		},
		{
			name:     "the user listed it",
			hosts:    []string{"ghe.example.com"},
			host:     "ghe.example.com",
			standard: "api.github.com",
			want:     true,
		},
		{
			name:     "host names are not case sensitive",
			hosts:    []string{"GHE.Example.COM"},
			host:     "ghe.example.com",
			standard: "api.github.com",
			want:     true,
		},
		{
			name:     "a list that names other hosts approves nothing",
			hosts:    []string{"ghe.example.com", "api.github.com"},
			host:     "evil.example.com",
			standard: "api.github.com",
			want:     false,
		},
		{
			// A provider flow ships no host for — one of spec 11's trackers
			// before its adapter exists — must not have every request
			// approved because two empty strings are equal.
			name:     "no standard host approves no host",
			host:     "",
			standard: "",
			want:     false,
		},
		{
			name:     "and approves no named host either",
			host:     "jira.example.com",
			standard: "",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credentials := hostAuth("github", tt.hosts...)
			if got := credentials.ApprovedHost("github", tt.host, tt.standard); got != tt.want {
				t.Errorf("ApprovedHost(%q, standard %q) = %v, want %v", tt.host, tt.standard, got, tt.want)
			}
		})
	}
}

// An approval given once is remembered for the rest of the run, so a second
// client for the same provider does not ask the same question again.
func TestAnAnswerGivenOnceIsNotAskedAgain(t *testing.T) {
	credentials := hostAuth("github")
	if credentials.ApprovedHost("github", "ghe.example.com", "api.github.com") {
		t.Fatal("a host nobody approved was approved")
	}
	credentials.ApproveHost("github", "ghe.example.com")
	if !credentials.ApprovedHost("github", "ghe.example.com", "api.github.com") {
		t.Error("the answer was not remembered")
	}
	if !credentials.ApprovedHost("github", "GHE.EXAMPLE.COM", "api.github.com") {
		t.Error("the answer was remembered case sensitively")
	}
}

// Agreeing to send one provider's credential somewhere says nothing about
// another provider's: they are two secrets, and the board's token is not the
// forge's even when both answer at the same host.
func TestAnApprovalIsOfOneCredential(t *testing.T) {
	credentials := hostAuth("github")
	credentials.ApproveHost("github", "ghe.example.com")
	if credentials.ApprovedHost("github-projects", "ghe.example.com", "api.github.com") {
		t.Error("approving the forge's credential approved the tracker's")
	}
}

// The approval lives beside the resolved credentials, which are read under a
// different lock; both are reached from the same resolver.
func TestApprovalsAreSafeToShare(t *testing.T) {
	credentials := hostAuth("github", "ghe.example.com")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			credentials.ApproveHost("github", "host-"+string(rune('a'+i))+".example.com")
			credentials.ApprovedHost("github", "ghe.example.com", "api.github.com")
		}()
	}
	wg.Wait()
}

func TestTheRefusalSaysWhatToEdit(t *testing.T) {
	const path = "/home/dev/.config/flow/config.toml"
	err := hostAuth("github").UnapprovedHost("github", "ghe.example.com", "api.github.com", path)

	if core.ExitCode(err) != core.ExitNoPerm {
		t.Errorf("ExitCode = %d, want %d", core.ExitCode(err), core.ExitNoPerm)
	}
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("not a UserError: %v", err)
	}
	switch {
	case !strings.Contains(user.Error(), "ghe.example.com"):
		t.Errorf("%q does not name the host", user.Error())
	case !strings.Contains(user.Why, "api.github.com"):
		t.Errorf("why %q does not say where github does answer", user.Why)
	case !strings.Contains(user.Why, "[auth.github] hosts"):
		t.Errorf("why %q does not name the list that would have approved it", user.Why)
	case !strings.Contains(user.Fix, `hosts = ["ghe.example.com"]`):
		t.Errorf("fix %q is not the line to write", user.Fix)
	case !strings.Contains(user.Fix, path):
		t.Errorf("fix %q does not say which file to write it in", user.Fix)
	}
}

// Nothing flow ships knows where a provider it has no adapter for answers, and
// saying "github answers at " would be worse than saying so.
func TestTheRefusalAdmitsWhenFlowShipsNoHost(t *testing.T) {
	err := hostAuth("jira-cloud").UnapprovedHost("jira-cloud", "jira.example.com", "", "")
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("not a UserError: %v", err)
	}
	if !strings.Contains(user.Why, "ships no API host for jira-cloud") {
		t.Errorf("why %q does not admit flow has no host for the provider", user.Why)
	}
	if !strings.Contains(user.Fix, "your flow config") {
		t.Errorf("fix %q does not describe the file on a machine with no path for it", user.Fix)
	}
}

// Declining is not the same as never being asked: the reason differs, the
// remedy does not.
func TestDecliningSaysSoAndStillSaysWhatToEdit(t *testing.T) {
	err := DeclinedHost("github", "ghe.example.com", "/home/dev/config.toml")
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("not a UserError: %v", err)
	}
	if user.Why != "you said no" {
		t.Errorf("why = %q, want the answer that was given", user.Why)
	}
	if !strings.Contains(user.Fix, `hosts = ["ghe.example.com"]`) {
		t.Errorf("fix %q is not the line to write", user.Fix)
	}
	if core.ExitCode(err) != core.ExitNoPerm {
		t.Errorf("ExitCode = %d, want %d", core.ExitCode(err), core.ExitNoPerm)
	}
}

func TestTheQuestionNamesBothSides(t *testing.T) {
	question := HostQuestion("github-projects", "ghe.example.com")
	if !strings.Contains(question, "github-projects") || !strings.Contains(question, "ghe.example.com") {
		t.Errorf("question %q does not name the credential and the host", question)
	}
	if !strings.HasSuffix(question, "?") {
		t.Errorf("question %q is not one", question)
	}
}

// A table named [auth.github_projects] is a provider nothing asks about, so
// it is never read and nothing says so. The two places anybody finds out are
// the credential that is missing and the host that was not approved, and both
// say it.
func TestAProviderNamedWithTheWrongPunctuationIsPointedAt(t *testing.T) {
	credentials := NewCredentials(env(nil), map[string]AuthSource{
		"github_projects": {TokenCmd: []string{"gh", "auth", "token"}, Hosts: []string{"ghe.example.com"}},
	})

	err := credentials.UnapprovedHost("github-projects", "ghe.example.com", "api.github.com", "/home/dev/config.toml")
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("not a UserError: %v", err)
	}
	if !strings.Contains(user.Why, "[auth.github_projects]") {
		t.Errorf("why %q does not point at the table that was written instead", user.Why)
	}

	_, err = credentials.Token(context.Background(), "github-projects", Auth{})
	if !errors.As(err, &user) {
		t.Fatalf("not a UserError: %v", err)
	}
	if !strings.Contains(user.Why, "[auth.github_projects]") {
		t.Errorf("why %q does not point at the table that was written instead", user.Why)
	}
}

// And a configuration that spells it right is not told it spelled it wrong.
func TestTheRightTableIsNotCorrected(t *testing.T) {
	credentials := hostAuth("github", "api.github.com")
	err := credentials.UnapprovedHost("github", "ghe.example.com", "api.github.com", "/home/dev/config.toml")
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("not a UserError: %v", err)
	}
	if strings.Contains(user.Why, "your config has [auth.") {
		t.Errorf("why %q corrects a spelling that was right", user.Why)
	}
}
