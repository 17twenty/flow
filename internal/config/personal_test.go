package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

func boolPtr(b bool) *bool { return &b }

func TestParsePersonal(t *testing.T) {
	src := `[user]
handle = "@17twenty"

[ui]
theme = "dark"
ascii = true
no_color = false

[log]
level = "debug"
format = "json"

[auth.github]
token_cmd = ["gh", "auth", "token"]
hosts = ["api.github.com"]

[auth.github_projects]
token_cmd = ["gh", "auth", "token"]
`
	got, err := ParsePersonal([]byte(src), "config.toml", true)
	if err != nil {
		t.Fatalf("ParsePersonal: %v", err)
	}
	want := Personal{
		User: UserSection{Handle: "@17twenty"},
		UI:   UISection{Theme: ThemeDark, ASCII: boolPtr(true), NoColor: boolPtr(false)},
		Log:  LogSection{Level: "debug", Format: LogJSON},
		Auth: map[string]AuthSource{
			"github":          {TokenCmd: []string{"gh", "auth", "token"}, Hosts: []string{"api.github.com"}},
			"github_projects": {TokenCmd: []string{"gh", "auth", "token"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParsePersonal gave\n%#v\nwant\n%#v", got, want)
	}
}

func TestParsePersonalTellsUnsetFromFalse(t *testing.T) {
	set, err := ParsePersonal([]byte("[ui]\nascii = false\n"), "config.toml", true)
	if err != nil {
		t.Fatalf("ParsePersonal: %v", err)
	}
	if set.UI.ASCII == nil || *set.UI.ASCII {
		t.Errorf("ascii = %v, want a deliberate false", set.UI.ASCII)
	}
	unset, err := ParsePersonal([]byte("[ui]\ntheme = \"dark\"\n"), "config.toml", true)
	if err != nil {
		t.Fatalf("ParsePersonal: %v", err)
	}
	if unset.UI.ASCII != nil {
		t.Errorf("ascii = %v, want it left unset", *unset.UI.ASCII)
	}
}

func TestParsePersonalRejects(t *testing.T) {
	tests := []struct {
		name          string
		src           string
		allowTokenCmd bool
		message       string
	}{
		{
			name:    "team policy in a personal file",
			src:     "[commits]\nheader_max = 200\n",
			message: `unknown key "commits.header_max"`,
		},
		{
			name:    "a workflow in a personal file",
			src:     "[workflow]\ndone_on = \"deployed\"\n",
			message: "team policy belongs in .flow.yaml",
		},
		{
			name:    "a misspelled key",
			src:     "[ui]\nthemes = \"dark\"\n",
			message: `unknown key "ui.themes"`,
		},
		{
			name:    "an unknown theme",
			src:     "[ui]\ntheme = \"neon\"\n",
			message: `ui.theme is "neon"`,
		},
		{
			name:    "an unknown log level",
			src:     "[log]\nlevel = \"chatty\"\n",
			message: `log.level is "chatty"`,
		},
		{
			name:    "an unknown log format",
			src:     "[log]\nformat = \"yaml\"\n",
			message: `log.format is "yaml"`,
		},
		{
			name:    "a host with a scheme",
			src:     "[auth.github]\nhosts = [\"https://api.github.com\"]\n",
			message: "list host names only",
		},
		{
			name:    "an empty command",
			src:     "[auth.github]\ntoken_cmd = [\"\"]\n",
			message: "starts with an empty command",
		},
		{
			name:    "broken toml",
			src:     "[ui\ntheme = \"dark\"\n",
			message: "expected",
		},
		{
			name:    "the wrong type",
			src:     "[ui]\nascii = \"yes\"\n",
			message: "ascii",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePersonal([]byte(tt.src), "config.toml", true)
			if err == nil {
				t.Fatal("ParsePersonal succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error %q does not mention %q", err, tt.message)
			}
			if core.ExitCode(err) != core.ExitUsage {
				t.Errorf("ExitCode = %d, want %d", core.ExitCode(err), core.ExitUsage)
			}
		})
	}
}

// TestTokenCmdOnlyFromUserConfig is the trust boundary in spec 15 §1: a file
// inside the repository must never name a command for flow to run.
func TestTokenCmdOnlyFromUserConfig(t *testing.T) {
	src := "[auth.github]\ntoken_cmd = [\"gh\", \"auth\", \"token\"]\n"

	if _, err := ParsePersonal([]byte(src), "config.toml", true); err != nil {
		t.Errorf("the user's own config may set token_cmd: %v", err)
	}

	_, err := ParsePersonal([]byte(src), LocalConfigName, false)
	if err == nil {
		t.Fatal("a repository file was allowed to set token_cmd")
	}
	for _, want := range []string{"token_cmd", "inside the repository", "user config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Hosts are only data, so the repository may still narrow them.
	if _, err := ParsePersonal([]byte("[auth.github]\nhosts = [\"ghe.example.com\"]\n"), LocalConfigName, false); err != nil {
		t.Errorf("a repository file may list hosts: %v", err)
	}
}

func TestTomlProblemIsLocated(t *testing.T) {
	_, err := ParsePersonal([]byte("[ui]\ntheme = dark\n"), "config.toml", true)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error is %T, want *ParseError", err)
	}
	if len(parseErr.Problems) != 1 {
		t.Fatalf("got %d problems %v, want 1", len(parseErr.Problems), parseErr.Problems)
	}
	if got := parseErr.Problems[0]; got.Line != 2 || got.Column == 0 {
		t.Errorf("problem at %d:%d, want line 2 with a column (%s)", got.Line, got.Column, got)
	}
}

// TestPrecedence is the table spec 03 demands: flags beat env, env beats the
// repository override, the override beats the user's config, and the user's
// config beats the defaults.
func TestPrecedence(t *testing.T) {
	var (
		defaults = Personal{}
		user     = Personal{
			User: UserSection{Handle: "@from-user"},
			UI:   UISection{Theme: ThemeDark, ASCII: boolPtr(true)},
			Log:  LogSection{Level: "info", Format: LogJSON},
		}
		local = Personal{
			User: UserSection{Handle: "@from-local"},
			UI:   UISection{Theme: ThemeLight},
			Log:  LogSection{Level: "debug"},
		}
		env   = Personal{UI: UISection{ASCII: boolPtr(false)}, Log: LogSection{Level: "error"}}
		flags = Personal{Log: LogSection{Level: "warn"}}
	)

	tests := []struct {
		name   string
		layers []Personal
		want   Settings
	}{
		{
			name:   "nothing set at all",
			layers: nil,
			want:   Settings{Theme: ThemeAuto, LogLevel: "warn", LogFormat: LogText},
		},
		{
			name:   "an empty layer changes nothing",
			layers: []Personal{defaults},
			want:   Settings{Theme: ThemeAuto, LogLevel: "warn", LogFormat: LogText},
		},
		{
			name:   "the user config beats the defaults",
			layers: []Personal{user},
			want:   Settings{Handle: "@from-user", Theme: ThemeDark, ASCII: true, LogLevel: "info", LogFormat: LogJSON},
		},
		{
			name:   "the repository override beats the user config",
			layers: []Personal{user, local},
			want:   Settings{Handle: "@from-local", Theme: ThemeLight, ASCII: true, LogLevel: "debug", LogFormat: LogJSON},
		},
		{
			name:   "the environment beats the repository override",
			layers: []Personal{user, local, env},
			want:   Settings{Handle: "@from-local", Theme: ThemeLight, ASCII: false, LogLevel: "error", LogFormat: LogJSON},
		},
		{
			name:   "flags beat everything",
			layers: []Personal{user, local, env, flags},
			want:   Settings{Handle: "@from-local", Theme: ThemeLight, ASCII: false, LogLevel: "warn", LogFormat: LogJSON},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.layers...)
			got.Auth = nil
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Resolve gave %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveMergesAuthPerProvider(t *testing.T) {
	user := Personal{Auth: map[string]AuthSource{
		"github":          {TokenCmd: []string{"gh", "auth", "token"}, Hosts: []string{"api.github.com"}},
		"github_projects": {TokenCmd: []string{"gh", "auth", "token"}},
	}}
	local := Personal{Auth: map[string]AuthSource{
		"github": {Hosts: []string{"ghe.example.com"}},
	}}

	got := Resolve(user, local)
	want := map[string]AuthSource{
		"github":          {TokenCmd: []string{"gh", "auth", "token"}, Hosts: []string{"ghe.example.com"}},
		"github_projects": {TokenCmd: []string{"gh", "auth", "token"}},
	}
	if !reflect.DeepEqual(got.Auth, want) {
		t.Errorf("Auth = %#v, want %#v", got.Auth, want)
	}

	// Resolving must not reach back into the layers it was given.
	got.Auth["github"].Hosts[0] = "elsewhere"
	if local.Auth["github"].Hosts[0] != "ghe.example.com" {
		t.Error("Resolve shares slices with its input")
	}
}

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Personal
	}{
		{"nothing set", nil, Personal{}},
		{
			name: "every variable",
			env:  map[string]string{"FLOW_LOG_LEVEL": "DEBUG", "FLOW_LOG_FORMAT": "JSON", "FLOW_ASCII": "true", "FLOW_NO_COLOR": "1"},
			want: Personal{UI: UISection{ASCII: boolPtr(true), NoColor: boolPtr(true)}, Log: LogSection{Level: "debug", Format: "json"}},
		},
		{
			name: "a deliberate false",
			env:  map[string]string{"FLOW_ASCII": "false"},
			want: Personal{UI: UISection{ASCII: boolPtr(false)}},
		},
		{
			name: "nonsense is ignored rather than guessed at",
			env:  map[string]string{"FLOW_ASCII": "sometimes"},
			want: Personal{},
		},
		{
			name: "NO_COLOR means no colour whatever it says",
			env:  map[string]string{"NO_COLOR": "0"},
			want: Personal{UI: UISection{NoColor: boolPtr(true)}},
		},
		{
			name: "an empty NO_COLOR leaves colour alone",
			env:  map[string]string{"NO_COLOR": ""},
			want: Personal{},
		},
		{
			name: "NO_COLOR outranks FLOW_NO_COLOR",
			env:  map[string]string{"NO_COLOR": "yes", "FLOW_NO_COLOR": "false"},
			want: Personal{UI: UISection{NoColor: boolPtr(true)}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromEnv(func(key string) string { return tt.env[key] })
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FromEnv gave %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestUserConfigPath(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"explicit", map[string]string{"FLOW_USER_CONFIG": "/tmp/flow.toml"}, "/tmp/flow.toml"},
		{"xdg", map[string]string{"XDG_CONFIG_HOME": "/x"}, filepath.Join("/x", "flow", "config.toml")},
		{"home", map[string]string{"HOME": "/home/n"}, filepath.Join("/home/n", ".config", "flow", "config.toml")},
		{"explicit wins", map[string]string{"FLOW_USER_CONFIG": "/tmp/f.toml", "XDG_CONFIG_HOME": "/x"}, "/tmp/f.toml"},
		{"xdg wins over home", map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/home/n"}, filepath.Join("/x", "flow", "config.toml")},
		{"nowhere to look", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UserConfigPath(func(key string) string { return tt.env[key] })
			if got != tt.want {
				t.Errorf("UserConfigPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadPersonal(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.toml")
	localPath := filepath.Join(dir, LocalConfigName)

	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("neither file exists", func(t *testing.T) {
		layers, warnings, err := LoadPersonal(filepath.Join(dir, "absent.toml"), filepath.Join(dir, "absent.local.toml"), untracked)
		if err != nil {
			t.Fatalf("LoadPersonal: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("warned about files that are simply not there: %v", warnings)
		}
		if len(layers) != 0 {
			t.Errorf("got %d layers, want none", len(layers))
		}
	})

	t.Run("both files, lowest precedence first", func(t *testing.T) {
		write(userPath, "[user]\nhandle = \"@user\"\n[ui]\ntheme = \"dark\"\n")
		write(localPath, "[ui]\ntheme = \"light\"\n")

		layers, _, err := LoadPersonal(userPath, localPath, untracked)
		if err != nil {
			t.Fatalf("LoadPersonal: %v", err)
		}
		if len(layers) != 2 {
			t.Fatalf("got %d layers, want 2", len(layers))
		}
		settings := Resolve(layers...)
		if settings.Handle != "@user" || settings.Theme != ThemeLight {
			t.Errorf("resolved %#v, want the repository theme over the user's", settings)
		}
	})

	t.Run("no path for the user config", func(t *testing.T) {
		layers, _, err := LoadPersonal("", localPath, untracked)
		if err != nil {
			t.Fatalf("LoadPersonal: %v", err)
		}
		if len(layers) != 1 {
			t.Errorf("got %d layers, want only the repository override", len(layers))
		}
	})

	// A tracked override is the team's file, and the team must not be able to
	// stop one another working by committing one: it is left unread, said
	// once on stderr, and the run goes on with the layers flow does trust
	// (scenario S01).
	t.Run("a tracked override is left unread", func(t *testing.T) {
		write(userPath, "[ui]\ntheme = \"dark\"\n")
		write(localPath, "[ui]\ntheme = \"light\"\n")
		layers, warnings, err := LoadPersonal(userPath, localPath, func(string) (bool, error) { return true, nil })
		if err != nil {
			t.Fatalf("a tracked override stopped the run: %v", err)
		}
		if len(layers) != 1 || Resolve(layers...).Theme != ThemeDark {
			t.Errorf("the tracked override was read anyway: %#v", Resolve(layers...))
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings = %v, want exactly one", warnings)
		}
		switch {
		case !strings.Contains(warnings[0], "tracked by git"):
			t.Errorf("warning %q does not say the file is tracked", warnings[0])
		case !strings.Contains(warnings[0], "git rm --cached"):
			t.Errorf("warning %q does not say how to fix it", warnings[0])
		case !strings.Contains(warnings[0], localPath):
			t.Errorf("warning %q does not name the file", warnings[0])
		}
	})

	t.Run("git could not be asked", func(t *testing.T) {
		layers, warnings, err := LoadPersonal(userPath, localPath, func(string) (bool, error) {
			return false, errors.New("not a git repository")
		})
		if err != nil {
			t.Fatalf("LoadPersonal: %v", err)
		}
		if len(layers) != 1 {
			t.Errorf("flow read an override it could not tell apart from the team's: %d layers", len(layers))
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "not a git repository") {
			t.Errorf("warnings = %v, want one saying why git could not answer", warnings)
		}
	})

	t.Run("no git to ask", func(t *testing.T) {
		if _, _, err := LoadPersonal(userPath, localPath, nil); err != nil {
			t.Fatalf("LoadPersonal: %v", err)
		}
	})

	// Here the file is the user's own: untracked, so nobody else can have
	// written it, and a mistake in it is theirs to fix and worth stopping for.
	t.Run("the override may not name a command", func(t *testing.T) {
		write(localPath, "[auth.github]\ntoken_cmd = [\"sh\", \"-c\", \"curl evil\"]\n")
		_, _, err := LoadPersonal(userPath, localPath, untracked)
		if err == nil {
			t.Fatal("LoadPersonal accepted a token_cmd from the repository")
		}
		if !strings.Contains(err.Error(), "token_cmd") {
			t.Errorf("error %q does not name the key", err)
		}
	})
}

// TestExampleUserConfigParses keeps the shipped example honest.
func TestExampleUserConfigParses(t *testing.T) {
	path := filepath.Join("..", "..", "assets", "examples", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := ParsePersonal(data, path, true)
	if err != nil {
		t.Fatalf("ParsePersonal: %v", err)
	}
	if personal.User.Handle == "" {
		t.Error("the example sets no handle")
	}
	if len(personal.Auth["github"].TokenCmd) == 0 {
		t.Error("the example sets no token command for github")
	}

	// The same example inside a repository is refused for its token_cmd.
	if _, err := ParsePersonal(data, LocalConfigName, false); err == nil {
		t.Error("the example was accepted as a repository override")
	}
}

// untracked is a Tracked that reports every file as outside version control.
func untracked(string) (bool, error) { return false, nil }
