package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheRepositoryCopyOfEachSkillIsTheOneTheBinaryInstalls(t *testing.T) {
	for _, name := range Names() {
		want, err := Read(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join("..", "..", ".claude", "skills", name, "SKILL.md")) //nolint:gosec // G304: a fixed path in the repository
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf(".claude/skills/%s/SKILL.md differs from internal/skills/%s/SKILL.md; copy the edited one over the other", name, name)
		}
	}
}

func TestInstallWritesTheSkillAndKeepsAnEditedOne(t *testing.T) {
	dir := t.TempDir()
	path, err := Install(dir, "lodog-onboard", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: a path inside the test's own temp dir
	if err != nil || !strings.HasPrefix(string(data), "---\nname: lodog-onboard\n") {
		t.Fatalf("installed %s: %v\n%.80s", path, err, data)
	}
	if _, err := Install(dir, "lodog-onboard", false); err != nil {
		t.Fatalf("reinstalling the same skill: %v", err)
	}
	if err := os.WriteFile(path, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "lodog-onboard", false); err == nil {
		t.Fatal("replaced an edited skill without --force")
	}
	if _, err := Install(dir, "lodog-onboard", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "nope", false); err == nil {
		t.Fatal("installed a skill lodog does not have")
	}
}

func TestDirIsTheProjectUnlessAskedOtherwise(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		dir    string
		global bool
		want   string
	}{
		{"", false, filepath.Join(".claude", "skills")},
		{"/x", false, "/x"},
		{"", true, filepath.Join(home, ".claude", "skills")},
	}
	for _, c := range cases {
		got, err := Dir(c.dir, c.global)
		if err != nil || got != c.want {
			t.Errorf("Dir(%q, %v) = %q, %v; want %q", c.dir, c.global, got, err, c.want)
		}
	}
	if _, err := Dir("/x", true); err == nil {
		t.Error("--dir with --global was accepted")
	}
}

// The owner's complaint (#87) was not knowing what the agent was doing. Each
// phrase below is one of the five promises of lodog-onboard; the second list
// is the CLI surface the skill must know about.
func TestOnboardSkillKeepsItsProgressPromisesAndKnowsTheCurrentCLI(t *testing.T) {
	data, err := Read("lodog-onboard")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for what, phrase := range map[string]string{
		"roadmap before step 1":      "Roadmap, your first message",
		"step opening line format":   "【3/7】",
		"step closing line format":   "✓ 3/7 完成",
		"long step heartbeat":        "… 3/7 還在跑",
		"gate is marked apart":       "⏸ 現在等你決定",
		"closing summary":            "Closing summary",
		"estimate scales with size":  "per operation",
		"estimate is not a constant": "The estimate is not a constant",
		"flags are checked":          "never guess a name",
		"init --insecure":            "init <name> --swagger <document> [--upstream <base URL with base path>] [--insecure]",
		"enrich --source":            "lodog enrich --case <name> [--source <source dir>]",
		"login found in source":      "find the login in it",
		"serve --upstream":           "--upstream",
		"serve --on-behalf-of":       "--on-behalf-of",
		"no task bank":               "If they have no task bank",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("lodog-onboard lost %s: %q is gone", what, phrase)
		}
	}
	if n := strings.Count(text, "\n"); n > 200 {
		t.Errorf("lodog-onboard is %d lines; keep the skill short", n)
	}
}
