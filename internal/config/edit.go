package config

import (
	"bytes"
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/17twenty/flow/internal/core"
)

// Editing a configuration document in place (spec 03 §7).
//
// `flow init` starts from a shipped preset and fills in what it knows: the
// repository, the forge, the board. A preset is mostly explanation — every key
// has a comment beside it saying what the alternatives are — and that
// explanation is the point of shipping one, so the file a team ends up with
// has to be the file flow ships with a few values changed, and not a
// re-encoding of it.
//
// Decoding to a struct and marshalling back loses every comment. Decoding to a
// yaml.Node and marshalling back keeps the comments but loses their alignment,
// reflows the flow mappings and re-indents the sequences, which turns a
// one-line diff into a whole-file one. So [Set] does neither: it parses the
// document only to find out where a value is, and then replaces those bytes
// and no others.

// Set returns src with the value at the dotted key path replaced, leaving
// every other byte of the document exactly as it was. A key that its parent
// mapping does not have yet is inserted at the end of that mapping.
//
// The edit is verified before it is returned: the result is re-parsed, the
// path must now hold value, and no other value in the document may have
// changed. A document flow cannot prove it edited correctly is not returned at
// all, because the alternative is writing a corrupted `.flow.yaml` and finding
// out when somebody runs doctor.
func Set(src []byte, keyPath, value string) ([]byte, error) {
	path := strings.Split(keyPath, ".")
	if keyPath == "" || slicesHasEmpty(path) {
		return nil, core.Errorf(core.ErrValidation, "set %q", keyPath).
			WithWhy("a key path is one or more dotted names").
			WithFix("use a path such as tracker.project")
	}

	edited, err := editValue(src, path, value)
	if err != nil {
		return nil, err
	}
	if err := verifySet(src, edited, keyPath, value); err != nil {
		return nil, err
	}
	return edited, nil
}

// slicesHasEmpty reports whether any segment is empty, which is what a leading,
// trailing or doubled dot produces.
func slicesHasEmpty(path []string) bool {
	for _, segment := range path {
		if segment == "" {
			return true
		}
	}
	return false
}

// editValue finds the value at path and replaces or inserts it.
func editValue(src []byte, path []string, value string) ([]byte, error) {
	root, err := documentRoot(src)
	if err != nil {
		return nil, err
	}

	parent, flow, err := walkTo(root, path[:len(path)-1], false)
	if err != nil {
		return nil, err
	}
	name := path[len(path)-1]
	if parent.Kind != yaml.MappingNode {
		return nil, core.Errorf(core.ErrNotFound, "find %s", strings.Join(path, ".")).
			WithWhy("its parent holds a single value, not a section").
			WithFix("check the key path against the shipped preset")
	}
	_, existing := mappingEntry(parent, name)
	if existing == nil {
		return insertKey(src, parent, name, value)
	}
	if existing.Kind != yaml.ScalarNode {
		return nil, core.Errorf(core.ErrConflict, "set %s", strings.Join(path, ".")).
			WithWhy("that key holds a list or a mapping, not a single value").
			WithFix("edit the file by hand")
	}
	return replaceScalar(src, existing, value, flow || isFlow(parent))
}

// documentRoot parses src and returns the mapping at the top of the document.
func documentRoot(src []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, core.Errorf(core.ErrValidation, "read the configuration").
			WithWhy("%s", err).
			WithFix("fix the YAML syntax first")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, core.Errorf(core.ErrValidation, "read the configuration").
			WithWhy("the document is empty").
			WithFix("start from a preset: flow init --preset lean-kanban")
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, core.Errorf(core.ErrValidation, "read the configuration").
			WithWhy("the document is not a mapping of keys to values").
			WithFix("start from a preset: flow init --preset lean-kanban")
	}
	return doc.Content[0], nil
}

// walkTo descends path from node, reporting whether anything on the way was
// written in flow style — which decides where a scalar's token ends.
func walkTo(node *yaml.Node, path []string, flow bool) (*yaml.Node, bool, error) {
	flow = flow || isFlow(node)
	if len(path) == 0 {
		return node, flow, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, false, core.Errorf(core.ErrNotFound, "find %s", path[0]).
			WithWhy("its parent is not a mapping").
			WithFix("edit the file by hand")
	}
	_, value := mappingEntry(node, path[0])
	if value == nil {
		return nil, false, core.Errorf(core.ErrNotFound, "find %s", path[0]).
			WithWhy("the document has no such section").
			WithFix("add the section by hand, or start again from a preset")
	}
	return walkTo(value, path[1:], flow)
}

// mappingEntry returns the key and value nodes for name in a mapping.
func mappingEntry(node *yaml.Node, name string) (key, value *yaml.Node) {
	if node.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == name {
			return node.Content[i], node.Content[i+1]
		}
	}
	return nil, nil
}

// isFlow reports whether a node was written in flow style: `{a: 1}` rather
// than an indented block.
func isFlow(node *yaml.Node) bool { return node != nil && node.Style&yaml.FlowStyle != 0 }

// replaceScalar swaps the bytes a scalar occupies for a new value, rendered so
// that it parses back as the string it is.
//
// A value with a comment beside it is padded back out to the comment's
// original column when the new value is shorter. The presets are written in
// columns — every key has its explanation lined up with the others — and a
// file whose comments have gone ragged reads like something a script got at,
// which is the opposite of what shipping a commented preset is for. Nothing
// but a comment is realigned: the ` }` closing a flow mapping is punctuation,
// and pushing it away from its value would be worse than leaving it.
func replaceScalar(src []byte, node *yaml.Node, value string, flow bool) ([]byte, error) {
	lines := splitLines(src)
	index := node.Line - 1
	if index < 0 || index >= len(lines) {
		return nil, internalEditError("the value is not on a line of the file")
	}
	line := lines[index]
	start := node.Column - 1
	if start < 0 || start > len(line) {
		return nil, internalEditError("the value does not start where the parser said")
	}
	end := scalarEnd(line, start, node.Style, flow)
	pad := commentStart(line, start) >= 0

	for _, rendered := range renderings(value) {
		if pad && len(rendered) < end-start {
			rendered += strings.Repeat(" ", end-start-len(rendered))
		}
		candidate := append([]byte{}, line[:start]...)
		candidate = append(candidate, rendered...)
		candidate = append(candidate, line[end:]...)
		edited := append([]string{}, lines...)
		edited[index] = string(candidate)
		if parsesAs(strings.Join(edited, "\n"), node.Line, value) {
			return []byte(strings.Join(edited, "\n")), nil
		}
	}
	return nil, internalEditError("no rendering of the new value parses back as itself")
}

// scalarEnd finds the byte after the last one belonging to the scalar that
// starts at line[start].
//
// A quoted scalar ends at its closing quote. A plain one runs to the end of
// the line, less any trailing comment — a `#` only starts a comment when
// something separates it from the value — and, inside a flow collection, less
// the punctuation that closes the collection. A `}` is a comma in `{a: b}` and
// an ordinary character in `a: b}c`, which is why flow style is tracked all
// the way down from the root rather than read off the scalar.
func scalarEnd(line string, start int, style yaml.Style, flow bool) int {
	switch {
	case style&yaml.SingleQuotedStyle != 0:
		return quotedEnd(line, start, '\'')
	case style&yaml.DoubleQuotedStyle != 0:
		return quotedEnd(line, start, '"')
	}
	end := len(line)
	if comment := commentStart(line, start); comment >= 0 {
		end = comment
	}
	cutset := " \t"
	if flow {
		cutset += ",}]"
	}
	for end > start && strings.ContainsRune(cutset, rune(line[end-1])) {
		end--
	}
	return end
}

// quotedEnd finds the byte after a quoted scalar's closing quote. A doubled
// quote inside a single-quoted scalar is an escaped quote, and a backslash
// escapes the next byte inside a double-quoted one.
func quotedEnd(line string, start int, quote byte) int {
	for i := start + 1; i < len(line); i++ {
		switch {
		case quote == '"' && line[i] == '\\':
			i++
		case line[i] != quote:
		case quote == '\'' && i+1 < len(line) && line[i+1] == '\'':
			i++
		default:
			return i + 1
		}
	}
	return len(line)
}

// commentStart finds the `#` that begins a trailing comment, or -1. The first
// byte of a scalar is never a comment, however it looks: the parser has
// already decided that much.
func commentStart(line string, start int) int {
	for i := start + 1; i < len(line); i++ {
		if line[i] == '#' && (line[i-1] == ' ' || line[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// renderings are the ways to write value, cheapest first. Most values need no
// quoting at all; the ones that do are better served by yaml's own choice than
// by a rule written here; and double quotes are the fallback that always works,
// including inside a flow mapping, where a comma would otherwise end the value.
func renderings(value string) []string {
	out := []string{value}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	if err := encoder.Encode(value); err == nil {
		_ = encoder.Close()
		if marshalled := strings.TrimRight(buf.String(), "\n"); marshalled != value {
			out = append(out, marshalled)
		}
	}
	return append(out, doubleQuote(value))
}

// doubleQuote writes value as a YAML double-quoted scalar.
func doubleQuote(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// parsesAs reports whether the document holds value as a string scalar on the
// given line. It is how a candidate rendering is accepted: not by reasoning
// about quoting rules, but by reading the result back.
//
// The tag is half the test. A bare `7` has the text of the string "7" and the
// meaning of the number 7, and a configuration that means a number where it
// said a string is a bug waiting for whoever decodes it — so a rendering is
// accepted only once YAML agrees it is a string.
func parsesAs(document string, line int, value string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(document), &doc); err != nil {
		return false
	}
	found := false
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node == nil || found {
			return
		}
		if node.Kind == yaml.ScalarNode && node.Line == line && node.Value == value && node.Tag == "!!str" {
			found = true
			return
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(&doc)
	return found
}

// insertKey adds a key the mapping does not have, at the end of it and at its
// own keys' indentation. A flow mapping is refused: `{a: 1}` has no line of its
// own to grow into, and rewriting it would undo the formatting this whole file
// exists to preserve.
func insertKey(src []byte, parent *yaml.Node, name, value string) ([]byte, error) {
	for _, rendered := range renderings(value) {
		edited, at, err := insertLine(src, parent, name, rendered)
		if err != nil {
			return nil, err
		}
		if parsesAs(string(edited), at, value) {
			return edited, nil
		}
	}
	return nil, internalEditError("no rendering of the new value parses back as itself")
}

// insertLine puts `name: rendered` on a line of its own after the last line
// of a block mapping, and says which line that is.
func insertLine(src []byte, parent *yaml.Node, name, rendered string) ([]byte, int, error) {
	if parent.Kind != yaml.MappingNode || isFlow(parent) || len(parent.Content) == 0 {
		return nil, 0, core.Errorf(core.ErrConflict, "add %s", name).
			WithWhy("its section is not a block mapping flow can grow").
			WithFix("add the key by hand")
	}
	indent := strings.Repeat(" ", parent.Content[0].Column-1)
	at := lastLine(parent)

	lines := splitLines(src)
	if at < 1 || at > len(lines) {
		return nil, 0, internalEditError("the section does not end on a line of the file")
	}
	edited := append([]string{}, lines[:at]...)
	edited = append(edited, indent+name+": "+rendered)
	edited = append(edited, lines[at:]...)
	return []byte(strings.Join(edited, "\n")), at + 1, nil
}

// SetList returns src with a list of strings added at the dotted key path,
// written on one line in flow style — `contexts: ['deploy/production']` —
// which is how the presets write a short list beside its comment.
//
// It only adds. A list the document already holds has items somebody chose
// and maybe comments between them, and replacing it in place is the
// restructuring [Set] exists to avoid; flow refuses rather than guess. The
// edit is verified as [Set]'s is: every item reads back as written, and no
// other value in the document has moved.
func SetList(src []byte, keyPath string, values []string) ([]byte, error) {
	path := strings.Split(keyPath, ".")
	if keyPath == "" || slicesHasEmpty(path) {
		return nil, core.Errorf(core.ErrValidation, "set %q", keyPath).
			WithWhy("a key path is one or more dotted names").
			WithFix("use a path such as deploys.contexts")
	}
	if len(values) == 0 {
		// An empty list leaves no item to read back, so the verification
		// below could not tell it from an edit that wrote nothing.
		return nil, core.Errorf(core.ErrValidation, "set %s", keyPath).
			WithWhy("the list is empty").
			WithFix("leave the key out instead")
	}
	root, err := documentRoot(src)
	if err != nil {
		return nil, err
	}
	parent, _, err := walkTo(root, path[:len(path)-1], false)
	if err != nil {
		return nil, err
	}
	name := path[len(path)-1]
	if _, existing := mappingEntry(parent, name); existing != nil {
		return nil, core.Errorf(core.ErrConflict, "set %s", keyPath).
			WithWhy("the document already has one, and flow only adds a list").
			WithFix("edit the file by hand")
	}

	// The list is built as a node and encoded, so the quoting is yaml's and
	// not a rule written here; single quotes are what the presets use, and
	// they keep a value such as 7 a string.
	list := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	want := make(map[string]string, len(values))
	for i, value := range values {
		list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.SingleQuotedStyle, Value: value})
		want[fmt.Sprintf("%s[%d]", keyPath, i)] = value
	}
	rendered, err := yaml.Marshal(list)
	if err != nil {
		return nil, internalEditError("the list does not encode: %s", err)
	}
	edited, _, err := insertLine(src, parent, name, strings.TrimRight(string(rendered), "\n"))
	if err != nil {
		return nil, err
	}
	if err := verifyLeaves(src, edited, want); err != nil {
		return nil, err
	}
	return edited, nil
}

// lastLine is the last line any part of a node occupies. A node reports where
// it starts and never where it ends, so the end is the deepest line of the
// deepest child — which for a mapping is its final value, and for that value
// its own final child.
func lastLine(node *yaml.Node) int {
	last := node.Line
	for _, child := range node.Content {
		if deeper := lastLine(child); deeper > last {
			last = deeper
		}
	}
	return last
}

// splitLines splits a document into lines, keeping a trailing newline as the
// empty final element so that joining them again is the identity.
func splitLines(src []byte) []string { return strings.Split(string(src), "\n") }

// verifySet checks that the edit did what it said and nothing else: the path
// holds the new value, and every other leaf in the document is untouched.
func verifySet(before, after []byte, keyPath, value string) error {
	return verifyLeaves(before, after, map[string]string{keyPath: value})
}

// verifyLeaves checks that every path in want holds its value after the edit,
// and that no other leaf in the document was changed, removed or added.
func verifyLeaves(before, after []byte, want map[string]string) error {
	original, err := leaves(before)
	if err != nil {
		return err
	}
	edited, err := leaves(after)
	if err != nil {
		return internalEditError("the edited document no longer parses: %s", err)
	}
	for keyPath, value := range want {
		if got, ok := edited[keyPath]; !ok || got != value {
			return internalEditError("%s reads %q after the edit, not %q", keyPath, got, value)
		}
	}
	for path, was := range original {
		if _, wanted := want[path]; wanted {
			continue
		}
		if now, ok := edited[path]; !ok || now != was {
			return internalEditError("the edit also changed %s, from %q to %q", path, was, now)
		}
	}
	for path := range edited {
		if _, ok := original[path]; !ok {
			if _, wanted := want[path]; !wanted {
				return internalEditError("the edit also added %s", path)
			}
		}
	}
	return nil
}

// leaves maps every scalar in a document to its dotted path. Sequences are
// indexed, so that two documents with a list reordered do not compare equal.
func leaves(src []byte) (map[string]string, error) {
	root, err := documentRoot(src)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var walk func(node *yaml.Node, path string)
	walk = func(node *yaml.Node, path string) {
		switch node.Kind {
		case yaml.ScalarNode:
			out[path] = node.Value
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				walk(node.Content[i+1], joinPath(path, node.Content[i].Value))
			}
		case yaml.SequenceNode:
			for i, child := range node.Content {
				walk(child, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(root, "")
	return out, nil
}

// join builds a dotted path, with no leading dot at the root.
func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// internalEditError reports that flow's own editing is wrong. It is never a
// user's mistake: the document parsed, the path was found, and the edit still
// did not hold.
func internalEditError(format string, args ...any) error {
	return core.Errorf(core.ErrValidation, "edit the configuration").
		WithWhy("flow could not make the change safely: "+format, args...).
		WithFix("edit the file by hand and report this as a bug")
}
