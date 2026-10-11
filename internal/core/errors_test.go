package core_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

func TestExitCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sentinel error
		want     int
	}{
		{"nil", nil, core.ExitOK},
		{"not found", core.ErrNotFound, core.ExitFail},
		{"permission", core.ErrPermission, core.ExitNoPerm},
		{"rate limited", core.ErrRateLimited, core.ExitUnavailable},
		{"conflict", core.ErrConflict, core.ExitFail},
		{"unsupported", core.ErrUnsupported, core.ExitFail},
		{"needs input", core.ErrNeedsInput, core.ExitActionRequired},
		{"validation", core.ErrValidation, core.ExitUsage},
		{"unavailable", core.ErrUnavailable, core.ExitUnavailable},
		{"unrecognised", errors.New("boom"), core.ExitFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := core.ExitCode(tc.sentinel); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.sentinel, got, tc.want)
			}
			if tc.sentinel == nil {
				return
			}
			// The same mapping must hold through arbitrary wrapping.
			wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", tc.sentinel))
			if got := core.ExitCode(wrapped); got != tc.want {
				t.Errorf("ExitCode(wrapped %v) = %d, want %d", tc.sentinel, got, tc.want)
			}
			ue := core.Errorf(tc.sentinel, "doing a thing")
			if got := core.ExitCode(ue); got != tc.want {
				t.Errorf("ExitCode(UserError %v) = %d, want %d", tc.sentinel, got, tc.want)
			}
		})
	}
}

// Every sentinel must map to a code the CLI documents in spec 02 §4.
func TestExitCodeIsDocumented(t *testing.T) {
	t.Parallel()
	documented := map[int]bool{
		core.ExitOK: true, core.ExitFail: true, core.ExitActionRequired: true,
		core.ExitUsage: true, core.ExitUnavailable: true, core.ExitNoPerm: true,
		core.ExitInterrupted: true,
	}
	sentinels := []error{
		core.ErrNotFound, core.ErrPermission, core.ErrRateLimited, core.ErrConflict,
		core.ErrUnsupported, core.ErrNeedsInput, core.ErrValidation, core.ErrUnavailable,
	}
	for _, s := range sentinels {
		if !documented[core.ExitCode(s)] {
			t.Errorf("%v maps to undocumented exit code %d", s, core.ExitCode(s))
		}
	}
}

func TestUserError(t *testing.T) {
	t.Parallel()
	err := core.Errorf(core.ErrValidation, "read %s", ".flow.yaml").
		WithWhy("unknown key %q", "colour").
		WithFix("remove it or run %s", "flow schema")

	if !errors.Is(err, core.ErrValidation) {
		t.Fatal("UserError must unwrap to its sentinel")
	}
	if got, want := err.What, "read .flow.yaml"; got != want {
		t.Errorf("What = %q, want %q", got, want)
	}
	if got, want := err.Why, `unknown key "colour"`; got != want {
		t.Errorf("Why = %q, want %q", got, want)
	}
	if got, want := err.Fix, "remove it or run flow schema"; got != want {
		t.Errorf("Fix = %q, want %q", got, want)
	}
	if got, want := err.Error(), `read .flow.yaml: unknown key "colour"`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// WithWhy and WithFix must not mutate the receiver.
func TestUserErrorCopies(t *testing.T) {
	t.Parallel()
	base := core.Errorf(core.ErrNotFound, "find project")
	withWhy := base.WithWhy("no project named flow")
	if base.Why != "" {
		t.Errorf("WithWhy mutated the receiver: %q", base.Why)
	}
	if withWhy.Why == "" {
		t.Error("WithWhy did not set Why on the copy")
	}
	if withFix := base.WithFix("create one"); base.Fix != "" || withFix.Fix == "" {
		t.Errorf("WithFix mutated the receiver (base.Fix=%q)", base.Fix)
	}
}
