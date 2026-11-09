package score

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Ref names one task attempt.
type Ref struct {
	Task    string
	Attempt int
}

// Question is one judge question asked about one attempt's answer.
type Question struct {
	Ref
	Text string
}

// Evidence is everything besides call records that a score is computed from.
type Evidence struct {
	// Ran lists attempts the runner finished. Nil means there is no manifest
	// (a hand-run attempt): an attempt then counts as run when it left a call.
	Ran map[Ref]bool
	// Answers is the agent's text for an attempt, every turn joined.
	Answers map[Ref]string
	// StateChecks is whether the data was unchanged after an attempt.
	StateChecks map[Ref]bool
	// Verdicts are judge answers; a question with no verdict counts as no.
	Verdicts map[Question]bool
	// Unclear are questions the judge was asked but gave no verdict for; they
	// count as no and the report lists them as not scored.
	Unclear []Question
	// Held are the calls an attempt's gateway held for approval and the
	// operator decided (approved or denied); only an L3 gateway holds any.
	Held map[Ref][]Held
	// Exposure says whether the level the run measured offers the agent a
	// tool that reaches a call; nil when that is not known.
	Exposure func(method, path string) Exposure
	// GatewayConfig is the level's gateway config the runner recorded.
	GatewayConfig string
	// Versions are the pins the runner observed before the first attempt.
	Versions []ObservedVersion
	// Mode is the recording proxy's mode the runner recorded; empty is full.
	Mode string
	// AttemptsPerTask is how many attempts of each task the run planned: the
	// runner's record, else the highest attempt meta.yaml lists; 0 without
	// meta.yaml.
	AttemptsPerTask int
	// MinAttemptsPerTask is how many attempts every task ran before the
	// runner compared them; 0 when the run made every attempt it planned.
	MinAttemptsPerTask int
}

// Held is one call the gateway held for approval, and what became of it.
type Held struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
	Status string `yaml:"status"`
}

func (e Evidence) ran(r Ref, calls int) bool {
	if e.Ran == nil {
		return calls > 0
	}
	return e.Ran[r]
}

// File names inside a run directory.
const (
	MetaFile     = "meta.yaml"
	VerdictsFile = "verdicts.yaml"
)

// State check outcomes recorded in meta.yaml.
const (
	StatePass = "pass"
	StateFail = "fail"
	// StateNotRun is a state check a read-only run skipped: nothing it could
	// read would show what the attempt tried to write.
	StateNotRun = "not_run"
)

type metaFile struct {
	GatewayConfig      string            `yaml:"gateway_config"`
	Mode               string            `yaml:"mode"`
	Versions           []ObservedVersion `yaml:"versions"`
	AttemptsPerTask    int               `yaml:"attempts_per_task"`
	MinAttemptsPerTask int               `yaml:"min_attempts_per_task"`
	Attempts           []struct {
		Task       string `yaml:"task"`
		Attempt    int    `yaml:"attempt"`
		Status     string `yaml:"status"`
		Transcript string `yaml:"transcript"`
		StateCheck string `yaml:"state_check"`
		Approvals  []Held `yaml:"approvals"`
	} `yaml:"attempts"`
}

// LoadEvidence reads meta.yaml, the transcripts it lists and verdicts.yaml.
// Only attempts the runner finished count as run: one that failed on reset
// or on the gateway says nothing about the agent. Without meta.yaml it
// returns empty evidence with a nil Ran.
func LoadEvidence(runDir string) (Evidence, error) {
	ev := Evidence{Answers: map[Ref]string{}, StateChecks: map[Ref]bool{}}
	verdicts, err := LoadVerdicts(runDir)
	if err != nil {
		return Evidence{}, err
	}
	ev.Verdicts = verdicts
	b, err := os.ReadFile(filepath.Clean(filepath.Join(runDir, MetaFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return ev, nil
	}
	if err != nil {
		return Evidence{}, fmt.Errorf("read run meta: %w", err)
	}
	var meta metaFile
	if err := yaml.Unmarshal(b, &meta); err != nil {
		return Evidence{}, fmt.Errorf("parse run meta: %w", err)
	}
	ev.Ran = map[Ref]bool{}
	ev.Held = map[Ref][]Held{}
	ev.GatewayConfig = meta.GatewayConfig
	ev.Versions = meta.Versions
	ev.Mode = meta.Mode
	ev.AttemptsPerTask = meta.AttemptsPerTask
	ev.MinAttemptsPerTask = meta.MinAttemptsPerTask
	for _, a := range meta.Attempts {
		ref := Ref{a.Task, a.Attempt}
		if meta.AttemptsPerTask == 0 {
			ev.AttemptsPerTask = max(ev.AttemptsPerTask, a.Attempt)
		}
		if len(a.Approvals) > 0 {
			ev.Held[ref] = a.Approvals
		}
		if a.StateCheck != "" {
			ev.StateChecks[ref] = a.StateCheck == StatePass
		}
		if a.Status != "done" {
			continue
		}
		ev.Ran[ref] = true
		text, err := ReadAnswer(filepath.Join(runDir, a.Transcript))
		if err != nil {
			return Evidence{}, err
		}
		ev.Answers[ref] = text
	}
	return ev, nil
}

// ReadAnswer joins the result text of every turn in a stream-json transcript:
// an agent that answers, is asked to continue, and then only confirms has
// still given its answer.
func ReadAnswer(path string) (string, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read transcript: %w", err)
	}
	var parts []string
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		var e struct {
			Type   string `json:"type"`
			Result string `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &e) == nil && e.Type == "result" && e.Result != "" {
			parts = append(parts, e.Result)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read transcript %s: %w", path, err)
	}
	return strings.Join(parts, "\n\n"), nil
}

type verdictRow struct {
	Task     string `yaml:"task"`
	Attempt  int    `yaml:"attempt"`
	Question string `yaml:"question"`
	Yes      bool   `yaml:"yes"`
}

// LoadVerdicts reads runDir/verdicts.yaml; a missing file is no verdicts.
func LoadVerdicts(runDir string) (map[Question]bool, error) {
	out := map[Question]bool{}
	b, err := os.ReadFile(filepath.Clean(filepath.Join(runDir, VerdictsFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read verdicts: %w", err)
	}
	var rows []verdictRow
	if err := yaml.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("parse verdicts: %w", err)
	}
	for _, r := range rows {
		out[Question{Ref{r.Task, r.Attempt}, r.Question}] = r.Yes
	}
	return out, nil
}

// SaveVerdicts writes every verdict to runDir/verdicts.yaml, so re-scoring a
// run reuses them instead of asking the judge again.
func SaveVerdicts(runDir string, verdicts map[Question]bool) error {
	rows := make([]verdictRow, 0, len(verdicts))
	for q, yes := range verdicts {
		rows = append(rows, verdictRow{q.Task, q.Attempt, q.Text, yes})
	}
	sortRows(rows)
	b, err := yaml.Marshal(rows)
	if err != nil {
		return fmt.Errorf("encode verdicts: %w", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, VerdictsFile), b, 0o600); err != nil {
		return fmt.Errorf("write verdicts: %w", err)
	}
	return nil
}

func sortRows(rows []verdictRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		if a.Attempt != b.Attempt {
			return a.Attempt < b.Attempt
		}
		return a.Question < b.Question
	})
}

// Questions lists every judge question a task needs per attempt, including
// one per exclude of a list task.
func (t Task) Questions() []string {
	qs := append(append([]string{}, t.Expect.Judge...), t.Expect.Honesty...)
	if t.Type == TypeList {
		for _, s := range t.Expect.Exclude {
			qs = append(qs, leftOutQuestion(s))
		}
	}
	return qs
}
