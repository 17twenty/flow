package config

import (
	"fmt"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// ApprovedHost reports whether the credential flow holds for provider may be
// sent to host.
//
// Two things approve a host, and nothing else does: it is the host the
// provider itself answers at — standard, which flow ships rather than reads —
// or the person running flow listed it under `[auth.<provider>] hosts` in a
// file the repository cannot write (spec 15 §1). A host approved for this run
// by [Credentials.ApproveHost] counts as listed.
//
// Hosts are compared by name, case-folded: `.flow.local.toml` may carry a
// hosts list but may not carry a port — [checkPersonalValues] refuses a colon
// — so a port is never part of what was approved.
func (c *Credentials) ApprovedHost(provider, host, standard string) bool {
	if sameHost(host, standard) {
		return true
	}
	for _, listed := range c.auth[provider].Hosts {
		if sameHost(host, listed) {
			return true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.approved[approval(provider, host)]
}

// ApproveHost records that somebody agreed to send provider's credential to
// host. The approval lasts for this run and goes no further: flow does not
// write to the user's configuration behind their back, and the refusal it
// would otherwise print says how to make the answer permanent.
func (c *Credentials) ApproveHost(provider, host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.approved == nil {
		c.approved = map[string]bool{}
	}
	c.approved[approval(provider, host)] = true
}

// approval is the key one answer is remembered under. Provider and host are
// both part of it: two providers on the same host are two credentials, and
// agreeing to send one of them there says nothing about the other.
func approval(provider, host string) string {
	return strings.ToLower(provider) + "\x00" + strings.ToLower(host)
}

// sameHost reports whether two host names name the same host. Neither may be
// empty: a provider flow ships no default host for has no standard host, and
// that must not approve a request to nowhere.
func sameHost(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(a, b)
}

// HostQuestion is how flow asks a person whether a credential may go to a host
// it does not already approve of.
func HostQuestion(provider, host string) string {
	return "send the " + provider + " credential to " + host + "?"
}

// UnapprovedHost is the refusal to send a credential to a host nobody has
// approved, when there was nobody to ask (spec 15 §1, scenario S02). path is
// where the user's own configuration lives, which is where the answer
// belongs; it may be empty on a machine with no home directory.
func (c *Credentials) UnapprovedHost(provider, host, standard, path string) error {
	return hostRefusal(provider, host, path).
		WithWhy("%s, and %s does not list it%s", standardly(provider, standard), hostsTable(provider, path), nearMiss(c.auth, provider))
}

// DeclinedHost is the refusal to send a credential to a host somebody was
// asked about and said no to.
func DeclinedHost(provider, host, path string) error {
	return hostRefusal(provider, host, path).WithWhy("you said no")
}

// hostRefusal is what both refusals have in common: the action that did not
// happen, and the one edit that would let it happen next time.
func hostRefusal(provider, host, path string) *core.UserError {
	return core.Errorf(core.ErrPermission, "send the %s credential to %s", provider, host).
		WithFix("if %s is yours, add hosts = [%q] under [auth.%s] in %s", host, host, provider, where(path))
}

// standardly says where the provider answers when nobody has configured
// anything, or that flow does not know.
func standardly(provider, standard string) string {
	if standard == "" {
		return "flow ships no API host for " + provider
	}
	return provider + " answers at " + standard
}

// hostsTable names the list that would have approved the host, as the person
// reading this would have to write it.
func hostsTable(provider, path string) string {
	return "[auth." + provider + "] hosts in " + where(path)
}

// where names the user's configuration file, or describes it for a machine
// where flow could not work out a path.
func where(path string) string {
	if path == "" {
		return "your flow config"
	}
	return path
}

// nearMiss describes a table in the user's configuration that names this
// provider with the wrong punctuation, and is empty when there is none.
//
// The table is keyed by the adapter kind exactly as `.flow.yaml` spells it —
// `github-projects`, with a hyphen — and a `[auth.github_projects]` is read
// as a provider nothing asks about, so it is silently never used. The one
// place anybody notices is here, where the credential or the host it would
// have approved is missing.
func nearMiss(auth map[string]AuthSource, provider string) string {
	for _, name := range sortedKeys(auth) {
		if name != provider && punctuationFolded(name) == punctuationFolded(provider) {
			return fmt.Sprintf(" (your config has [auth.%s]; the table is named after the provider exactly as .flow.yaml spells it)", name)
		}
	}
	return ""
}

// punctuationFolded is a provider name with the difference between a hyphen
// and an underscore taken out of it.
func punctuationFolded(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}
