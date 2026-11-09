package casecfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gatewayDir(t *testing.T, levels ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range levels {
		level := strings.SplitN(name, "-", 2)[0]
		body := "case: c\nlevel: " + level + "\nlisten: 127.0.0.1:7400\nupstream: http://u\nauth: {type: api-key}\nidentity: {actor: a}\n"
		if level == "L3" {
			body += "approvals: {listen: 127.0.0.1:7402}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestALevelCannotBeSkipped(t *testing.T) {
	for name, tc := range map[string]struct {
		have []string
		want string // empty: accepted
	}{
		"L0 alone":                            {[]string{"L0"}, ""},
		"L0 and L1":                           {[]string{"L0", "L1"}, ""},
		"all four":                            {[]string{"L0", "L1", "L2", "L3"}, ""},
		"a variant is its own":                {[]string{"L0", "L1", "L2", "L3", "L3-reviewed"}, ""},
		"L2 without L1":                       {[]string{"L0", "L2"}, "no config for L1"},
		"L3 without L2":                       {[]string{"L0", "L1", "L3"}, "no config for L2"},
		"L0 and L3":                           {[]string{"L0", "L3"}, "no config for L1, L2"},
		"L3 alone":                            {[]string{"L3"}, "no config for L0, L1, L2"},
		"a variant needs the levels below it": {[]string{"L0", "L1", "L3-reviewed"}, "no config for L2"},
	} {
		dir := gatewayDir(t, tc.have...)
		err := CheckChain(dir)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want one mentioning %q", name, err, tc.want)
		}
	}
}

// Every entry point reads a config through LoadConfig, so refusing there
// refuses serve, score -c, bench and report at once.
func TestLoadingAConfigThatSitsAboveAMissingLevelIsRefusedWithTheFix(t *testing.T) {
	dir := gatewayDir(t, "L0", "L2")
	_, err := LoadConfig(filepath.Join(dir, "L2.yaml"))
	if err == nil {
		t.Fatal("L2 with no L1 loaded")
	}
	for _, want := range []string{"levels cannot be skipped", "L1.yaml", "lodog init", "has L0, L2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if _, err := LoadConfig(filepath.Join(dir, "L0.yaml")); err == nil {
		t.Error("L0 loaded from a directory that skips a level: the whole case is refused, not just the config above the gap")
	}
	if _, err := LoadConfig(filepath.Join(gatewayDir(t, "L0", "L1", "L2"), "L2.yaml")); err != nil {
		t.Errorf("a full chain was refused: %v", err)
	}
}

// A new case that skips a level turns this red.
func TestEveryCaseInTheRepositoryBuildsItsLevelsInOrder(t *testing.T) {
	cases, err := filepath.Glob("../../cases/*/gateway")
	if err != nil || len(cases) == 0 {
		t.Fatalf("no cases found (err %v): the scan would pass on nothing", err)
	}
	for _, dir := range cases {
		if err := CheckChain(dir); err != nil {
			t.Error(err)
		}
	}
}

// A case has one config per level: the L3 is the one with the policy lodog
// drafted, a reviewed or hand-written policy is named in its `policy:` instead
// of getting a second L3 (docs/levels.md).
func TestEveryCaseInTheRepositoryHasOneConfigPerLevel(t *testing.T) {
	dirs, err := filepath.Glob("../../cases/*/gateway")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no case gateway directories found: %v", err)
	}
	for _, dir := range dirs {
		chain, err := ReadChain(dir)
		if err != nil {
			t.Fatal(err)
		}
		for level, files := range chain {
			if len(files) > 1 {
				t.Errorf("%s has %d configs for %s (%s): keep one", dir, len(files), level, strings.Join(files, ", "))
			}
		}
	}
}
