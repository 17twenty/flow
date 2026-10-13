package deploys

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// ghRepo is the application repository a deployment source reads, and the
// conventions every GitHub call about it shares: the media type, the dated
// API version and the path the records hang under.
//
// Both sources need all four, and two copies of a thing this small drift
// apart in exactly the way a missing API version header does — silently, and
// only on the provider's side.
type ghRepo struct {
	client *core.Client
	owner  string
	repo   string
}

// slug is the repository the records are read from, as owner/name.
func (r ghRepo) slug() string { return r.owner + "/" + r.repo }

// path builds a path under this repository. Every segment below it is a
// literal or a decimal identifier, so only the repository's own name needs
// escaping.
func (r ghRepo) path(segments ...string) string {
	parts := append([]string{"repos", url.PathEscape(r.owner), url.PathEscape(r.repo)}, segments...)
	return strings.Join(parts, "/")
}

// decorate returns the request with GitHub's media type and API version on it.
func (r ghRepo) decorate(req core.Request) core.Request {
	header := http.Header{}
	for name, values := range req.Header {
		header[name] = slices.Clone(values)
	}
	header.Set("Accept", gitHubAccept)
	header.Set("X-GitHub-Api-Version", gitHubAPIVersion)
	req.Header = header
	return req
}

// do sends one request with GitHub's media type and API version.
func (r ghRepo) do(ctx context.Context, req core.Request) (*core.Response, error) {
	return r.client.Do(ctx, r.decorate(req))
}

// validate reports whether a source can be built for this repository at all.
// The caller names itself, because "build the github deployment source" and
// "build the commit status deployment source" are different problems to the
// person reading the error.
func (r ghRepo) validate(what string) error {
	switch {
	case r.client == nil:
		return core.Errorf(core.ErrValidation, "build the %s", what).
			WithWhy("no HTTP client was given")
	case r.owner == "" || r.repo == "":
		return core.Errorf(core.ErrValidation, "build the %s", what).
			WithWhy("the repository was given as %q/%q", r.owner, r.repo).
			WithFix("name the application repository as owner/name")
	}
	return nil
}
