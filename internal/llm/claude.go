// Package llm asks a model a question through headless Claude Code with every
// tool removed, or with read-only access to one directory when the caller
// gives it one. The benchmark's judges, lodog policy draft and lodog enrich
// share it, so there is one way lodog calls a model.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
)

// Exec runs a command and returns its stdout.
type Exec func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)

// Command is the real Exec.
func Command(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // binary and arguments come from lodog's own flags and the case files
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return out, fmt.Errorf("%s: command not found; is it installed and on PATH, or the path spelled right? (%w)", name, err)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Env is the environment for a Claude Code child process. It drops variables
// that would load the parent session's plugins or make Claude Code treat
// itself as nested inside another session.
func Env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDECODE=") || strings.HasPrefix(kv, "CLAUDE_CODE_PLUGIN") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// Claude sends one prompt to a model through headless Claude Code with no
// tools, no MCP servers and no saved session: the model sees the prompt and
// nothing else it could act on. With ReadDir set it may also read, and only
// read (Read, Grep, Glob), inside that directory.
type Claude struct {
	Bin     string
	Model   string
	Effort  string
	WorkDir string
	ReadDir string
	Exec    Exec
}

// Ask sends prompt and returns the model's text.
func (c Claude) Ask(ctx context.Context, prompt string) (string, error) {
	tools := []string{"--restricted", "--tools", ""}
	dir := c.WorkDir
	if c.ReadDir != "" {
		tools = []string{"--tools", "Read,Grep,Glob", "--allowedTools", "Read,Grep,Glob"}
		dir = c.ReadDir
	}
	args := append([]string{
		"-p", prompt,
		"--output-format", "json",
		"--model", c.Model,
	}, tools...)
	args = append(args, "--strict-mcp-config",
		"--permission-mode", "dontAsk", "--disable-slash-commands", "--no-session-persistence")
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	out, err := c.Exec(ctx, dir, Env(), c.Bin, args...)
	if err != nil {
		return "", err
	}
	var res struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("parse model output: %w", err)
	}
	return res.Result, nil
}

// Isolated is c working in a fresh empty directory, so the model reads no
// project files, such as a case's benchmark answers. The caller runs the
// returned cleanup when done. A c with no Exec uses Command.
func Isolated(c Claude, prefix string) (Claude, func(), error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return Claude{}, nil, fmt.Errorf("create model work dir: %w", err)
	}
	c.WorkDir = dir
	if c.Exec == nil {
		c.Exec = Command
	}
	return c, func() { _ = os.RemoveAll(dir) }, nil
}
