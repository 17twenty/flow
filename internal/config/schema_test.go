package config

import (
	"bytes"
	"encoding/json"
	"flag"
	stdmaps "maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

var update = flag.Bool("update", false, "rewrite the committed JSON Schema and golden files")

// TestSchemaFileIsCurrent is both the generator and the staleness check: with
// -update it writes assets/schema/flow.v1.json, and without it fails when the
// committed file no longer matches the schema this build would print
// (spec 03 §6).
func TestSchemaFileIsCurrent(t *testing.T) {
	got, err := SchemaJSON(ConfigSchema())
	if err != nil {
		t.Fatalf("SchemaJSON: %v", err)
	}
	path := filepath.Join("..", "..", filepath.FromSlash(SchemaFile))

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", SchemaFile, len(got))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — run go generate ./internal/config", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale; run go generate ./internal/config", SchemaFile)
	}
}

func TestSchemaIsValidJSON(t *testing.T) {
	data, err := SchemaJSON(ConfigSchema())
	if err != nil {
		t.Fatalf("SchemaJSON: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("the schema is not valid JSON: %v", err)
	}
	if document["$id"] != SchemaID {
		t.Errorf("$id = %v, want %q", document["$id"], SchemaID)
	}
	if document["$schema"] == "" {
		t.Error("the schema names no dialect")
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Error("the schema does not end with a newline")
	}
	if !bytes.Contains(data, []byte(`"additionalProperties": false`)) {
		t.Error("the schema permits unknown keys; decoding does not")
	}
}

// TestSchemaMatchesTheStructs walks the Config type and the schema together in
// both directions. The schema is written by hand because it says things Go
// types cannot, but a key that exists in one and not the other is a lie to
// whichever tool trusted it. Reflection lives here, in the test, and nowhere
// else (spec 01 §4).
func TestSchemaMatchesTheStructs(t *testing.T) {
	compare(t, "", reflect.TypeOf(Config{}), ConfigSchema())
}

func compare(t *testing.T, path string, typ reflect.Type, schema *Schema) {
	t.Helper()
	where := path
	if where == "" {
		where = "(root)"
	}
	if schema == nil {
		t.Errorf("%s: the schema describes nothing", where)
		return
	}

	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ {
	case reflect.TypeOf(Duration(0)):
		expectType(t, where, schema, "string")
		if schema.Pattern != durationPattern {
			t.Errorf("%s: a duration is described without the duration pattern", where)
		}
		return
	case reflect.TypeOf(Pending{}):
		expectType(t, where, schema, "string")
		if !strings.Contains(schema.Description, "${pending:") {
			t.Errorf("%s: a value that may be a placeholder does not say so", where)
		}
		return
	case reflect.TypeOf(time.Time{}):
		expectType(t, where, schema, "string")
		return
	}

	switch typ.Kind() {
	case reflect.Struct:
		expectType(t, where, schema, "object")
		fields := map[string]reflect.Type{}
		for i := range typ.NumField() {
			field := typ.Field(i)
			name, ok := yamlName(field)
			if !ok {
				continue
			}
			fields[name] = field.Type
		}

		for name := range fields {
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("%s: the struct has %q and the schema does not", where, name)
			}
		}
		for name := range schema.Properties {
			if _, ok := fields[name]; !ok {
				t.Errorf("%s: the schema has %q and the struct does not", where, name)
			}
		}
		for _, name := range slices.Sorted(stdmaps.Keys(schema.Properties)) {
			if fieldType, ok := fields[name]; ok {
				compare(t, join(path, name), fieldType, schema.Properties[name])
			}
		}
		for _, required := range schema.Required {
			if _, ok := schema.Properties[required]; !ok {
				t.Errorf("%s: %q is required but not described", where, required)
			}
		}
		if schema.AdditionalProperties != deny {
			t.Errorf("%s: the schema allows keys the decoder would refuse", where)
		}

	case reflect.Slice:
		expectType(t, where, schema, "array")
		compare(t, path+"[]", typ.Elem(), schema.Items)

	case reflect.Map:
		expectType(t, where, schema, "object")
		if schema.AdditionalProperties == nil || schema.AdditionalProperties == deny {
			t.Errorf("%s: a map is described as a closed object", where)
			return
		}
		compare(t, path+".*", typ.Elem(), schema.AdditionalProperties)

	case reflect.String:
		expectType(t, where, schema, "string")
	case reflect.Int, reflect.Int64:
		expectType(t, where, schema, "integer")
	case reflect.Bool:
		expectType(t, where, schema, "boolean")
	default:
		t.Errorf("%s: no rule for a %s", where, typ.Kind())
	}

	if schema.Description == "" {
		t.Errorf("%s: the schema says nothing about what this is for", where)
	}
}

// expectType reports a schema whose JSON type is not the one the Go type has.
func expectType(t *testing.T, where string, schema *Schema, want string) {
	t.Helper()
	if schema.Type != want {
		t.Errorf("%s: the schema says %q, the struct says %q", where, schema.Type, want)
	}
}

// yamlName returns the key a struct field decodes from, and whether it decodes
// at all.
func yamlName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("yaml")
	if tag == "-" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = strings.ToLower(field.Name)
	}
	return name, true
}

// join builds a dotted path for error messages.
func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// TestSchemaDescribesEveryPresetKey parses the shipped presets and flow's own
// configuration and insists the schema knows every key they use: a file the
// editor underlines in red would teach people to ignore the schema.
func TestSchemaDescribesEveryPresetKey(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "assets", "presets", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no presets found: %v", err)
	}
	paths = append(paths, filepath.Join("..", "..", ".flow.yaml"))
	schema := ConfigSchema()
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := yaml.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			checkKeys(t, "", document, schema)
		})
	}
}

// The editor hint at the top of flow's own configuration must name the schema
// this build publishes, or every contributor edits against a stale contract.
func TestRepositoryConfigPointsAtTheSchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".flow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# yaml-language-server: $schema=" + SchemaID
	if first, _, _ := strings.Cut(string(data), "\n"); first != want {
		t.Errorf("first line is %q, want %q", first, want)
	}
}

// checkKeys walks a decoded document against the schema, reporting any key the
// schema does not describe.
func checkKeys(t *testing.T, path string, value any, schema *Schema) {
	t.Helper()
	if schema == nil {
		t.Errorf("%s: the schema describes nothing", path)
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(stdmaps.Keys(typed)) {
			child := join(path, key)
			switch {
			case schema.Properties != nil:
				next, ok := schema.Properties[key]
				if !ok {
					t.Errorf("%s: the schema does not describe this key", child)
					continue
				}
				checkKeys(t, child, typed[key], next)
			case schema.AdditionalProperties != nil && schema.AdditionalProperties != deny:
				checkKeys(t, child, typed[key], schema.AdditionalProperties)
			default:
				t.Errorf("%s: the schema does not allow keys here", child)
			}
		}
	case []any:
		for _, item := range typed {
			checkKeys(t, path, item, schema.Items)
		}
	}
}
