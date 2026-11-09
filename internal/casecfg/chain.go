package casecfg

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// GatewayDir is where a case keeps one gateway config per level.
func GatewayDir(caseDir string) string { return filepath.Join(caseDir, "gateway") }

// Chain is the gateway configs of one directory by the level each declares.
// A variant such as L3-reviewed.yaml counts for the level it declares.
type Chain map[Level][]string

// ReadChain reads the level every *.yaml in dir declares. A file that does
// not parse or declares no level is left out: loading it fails on its own.
func ReadChain(dir string) (Chain, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list gateway configs: %w", err)
	}
	chain := Chain{}
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // G304: the operator names their own case
		if err != nil {
			return nil, fmt.Errorf("read gateway config: %w", err)
		}
		var head struct {
			Level Level `yaml:"level"`
		}
		if yaml.Unmarshal(data, &head) != nil || head.Level == "" {
			continue
		}
		chain[head.Level] = append(chain[head.Level], filepath.Base(p))
	}
	return chain, nil
}

// Missing lists, lowest first, the levels below the highest one the chain
// has that it has no config for.
func (c Chain) Missing() []Level {
	top := -1
	for i, l := range stack {
		if len(c[l]) > 0 {
			top = i
		}
	}
	var out []Level
	for _, l := range stack[:max(top, 0)] {
		if len(c[l]) == 0 {
			out = append(out, l)
		}
	}
	return out
}

// stack is the order levels are built in: each one adds a single thing to the
// one below, so a case that has Lk has every level under it (docs/levels.md).
var stack = []Level{L0, L1, L2, L3}

// CheckChain refuses a gateway directory that skips a level: an L2 with no L1
// would leave the score chain with a hole where one step's effect should show.
func CheckChain(dir string) error {
	chain, err := ReadChain(dir)
	if err != nil {
		return err
	}
	missing := chain.Missing()
	if len(missing) == 0 {
		return nil
	}
	var have []string
	for _, l := range stack {
		if len(chain[l]) > 0 {
			have = append(have, string(l))
		}
	}
	names := make([]string, len(missing))
	for i, l := range missing {
		names[i] = string(l)
	}
	return fmt.Errorf("levels cannot be skipped: %s has %s but no config for %s. A case is built L0 → L1 → L2 → L3, so every level shows what its one step adds. Add %s (copy the level below, change `level:` and what that level adds; `lodog init` writes all of them) — docs/levels.md",
		dir, strings.Join(have, ", "), strings.Join(names, ", "), strings.Join(withSuffix(names, ".yaml"), ", "))
}

func withSuffix(names []string, suffix string) []string {
	return slices.Collect(func(yield func(string) bool) {
		for _, n := range names {
			if !yield(n + suffix) {
				return
			}
		}
	})
}
