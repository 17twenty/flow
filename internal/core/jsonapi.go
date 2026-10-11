package core

import (
	"encoding/json"
	"io"
)

// SchemaID returns the stable schema identifier for a JSON document kind, for
// example SchemaID("version") == "flow.version/v1". Every --json document
// carries one in its "schema" field so consumers can version their parsing.
func SchemaID(kind string) string { return "flow." + kind + "/v1" }

// WriteJSON writes exactly one indented JSON document to w, followed by a
// newline. Commands using --json must call it once and write nothing else to
// stdout (spec 02 §3).
func WriteJSON(w io.Writer, doc any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}
