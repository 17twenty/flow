package git_test

import (
	"reflect"
	"testing"

	"github.com/17twenty/flow/internal/git"
)

func TestTrailers(t *testing.T) {
	f := newFixture(t)
	repo := open(t, f)

	tests := []struct {
		name    string
		message string
		want    []git.Trailer
	}{
		{
			name:    "no trailers",
			message: "feat(cli): add --json\n\nJust a body.\n",
		},
		{
			name:    "empty message",
			message: "",
		},
		{
			name:    "one trailer",
			message: "feat(cli): add --json\n\nWhy it was needed.\n\nRefs: #12\n",
			want:    []git.Trailer{{Key: "Refs", Value: "#12"}},
		},
		{
			name:    "several trailers",
			message: "feat(ui): new checkout\n\nBody.\n\nRefs: #12\nFlag-Added: checkout_v2\nCo-authored-by: Ada <ada@example.com>\n",
			want: []git.Trailer{
				{Key: "Refs", Value: "#12"},
				{Key: "Flag-Added", Value: "checkout_v2"},
				{Key: "Co-authored-by", Value: "Ada <ada@example.com>"},
			},
		},
		{
			name:    "a folded value comes back as one line",
			message: "fix(core): tidy\n\nBody.\n\nRefs: #12\n  and #13\n",
			want:    []git.Trailer{{Key: "Refs", Value: "#12 and #13"}},
		},
		{
			name:    "a header that looks like a trailer is not one",
			message: "Refs: #12\n",
		},
		{
			// git requires a trailer key to be one token, so Conventional
			// Commits' `BREAKING CHANGE:` is body text to it. internal/commits
			// finds that footer itself (spec 08 §2); this is here so the
			// difference is recorded rather than discovered.
			name:    "BREAKING CHANGE is not a git trailer",
			message: "feat(api)!: drop v1\n\nBody.\n\nBREAKING CHANGE: the v1 endpoints are gone\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.Trailers(ctx(t), tt.message)
			if err != nil {
				t.Fatalf("Trailers: %v", err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Trailers()\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestTrailerLookup(t *testing.T) {
	t.Parallel()
	trailers := []git.Trailer{
		{Key: "Refs", Value: "#12"},
		{Key: "Flag-Added", Value: "checkout_v2"},
		{Key: "refs", Value: "#13"},
	}

	// Keys are conventionally case-insensitive, and a later trailer amends an
	// earlier one.
	if value, ok := git.Lookup(trailers, "REFS"); !ok || value != "#13" {
		t.Errorf("Lookup(REFS) = %q, %v; want \"#13\", true", value, ok)
	}
	if _, ok := git.Lookup(trailers, "Reviewed-by"); ok {
		t.Error("Lookup found a trailer that is not there")
	}
	if got := git.Values(trailers, "refs"); !reflect.DeepEqual(got, []string{"#12", "#13"}) {
		t.Errorf("Values(refs) = %v, want [#12 #13]", got)
	}
	if got := git.Values(trailers, "nothing"); got != nil {
		t.Errorf("Values for an absent key = %v, want nil", got)
	}
}

// Trailers are read with git so that a repository's own trailer.* settings are
// respected rather than reimplemented (spec 08).
func TestTrailersHonourRepositoryConfiguration(t *testing.T) {
	f := newFixture(t)
	f.git("config", "trailer.ref.key", "Refs: ")
	repo := open(t, f)

	got, err := repo.Trailers(ctx(t), "fix(core): tidy\n\nBody.\n\nRefs: #12\n")
	if err != nil {
		t.Fatalf("Trailers: %v", err)
	}
	if len(got) != 1 || got[0].Key != "Refs" || got[0].Value != "#12" {
		t.Errorf("Trailers() = %+v", got)
	}
}
