package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillInstallWritesTheOnboardingSkill(t *testing.T) {
	dir := t.TempDir()
	out, err := runLodog(t, "skill", "install", "--dir", dir)
	if err != nil || !strings.Contains(out, "installed") {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "lodog-onboard", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := runLodog(t, "skill", "install", "--dir", dir, "--global"); err == nil {
		t.Fatal("--dir with --global was accepted")
	}
	if out, err := runLodog(t, "skill", "show"); err != nil || !strings.Contains(out, "name: lodog-onboard") {
		t.Fatalf("show: %v\n%.200s", err, out)
	}
}
