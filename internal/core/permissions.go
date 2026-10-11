package core

import (
	"slices"
	"strings"
)

// Permissions is what a credential turned out to be able to do, as reported by
// an adapter's Probe. Tokens are the provider's own vocabulary — `admin`,
// `project_write`, `statuses_write` — the same words a capability declaration
// lists in `requires` (spec 05 §1, §3).
//
// Evidence records how flow found out, so that a refusal can say what it saw
// rather than only what it wanted.
type Permissions struct {
	// Tokens are the permissions the credential holds.
	Tokens []string
	// Evidence is what flow read to decide that, one line each: for example
	// "repository permissions: maintain" or "X-OAuth-Scopes: repo, project".
	Evidence []string
}

// Has reports whether the credential holds one permission.
func (p Permissions) Has(token string) bool { return slices.Contains(p.Tokens, token) }

// Missing returns the required tokens the credential does not hold, in the
// order they were required. An empty result means every one is held.
func (p Permissions) Missing(required []string) []string {
	var missing []string
	for _, token := range required {
		if !p.Has(token) {
			missing = append(missing, token)
		}
	}
	return missing
}

// Allows reports whether the credential holds every required permission.
func (p Permissions) Allows(required []string) bool { return len(p.Missing(required)) == 0 }

// String renders the permissions for a log line.
func (p Permissions) String() string {
	if len(p.Tokens) == 0 {
		return "no permissions"
	}
	return strings.Join(p.Tokens, ", ")
}
