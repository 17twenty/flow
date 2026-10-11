package core_test

import (
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

func TestMarkerRendering(t *testing.T) {
	tests := []struct {
		name   string
		marker core.Marker
		want   string
	}{
		{
			name:   "a kind on its own",
			marker: core.NewMarker(core.MarkerPolicy),
			want:   "flow:policy",
		},
		{
			name:   "fields in sorted order, whatever order they were given",
			marker: core.NewMarker(core.MarkerFollowUp, "repo", "17twenty/flow", "check", "config.semantics"),
			want:   `flow:followup check=config.semantics repo=17twenty/flow`,
		},
		{
			name:   "a value with a space is quoted",
			marker: core.NewMarker(core.MarkerFlag, "name", "new checkout"),
			want:   `flow:flag name="new checkout"`,
		},
		{
			name:   "an empty value is quoted, so the field is still a field",
			marker: core.NewMarker(core.MarkerFlag, "name", ""),
			want:   `flow:flag name=""`,
		},
		{
			name:   "an odd trailing key narrows nothing and is dropped",
			marker: core.NewMarker(core.MarkerHealth, "repo"),
			want:   "flow:health",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.marker.String(); got != test.want {
				t.Errorf("String() = %q, want %q", got, test.want)
			}
			if got, want := test.marker.HTML(), "<!-- "+test.want+" -->"; got != want {
				t.Errorf("HTML() = %q, want %q", got, want)
			}
		})
	}
}

// Both renderings must read back, whichever provider wrote them: a follow-up
// filed on GitHub and one filed on Linear are the same follow-up.
func TestMarkerRoundTrip(t *testing.T) {
	markers := []core.Marker{
		core.NewMarker(core.MarkerPolicy),
		core.NewMarker(core.MarkerFollowUp, "check", "tracker.columns", "repo", "17twenty/flow"),
		core.NewMarker(core.MarkerFlag, "name", "new checkout = better"),
		core.NewMarker(core.MarkerHealth, "repo", `a "quoted" one`),
		core.NewMarker(core.MarkerFlag, "name", `back\slash`),
	}

	for _, want := range markers {
		for _, rendered := range []string{want.String(), want.HTML(), "  " + want.HTML() + "  "} {
			got, ok := core.ParseMarker(rendered)
			if !ok {
				t.Errorf("ParseMarker(%q) failed", rendered)
				continue
			}
			if !got.Equal(want) {
				t.Errorf("ParseMarker(%q) = %+v, want %+v", rendered, got, want)
			}
		}
	}
}

func TestParseMarkerRefuses(t *testing.T) {
	for _, line := range []string{
		"",
		"just a comment",
		"<!-- a plain html comment -->",
		"<!-- flow:policy",
		"flow:",
		"flow:policy check",
		`flow:policy ="value"`,
		`flow:policy check="unterminated`,
		`flow:policy check=a"b`,
		"not flow:policy at the start",
	} {
		if marker, ok := core.ParseMarker(line); ok {
			t.Errorf("ParseMarker(%q) = %+v, want it refused", line, marker)
		}
	}
}

func TestMarkerMatches(t *testing.T) {
	written := core.NewMarker(core.MarkerFollowUp, "check", "tracker.columns", "repo", "17twenty/flow")

	for _, search := range []core.Marker{
		core.NewMarker(core.MarkerFollowUp),
		core.NewMarker(core.MarkerFollowUp, "check", "tracker.columns"),
		written,
	} {
		if !search.Matches(written) {
			t.Errorf("%s does not match %s, but it names nothing the other contradicts", search, written)
		}
	}

	for _, search := range []core.Marker{
		core.NewMarker(core.MarkerPolicy, "check", "tracker.columns"),
		core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip"),
		core.NewMarker(core.MarkerFollowUp, "pr", "12"),
	} {
		if search.Matches(written) {
			t.Errorf("%s matched %s", search, written)
		}
	}

	// Matching is one-way: a search naming nothing finds everything of its
	// kind, but a specific marker is not found by searching for another.
	if written.Matches(core.NewMarker(core.MarkerFollowUp)) {
		t.Error("a marker with fields matched a search that has none of them")
	}
	if !written.Equal(written) || written.Equal(core.NewMarker(core.MarkerFollowUp)) {
		t.Error("Equal is not equality")
	}
	var zero core.Marker
	if !zero.IsZero() {
		t.Error("the zero Marker identifies nothing")
	}
}

func TestFindAndStripMarkers(t *testing.T) {
	body := "### Policy\n\nTwo rules failed.\n\n<!-- flow:policy pr=12 -->\n"

	marker, ok := core.FindMarker(body)
	if !ok {
		t.Fatal("FindMarker found nothing in a body flow wrote")
	}
	if !marker.Equal(core.NewMarker(core.MarkerPolicy, "pr", "12")) {
		t.Errorf("FindMarker = %+v", marker)
	}
	if !core.HasMarker(body, core.NewMarker(core.MarkerPolicy)) {
		t.Error("HasMarker missed a marker of the kind asked for")
	}
	if core.HasMarker(body, core.NewMarker(core.MarkerPolicy, "pr", "13")) {
		t.Error("HasMarker matched another pull request's comment")
	}

	if got := core.StripMarkers(body); strings.Contains(got, "flow:") {
		t.Errorf("StripMarkers left a marker behind: %q", got)
	} else if !strings.Contains(got, "Two rules failed.") {
		t.Errorf("StripMarkers took the body with it: %q", got)
	}

	if _, ok := core.FindMarker("nothing of ours here\n"); ok {
		t.Error("FindMarker invented a marker")
	}
}

// Rewriting a comment must replace the marker, not accumulate one per edit.
func TestMarkBodyIsIdempotent(t *testing.T) {
	marker := core.NewMarker(core.MarkerPolicy, "pr", "12")

	once := core.MarkBody("Two rules failed.", marker, true)
	twice := core.MarkBody(once, marker, true)
	if once != twice {
		t.Errorf("marking twice changed the body:\n%q\n%q", once, twice)
	}
	if strings.Count(once, "flow:policy") != 1 {
		t.Errorf("body carries more than one marker:\n%q", once)
	}
	if !strings.HasSuffix(once, marker.HTML()+"\n") {
		t.Errorf("the marker is not the last line:\n%q", once)
	}

	visible := core.MarkBody(once, marker, false)
	if strings.Contains(visible, "<!--") {
		t.Errorf("re-marking for a tracker that strips HTML left the comment form:\n%q", visible)
	}
	if !strings.HasSuffix(visible, marker.String()+"\n") {
		t.Errorf("the visible marker is not the last line:\n%q", visible)
	}
	if got, ok := core.FindMarker(visible); !ok || !got.Equal(marker) {
		t.Errorf("a re-marked body lost its marker: %q", visible)
	}

	if got := core.MarkBody("", marker, false); got != marker.String()+"\n" {
		t.Errorf("marking an empty body = %q", got)
	}
}

func FuzzMarkerParse(f *testing.F) {
	f.Add("flow:policy pr=12")
	f.Add("<!-- flow:followup check=config.semantics repo=17twenty/flow -->")
	f.Add(`flow:flag name="new checkout"`)
	f.Add(`flow:flag name="\"`)
	f.Add("<!-- flow: -->")
	f.Add("flow:policy =")
	f.Add("flow:policy a=b=c")

	f.Fuzz(func(t *testing.T, line string) {
		marker, ok := core.ParseMarker(line)
		if !ok {
			return
		}
		if marker.Kind == "" {
			t.Fatalf("ParseMarker(%q) accepted a marker with no kind", line)
		}
		// Anything flow accepts, it must be able to write back and read again
		// as the same marker, or an update would file a duplicate instead.
		again, ok := core.ParseMarker(marker.String())
		if !ok {
			t.Fatalf("ParseMarker(%q) = %+v, which does not render back as a marker: %q", line, marker, marker.String())
		}
		if !again.Equal(marker) {
			t.Fatalf("round trip changed %+v into %+v (%q)", marker, again, marker.String())
		}
		if html, ok := core.ParseMarker(marker.HTML()); !ok || !html.Equal(marker) {
			t.Fatalf("HTML round trip changed %+v into %+v", marker, html)
		}
	})
}
