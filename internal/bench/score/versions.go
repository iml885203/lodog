package score

import (
	"fmt"
	"strings"
)

// VersionPin is one thing the case pins about the system under test, with the
// command that reports what is actually running. Only the case knows how to
// ask its system, so the command is data: lodog compares its output and never
// interprets it.
type VersionPin struct {
	Name    string
	Expect  string
	Observe []string
}

// ObservedVersion is one pin as the runner found it before the first attempt.
type ObservedVersion struct {
	Name     string `yaml:"name" json:"name"`
	Expected string `yaml:"expected" json:"expected"`
	Observed string `yaml:"observed" json:"observed"`
	Match    bool   `yaml:"match" json:"match"`
	Error    string `yaml:"error,omitempty" json:"error,omitempty"`
}

type versionEntry struct {
	Name    string `yaml:"name"`
	Expect  string `yaml:"expect"`
	Observe string `yaml:"observe"`
}

func parseVersions(entries []versionEntry) ([]VersionPin, error) {
	pins := make([]VersionPin, 0, len(entries))
	seen := map[string]bool{}
	for i, e := range entries {
		name, expect, observe := strings.TrimSpace(e.Name), strings.TrimSpace(e.Expect), strings.Fields(e.Observe)
		if name == "" || expect == "" || len(observe) == 0 {
			return nil, fmt.Errorf("%w: case.yaml versions[%d] needs name, expect and observe", ErrIncompleteCase, i)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: case.yaml versions has %s twice", ErrIncompleteCase, name)
		}
		seen[name] = true
		pins = append(pins, VersionPin{Name: name, Expect: expect, Observe: observe})
	}
	return pins, nil
}
