package tracker

import (
	"encoding/json"
	"strings"
)

// Atlassian Document Format: the JSON tree Jira Cloud's v3 API keeps every
// body in. A description is not a string there, and neither is a comment.
//
// Flow's bodies are text, written once in `internal/engine` and read back by
// flow on the next run: a follow-up that already says the right thing must
// not be rewritten (spec 07 §4), and the pinned health report parses its own
// table out of the issue it wrote (spec 07 §10). Both rest on a body coming
// back as what went in.
//
// So flow does not translate its markdown into Jira's formatting. A
// translation flow cannot reverse would make every run find the ticket
// changed and rewrite it, which is worse than plain text that is merely
// plain: the heading stays `## What to do` rather than becoming a heading
// node that reads back as something else (spec 11 §3).
//
// What is carried is the one thing text has and ADF does not come by
// honestly: line structure. One line is one paragraph, and an empty line is
// an empty paragraph, which is valid ADF and exactly reversible.

// adfVersion and adfDocType are what every Jira document carries.
const (
	adfVersion = 1
	adfDocType = "doc"

	adfParagraph = "paragraph"
	adfText      = "text"
)

// adfDoc is an Atlassian Document Format document.
type adfDoc struct {
	Type    string    `json:"type"`
	Version int       `json:"version"`
	Content []adfNode `json:"content"`
}

// adfNode is one node of the tree. Flow writes only paragraphs and text, and
// reads whatever it is given: an issue somebody wrote by hand is full of
// nodes flow has no name for, and every one of them has text somewhere.
type adfNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text,omitempty"`
	Content []adfNode `json:"content,omitempty"`
}

// adfFromText renders text as a document.
//
// One line is one paragraph. A blank line is an empty paragraph, because a
// paragraph with no content is valid ADF and is the only node that reads back
// as a blank line. Carriage returns are dropped on the way in: ADF text is
// not a place to keep them, and a body that came from a Windows editor must
// still compare equal to the one flow would write.
func adfFromText(body string) adfDoc {
	normalised := strings.ReplaceAll(body, "\r\n", "\n")
	normalised = strings.ReplaceAll(normalised, "\r", "\n")

	lines := strings.Split(normalised, "\n")
	doc := adfDoc{Type: adfDocType, Version: adfVersion, Content: make([]adfNode, 0, len(lines))}
	for _, line := range lines {
		paragraph := adfNode{Type: adfParagraph}
		if line != "" {
			paragraph.Content = []adfNode{{Type: adfText, Text: line}}
		}
		doc.Content = append(doc.Content, paragraph)
	}
	return doc
}

// adfText reads a document back as text.
//
// Flow's own documents are paragraphs of one line each and come back exactly
// as they went in. Somebody else's are not, and the reading has to be useful
// anyway: every block node becomes a line and every text node inside it is
// joined, so a bulleted list written in Jira reads as its items rather than
// as nothing. A hard break inside a paragraph is a line of its own, because
// that is what somebody pressing shift-return meant.
func adfTextOf(doc adfDoc) string {
	var lines []string
	for _, node := range doc.Content {
		lines = append(lines, blockLines(node)...)
	}
	return strings.Join(lines, "\n")
}

// blockLines is the lines one block node contributes.
func blockLines(node adfNode) []string {
	if node.Type == adfText {
		return []string{node.Text}
	}
	if len(node.Content) == 0 {
		return []string{""}
	}

	// A node whose children are themselves blocks — a list, a table, a
	// quote — contributes each child's lines. One whose children are
	// inline nodes contributes one line made of them.
	if blocky(node) {
		var lines []string
		for _, child := range node.Content {
			lines = append(lines, blockLines(child)...)
		}
		return lines
	}
	return []string{inlineText(node)}
}

// blocky reports whether a node's children are blocks rather than inline
// text. ADF does not say so in the payload, so it is read off the children:
// a node holding a paragraph, a list item or a row holds blocks.
func blocky(node adfNode) bool {
	for _, child := range node.Content {
		switch child.Type {
		case adfText, "hardBreak", "emoji", "mention", "inlineCard", "date", "status":
			return false
		}
		return true
	}
	return false
}

// inlineText joins the text of one block's inline children. A hard break
// inside it is the one inline node that is not text, and it ends the line.
func inlineText(node adfNode) string {
	var b strings.Builder
	for _, child := range node.Content {
		switch child.Type {
		case adfText:
			b.WriteString(child.Text)
		case "hardBreak":
			b.WriteString("\n")
		default:
			b.WriteString(inlineText(child))
		}
	}
	return b.String()
}

// jiraBody is a field Jira keeps as a document and flow keeps as text. It
// unmarshals from ADF and marshals back to it, so that a payload type can
// name a description or a comment as the string it is everywhere else.
type jiraBody string

// MarshalJSON renders the text as a document.
func (b jiraBody) MarshalJSON() ([]byte, error) {
	return json.Marshal(adfFromText(string(b)))
}

// UnmarshalJSON reads a document back as text.
//
// The v2 API — which some integrations and every webhook payload still write
// through — keeps the same field as a plain string, and that reads as the
// text it is rather than as an error: a body flow cannot parse is a ticket
// flow cannot read, and the marker it is looking for is in the text.
//
// `description: null`, which is what an issue nobody described has, needs no
// branch of its own. Decoding null into a document leaves the zero document,
// whose text is the empty string, which is the answer.
func (b *jiraBody) UnmarshalJSON(data []byte) error {
	if strings.HasPrefix(strings.TrimSpace(string(data)), `"`) {
		var plain string
		if err := json.Unmarshal(data, &plain); err != nil {
			return err
		}
		*b = jiraBody(plain)
		return nil
	}
	var doc adfDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	*b = jiraBody(adfTextOf(doc))
	return nil
}
