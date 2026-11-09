// Package review turns what lodog drafted and a person has not reviewed yet
// (policy rules, overlay risks, task tools) into a few questions a person can
// answer without reading YAML, and writes the answers back to the files they
// came from (docs/review.md).
//
// Items are grouped by what they would do if left as drafted, so one question
// covers every item of a group: the person confirms the group and names the
// exceptions. The checklist carries its items, so answers apply to exactly the
// items the person was shown; an item that is no longer pending is an error,
// never a silent skip.
package review

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// The sources a question comes from.
const (
	SourcePolicy    = "policy"
	SourceOverlay   = "overlay"
	SourceWorkflows = "workflows"
)

// Drop is the change that removes a task tool.
const Drop = "drop"

// Checklist is the machine-readable review: what an agent asks a person,
// question by question, and fills in with the answers.
type Checklist struct {
	Files     Files      `yaml:"files"`
	Questions []Question `yaml:"questions"`
}

// Files are the drafts the questions were asked about.
type Files struct {
	Policy    string `yaml:"policy,omitempty"`
	Overlay   string `yaml:"overlay,omitempty"`
	Workflows string `yaml:"workflows,omitempty"`
}

// Question is one yes-or-no question about a group of items.
type Question struct {
	ID     string `yaml:"id"`
	Source string `yaml:"source"`
	Ask    string `yaml:"ask"`
	Why    string `yaml:"why"`
	// Proposed is what every item gets on a yes: a risk, or "keep" for a
	// task tool.
	Proposed string `yaml:"proposed"`
	// Choices are the values a change may give an item.
	Choices []string `yaml:"choices"`
	Items   []Item   `yaml:"items"`
	Answer  *Answer  `yaml:"answer,omitempty"`
}

// Item is one operation or task tool, with why it was drafted this way.
type Item struct {
	Key string `yaml:"key"`
	Why string `yaml:"why,omitempty"`
}

// Answer is a person's answer. Confirmed means yes for every item Change does
// not name; a no must name every item's right value in Change.
type Answer struct {
	Confirmed bool              `yaml:"confirmed"`
	Change    map[string]string `yaml:"change,omitempty"`
	Note      string            `yaml:"note,omitempty"`
}

// Load reads a checklist.
func Load(path string) (*Checklist, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own checklist
	if err != nil {
		return nil, fmt.Errorf("read checklist: %w", err)
	}
	var c Checklist
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse checklist %s: %w", path, err)
	}
	return &c, nil
}

// Marshal writes the checklist as YAML.
func (c *Checklist) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode checklist: %w", err)
	}
	return data, nil
}

// Answered are the questions that carry an answer.
func (c *Checklist) Answered() []Question {
	var out []Question
	for _, q := range c.Questions {
		if q.Answer != nil {
			out = append(out, q)
		}
	}
	return out
}

// Validate reports every answer that cannot be applied as written.
func (c *Checklist) Validate() error {
	var errs []error
	seen := map[string]bool{}
	for _, q := range c.Questions {
		if seen[q.ID] {
			errs = append(errs, fmt.Errorf("question %s appears twice", q.ID))
		}
		seen[q.ID] = true
		if q.Answer != nil {
			errs = append(errs, q.validateAnswer())
		}
	}
	return errors.Join(errs...)
}

func (q Question) validateAnswer() error {
	var errs []error
	keys := map[string]bool{}
	for _, it := range q.Items {
		keys[it.Key] = true
	}
	for k, v := range q.Answer.Change {
		if !keys[k] {
			errs = append(errs, fmt.Errorf("question %s: change names %q, which is not one of its items", q.ID, k))
		}
		if !slices.Contains(q.Choices, v) {
			errs = append(errs, fmt.Errorf("question %s: %q cannot become %q; choose one of %v", q.ID, k, v, q.Choices))
		}
	}
	if !q.Answer.Confirmed || !slices.Contains(q.Choices, q.Proposed) {
		for _, it := range q.Items {
			if _, ok := q.Answer.Change[it.Key]; !ok {
				errs = append(errs, fmt.Errorf("question %s: %q has no value under change; a no, or a question with nothing proposed, names the right value of every item", q.ID, it.Key))
			}
		}
	}
	return errors.Join(errs...)
}

// valueOf is what an answered question gives item key.
func (q Question) valueOf(key string) string {
	if v, ok := q.Answer.Change[key]; ok {
		return v
	}
	return q.Proposed
}
