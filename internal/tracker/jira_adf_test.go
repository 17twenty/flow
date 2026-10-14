package tracker

import (
	"encoding/json"
	"strings"
	"testing"
)

// The whole point of the rendering: what flow wrote is what flow reads back.
// A follow-up that already says the right thing must not be rewritten
// (spec 07 §4), and the health report parses its own table out of the issue
// it wrote (spec 07 §10). Both are false the moment a round trip loses a
// character.
func TestADFCarriesFlowsOwnBodyUnchanged(t *testing.T) {
	const body = `flow/policy is not required on main.

## What to do

1. Open the branch protection settings
2. Require the check named flow/policy

## What flow saw

- main requires nothing
- the ruleset is disabled

<!-- flow:followup check=forge.protection repo=acme/widget -->
`
	if got := adfTextOf(adfFromText(body)); got != body {
		t.Errorf("the body came back as\n%q\nwant\n%q", got, body)
	}
}

// Every blank line is a line, and two of them in a row are two. A rendering
// that collapsed them would make the body flow reads back differ from the one
// it wrote, and flow would rewrite the ticket on every run for ever.
func TestADFKeepsEveryLineIncludingTheEmptyOnes(t *testing.T) {
	for _, body := range []string{
		"",
		"\n",
		"one",
		"one\n",
		"one\n\n\n\ntwo",
		"\n\nleading blanks",
		"trailing blanks\n\n\n",
		"   spaces   \n\ttab",
	} {
		if got := adfTextOf(adfFromText(body)); got != body {
			t.Errorf("%q came back as %q", body, got)
		}
	}
}

// A carriage return is not something ADF keeps, and a body that came from an
// editor that writes them must still compare equal to the one flow would
// write. They are normalised on the way in rather than carried.
func TestADFNormalisesTheLineEndingsItWasGiven(t *testing.T) {
	if got := adfTextOf(adfFromText("one\r\ntwo\rthree")); got != "one\ntwo\nthree" {
		t.Errorf("got %q, want the three lines with Unix endings", got)
	}
}

// A paragraph with no content is the node a blank line becomes, and it is
// what makes the round trip exact. Jira accepts it; a text node holding an
// empty string it does not.
func TestADFWritesNoEmptyTextNode(t *testing.T) {
	doc := adfFromText("one\n\ntwo")
	if len(doc.Content) != 3 {
		t.Fatalf("got %d paragraphs for three lines, want 3", len(doc.Content))
	}
	if doc.Type != adfDocType || doc.Version != adfVersion {
		t.Errorf("the document says %q version %d, want %q version %d", doc.Type, doc.Version, adfDocType, adfVersion)
	}
	for i, paragraph := range doc.Content {
		if paragraph.Type != adfParagraph {
			t.Errorf("node %d is %q, want %q", i, paragraph.Type, adfParagraph)
		}
		for _, child := range paragraph.Content {
			if child.Text == "" {
				t.Errorf("node %d holds a text node with no text, which Jira rejects", i)
			}
		}
	}
	if len(doc.Content[1].Content) != 0 {
		t.Errorf("the blank line became %+v, want a paragraph with no content", doc.Content[1].Content)
	}
}

// Somebody else's issue is full of nodes flow has no name for, and the marker
// flow is looking for is in the text of them. A reading that understood only
// its own two nodes would report a described issue as having no description.
func TestADFReadsADocumentFlowDidNotWrite(t *testing.T) {
	const raw = `{
	  "type": "doc", "version": 1,
	  "content": [
	    {"type": "heading", "attrs": {"level": 2},
	     "content": [{"type": "text", "text": "What to do"}]},
	    {"type": "paragraph",
	     "content": [
	       {"type": "text", "text": "ask "},
	       {"type": "text", "text": "someone", "marks": [{"type": "strong"}]},
	       {"type": "hardBreak"},
	       {"type": "text", "text": "then wait"}
	     ]},
	    {"type": "bulletList", "content": [
	      {"type": "listItem", "content": [
	        {"type": "paragraph", "content": [{"type": "text", "text": "first"}]}]},
	      {"type": "listItem", "content": [
	        {"type": "paragraph", "content": [{"type": "text", "text": "second"}]}]}
	    ]},
	    {"type": "rule"}
	  ]
	}`
	var doc adfDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := "What to do\nask someone\nthen wait\nfirst\nsecond\n"
	if got := adfTextOf(doc); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// The field type is what a payload names, so that a description is a string
// everywhere in flow and a document only on the wire.
func TestJiraBodyIsTextOnOneSideAndADocumentOnTheOther(t *testing.T) {
	raw, err := json.Marshal(jiraBody("one\n\ntwo"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"doc"`) {
		t.Errorf("the field went over the wire as %s, want a document", raw)
	}
	var back jiraBody
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != "one\n\ntwo" {
		t.Errorf("the field came back as %q", back)
	}
}

// Jira answers `description: null` for an issue nobody described, and the v2
// API keeps the same field as a plain string. Neither is an error: a body
// flow cannot parse is a ticket flow cannot read, and the marker it is
// looking for is in the text.
func TestJiraBodyReadsTheOtherTwoShapesTheFieldHas(t *testing.T) {
	for _, row := range []struct{ raw, want string }{
		{"null", ""},
		{`"h2. What to do"`, "h2. What to do"},
		{`{"type":"doc","version":1,"content":[]}`, ""},
	} {
		var body jiraBody
		if err := json.Unmarshal([]byte(row.raw), &body); err != nil {
			t.Fatalf("unmarshal(%s): %v", row.raw, err)
		}
		if string(body) != row.want {
			t.Errorf("%s read as %q, want %q", row.raw, body, row.want)
		}
	}

	var body jiraBody
	if err := json.Unmarshal([]byte("[1,2]"), &body); err == nil {
		t.Error("a description that is neither a document nor a string was accepted")
	}
}

// Fuzzing is for the round trip rather than for panics: every body flow ever
// writes has to survive it, and the ones that break a rendering are the ones
// nobody thinks to write down.
func FuzzADFRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"", "\n", "a", "a\nb", "a\n\nb", "## heading\n\n- item\n", "```\ncode\n```\n",
		"<!-- flow:health repo=acme/widget -->\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		// What goes in is what a normalised body is: the rendering is
		// exact for those, and carriage returns are the one thing it is
		// allowed to change.
		body = strings.ReplaceAll(body, "\r\n", "\n")
		body = strings.ReplaceAll(body, "\r", "\n")
		doc := adfFromText(body)
		if got := adfTextOf(doc); got != body {
			t.Fatalf("%q came back as %q", body, got)
		}
		// And what flow sends must be a document Jira will take: no
		// text node may be empty.
		for _, paragraph := range doc.Content {
			for _, child := range paragraph.Content {
				if child.Type == adfText && child.Text == "" {
					t.Fatalf("%q rendered an empty text node", body)
				}
			}
		}
	})
}
