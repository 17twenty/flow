package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/core"
	yaml "go.yaml.in/yaml/v3"
)

// DefaultPath is the name of the committed workflow file at the repository root.
const DefaultPath = ".flow.yaml"

// Problem is one thing wrong with a configuration file, located precisely
// enough for an editor to jump to it.
type Problem struct {
	// Path is the file the problem was found in.
	Path string `json:"path"`
	// Line is the 1-based line number.
	Line int `json:"line"`
	// Column is the 1-based column, in bytes from the start of the line.
	Column int `json:"column"`
	// Message states what is wrong, in one line.
	Message string `json:"message"`
}

// String renders the problem as path:line:col: message, the form editors and
// CI log scrapers already understand.
func (p Problem) String() string {
	if p.Line == 0 {
		return fmt.Sprintf("%s: %s", p.Path, p.Message)
	}
	if p.Column == 0 {
		return fmt.Sprintf("%s:%d: %s", p.Path, p.Line, p.Message)
	}
	return fmt.Sprintf("%s:%d:%d: %s", p.Path, p.Line, p.Column, p.Message)
}

// ParseError is everything wrong with one configuration file. It wraps a
// [core.UserError] so the caller still gets what/why/fix and exit 64, while
// tooling can reach the structured problems.
type ParseError struct {
	// Path is the file that failed to decode.
	Path string
	// Problems are every problem found, in file order.
	Problems []Problem

	user *core.UserError
}

// newParseError builds the user-facing error from the problems found.
func newParseError(path string, problems []Problem) *ParseError {
	lines := make([]string, len(problems))
	for i, p := range problems {
		lines[i] = p.String()
	}
	// Align continuation lines under the text of the "why:" label.
	user := core.Errorf(core.ErrValidation, "read %s", path).
		WithWhy("%s", strings.Join(lines, "\n       ")).
		WithFix("fix the lines above; run flow schema to see every valid key and value")
	return &ParseError{Path: path, Problems: problems, user: user}
}

// Error implements error.
func (e *ParseError) Error() string { return e.user.Error() }

// Unwrap exposes the user error, and through it [core.ErrValidation].
func (e *ParseError) Unwrap() error { return e.user }

// Load reads and decodes the workflow file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, core.Errorf(core.ErrNotFound, "read %s", path).
				WithWhy("the file does not exist").
				WithFix("run flow init to create it")
		}
		return nil, core.Errorf(core.ErrValidation, "read %s", path).WithWhy("%s", err)
	}
	return Parse(data, path)
}

// Parse decodes src as a `.flow.yaml`. Decoding is strict: an unknown key is
// an error, not a silently ignored line, because a typo in a policy file that
// quietly does nothing is worse than one that stops the build (spec 03 §1).
//
// path is used only to locate problems; nothing is read from disk.
func Parse(src []byte, path string) (*Config, error) {
	if path == "" {
		path = DefaultPath
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		if hint, ok := placeholderSyntaxHint(src, path); ok {
			return nil, newParseError(path, []Problem{hint})
		}
		return nil, newParseError(path, translate(err, path, nil))
	}
	if doc.Kind == 0 {
		return nil, newParseError(path, []Problem{{
			Path:    path,
			Message: "the file is empty; a workflow needs at least version, forge and tracker",
		}})
	}

	index := indexNodes(&doc)
	problems := checkPlaceholders(&doc, path)
	problems = append(problems, checkSecrets(&doc, path)...)

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		problems = append(problems, translate(err, path, index)...)
	}
	if len(problems) > 0 {
		return nil, newParseError(path, sortProblems(problems))
	}
	if cfg.Version != Version {
		return nil, newParseError(path, []Problem{versionProblem(cfg.Version, path, index)})
	}
	return &cfg, nil
}

// versionProblem locates and explains an unsupported schema version.
func versionProblem(got int, path string, index *nodeIndex) Problem {
	p := Problem{Path: path}
	if key, value := index.field("version"); key != nil {
		p.Line, p.Column = value.Line, value.Column
	}
	if got == 0 {
		p.Message = fmt.Sprintf("missing version; this build reads version %d", Version)
		if p.Line == 0 {
			p.Line, p.Column = 1, 1
		}
		return p
	}
	p.Message = fmt.Sprintf("version %d is not supported; this build reads version %d", got, Version)
	return p
}

// yamlProblem matches the "line N: …" prefix yaml.v3 puts on both syntax and
// type errors. The library reports a line but never a column, so flow recovers
// the column from the document tree.
var yamlProblem = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// unknownField matches yaml.v3's report of a key with nowhere to go.
var unknownField = regexp.MustCompile("^field (.+) not found in type ")

// duplicateKey matches yaml.v3's report of the same key twice in one mapping.
var duplicateKey = regexp.MustCompile(`^mapping key (.+) already defined at line (\d+)$`)

// translate converts yaml.v3's errors into located, plain-English problems.
func translate(err error, path string, index *nodeIndex) []Problem {
	var typeErr *yaml.TypeError
	raw := []string{err.Error()}
	if errors.As(err, &typeErr) {
		raw = typeErr.Errors
	}

	problems := make([]Problem, 0, len(raw))
	for _, message := range raw {
		problems = append(problems, locate(message, path, index))
	}
	return sortProblems(problems)
}

// sortProblems puts problems in file order, so a reader works down the file
// once instead of jumping about.
func sortProblems(problems []Problem) []Problem {
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Column - b.Column
	})
	return problems
}

// locate turns one yaml.v3 message into a Problem, recovering the column from
// the document tree and rewording the messages flow has something better to
// say about.
func locate(message, path string, index *nodeIndex) Problem {
	p := Problem{Path: path, Message: strings.TrimPrefix(message, "yaml: ")}

	match := yamlProblem.FindStringSubmatch(message)
	if match == nil {
		return p
	}
	p.Line, _ = strconv.Atoi(match[1])
	p.Message = match[2]

	switch {
	case unknownField.MatchString(p.Message):
		name := unknownField.FindStringSubmatch(p.Message)[1]
		p.Message = fmt.Sprintf("unknown key %q", name)
		if node := index.key(p.Line, name); node != nil {
			p.Column = node.Column
		}
	case duplicateKey.MatchString(p.Message):
		parts := duplicateKey.FindStringSubmatch(p.Message)
		p.Message = fmt.Sprintf("key %s is set twice; it is already set on line %s", parts[1], parts[2])
		if node := index.key(p.Line, strings.Trim(parts[1], `"`)); node != nil {
			p.Column = node.Column
		}
	default:
		if node := index.value(p.Line); node != nil {
			p.Column = node.Column
		}
	}
	return p
}

// nodeIndex maps source lines back to the YAML nodes on them, which is how
// flow reports a column for an error the YAML library located only by line.
type nodeIndex struct {
	// keys holds mapping key nodes by line.
	keys map[int][]*yaml.Node
	// values holds every non-key node by line.
	values map[int][]*yaml.Node
	// root is the document's top-level mapping, or nil.
	root *yaml.Node
}

// indexNodes walks the document once, recording where everything sits.
func indexNodes(doc *yaml.Node) *nodeIndex {
	index := &nodeIndex{keys: map[int][]*yaml.Node{}, values: map[int][]*yaml.Node{}}
	var walk func(n *yaml.Node, isKey bool)
	walk = func(n *yaml.Node, isKey bool) {
		if n == nil {
			return
		}
		if isKey {
			index.keys[n.Line] = append(index.keys[n.Line], n)
		} else {
			index.values[n.Line] = append(index.values[n.Line], n)
		}
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i], true)
				walk(n.Content[i+1], false)
			}
		default:
			for _, child := range n.Content {
				walk(child, false)
			}
		}
	}
	walk(doc, false)
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		index.root = doc.Content[0]
	}
	return index
}

// key returns the mapping key node named name on the given line.
func (x *nodeIndex) key(line int, name string) *yaml.Node {
	if x == nil {
		return nil
	}
	for _, n := range x.keys[line] {
		if n.Value == name {
			return n
		}
	}
	return nil
}

// value returns the last value node on the given line, which is the one an
// unmarshalling error is about: keys come first, values after.
func (x *nodeIndex) value(line int) *yaml.Node {
	if x == nil {
		return nil
	}
	if nodes := x.values[line]; len(nodes) > 0 {
		return nodes[len(nodes)-1]
	}
	if nodes := x.keys[line]; len(nodes) > 0 {
		return nodes[len(nodes)-1]
	}
	return nil
}

// field returns the key and value nodes of a top-level field.
func (x *nodeIndex) field(name string) (key, value *yaml.Node) {
	if x == nil || x.root == nil || x.root.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(x.root.Content); i += 2 {
		if x.root.Content[i].Value == name {
			return x.root.Content[i], x.root.Content[i+1]
		}
	}
	return nil, nil
}
