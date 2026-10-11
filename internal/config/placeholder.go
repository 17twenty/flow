package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// PlaceholderPrefix is the only interpolation flow understands. A value
// written `${pending:project}` says "nobody knows this yet, and doctor will
// find it out" — it is not a template language, and flow deliberately has no
// other one (spec 03 §2).
const PlaceholderPrefix = "${pending:"

// placeholder matches a whole value that is a placeholder, capturing its name.
var placeholder = regexp.MustCompile(`^\$\{pending:([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*)\}$`)

// interpolation finds anything that looks like an attempt at interpolation,
// so that a near miss is refused rather than silently taken literally.
var interpolation = regexp.MustCompile(`\$\{[^}]*\}?`)

// placeholderPaths are the only places in `.flow.yaml` a placeholder may
// appear (spec 03 §1.1). Everything else is either known when the file is
// written or has no resolution step that could fill it in.
var placeholderPaths = []string{
	"tracker.project",
	"followups.owners.default",
	"followups.owners.tracker_admin",
	"followups.owners.forge_admin",
	"followups.owners.platform",
}

// Pending is a configured value that may not be known yet. It holds either the
// text a human wrote or the name of the placeholder standing in for it, and
// doctor resolves the second into the lock rather than back into `.flow.yaml`
// (spec 03 §2).
type Pending struct {
	// Text is the value as written. It is empty while the value is pending.
	Text string
	// Name is the placeholder's name, for example "project". It is empty once
	// the value is known.
	Name string
}

// Known returns a Pending holding a value that is already known.
func Known(text string) Pending { return Pending{Text: text} }

// IsPending reports whether the value still has to be resolved.
func (p Pending) IsPending() bool { return p.Text == "" && p.Name != "" }

// IsZero reports whether nothing was configured at all.
func (p Pending) IsZero() bool { return p.Text == "" && p.Name == "" }

// Resolved returns a copy of p holding text, which is what doctor has just
// discovered. The placeholder name is kept so that output can still say which
// placeholder was filled in.
func (p Pending) Resolved(text string) Pending {
	p.Text = text
	return p
}

// String renders the value as it appears in the file: the text, or the
// placeholder that is standing in for it.
func (p Pending) String() string {
	if p.Text != "" {
		return p.Text
	}
	if p.Name != "" {
		return PlaceholderPrefix + p.Name + "}"
	}
	return ""
}

// UnmarshalYAML decodes a value that may be a placeholder.
func (p *Pending) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Decode(&text); err != nil {
		return yamlError(node, "this must be written as text")
	}
	if !strings.Contains(text, "${") {
		*p = Pending{Text: text}
		return nil
	}
	name, ok := PlaceholderName(text)
	if !ok {
		return yamlError(node, "%s", explainPlaceholder(text))
	}
	*p = Pending{Name: name}
	return nil
}

// MarshalYAML writes the value back exactly as it was read.
func (p Pending) MarshalYAML() (any, error) { return p.String(), nil }

// MarshalJSON writes the value back exactly as it was read.
func (p Pending) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(p.String())), nil }

// PlaceholderName returns the name in a `${pending:name}` value, and whether
// the whole value was one.
func PlaceholderName(text string) (string, bool) {
	match := placeholder.FindStringSubmatch(text)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// explainPlaceholder says what is wrong with something that looks like a
// placeholder but is not one.
func explainPlaceholder(text string) string {
	found := interpolation.FindString(text)
	switch {
	case !strings.HasSuffix(found, "}"):
		return fmt.Sprintf("%q is missing its closing brace", found)
	case !strings.HasPrefix(found, PlaceholderPrefix):
		return fmt.Sprintf("%q is not a placeholder: the only one flow understands is %sname}, and nothing else is interpolated", found, PlaceholderPrefix)
	case found != text:
		return fmt.Sprintf("a placeholder must be the whole value, not part of %q", text)
	default:
		return fmt.Sprintf("%q has no name: write %sname} with a lowercase name, for example %sproject}", text, PlaceholderPrefix, PlaceholderPrefix)
	}
}

// checkPlaceholders reports every interpolation in the document that is not a
// well-formed placeholder in a place that has a way of being resolved.
func checkPlaceholders(root *yaml.Node, path string) []Problem {
	var problems []Problem
	walkScalars(root, "", func(node *yaml.Node, at string) {
		if node.Tag != "!!str" || !strings.Contains(node.Value, "${") {
			return
		}
		problem := Problem{Path: path, Line: node.Line, Column: node.Column}
		name, ok := PlaceholderName(node.Value)
		switch {
		case !ok:
			problem.Message = explainPlaceholder(node.Value)
		case allowsPlaceholder(at):
			return
		default:
			problem.Message = fmt.Sprintf("%s%s} cannot be used at %s: placeholders are only allowed at %s",
				PlaceholderPrefix, name, at, strings.Join(placeholderPaths, ", "))
		}
		problems = append(problems, problem)
	})
	return problems
}

// allowsPlaceholder reports whether a placeholder may appear at a path.
func allowsPlaceholder(path string) bool {
	for _, allowed := range placeholderPaths {
		if path == allowed {
			return true
		}
	}
	return false
}

// walkScalars visits every scalar in the document, telling the visitor the
// dotted path it sits at, for example workflow.columns[0].name.
func walkScalars(node *yaml.Node, path string, visit func(*yaml.Node, string)) {
	walkNode(node, path, visit, map[*yaml.Node]bool{})
}

// walkNode is walkScalars with a record of the nodes it is already inside, so
// that an anchor referring to itself — `&a [*a]` is valid YAML to parse, if
// not to use — is left alone instead of being followed forever.
func walkNode(node *yaml.Node, path string, visit func(*yaml.Node, string), open map[*yaml.Node]bool) {
	if node == nil || open[node] {
		return
	}
	open[node] = true
	defer delete(open, node)

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			walkNode(child, path, visit, open)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			child := key.Value
			if path != "" {
				child = path + "." + key.Value
			}
			visit(key, child)
			walkNode(value, child, visit, open)
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			walkNode(child, fmt.Sprintf("%s[%d]", path, i), visit, open)
		}
	case yaml.ScalarNode:
		visit(node, path)
	case yaml.AliasNode:
		walkNode(node.Alias, path, visit, open)
	}
}

// flowPlaceholder matches a placeholder written unquoted inside a flow
// mapping, which YAML cannot represent: `{` starts a flow collection, and a
// plain scalar inside one may not contain `{` or `}`.
var flowPlaceholder = regexp.MustCompile(`\{[^}'"]*\$\{`)

// placeholderSyntaxHint explains a YAML syntax error that a placeholder in a
// flow mapping caused, which the parser can only describe as a missing brace.
// Returning the real cause is worth more than repeating what YAML said.
func placeholderSyntaxHint(src []byte, path string) (Problem, bool) {
	for i, line := range strings.Split(string(src), "\n") {
		where := flowPlaceholder.FindStringIndex(line)
		if where == nil {
			continue
		}
		column := strings.Index(line[where[0]:], "${") + where[0] + 1
		return Problem{
			Path:   path,
			Line:   i + 1,
			Column: column,
			Message: "a placeholder cannot be written inside a flow mapping {…}: " +
				"YAML reads the ${ as the start of another collection; put each key on its own line, or quote the value",
		}, true
	}
	return Problem{}, false
}
