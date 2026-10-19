package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Doctor (spec 07 §1–2, §9) is the frame the checks hang in: what a check is,
// what it may answer, what order they run in, what a run's exit code is, and
// how a run is written down.
//
// The checks themselves live elsewhere — the setup checks of §5 and the
// live-health audit of §6 are their own work. What is here is everything they
// have in common, and in particular the two things that must never be decided
// twice: a check whose dependency did not hold is not asked its question, and
// the exit code is a property of the states, not of whoever is rendering them.

// DoctorVersion is the schema a report's JSON declares (spec 02 §3).
const DoctorVersion = "flow.doctor/v1"

// State is what a check found (spec 07 §2).
type State string

// The states a check may report.
const (
	// StatePass means the thing is as declared.
	StatePass State = "pass"
	// StateFail means it is not, and flow is sure.
	StateFail State = "fail"
	// StateWarn means a live check is below 100% but above its threshold.
	StateWarn State = "warn"
	// StateInput means a human must supply a value flow cannot discover.
	StateInput State = "input"
	// StateBlocked means a check this one depends on did not hold, so the
	// question was not asked.
	StateBlocked State = "blocked"
	// StateExternal means somebody else has been asked, and has not yet done
	// it: a follow-up is open.
	StateExternal State = "external"
	// StateAttest means a human's word is what would settle this, and there
	// is none, or it has expired (spec 07 §8).
	StateAttest State = "attest"
	// StateDrift means it passed before and the world has moved since
	// (spec 07 §7).
	StateDrift State = "drift"
	// StateSkip means the check does not apply to this configuration.
	StateSkip State = "skip"
	// StateError means flow could not find out.
	StateError State = "error"
)

// Symbol is the glyph that stands for the state (spec 07 §2).
func (s State) Symbol() string {
	switch s {
	case StatePass:
		return "✓"
	case StateFail:
		return "✗"
	case StateWarn:
		return "⚠"
	case StateInput:
		return "?"
	case StateBlocked:
		return "⏸"
	case StateExternal:
		return "⏳"
	case StateAttest:
		return "✎"
	case StateDrift:
		return "≠"
	case StateError:
		return "!"
	default:
		return "·"
	}
}

// OK reports whether the state is one a run can end on without anybody doing
// anything. A blocked check is OK because the dependency it waited on carries
// the signal; reporting both would count one problem twice (spec 07 §2).
func (s State) OK() bool {
	return s == StatePass || s == StateSkip || s == StateBlocked
}

// Satisfies reports whether a check in this state lets the checks that depend
// on it be asked their question.
//
// Only a pass and a skip do. A skip is not a worse answer than a pass: a check
// that does not apply has nothing to say about anything downstream of it, and
// blocking there would hide every check behind the first part of the
// configuration a team did not switch on.
func (s State) Satisfies() bool {
	return s == StatePass || s == StateSkip
}

// Doctor check categories (spec 07 §1), in the order a report groups them.
const (
	CategoryConfig    = "config"
	CategoryLocal     = "local"
	CategoryAuth      = "auth"
	CategoryForge     = "forge"
	CategoryTracker   = "tracker"
	CategoryDeploys   = "deploys"
	CategoryLive      = "live"
	CategoryFollowUps = "followups"
	CategoryMeta      = "meta"
)

// Categories is every category, in report order.
var Categories = []string{
	CategoryConfig, CategoryLocal, CategoryAuth, CategoryForge, CategoryTracker,
	CategoryDeploys, CategoryLive, CategoryFollowUps, CategoryMeta,
}

// The kinds of remediation a result can carry (spec 07 §1). The kind is what
// decides whether a failure is something the user can act on or something
// that has simply gone wrong.
const (
	// RemedyAuto means `flow doctor --fix` can do it.
	RemedyAuto = "auto"
	// RemedyInput means a value is needed (spec 07 §3).
	RemedyInput = "input"
	// RemedyFollowUp means a ticket is the remedy (spec 07 §4).
	RemedyFollowUp = "followup"
	// RemedyAttest means a human's word settles it (spec 07 §8).
	RemedyAttest = "attest"
	// RemedyManual means somebody must do it by hand, with the steps given.
	RemedyManual = "manual"
	// RemedyNone means there is nothing to be done about it here.
	RemedyNone = "none"
)

// Evidence is one thing a check saw, with somewhere to go and look at it.
type Evidence struct {
	// Text is the thing, in one line.
	Text string `json:"text"`
	// URL is where to see it, empty when it is not a thing with a page.
	URL string `json:"url,omitempty"`
}

// Remediation is what to do about a result.
type Remediation struct {
	// Kind is one of the Remedy constants.
	Kind string `json:"kind,omitempty"`
	// Summary is the remedy in one line.
	Summary string `json:"summary,omitempty"`
	// Command is the exact command that does it, when one does.
	Command string `json:"command,omitempty"`
	// Steps are what to do by hand, in order.
	Steps []string `json:"steps,omitempty"`
	// Owner is who it is for, resolved from followups.owners.
	Owner string `json:"owner,omitempty"`
}

// Actionable reports whether there is something to do. A failure with no
// remedy is a hard failure: flow has found something wrong and has nothing to
// suggest, which is a different thing from a failure a human can fix today
// (spec 07 §2).
func (r Remediation) Actionable() bool {
	return r.Kind != "" && r.Kind != RemedyNone
}

// Counts is how much of what a live check looked at was right (spec 07 §6).
type Counts struct {
	// Pass is how many items held.
	Pass int `json:"pass"`
	// Total is how many were looked at.
	Total int `json:"total"`
}

// Ratio is the share that held. Nothing looked at is nothing wrong.
func (c Counts) Ratio() float64 {
	if c.Total <= 0 {
		return 1
	}
	return float64(c.Pass) / float64(c.Total)
}

// String is the `n/m` a live check's line carries (spec 07 §9).
func (c Counts) String() string { return fmt.Sprintf("%d/%d", c.Pass, c.Total) }

// Grade turns a count into a state at the thresholds of spec 07 §6: everything
// passing is a pass, anything below failBelow is a failure, and in between is
// a warning. A critical check has no warning band — the point of marking one
// critical is that a single item getting through is a failure.
func Grade(c Counts, failBelow float64, critical bool) State {
	switch {
	case c.Ratio() >= 1:
		return StatePass
	case critical, c.Ratio() < failBelow:
		return StateFail
	default:
		return StateWarn
	}
}

// Result is what a check answered (spec 07 §1).
type Result struct {
	// State is the answer.
	State State `json:"state"`
	// Message is the answer in one line, in the team's own terms.
	Message string `json:"message,omitempty"`
	// Evidence is what the check saw, with links. A live check lists the
	// items that did not hold.
	Evidence []Evidence `json:"evidence,omitempty"`
	// Remediation is what to do about it.
	Remediation Remediation `json:"remediation,omitzero"`
	// ObservedHash is the canonical hash of what the check looked at, which
	// is what tells drift from a first failure (spec 07 §7).
	ObservedHash string `json:"observed_hash,omitempty"`
	// Counts is set by live checks, nil elsewhere.
	Counts *Counts `json:"counts,omitempty"`
	// Err is why a check could not find out. It is what decides whether a run
	// of nothing but errors exits 69 or 77 rather than 1 (spec 07 §2), which
	// cannot be worked out from the state alone.
	Err error `json:"-"`
}

// MarshalJSON renders the error as its message: a report is read by people and
// by CI, and neither can do anything with a Go value.
func (r Result) MarshalJSON() ([]byte, error) {
	type alias Result
	out := struct {
		alias
		Error string `json:"error,omitempty"`
	}{alias(r), ""}
	if r.Err != nil {
		out.Error = r.Err.Error()
	}
	return json.Marshal(out)
}

// Pass is a result that holds.
func Pass(format string, args ...any) Result {
	return Result{State: StatePass, Message: fmt.Sprintf(format, args...)}
}

// Skip is a result for a check this configuration does not call for.
func Skip(format string, args ...any) Result {
	return Result{State: StateSkip, Message: fmt.Sprintf(format, args...)}
}

// Fail is a result that does not hold, with nothing yet said about what to do.
// Attach a [Remediation] to make it actionable rather than hard.
func Fail(format string, args ...any) Result {
	return Result{State: StateFail, Message: fmt.Sprintf(format, args...)}
}

// Failed is a result for a check that could not find out.
func Failed(err error) Result {
	return Result{State: StateError, Message: err.Error(), Err: err}
}

// Fix returns a copy of the result with a remediation attached.
func (r Result) Fix(remedy Remediation) Result {
	r.Remediation = remedy
	return r
}

// Said returns a copy of the result with its message set. A live check grades
// itself before it knows what to say — "all nine held" and "two of nine did
// not" are different sentences — so the message arrives after the state.
func (r Result) Said(format string, args ...any) Result {
	r.Message = fmt.Sprintf(format, args...)
	return r
}

// Saw returns a copy of the result with one more piece of evidence.
func (r Result) Saw(text, url string) Result {
	r.Evidence = append(slices.Clip(r.Evidence), Evidence{Text: text, URL: url})
	return r
}

// Check is one question doctor asks (spec 07 §1).
type Check struct {
	// ID is the stable dotted id. It is also the id an attestation and a
	// follow-up marker use, so renaming one is a breaking change.
	ID string
	// Title is the question in the words a person reads.
	Title string
	// Category is which group it is reported under.
	Category string
	// DependsOn are the checks that must hold before this one is worth
	// asking.
	DependsOn []string
	// Run asks the question. It must not mutate anything unless Env.Fix is
	// set, and must not panic: a check that cannot find out returns
	// [Failed].
	Run func(ctx context.Context, env *Env) Result
}

// Env is the world the checks run against. A check reads it and, when Fix is
// set, is allowed to act on it.
type Env struct {
	// Config is the workflow the team asked for. It may be nil when the
	// configuration itself is what failed to load, which is exactly what
	// config.syntax reports — so every other check depends on that one.
	Config *config.Config
	// Lock is what flow has learned: identifiers, attestations, creations.
	Lock *config.Lock
	// Desired is what the configuration asks of the providers (spec 06 §1).
	Desired Desired
	// Plan is the plan made for this run, which is what the setup checks
	// about a provider's state are reading.
	Plan Plan
	// PlanErr is why there is no plan, when a provider would not answer.
	//
	// It has to be carried rather than swallowed. An empty plan and a plan
	// in which nothing is wanted look identical to a check reading it, and
	// the second of those is a skip — so a run that could not see the
	// providers at all would otherwise report every one of them as "this
	// configuration does not ask for it" and exit 0.
	PlanErr error
	// Adapters are the providers. Any may be nil.
	Adapters Adapters
	// Ancestry answers whether a merge is contained in a deployed commit,
	// remembering what it was told (spec 13 §3). Nil asks the forge, which
	// is the same answer without the cache in front of it.
	Ancestry core.Comparer
	// Trailers reads the trailer block of a commit message, which is how the
	// flag audit finds what a commit announced (spec 07 §6). It is the
	// working copy's own git, because the rules about where a trailer block
	// begins are git's (spec 08 §2). Nil is not "no trailers": the check
	// that needs it fails rather than reporting a window with nothing in it.
	Trailers TrailerParser
	// Clock is where the time comes from.
	Clock core.Clock
	// Window is how far back the live-health audit looks (spec 07 §6).
	Window time.Duration
	// FailBelow is the share of items a live check must see hold before it
	// fails rather than warns.
	FailBelow float64
	// Fix permits the remediations of spec 07 §6: placeholder resolution,
	// native apply items, the follow-up lifecycle, flag cleanup tickets and
	// catch-up transitions. Nothing else, ever.
	Fix bool
	// Set carries `--set key=value` answers for placeholders (spec 07 §3).
	Set map[string]string
	// Log receives one line per mutating call.
	Log *slog.Logger

	// results is what the checks before this one found, so that a check can
	// read its dependency's answer instead of asking the provider again.
	results map[string]Result
}

// Now is the instant this run is happening at.
func (e *Env) Now() time.Time {
	if e.Clock == nil {
		return time.Time{}
	}
	return e.Clock.Now()
}

// Since is the start of the window the live-health audit is about. An Env
// that was not told how far back to look takes [DefaultWindow], because a
// window of zero would be read as "no bound" by every adapter and quietly
// turn a fortnight's audit into the whole history of the repository.
func (e *Env) Since() time.Time {
	window := e.Window
	if window <= 0 {
		window = DefaultWindow
	}
	return e.Now().Add(-window)
}

// Comparer is what answers whether one commit contains another. It is the
// cache when the run was given one and the forge itself otherwise, so that a
// check never has to know which it got — and so that two checks asking about
// the same pair ask the same thing (spec 13 §3).
func (e *Env) Comparer() core.Comparer {
	if e.Ancestry != nil {
		return e.Ancestry
	}
	if e.Adapters.Forge == nil {
		return nil
	}
	return e.Adapters.Forge
}

// Threshold is the share of items a live check must see hold before it fails
// rather than warns. An Env that was not told takes the configured default,
// so that forgetting to set it cannot make the audit lenient: a zero would
// mean nothing can ever fall below the line (spec 07 §6).
func (e *Env) Threshold() float64 {
	if e.FailBelow <= 0 {
		return float64(config.DefaultFailBelow) / 100
	}
	return e.FailBelow
}

// Patience is how many working days work may sit in a column with nothing
// happening to it before the audit says so. Like [Env.Threshold] the default
// is applied here and nowhere else, so that the number the report prints and
// the number it judged by cannot come apart.
func (e *Env) Patience() int {
	if e.Config == nil || e.Config.Health.StaleAfter <= 0 {
		return config.DefaultStaleAfter
	}
	return e.Config.Health.StaleAfter
}

// Result returns what an earlier check found.
func (e *Env) Result(id string) (Result, bool) {
	result, ok := e.results[id]
	return result, ok
}

// Registry is the set of checks a run may ask. It is ordered: the order
// checks were registered in is the order they are reported in, within the
// constraint that a dependency runs first.
type Registry struct {
	checks []Check
	at     map[string]int
}

// NewRegistry builds a registry, refusing anything it could not run.
func NewRegistry(checks ...Check) (*Registry, error) {
	r := &Registry{at: map[string]int{}}
	for _, check := range checks {
		if err := r.Add(check); err != nil {
			return nil, err
		}
	}
	if _, err := r.Order(); err != nil {
		return nil, err
	}
	return r, nil
}

// Add registers one check. Everything that would make a check unrunnable is
// refused here rather than discovered half way through a run: a registry is
// built at startup, and a doctor that fails after doing half its work has
// told a worse story than one that would not start.
func (r *Registry) Add(c Check) error {
	if r.at == nil {
		r.at = map[string]int{}
	}
	switch {
	case c.ID == "":
		return fmt.Errorf("%w: a check with no id", core.ErrValidation)
	case c.Title == "":
		return fmt.Errorf("%w: %s has no title", core.ErrValidation, c.ID)
	case c.Run == nil:
		return fmt.Errorf("%w: %s has nothing to run", core.ErrValidation, c.ID)
	case !slices.Contains(Categories, c.Category):
		return fmt.Errorf("%w: %s is in category %q, which is not one of %v", core.ErrValidation, c.ID, c.Category, Categories)
	}
	if _, clash := r.at[c.ID]; clash {
		return fmt.Errorf("%w: %s is registered twice", core.ErrValidation, c.ID)
	}
	if slices.Contains(c.DependsOn, c.ID) {
		return fmt.Errorf("%w: %s depends on itself", core.ErrValidation, c.ID)
	}
	r.at[c.ID] = len(r.checks)
	r.checks = append(r.checks, c)
	return nil
}

// Find returns a check by id.
func (r *Registry) Find(id string) (Check, bool) {
	at, ok := r.at[id]
	if !ok {
		return Check{}, false
	}
	return r.checks[at], true
}

// Checks returns every registered check, in registration order.
func (r *Registry) Checks() []Check { return slices.Clone(r.checks) }

// Order returns the checks in the order they will run: registration order,
// with every check after the ones it depends on.
//
// Registration order is already dependency order if whoever wrote the list was
// careful. Sorting anyway means nobody has to be, and means a dependency added
// later cannot quietly put a check in front of what it depends on.
func (r *Registry) Order() ([]Check, error) {
	ordered := make([]Check, 0, len(r.checks))
	done := make(map[string]bool, len(r.checks))
	for _, check := range r.checks {
		for _, needed := range check.DependsOn {
			if _, known := r.at[needed]; !known {
				return nil, fmt.Errorf("%w: %s depends on %s, which is not a check", core.ErrValidation, check.ID, needed)
			}
		}
	}
	for len(ordered) < len(r.checks) {
		progress := false
		for _, check := range r.checks {
			if done[check.ID] {
				continue
			}
			ready := true
			for _, needed := range check.DependsOn {
				if !done[needed] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			done[check.ID] = true
			ordered = append(ordered, check)
			progress = true
		}
		if !progress {
			return nil, fmt.Errorf("%w: the checks %s depend on each other in a circle", core.ErrValidation, stuck(r.checks, done))
		}
	}
	return ordered, nil
}

// stuck names the checks a cycle is among, so the error says which ones.
func stuck(checks []Check, done map[string]bool) string {
	var names []string
	for _, check := range checks {
		if !done[check.ID] {
			names = append(names, check.ID)
		}
	}
	return joinWords(names)
}

// Select returns the checks matching any of the globs, in run order. No globs
// means every check. A glob that matches nothing is refused: `--check` is the
// user saying what they want run, and running nothing instead of saying the
// pattern was wrong would exit 0 and look like health (spec 02 §2).
func (r *Registry) Select(globs []string) ([]Check, error) {
	ordered, err := r.Order()
	if err != nil {
		return nil, err
	}
	if len(globs) == 0 {
		return ordered, nil
	}
	chosen := map[string]bool{}
	for _, glob := range globs {
		matched := false
		for _, check := range ordered {
			ok, err := path.Match(glob, check.ID)
			if err != nil {
				return nil, fmt.Errorf("%w: --check %q is not a valid pattern: %v", core.ErrValidation, glob, err)
			}
			if ok {
				chosen[check.ID] = true
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("%w: --check %q matches no check", core.ErrValidation, glob)
		}
	}
	selected := make([]Check, 0, len(chosen))
	for _, check := range ordered {
		if chosen[check.ID] {
			selected = append(selected, check)
		}
	}
	return selected, nil
}

// DoctorInput is a run of doctor.
type DoctorInput struct {
	// Registry is the checks that exist.
	Registry *Registry
	// Env is the world to ask them about.
	Env *Env
	// Only limits the run to the checks matching these globs
	// (`flow doctor --check`).
	Only []string
	// FlowVersion is the build doing the asking.
	FlowVersion string
	// Progress is told how the run is going, for a caller drawing it. It may
	// be nil, which is a run nobody is watching.
	Progress DoctorProgress
}

// DoctorProgress is told, as a run goes, which check has been reached and
// what it answered (spec 12 §3).
//
// It is a callback rather than a channel because a run is one goroutine
// asking one question at a time, and the thing being told is a terminal
// drawing a line: a channel would add a buffer whose size decides whether
// flow blocks on a screen. It is called from the goroutine running the
// checks, so an implementation that does anything slow must hand it on.
type DoctorProgress func(DoctorStep)

// DoctorStep is one moment of a run: a check about to be asked, and then the
// same check with its answer.
//
// Both moments are reported, because the thing a progress display is for is
// the gap between them — a live check reading a fortnight of pull requests is
// where a doctor run spends its time, and a screen that only spoke when a
// check finished would be silent for exactly as long as the wait that made
// somebody wonder whether flow had hung.
type DoctorStep struct {
	// Check is the one being asked.
	Check Check
	// Asked is how many checks have answered so far, and Total how many will
	// be asked in all. A check in flight is not yet counted in Asked.
	Asked, Total int
	// Result is what it answered, and is the zero result while it is still
	// being asked.
	Result Result
}

// Answered reports whether this step is a check that has answered rather than
// one being asked. Every result has a state, so the zero one cannot be
// mistaken for an answer.
func (s DoctorStep) Answered() bool { return s.Result.State != "" }

// tell reports a step, when there is anybody to tell.
func (in DoctorInput) tell(step DoctorStep) {
	if in.Progress == nil {
		return
	}
	in.Progress(step)
}

// Checked is one check and what it answered.
type Checked struct {
	// ID, Title and Category are the check's own.
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	// DependsOn is what it waited for.
	DependsOn []string `json:"depends_on,omitempty"`
	// BlockedBy names the check whose answer stopped this one being asked.
	BlockedBy string `json:"blocked_by,omitempty"`
	// Result is the answer.
	Result Result `json:"result"`
}

// Report is a whole run (spec 07 §9).
type Report struct {
	// Version is [DoctorVersion].
	Version string `json:"version"`
	// FlowVersion is the build that ran it.
	FlowVersion string `json:"flow_version,omitempty"`
	// GeneratedAt is when it ran.
	GeneratedAt time.Time `json:"generated_at"`
	// Window is how far back the live-health audit looks, written the way a
	// human would have typed it (spec 02 §2's --window).
	Window string `json:"window,omitempty"`
	// Checks is one entry per check that was selected, in run order.
	Checks []Checked `json:"checks"`
}

// RunDoctor asks every selected check its question, in dependency order,
// skipping the ones whose dependencies did not hold.
//
// It returns an error only when the run could not be made: a registry that
// does not hang together, or a pattern that matches nothing. A check that goes
// wrong is a result, not an error — the point of doctor is to say everything
// that is true about the repository in one pass, and stopping at the first
// provider that is having a bad day would say almost none of it.
func RunDoctor(ctx context.Context, in DoctorInput) (Report, error) {
	if in.Registry == nil {
		return Report{}, fmt.Errorf("%w: doctor was given no checks", core.ErrValidation)
	}
	if in.Env == nil {
		return Report{}, fmt.Errorf("%w: doctor was given no environment", core.ErrValidation)
	}
	selected, err := in.Registry.Select(in.Only)
	if err != nil {
		return Report{}, err
	}
	if in.Env.Log == nil {
		in.Env.Log = slog.New(slog.DiscardHandler)
	}
	in.Env.results = make(map[string]Result, len(selected))

	report := Report{
		Version:     DoctorVersion,
		FlowVersion: in.FlowVersion,
		GeneratedAt: in.Env.Now().UTC(),
		Checks:      make([]Checked, 0, len(selected)),
	}
	if in.Env.Window > 0 {
		report.Window = config.Duration(in.Env.Window).String()
	}

	for asked, check := range selected {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		in.tell(DoctorStep{Check: check, Asked: asked, Total: len(selected)})
		checked := Checked{
			ID:        check.ID,
			Title:     check.Title,
			Category:  check.Category,
			DependsOn: check.DependsOn,
		}
		if blocker, ok := blockedBy(in.Env.results, check); ok {
			checked.BlockedBy = blocker
			checked.Result = Result{
				State:   StateBlocked,
				Message: "not asked: " + blocker + " did not hold",
			}
		} else {
			checked.Result = run(ctx, check, in.Env)
		}
		in.Env.results[check.ID] = checked.Result
		report.Checks = append(report.Checks, checked)
		in.tell(DoctorStep{Check: check, Asked: asked + 1, Total: len(selected), Result: checked.Result})
	}
	return report, nil
}

// run asks one check, and turns a panic into an error result. A check reaches
// into a provider's reply, and a doctor that dies on one unexpected nil says
// nothing about the other twenty things that were fine.
func run(ctx context.Context, check Check, env *Env) (result Result) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("%s panicked: %v", check.ID, r)
			env.Log.Error("a check panicked", "check", check.ID, "panic", fmt.Sprint(r))
			result = Failed(err)
		}
	}()
	result = check.Run(ctx, env)
	if result.State == "" {
		return Failed(fmt.Errorf("%s answered nothing", check.ID))
	}
	return result
}

// blockedBy reports the dependency that stopped this check being asked.
//
// A dependency that was not run at all does not block: `--check 'forge.*'` is
// the user asking about the forge, and refusing to look because auth.forge was
// not in the selection would answer a question nobody asked. Because a blocked
// check is itself recorded as blocked, and blocked does not satisfy, this
// carries down a chain of any length.
func blockedBy(results map[string]Result, check Check) (string, bool) {
	for _, needed := range check.DependsOn {
		result, ran := results[needed]
		if ran && !result.State.Satisfies() {
			return needed, true
		}
	}
	return "", false
}

// Count returns how many checks are in a state.
func (r Report) Count(state State) int {
	n := 0
	for _, checked := range r.Checks {
		if checked.Result.State == state {
			n++
		}
	}
	return n
}

// Find returns one check's result.
func (r Report) Find(id string) (Checked, bool) {
	for _, checked := range r.Checks {
		if checked.ID == id {
			return checked, true
		}
	}
	return Checked{}, false
}

// Actions is how many things the person running doctor must do themselves: a
// failure, a value only they can give, a drift they must accept or undo, a
// live check below its threshold, and anything flow could not find out — not
// knowing is also something to look into.
func (r Report) Actions() int {
	n := 0
	for _, checked := range r.Checks {
		switch checked.Result.State {
		case StateFail, StateInput, StateDrift, StateWarn, StateError:
			n++
		}
	}
	return n
}

// Waiting is how many things somebody else has been asked for.
func (r Report) Waiting() int { return r.Count(StateExternal) }

// Attestations is how many need a human's word (spec 07 §8).
func (r Report) Attestations() int { return r.Count(StateAttest) }

// Exit is the run's exit code (spec 07 §2).
//
// The states do not simply map onto codes, because the same state means
// different things: a failure with a remedy is somebody's afternoon, and a
// failure without one is flow saying something is wrong that it cannot help
// with. And a run that found nothing but errors is a run that could not talk
// to a provider, which is worth saying as 69 or 77 rather than as a flat
// failure — but only when there is nothing else wrong, because a real failure
// hiding behind an unreachable provider must not be reported as weather.
func (r Report) Exit() int {
	hard, actionable, errored := false, false, false
	codes := map[int]bool{}
	for _, checked := range r.Checks {
		switch checked.Result.State {
		case StateError:
			errored = true
			codes[errorCode(checked.Result.Err)] = true
		case StateFail:
			if checked.Result.Remediation.Actionable() {
				actionable = true
			} else {
				hard = true
			}
		case StateWarn, StateInput, StateExternal, StateAttest, StateDrift:
			actionable = true
		}
	}
	switch {
	case hard:
		return core.ExitFail
	case errored && !actionable && len(codes) == 1:
		return slices.Max(slices.Collect(maps.Keys(codes)))
	case errored:
		return core.ExitFail
	case actionable:
		return core.ExitActionRequired
	default:
		return core.ExitOK
	}
}

// MarshalJSON writes the report with the code the run exited on in it.
//
// The code is derived from the results rather than stored alongside them, so
// the two cannot come to disagree — but nothing reading the document should
// have to re-derive it, and spec 07 §9 puts it on the summary line a person
// reads for exactly that reason.
func (r Report) MarshalJSON() ([]byte, error) {
	type report Report
	return json.Marshal(struct {
		report
		ExitCode int `json:"exit_code"`
	}{report(r), r.Exit()})
}

// errorCode is the exit code one error on its own would have had. An error
// flow has nothing to say about is a plain failure rather than weather.
func errorCode(err error) int {
	if err == nil {
		return core.ExitFail
	}
	switch code := core.ExitCode(err); code {
	case core.ExitUnavailable, core.ExitNoPerm:
		return code
	default:
		return core.ExitFail
	}
}

// Err returns the first error a check reported, so that a caller which only
// wants to know what went wrong does not have to walk the report.
func (r Report) Err() error {
	for _, checked := range r.Checks {
		if checked.Result.Err != nil {
			return fmt.Errorf("%s: %w", checked.ID, checked.Result.Err)
		}
	}
	return nil
}

// JSON renders the report as the one document `--json` prints (spec 02 §3).
func (r Report) JSON() ([]byte, error) {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the doctor report: %w", err)
	}
	return append(out, '\n'), nil
}

// joinWords lists names the way a sentence does.
func joinWords(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
