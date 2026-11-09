package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/llm"
)

// ErrVersionMismatch means the running system is not the one the case pins,
// so a score from it could not be compared with any other.
var ErrVersionMismatch = errors.New("the running system does not match case.yaml versions")

// Observer runs one pin's observe command and returns its output.
type Observer func(ctx context.Context, argv []string) (string, error)

const observeTimeout = 30 * time.Second

// CheckVersions observes every pin. A case that pins nothing, a command that
// fails and an output that differs from the pin all refuse the run: a score
// is only worth recording when it says which system produced it.
func CheckVersions(ctx context.Context, pins []score.VersionPin, observe Observer) ([]score.ObservedVersion, error) {
	if len(pins) == 0 {
		return nil, fmt.Errorf("%w: case.yaml has no versions; pin what the benchmark was built against and how to observe it (docs/benchmark.md)", ErrPreflight)
	}
	observed := make([]score.ObservedVersion, 0, len(pins))
	var mismatches []string
	for _, p := range pins {
		v := observeOne(ctx, p, observe)
		observed = append(observed, v)
		if !v.Match {
			mismatches = append(mismatches, describeMismatch(v))
		}
	}
	if len(mismatches) > 0 {
		return observed, fmt.Errorf("%w: %w; bring the system to the pinned versions, or change the pins in case.yaml if the benchmark now targets another version:\n%s",
			ErrPreflight, ErrVersionMismatch, strings.Join(mismatches, "\n"))
	}
	return observed, nil
}

func observeOne(ctx context.Context, p score.VersionPin, observe Observer) score.ObservedVersion {
	ctx, cancel := context.WithTimeout(ctx, observeTimeout)
	defer cancel()
	out, err := observe(ctx, p.Observe)
	v := score.ObservedVersion{Name: p.Name, Expected: p.Expect, Observed: strings.TrimSpace(out)}
	if err != nil {
		v.Error = err.Error()
	}
	v.Match = v.Error == "" && v.Observed == v.Expected
	return v
}

func describeMismatch(v score.ObservedVersion) string {
	if v.Error != "" {
		return fmt.Sprintf("  %s: expected %q, could not observe: %s", v.Name, v.Expected, v.Error)
	}
	return fmt.Sprintf("  %s: expected %q, running %q", v.Name, v.Expected, v.Observed)
}

// ObserveCommand runs an observe command from the working directory, like
// benchmark.reset: split on spaces, no shell.
func ObserveCommand(ctx context.Context, argv []string) (string, error) {
	out, err := llm.Command(ctx, "", os.Environ(), argv[0], argv[1:]...)
	return string(out), err
}
