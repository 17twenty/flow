package config

import (
	"math"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"0", 0},
		{"-0", 0},
		{"0s", 0},
		{"500ms", 500 * time.Millisecond},
		{"1us", time.Microsecond},
		{"1µs", time.Microsecond},
		{"1μs", time.Microsecond},
		{"90s", 90 * time.Second},
		{"90m", 90 * time.Minute},
		{"2h", 2 * time.Hour},
		{"2h30m", 2*time.Hour + 30*time.Minute},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"1w2d3h4m5s", 9*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second},
		{"1.5d", 36 * time.Hour},
		{"0.5w", 84 * time.Hour},
		{"-2h", -2 * time.Hour},
		{"+1d", 24 * time.Hour},
		{"1d0h", 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseDurationRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"bare number", "2"},
		{"bare number with sign", "-2"},
		{"unit only", "h"},
		{"sign only", "-"},
		{"space before unit", "2 h"},
		{"doubled unit", "2hh"},
		{"unknown unit", "2y"},
		{"month is not a unit", "2M"},
		{"words", "two days"},
		{"two decimal points", "1.2.3d"},
		{"trailing sign", "2d-"},
		{"inner sign", "1h-30m"},
		{"uppercase unit", "2H"},
		{"overflow", "9999999999w"},
		{"overflow by addition", "9000000h9000000h"},
		{"zero alone is the only bare number", "0h0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ParseDuration(tt.in); err == nil {
				t.Fatalf("ParseDuration(%q) = %v, want an error", tt.in, got)
			}
		})
	}
}

func TestDurationString(t *testing.T) {
	tests := []struct {
		in   Duration
		want string
	}{
		{0, "0"},
		{Duration(2 * time.Hour), "2h"},
		{Duration(90 * time.Minute), "1h30m"},
		{Duration(7 * 24 * time.Hour), "7d"},
		{Week, "7d"},
		{Duration(30 * 24 * time.Hour), "30d"},
		{Day + Duration(time.Second), "1d1s"},
		{Duration(-2 * time.Hour), "-2h"},
		{Duration(1500 * time.Millisecond), "1.5s"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Fatalf("Duration(%d).String() = %q, want %q", int64(tt.in), got, tt.want)
			}
			back, err := ParseDuration(tt.want)
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", tt.want, err)
			}
			if Duration(back) != tt.in {
				t.Errorf("round trip of %q = %v, want %v", tt.want, back, tt.in.Std())
			}
		})
	}
}

func TestDurationAccessors(t *testing.T) {
	d := Duration(2 * time.Hour)
	if d.Std() != 2*time.Hour {
		t.Errorf("Std() = %v, want 2h", d.Std())
	}
	if d.IsZero() {
		t.Error("IsZero() = true for 2h")
	}
	if !Duration(0).IsZero() {
		t.Error("IsZero() = false for 0")
	}
	if Duration(math.MaxInt64).String() == "" {
		t.Error("String() of the largest duration is empty")
	}
}

func TestDurationYAML(t *testing.T) {
	var doc struct {
		D Duration `yaml:"d"`
	}
	if err := yaml.Unmarshal([]byte("d: 7d\n"), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.D != Week {
		t.Errorf("decoded %v, want 7d", doc.D)
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if string(out) != "d: 7d\n" {
		t.Errorf("encoded %q, want %q", out, "d: 7d\n")
	}

	for _, bad := range []string{"d: 2\n", "d: true\n", "d: [7d]\n", "d: 2y\n"} {
		if err := yaml.Unmarshal([]byte(bad), &doc); err == nil {
			t.Errorf("decoding %q succeeded, want an error", bad)
		}
	}
}

func TestDurationJSON(t *testing.T) {
	out, err := Duration(90 * time.Minute).MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(out) != `"1h30m"` {
		t.Errorf("MarshalJSON = %s, want %q", out, "1h30m")
	}

	var d Duration
	if err := d.UnmarshalJSON([]byte(`"7d"`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if d != Week {
		t.Errorf("UnmarshalJSON gave %v, want 7d", d)
	}
	if err := d.UnmarshalJSON([]byte(`7`)); err == nil {
		t.Error("UnmarshalJSON(7) succeeded, want an error")
	}
	if err := d.UnmarshalJSON([]byte(`"2y"`)); err == nil {
		t.Error(`UnmarshalJSON("2y") succeeded, want an error`)
	}
}
