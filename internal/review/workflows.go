package review

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/workflow"
)

// Keep is the answer that serves a task tool as drafted.
const Keep = "keep"

// WorkflowQuestions asks about the task tools of the Arazzo document at path
// that are still marked x-agent.needsReview, in one question.
func WorkflowQuestions(path string) ([]Question, error) {
	d, err := workflow.LoadFile(path)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, w := range d.Workflows {
		if w.Agent.NeedsReview {
			why := w.Summary
			if w.Agent.ReviewNote != "" {
				why = w.Agent.ReviewNote
			}
			items = append(items, Item{Key: w.WorkflowID, Why: why})
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	return []Question{{
		ID: "workflows-unsure", Source: SourceWorkflows, Proposed: Keep, Choices: []string{Keep, Drop},
		Ask:   fmt.Sprintf("The model was not sure about these %d task tools (what an input or fixed value means, or a write nothing takes back; each item says which). Should agents get every one as drafted?", len(items)),
		Why:   "A tool that fills in a wrong value does it on every call; a dropped tool is no longer served.",
		Items: items,
	}}, nil
}

// ApplyWorkflows writes the answered workflow questions back to the Arazzo
// document at path, keeping its comments: a kept tool loses needsReview, a
// dropped one is removed, along with the other tools' x-agent.next entries
// naming it. It refuses a tool that is no longer pending.
func ApplyWorkflows(path string, qs []Question) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own case files
	if err != nil {
		return fmt.Errorf("read workflows: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse workflows %s: %w", path, err)
	}
	seq := mappingValue(root(&doc), "workflows")
	if seq == nil {
		return fmt.Errorf("%s has no workflows", path)
	}
	verdicts := map[string]string{}
	for _, q := range qs {
		for _, it := range q.Items {
			verdicts[it.Key] = q.valueOf(it.Key)
		}
	}
	kept, err := reviewWorkflows(seq.Content, verdicts, path)
	if err != nil {
		return err
	}
	seq.Content = kept
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode workflows: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode workflows: %w", err)
	}
	if _, err := workflow.Parse(buf.Bytes()); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// reviewWorkflows is ws with the verdicts applied: a kept tool loses
// needsReview, a dropped one is left out and no other tool names it in
// x-agent.next.
func reviewWorkflows(ws []*yaml.Node, verdicts map[string]string, path string) ([]*yaml.Node, error) {
	pending := maps.Clone(verdicts)
	dropped := map[string]bool{}
	var kept []*yaml.Node
	for _, w := range ws {
		id := mappingValue(w, "workflowId")
		v, asked := "", false
		if id != nil {
			v, asked = pending[id.Value]
		}
		if !asked {
			kept = append(kept, w)
			continue
		}
		delete(pending, id.Value)
		agent := mappingValue(w, "x-agent")
		if flag := mappingValue(agent, "needsReview"); flag == nil || flag.Value != "true" {
			return nil, fmt.Errorf("%s is not pending review in %s; ask again", id.Value, path)
		}
		if v == Drop {
			dropped[id.Value] = true
			continue
		}
		removeKey(agent, "needsReview")
		removeKey(agent, "reviewNote")
		kept = append(kept, w)
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("%v have no workflow in %s; ask again", slices.Sorted(maps.Keys(pending)), path)
	}
	for _, w := range kept {
		if next := mappingValue(mappingValue(w, "x-agent"), "next"); next != nil {
			next.Content = slices.DeleteFunc(next.Content, func(n *yaml.Node) bool { return dropped[n.Value] })
		}
	}
	return kept, nil
}

func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}
