package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

func TestSchemaPrintsTheConfigSchema(t *testing.T) {
	for _, args := range [][]string{{"schema"}, {"schema", "--kind", "config"}, {"--json", "schema"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := exec(t, args...)
			if code != core.ExitOK {
				t.Fatalf("exited %d, want %d: %s", code, core.ExitOK, stderr)
			}
			if stderr != "" {
				t.Errorf("wrote to stderr: %s", stderr)
			}

			var document map[string]any
			if err := json.Unmarshal([]byte(stdout), &document); err != nil {
				t.Fatalf("stdout is not one JSON document: %v", err)
			}
			if document["$id"] != config.SchemaID {
				t.Errorf("$id = %v, want %q", document["$id"], config.SchemaID)
			}
		})
	}
}

// What flow prints must be what the repository publishes, or the URL in every
// generated .flow.yaml points at a different schema than the binary believes.
func TestSchemaMatchesTheCommittedFile(t *testing.T) {
	_, stdout, _ := exec(t, "schema")
	committed, err := os.ReadFile(filepath.FromSlash(config.SchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(committed) {
		t.Errorf("flow schema differs from %s; run go generate ./internal/config", config.SchemaFile)
	}
}

func TestSchemaRefusesKindsItCannotDescribe(t *testing.T) {
	for _, kind := range []string{"lock", "plan", "doctor", "policy"} {
		t.Run(kind, func(t *testing.T) {
			code, stdout, stderr := exec(t, "schema", "--kind", kind)
			if code != core.ExitFail {
				t.Errorf("exited %d, want %d", code, core.ExitFail)
			}
			if stdout != "" {
				t.Errorf("printed something anyway: %s", stdout)
			}
			if !strings.Contains(stderr, "flow schema --kind config") {
				t.Errorf("stderr does not say what does work: %s", stderr)
			}
		})
	}
}

func TestSchemaRejectsUnknownKinds(t *testing.T) {
	code, _, stderr := exec(t, "schema", "--kind", "invoice")
	if code != core.ExitUsage {
		t.Errorf("exited %d, want %d", code, core.ExitUsage)
	}
	if !strings.Contains(stderr, "invoice") {
		t.Errorf("stderr does not name the kind: %s", stderr)
	}
}
