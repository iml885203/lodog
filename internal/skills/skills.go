// Package skills ships the agent skills that drive lodog for a person, so an
// agent gets them from the binary without cloning the repository.
package skills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed lodog-onboard/SKILL.md
var files embed.FS

// Names are the skills the binary carries.
func Names() []string {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Read is the SKILL.md of the named skill.
func Read(name string) ([]byte, error) {
	data, err := files.ReadFile(name + "/SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("no skill %q; lodog has %v", name, Names())
	}
	return data, nil
}

// Install writes the named skill to dir/<name>/SKILL.md, the layout Claude
// Code reads from .claude/skills. It refuses to replace a SKILL.md that
// differs from this binary's unless force is set, and returns the path.
func Install(dir, name string, force bool) (string, error) {
	data, err := Read(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name, "SKILL.md")
	existing, err := os.ReadFile(path) //nolint:gosec // G304: the operator chooses where skills go
	switch {
	case err == nil && string(existing) != string(data) && !force:
		return "", fmt.Errorf("%s exists and differs from this lodog's; pass --force to replace it", path)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// Dir is where a skill is installed: dir when given, ~/.claude/skills with
// global, else ./.claude/skills. Naming both is refused.
func Dir(dir string, global bool) (string, error) {
	switch {
	case dir != "" && global:
		return "", errors.New("--dir and --global name different places; pass one")
	case dir != "":
		return dir, nil
	case global:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		return filepath.Join(home, ".claude", "skills"), nil
	}
	return filepath.Join(".claude", "skills"), nil
}
