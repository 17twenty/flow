package config

import (
	"strings"
	"testing"
	"time"
)

// FuzzParse checks that no input, however malformed, makes decoding panic, and
// that a file flow accepts is always reported as a whole config.
func FuzzParse(f *testing.F) {
	f.Add(fullConfig)
	f.Add("version: 1\n")
	f.Add("version: 1\ntracker:\n  project: ${pending:project}\n")
	f.Add("version: 1\ntracker: { project: ${pending:x} }\n")
	f.Add("version: 1\ndeploys:\n  max_lag: 2h\n")
	f.Add("version: 1\nworkflow:\n  columns:\n    - id: done\n")
	f.Add("- 1\n- 2\n")
	f.Add("&a [*a]\n")

	f.Fuzz(func(t *testing.T, src string) {
		cfg, err := Parse([]byte(src), "flow.yaml")
		switch {
		case err != nil && cfg != nil:
			t.Fatalf("Parse returned a config and an error: %v", err)
		case err == nil && cfg == nil:
			t.Fatal("Parse returned neither a config nor an error")
		case err == nil && cfg.Version != Version:
			t.Fatalf("Parse accepted version %d", cfg.Version)
		}
		if err == nil && cfg.Tracker.Project.IsPending() && cfg.Tracker.Project.Name == "" {
			t.Fatal("a pending value with no name got through")
		}
	})
}

// FuzzParseDuration checks that parsing cannot panic and that anything flow
// accepts survives being written out and read back.
func FuzzParseDuration(f *testing.F) {
	for _, seed := range []string{"0", "2h", "7d", "1w", "1.5d", "-2h", "90m", "1w2d3h4m5s", "", "2", "9999999999999999999d"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src string) {
		got, err := ParseDuration(src)
		if err != nil {
			if !strings.Contains(err.Error(), "duration") {
				t.Fatalf("ParseDuration(%q) failed without saying what was wrong: %v", src, err)
			}
			return
		}

		text := Duration(got).String()
		back, err := ParseDuration(text)
		if err != nil {
			t.Fatalf("ParseDuration(%q) = %v, whose String() %q does not parse: %v", src, got, text, err)
		}
		if back != got {
			t.Fatalf("ParseDuration(%q) = %v, round tripped through %q to %v", src, got, text, time.Duration(back))
		}
	})
}
