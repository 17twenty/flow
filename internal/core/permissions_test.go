package core_test

import (
	"slices"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

func TestPermissions(t *testing.T) {
	granted := core.Permissions{
		Tokens:   []string{"project_write", "issues_write"},
		Evidence: []string{"X-OAuth-Scopes: repo, project"},
	}

	if !granted.Has("project_write") {
		t.Error("Has missed a token the credential holds")
	}
	if granted.Has("admin") {
		t.Error("Has claimed a token the credential does not hold")
	}
	if !granted.Allows([]string{"project_write", "issues_write"}) {
		t.Error("Allows refused permissions that are all held")
	}
	if granted.Allows([]string{"project_write", "admin"}) {
		t.Error("Allows accepted a requirement that is only half met")
	}
	if got := granted.Missing([]string{"admin", "project_write", "repo"}); !slices.Equal(got, []string{"admin", "repo"}) {
		t.Errorf("Missing = %v, want [admin repo] in the order they were required", got)
	}
	if got := granted.String(); got != "project_write, issues_write" {
		t.Errorf("String = %q", got)
	}
}

// The zero Permissions is what flow has before it has probed, and the safe
// reading of it is that nothing is allowed.
func TestZeroPermissionsAllowNothing(t *testing.T) {
	var none core.Permissions

	if none.Has("admin") {
		t.Error("the zero Permissions claimed a token")
	}
	if !none.Allows(nil) {
		t.Error("requiring nothing is allowed even with no permissions at all")
	}
	if none.Allows([]string{"admin"}) {
		t.Error("the zero Permissions allowed a write")
	}
	if got := none.String(); got != "no permissions" {
		t.Errorf("String = %q, want %q", got, "no permissions")
	}
}
