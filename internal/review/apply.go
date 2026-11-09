package review

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

// Ask builds the checklist for the drafts files names: the policy's
// questions first, then the overlay's, then the task tools'.
func Ask(files Files) (*Checklist, error) {
	if files == (Files{}) {
		return nil, errors.New("name at least one draft to review: a policy, an overlay or a workflows file")
	}
	c := &Checklist{Files: files}
	for _, src := range []struct {
		path string
		ask  func(string) ([]Question, error)
	}{
		{files.Policy, PolicyQuestions},
		{files.Overlay, OverlayQuestions},
		{files.Workflows, WorkflowQuestions},
	} {
		if src.path == "" {
			continue
		}
		qs, err := src.ask(src.path)
		if err != nil {
			return nil, err
		}
		c.Questions = append(c.Questions, qs...)
	}
	return c, nil
}

// Options say who answered and where the reviewed policy goes.
type Options struct {
	// By names the person who answered, for the review log and the policy.
	By  string
	Now time.Time
	// PolicyOut is where the reviewed policy is written; the policy the
	// questions were asked about when empty.
	PolicyOut string
}

// Entry is one answered question in the review log.
type Entry struct {
	At       time.Time         `yaml:"at"`
	By       string            `yaml:"by"`
	Question string            `yaml:"question"`
	Ask      string            `yaml:"ask"`
	File     string            `yaml:"file"`
	Wrote    string            `yaml:"wrote"`
	Proposed string            `yaml:"proposed"`
	Items    int               `yaml:"items"`
	Changed  map[string]string `yaml:"changed,omitempty"`
	Note     string            `yaml:"note,omitempty"`
}

// Apply writes every answered question of c back to the file it was asked
// about and returns what the review log records. Nothing is written when an
// answer is invalid.
func Apply(c *Checklist, opts Options) ([]Entry, error) {
	if opts.By == "" {
		return nil, errors.New("say who answered: the review log and the policy record it")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	bySource := map[string][]Question{}
	for _, q := range c.Answered() {
		bySource[q.Source] = append(bySource[q.Source], q)
	}
	if len(bySource) == 0 {
		return nil, errors.New("no question has an answer yet")
	}
	policyOut := opts.PolicyOut
	if policyOut == "" {
		policyOut = c.Files.Policy
	}
	wrote := map[string]string{SourcePolicy: policyOut, SourceOverlay: c.Files.Overlay, SourceWorkflows: c.Files.Workflows}
	read := map[string]string{SourcePolicy: c.Files.Policy, SourceOverlay: c.Files.Overlay, SourceWorkflows: c.Files.Workflows}
	for src, qs := range bySource {
		if read[src] == "" {
			return nil, fmt.Errorf("question %s is about the %s, but the checklist names no %s file", qs[0].ID, src, src)
		}
	}
	var entries []Entry
	for _, src := range []string{SourcePolicy, SourceOverlay, SourceWorkflows} {
		qs := bySource[src]
		if len(qs) == 0 {
			continue
		}
		if err := applySource(src, read[src], wrote[src], qs, opts); err != nil {
			return entries, err
		}
		for _, q := range qs {
			entries = append(entries, Entry{
				At: opts.Now.UTC(), By: opts.By, Question: q.ID, Ask: q.Ask, File: read[src], Wrote: wrote[src],
				Proposed: q.Proposed, Items: len(q.Items), Changed: q.Answer.Change, Note: q.Answer.Note,
			})
		}
	}
	return entries, nil
}

func applySource(src, in, out string, qs []Question, opts Options) error {
	switch src {
	case SourcePolicy:
		return ApplyPolicy(in, out, qs, opts.By, opts.Now.UTC().Format(time.DateOnly))
	case SourceOverlay:
		return ApplyOverlay(in, qs)
	case SourceWorkflows:
		return ApplyWorkflows(in, qs)
	}
	return fmt.Errorf("unknown source %q", src)
}

// AppendLog adds entries to the review log at path, creating it if needed.
func AppendLog(path string, entries []Entry) error {
	var log []Entry
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own review log
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &log); err != nil {
			return fmt.Errorf("parse review log %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read review log: %w", err)
	}
	out, err := yaml.Marshal(slices.Concat(log, entries))
	if err != nil {
		return fmt.Errorf("encode review log: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("write review log: %w", err)
	}
	return nil
}
