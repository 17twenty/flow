package core

import "context"

// MaxPages bounds a paginated read, so that a provider looping its own Link
// header cannot make flow walk forever.
const MaxPages = 500

// Paginate walks a paginated collection, following the Link header's "next"
// relation rather than counting pages, and stops as soon as visit says so.
//
// It lives here rather than in an adapter because the Link header is read by
// [Client], and every adapter that reads a page of anything needs the same
// walk; they may not import each other (spec 01 §2).
func Paginate[T any](ctx context.Context, c *Client, req Request, visit func(T) bool) error {
	if c == nil {
		return Errorf(ErrValidation, "read a page of results").
			WithWhy("no HTTP client was given")
	}
	for page := 0; ; page++ {
		if page >= MaxPages {
			return Errorf(ErrUnavailable, "read a page of results").
				WithWhy("the provider offered more than %d pages", MaxPages).
				WithFix("narrow the query's window or limit")
		}
		var items []T
		req.Out = &items
		res, err := c.Do(ctx, req)
		if err != nil {
			return err
		}
		for _, item := range items {
			if !visit(item) {
				return nil
			}
		}
		next := res.Links["next"]
		if next == "" || len(items) == 0 {
			return nil
		}
		// The next URL is absolute and carries the whole query — the
		// window, the page size and the cursor — so the walk follows it
		// as it stands.
		req = Request{Path: next, Header: req.Header}
	}
}
