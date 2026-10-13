package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/core"
	"github.com/BurntSushi/toml"
)

// LocalConfigName is the personal, per-repository override file. It is not
// committed: it holds one person's preferences, not the team's policy.
const LocalConfigName = ".flow.local.toml"

// UI themes (spec 03 §4).
const (
	ThemeAuto  = "auto"
	ThemeDark  = "dark"
	ThemeLight = "light"
)

// Log formats (spec 02 §1).
const (
	LogText = "text"
	LogJSON = "json"
)

// Personal is what one person may set for themselves: who they are, how they
// want output to look, and where their credentials come from. It can never
// change team policy — that lives in `.flow.yaml`, which this file may not
// touch (spec 03 §4).
//
// Fields that may legitimately be false use pointers, so that an unset value
// and a deliberate `false` are told apart when layers are merged.
type Personal struct {
	// User is who flow attributes attestations and follow-ups to.
	User UserSection `toml:"user"`
	// UI is how output looks.
	UI UISection `toml:"ui"`
	// Log is how much flow says on stderr.
	Log LogSection `toml:"log"`
	// Auth is where credentials come from, keyed by provider.
	Auth map[string]AuthSource `toml:"auth"`
}

// UserSection identifies the person running flow.
type UserSection struct {
	// Handle is the login flow records as the author of an attestation.
	Handle string `toml:"handle"`
}

// UISection is personal presentation, never behaviour.
type UISection struct {
	// Theme is auto, dark or light.
	Theme string `toml:"theme"`
	// ASCII swaps Unicode glyphs for ASCII ones.
	ASCII *bool `toml:"ascii"`
	// NoColor turns off ANSI colour.
	NoColor *bool `toml:"no_color"`
}

// LogSection is personal logging preference.
type LogSection struct {
	// Level is debug, info, warn or error.
	Level string `toml:"level"`
	// Format is text or json.
	Format string `toml:"format"`
}

// AuthSource says where one provider's credential comes from, and where it may
// be sent. flow stores no secret itself (spec 15 §1).
type AuthSource struct {
	// TokenCmd is the argv flow runs to obtain a token. It is run directly,
	// never through a shell, and only when it came from the user's own config.
	TokenCmd []string `toml:"token_cmd"`
	// Hosts are the API hosts this credential may be sent to.
	Hosts []string `toml:"hosts"`
}

// Settings are the personal settings after every layer has had its say.
type Settings struct {
	// Handle is the login flow acts as in attestations and follow-ups.
	Handle string
	// Theme is auto, dark or light.
	Theme string
	// ASCII swaps Unicode glyphs for ASCII ones.
	ASCII bool
	// NoColor turns off ANSI colour.
	NoColor bool
	// LogLevel is debug, info, warn or error.
	LogLevel string
	// LogFormat is text or json.
	LogFormat string
	// Auth is where each provider's credential comes from.
	Auth map[string]AuthSource
}

// DefaultSettings are the settings flow uses when nobody has said otherwise
// (spec 02 §1).
func DefaultSettings() Settings {
	return Settings{Theme: ThemeAuto, LogLevel: "warn", LogFormat: LogText}
}

// Resolve layers personal settings in precedence order, lowest first, and
// returns what flow should actually do. Later layers override earlier ones
// field by field, so a user config that sets a theme is not undone by a repo
// override that only sets a handle.
//
// The full order in spec 03 is flags > env > `.flow.local.toml` > user TOML >
// `.flow.yaml` > preset > defaults; `.flow.yaml` has no say over any of these
// fields, so callers pass the personal layers in that order.
func Resolve(layers ...Personal) Settings {
	settings := DefaultSettings()
	for _, layer := range layers {
		if layer.User.Handle != "" {
			settings.Handle = layer.User.Handle
		}
		if layer.UI.Theme != "" {
			settings.Theme = layer.UI.Theme
		}
		if layer.UI.ASCII != nil {
			settings.ASCII = *layer.UI.ASCII
		}
		if layer.UI.NoColor != nil {
			settings.NoColor = *layer.UI.NoColor
		}
		if layer.Log.Level != "" {
			settings.LogLevel = layer.Log.Level
		}
		if layer.Log.Format != "" {
			settings.LogFormat = layer.Log.Format
		}
		for provider, source := range layer.Auth {
			settings.Auth = mergeAuth(settings.Auth, provider, source)
		}
	}
	return settings
}

// mergeAuth folds one provider's credential source into the resolved set,
// field by field so that a repo override naming extra hosts does not discard
// the token command the user configured.
func mergeAuth(into map[string]AuthSource, provider string, source AuthSource) map[string]AuthSource {
	if into == nil {
		into = map[string]AuthSource{}
	}
	existing := into[provider]
	if len(source.TokenCmd) > 0 {
		existing.TokenCmd = slices.Clone(source.TokenCmd)
	}
	if len(source.Hosts) > 0 {
		existing.Hosts = slices.Clone(source.Hosts)
	}
	into[provider] = existing
	return into
}

// FromEnv reads the personal settings the environment can carry (spec 03 §5).
// getenv is passed in so that tests never touch the process environment.
func FromEnv(getenv func(string) string) Personal {
	var personal Personal
	personal.Log.Level = strings.ToLower(getenv("FLOW_LOG_LEVEL"))
	personal.Log.Format = strings.ToLower(getenv("FLOW_LOG_FORMAT"))
	personal.UI.ASCII = envBool(getenv("FLOW_ASCII"))
	personal.UI.NoColor = envBool(getenv("FLOW_NO_COLOR"))

	// NO_COLOR is a cross-tool convention: any value at all, including "0",
	// means no colour. Only an unset or empty variable leaves it alone.
	// https://no-color.org
	if getenv("NO_COLOR") != "" {
		yes := true
		personal.UI.NoColor = &yes
	}
	return personal
}

// envBool reads a flow boolean environment variable. Anything unparseable is
// ignored rather than guessed at, leaving the lower layer in charge.
func envBool(value string) *bool {
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil
	}
	return &parsed
}

// UserConfigPath returns where the personal TOML lives, following
// FLOW_USER_CONFIG, then XDG, then the home directory (spec 02 §1).
func UserConfigPath(getenv func(string) string) string {
	if explicit := getenv("FLOW_USER_CONFIG"); explicit != "" {
		return explicit
	}
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "flow", "config.toml")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "flow", "config.toml")
	}
	return ""
}

// Tracked reports whether a path is under version control. flow refuses to
// read a personal override that the team can see, because a file everyone
// shares is not personal and must not be trusted like one (P9).
type Tracked func(path string) (bool, error)

// LoadPersonal reads the user's own TOML and the repository-local override,
// and returns them in precedence order, lowest first. Either path may be empty
// or absent; a personal file that does not exist is not a problem.
//
// tracked may be nil when flow has no git to ask — the local file is then
// trusted no further than the rules below allow it to be anyway.
//
// A local override flow will not trust is left unread and reported as a
// warning rather than as a refusal (scenario S01): the file belongs to the
// repository, and a file the team can commit must not be able to stop anybody
// else working. The run goes on with the layers flow does trust, and fails
// later — for want of a credential, say — only if it genuinely needed
// something that file was carrying.
func LoadPersonal(userPath, localPath string, tracked Tracked) ([]Personal, []string, error) {
	var layers []Personal

	user, ok, err := loadTOML(userPath, true)
	if err != nil {
		return nil, nil, err
	}
	if ok {
		layers = append(layers, user)
	}

	if localPath != "" && tracked != nil {
		isTracked, err := tracked(localPath)
		switch {
		case err != nil:
			return layers, []string{fmt.Sprintf("ignoring %s: flow could not ask git whether the file is tracked (%s), and a file it cannot tell apart from the team's is not personal", localPath, err)}, nil
		case isTracked:
			return layers, []string{fmt.Sprintf("ignoring %s: the file is tracked by git, so it is the team's and not yours; run git rm --cached %s and add it to .gitignore to have flow read it", localPath, filepath.Base(localPath))}, nil
		}
	}

	local, ok, err := loadTOML(localPath, false)
	if err != nil {
		return nil, nil, err
	}
	if ok {
		layers = append(layers, local)
	}
	return layers, nil, nil
}

// loadTOML reads one personal file. The second result reports whether the file
// was there at all.
func loadTOML(path string, allowTokenCmd bool) (Personal, bool, error) {
	if path == "" {
		return Personal{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Personal{}, false, nil
		}
		return Personal{}, false, core.Errorf(core.ErrValidation, "read %s", path).WithWhy("%s", err)
	}
	personal, err := ParsePersonal(data, path, allowTokenCmd)
	if err != nil {
		return Personal{}, false, err
	}
	return personal, true, nil
}

// ParsePersonal decodes a personal TOML file.
//
// Decoding is strict, and the vocabulary is deliberately small: user, ui, log
// and auth. A `[commits]` or `[workflow]` table here would be someone quietly
// holding themselves to a different standard than the team, so it is refused
// rather than ignored (spec 03 §4).
//
// allowTokenCmd must be false for anything inside the repository: a file the
// repository can change must never say what command flow runs (spec 15 §1).
func ParsePersonal(src []byte, path string, allowTokenCmd bool) (Personal, error) {
	var personal Personal
	meta, err := toml.Decode(string(src), &personal)
	if err != nil {
		return Personal{}, newParseError(path, []Problem{tomlProblem(err, path)})
	}

	var problems []Problem
	for _, key := range meta.Undecoded() {
		problems = append(problems, Problem{
			Path:    path,
			Message: fmt.Sprintf("unknown key %q; a personal file may only set user, ui, log and auth — team policy belongs in %s", key.String(), DefaultPath),
		})
	}
	problems = append(problems, checkPersonalValues(personal, path, allowTokenCmd)...)
	if len(problems) > 0 {
		return Personal{}, newParseError(path, problems)
	}
	return personal, nil
}

// checkPersonalValues enforces the values a personal file may hold.
func checkPersonalValues(personal Personal, path string, allowTokenCmd bool) []Problem {
	var problems []Problem
	add := func(format string, args ...any) {
		problems = append(problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	}

	if theme := personal.UI.Theme; theme != "" && !slices.Contains([]string{ThemeAuto, ThemeDark, ThemeLight}, theme) {
		add("ui.theme is %q; it must be %s, %s or %s", theme, ThemeAuto, ThemeDark, ThemeLight)
	}
	if level := personal.Log.Level; level != "" && !slices.Contains([]string{"debug", "info", "warn", "error"}, level) {
		add("log.level is %q; it must be debug, info, warn or error", level)
	}
	if format := personal.Log.Format; format != "" && !slices.Contains([]string{LogText, LogJSON}, format) {
		add("log.format is %q; it must be %s or %s", format, LogText, LogJSON)
	}

	for _, provider := range sortedKeys(personal.Auth) {
		source := personal.Auth[provider]
		if len(source.TokenCmd) > 0 && !allowTokenCmd {
			add("auth.%s.token_cmd cannot be set here: %s is inside the repository, and flow does not run commands named by files the repository can change. Move it to your user config", provider, filepath.Base(path))
		}
		if len(source.TokenCmd) > 0 && strings.TrimSpace(source.TokenCmd[0]) == "" {
			add("auth.%s.token_cmd starts with an empty command", provider)
		}
		for _, host := range source.Hosts {
			if strings.Contains(host, "/") || strings.Contains(host, ":") {
				add("auth.%s.hosts has %q; list host names only, without a scheme or path", provider, host)
			}
		}
	}
	return problems
}

// tomlProblem turns a TOML parse error into a located problem.
func tomlProblem(err error, path string) Problem {
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) {
		return Problem{
			Path:    path,
			Line:    parseErr.Position.Line,
			Column:  parseErr.Position.Col,
			Message: parseErr.Message,
		}
	}
	return Problem{Path: path, Message: err.Error()}
}
