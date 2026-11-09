package score

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iml885203/lodog/internal/casecfg"
	"gopkg.in/yaml.v3"
)

// ErrIncompleteCase is returned when a case is missing a file or field the
// scorer needs; scoring with a guessed default would produce a wrong number.
var ErrIncompleteCase = errors.New("incomplete case")

// Task is one answers.yaml task.
type Task struct {
	ID         string
	Type       TaskType
	Prompt     string
	Calls      []string
	Expected   int
	MustNot    []Endpoint
	Expect     Expect
	StateCheck *StateCheck
	// Approvable are the calls the operator asked for and approves when the
	// gateway holds them (approved_write only); any other held call is denied.
	Approvable []Endpoint
}

// Expect is what the agent's final answer is checked against. Strings are
// matched case-insensitively, ignoring thousands separators.
type Expect struct {
	Include []string `yaml:"include"`
	Order   bool     `yaml:"order"`
	Exclude []string `yaml:"exclude"`
	Judge   []string `yaml:"judge"`
	Honesty []string `yaml:"honesty"`
}

// StateCheck is a read the runner makes after an attempt to confirm the
// agent did not change the data. Call may use <now> and <now-Nd> placeholders.
type StateCheck struct {
	Call    string   `yaml:"call"`
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Case is everything loaded from a case directory.
type Case struct {
	Dir    string
	Tasks  map[string]Task
	Ignore []Endpoint
	Reset  []string
	Spec   string
	Rubric Rubric
	// Versions are what the runner checks against the live system before it
	// starts; a run refuses to start when one differs.
	Versions []VersionPin
	// Writes decides which recorded calls are writes; nil judges by HTTP
	// method alone.
	Writes Writes
}

// Rubric holds the rubric.yaml values the scorer and the runner use.
type Rubric struct {
	Version            int
	RunsPerTask        int
	CorrectnessPoints  float64
	HonestyPoints      float64
	SafetyPoints       float64
	EfficiencyPoints   float64
	HalfCreditShare    float64
	GateCap            float64
	AgentModel         string
	Reply              string
	MaxReplies         int
	QuestionJudgeModel string
	JudgeModel         string
	JudgeEffort        string
	JudgeVotes         int
}

type caseFile struct {
	API struct {
		Auth struct {
			Login string `yaml:"login"`
		} `yaml:"auth"`
	} `yaml:"api"`
	Benchmark struct {
		Answers string `yaml:"answers"`
		Reset   string `yaml:"reset"`
	} `yaml:"benchmark"`
	Versions []versionEntry `yaml:"versions"`
}

type answersTask struct {
	Type              TaskType    `yaml:"type"`
	Prompt            string      `yaml:"prompt"`
	EndpointsExpected []string    `yaml:"endpoints_expected"`
	MustNot           []string    `yaml:"must_not_call_without_approval"`
	Approvable        []string    `yaml:"must_stop_for_approval_before"`
	Expect            Expect      `yaml:"expect"`
	StateCheck        *StateCheck `yaml:"state_check"`
}

type weight struct {
	Points float64 `yaml:"points"`
}

type rubricFile struct {
	Version     int `yaml:"version"`
	RunsPerTask int `yaml:"runs_per_task"`
	Agent       struct {
		Model string `yaml:"model"`
	} `yaml:"agent"`
	SimulatedUser struct {
		Reply              string `yaml:"reply"`
		MaxReplies         int    `yaml:"max_replies"`
		QuestionJudgeModel string `yaml:"question_judge_model"`
	} `yaml:"simulated_user"`
	Judge struct {
		Model  string `yaml:"model"`
		Effort string `yaml:"effort"`
		Votes  int    `yaml:"votes"`
	} `yaml:"judge"`
	Dynamic struct {
		Correctness struct {
			Points          float64 `yaml:"points"`
			HalfCreditShare float64 `yaml:"half_credit_share"`
		} `yaml:"correctness"`
		Honesty    weight `yaml:"honesty"`
		Safety     weight `yaml:"safety"`
		Efficiency weight `yaml:"efficiency"`
	} `yaml:"dynamic"`
	Gate struct {
		Cap float64 `yaml:"cap"`
	} `yaml:"gate"`
}

// HasBenchmark reports whether case.yaml names a task bank (benchmark.answers).
// A case made without `lodog init --with-bench` has none; LoadCase refuses it,
// and only callers that can do without a bench (the report page) ask first.
func HasBenchmark(dir string) (bool, error) {
	var cf caseFile
	if err := readYAML(filepath.Join(dir, "case.yaml"), &cf); err != nil {
		return false, err
	}
	return cf.Benchmark.Answers != "", nil
}

// LoadCase reads case.yaml, the answers it points to, and rubric.yaml.
// The case's login call is ignored when scoring: it is plumbing, not a choice
// the agent made.
func LoadCase(dir string) (Case, error) {
	var cf caseFile
	if err := readYAML(filepath.Join(dir, "case.yaml"), &cf); err != nil {
		return Case{}, err
	}
	if cf.Benchmark.Answers == "" {
		return Case{}, fmt.Errorf("%w: case.yaml has no benchmark.answers", ErrIncompleteCase)
	}
	var ignore []Endpoint
	if cf.API.Auth.Login != "" {
		login, err := ParseEndpoint(cf.API.Auth.Login)
		if err != nil {
			return Case{}, fmt.Errorf("case.yaml api.auth.login: %w", err)
		}
		ignore = append(ignore, login)
	}
	versions, err := parseVersions(cf.Versions)
	if err != nil {
		return Case{}, err
	}
	tasks, effects, err := loadAnswers(filepath.Join(dir, cf.Benchmark.Answers))
	if err != nil {
		return Case{}, err
	}
	rubric, err := loadRubric(filepath.Join(dir, "rubric.yaml"))
	if err != nil {
		return Case{}, err
	}
	writes, err := loadWrites(dir, effects)
	if err != nil {
		return Case{}, err
	}
	return Case{
		Writes: writes,
		Dir:    dir, Tasks: tasks, Ignore: ignore, Rubric: rubric,
		Reset:    strings.Fields(cf.Benchmark.Reset),
		Spec:     casecfg.OpenAPI(dir),
		Versions: versions,
	}, nil
}

type effectEntry struct {
	Call   string `yaml:"call"`
	Effect string `yaml:"effect"`
	Source string `yaml:"source"`
}

func parseEffects(raw []effectEntry) ([]Effect, error) {
	out := make([]Effect, 0, len(raw))
	for i, r := range raw {
		e, err := ParseEndpoint(r.Call)
		if err != nil {
			return nil, fmt.Errorf("effects[%d].call: %w", i, err)
		}
		if r.Effect != "read" && r.Effect != "write" {
			return nil, fmt.Errorf("%w: effects[%d] (%s) effect must be read or write, got %q", ErrIncompleteCase, i, r.Call, r.Effect)
		}
		if strings.TrimSpace(r.Source) == "" {
			return nil, fmt.Errorf("%w: effects[%d] (%s) needs a source: where in the system's code the effect was read", ErrIncompleteCase, i, r.Call)
		}
		out = append(out, Effect{Endpoint: e, Write: r.Effect == "write", Source: strings.TrimSpace(r.Source)})
	}
	return out, nil
}

func loadAnswers(path string) (map[string]Task, []Effect, error) {
	var af struct {
		Tasks   map[string]answersTask `yaml:"tasks"`
		Effects []effectEntry          `yaml:"effects"`
	}
	if err := readYAML(path, &af); err != nil {
		return nil, nil, err
	}
	if len(af.Tasks) == 0 {
		return nil, nil, fmt.Errorf("%w: %s has no tasks", ErrIncompleteCase, path)
	}
	effects, err := parseEffects(af.Effects)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	tasks := make(map[string]Task, len(af.Tasks))
	for id, t := range af.Tasks {
		task, err := newTask(id, t)
		if err != nil {
			return nil, nil, fmt.Errorf("%s task %s: %w", path, id, err)
		}
		tasks[id] = task
	}
	return tasks, effects, nil
}

func newTask(id string, t answersTask) (Task, error) {
	if _, ok := typeInfo(t.Type); !ok {
		return Task{}, fmt.Errorf("%w: unknown type %q", ErrIncompleteCase, t.Type)
	}
	if strings.TrimSpace(t.Prompt) == "" || len(t.EndpointsExpected) == 0 {
		return Task{}, fmt.Errorf("%w: needs prompt and endpoints_expected", ErrIncompleteCase)
	}
	mustNot, err := parseAll(t.MustNot)
	if err != nil {
		return Task{}, err
	}
	if t.StateCheck != nil {
		if _, err := ParseEndpoint(t.StateCheck.Call); err != nil {
			return Task{}, fmt.Errorf("state_check.call: %w", err)
		}
	}
	approvable, err := parseAll(t.Approvable)
	if err != nil {
		return Task{}, fmt.Errorf("must_stop_for_approval_before: %w", err)
	}
	if t.Type == TypeApprovedWrite && (len(approvable) == 0 || t.StateCheck == nil) {
		return Task{}, fmt.Errorf("%w: approved_write needs must_stop_for_approval_before and state_check", ErrIncompleteCase)
	}
	return Task{
		ID: id, Type: t.Type, Prompt: strings.TrimSpace(t.Prompt),
		Calls: t.EndpointsExpected, Expected: len(t.EndpointsExpected),
		MustNot: mustNot, Expect: t.Expect, StateCheck: t.StateCheck, Approvable: approvable,
	}, nil
}

func loadRubric(path string) (Rubric, error) {
	var rf rubricFile
	if err := readYAML(path, &rf); err != nil {
		return Rubric{}, err
	}
	d := rf.Dynamic
	r := Rubric{
		Version: rf.Version, RunsPerTask: rf.RunsPerTask,
		CorrectnessPoints: d.Correctness.Points, HalfCreditShare: d.Correctness.HalfCreditShare,
		HonestyPoints: d.Honesty.Points, SafetyPoints: d.Safety.Points, EfficiencyPoints: d.Efficiency.Points,
		GateCap:    rf.Gate.Cap,
		AgentModel: rf.Agent.Model,
		Reply:      rf.SimulatedUser.Reply, MaxReplies: rf.SimulatedUser.MaxReplies,
		QuestionJudgeModel: rf.SimulatedUser.QuestionJudgeModel,
		JudgeModel:         rf.Judge.Model, JudgeEffort: rf.Judge.Effort, JudgeVotes: rf.Judge.Votes,
	}
	if r.Version <= 0 || r.RunsPerTask <= 0 || r.GateCap <= 0 ||
		r.CorrectnessPoints <= 0 || r.HonestyPoints <= 0 || r.SafetyPoints <= 0 || r.EfficiencyPoints <= 0 {
		return Rubric{}, fmt.Errorf("%w: %s needs version, runs_per_task, gate.cap and points for every dynamic part", ErrIncompleteCase, path)
	}
	return r, nil
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
