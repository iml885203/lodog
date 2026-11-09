package draft

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/bench/run"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/llm"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// ScanFile proposes endpoints from the OpenAPI document at specPath, reading
// the system's own signal words from the case in caseDir when one is given.
func ScanFile(specPath, caseDir string, limit int) (ScanResult, error) {
	doc, err := spec.LoadFile(specPath)
	if err != nil {
		return ScanResult{}, err
	}
	sig, err := readiness.SignalsOf(caseDir)
	if err != nil {
		return ScanResult{}, err
	}
	return Scan(doc, limit, sig), nil
}

// CheckCase checks the task bank of the case in caseDir against its OpenAPI
// document. With a gateway config it also re-reads the bank against the live
// system through that config's upstream.
func CheckCase(ctx context.Context, caseDir, configPath string) ([]Finding, error) {
	c, err := score.LoadCase(caseDir)
	if err != nil {
		return nil, err
	}
	doc, err := spec.LoadFile(c.Spec)
	if err != nil {
		return nil, err
	}
	findings := Check(c, doc)
	if configPath == "" {
		return findings, nil
	}
	session, err := LiveSession(ctx, c, configPath)
	if err != nil {
		return nil, err
	}
	return append(findings, Live(ctx, session, c, time.Now())...), nil
}

// HasError reports whether any finding is an error rather than a warning.
func HasError(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// LiveSession resets the data and signs in through the gateway config's
// upstream, the way the runner does before an attempt.
func LiveSession(ctx context.Context, c score.Case, configPath string) (*run.Session, error) {
	cfg, err := casecfg.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	if len(c.Reset) > 0 {
		if _, err := llm.Command(ctx, "", os.Environ(), c.Reset[0], c.Reset[1:]...); err != nil {
			return nil, fmt.Errorf("reset: %w", err)
		}
	}
	return run.NewSession(ctx, cfg, os.Getenv, "draft-"+time.Now().Format("20060102-150405"))
}

// RecordCase resets the data of the case in caseDir, makes each task's
// expected GET calls through the gateway config's upstream and saves the
// responses under <case>/expected/raw/, printing each path to out. No tasks
// means every task of the bank.
func RecordCase(ctx context.Context, out io.Writer, caseDir, configPath string, tasks []string) error {
	c, err := score.LoadCase(caseDir)
	if err != nil {
		return err
	}
	session, err := LiveSession(ctx, c, configPath)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		for id := range c.Tasks {
			tasks = append(tasks, id)
		}
		sort.Strings(tasks)
	}
	return recordTasks(ctx, out, c, session, tasks)
}

func recordTasks(ctx context.Context, out io.Writer, c score.Case, r Reader, tasks []string) error {
	dir := filepath.Join(c.Dir, "expected", "raw")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	for _, id := range tasks {
		t, ok := c.Tasks[id]
		if !ok {
			return fmt.Errorf("%w: unknown task %s", score.ErrIncompleteCase, id)
		}
		recorded, err := Record(ctx, r, t, time.Now())
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(recorded, "", "  ")
		if err != nil {
			return fmt.Errorf("encode %s: %w", id, err)
		}
		path := filepath.Join(dir, id+".json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		_, _ = fmt.Fprintln(out, path)
	}
	return nil
}
