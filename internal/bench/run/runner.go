// Package run drives one benchmark level end to end: for every task and
// attempt it starts the gateway labelled with the attempt, lets the agent
// work, plays the operator when the agent asks, checks the data the attempt
// must not have changed, and writes the transcript and
// runs/<run_id>/meta.yaml (docs/benchmark.md). Tasks that only read run side
// by side on data reset once; every other task runs alone, after a reset of
// its own before each attempt. A task stops early when its first attempts
// agree.
package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/iml885203/lodog/internal/approval"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
	"github.com/iml885203/lodog/internal/redact"
)

// Agent is the model under test.
type Agent interface {
	Turn(ctx context.Context, prompt, sessionID string) (Turn, error)
}

// QuestionJudge decides whether the agent is waiting on the operator.
type QuestionJudge interface {
	AsksOperator(ctx context.Context, text string) (bool, error)
}

// RunningGateway is one attempt's gateway.
type RunningGateway interface {
	Stop() error
	// DecidePending approves the held calls approve accepts and denies the
	// rest, as the simulated operator. Below L3 nothing is ever held.
	DecidePending(approve func(approval.Request) bool, by string) ([]approval.Request, error)
	// Pending lists the held calls nobody decided: the calls the gateway
	// stopped and the attempt ended without an approval for.
	Pending() []approval.Request
}

// simulatedOperator is who the runner records as deciding held calls.
const simulatedOperator = "user:simulated-operator"

// Plan is what one run executes.
type Plan struct {
	RunID    string
	Level    string
	RunDir   string
	Case     score.Case
	TaskIDs  []string
	Attempts int
	// MinAttempts is how many attempts every task makes before they are
	// compared: when the finished ones agree the task stops, otherwise it
	// runs on to Attempts. 0, or not below Attempts, makes every attempt.
	MinAttempts int
	// Parallel is how many shareable attempts run at once; below 2 they run
	// one at a time, still on data reset once.
	Parallel       int
	AttemptTimeout time.Duration
	Header         Meta
}

// Deps are the side effects a run needs; tests replace them. A slot is one
// of the Plan.Parallel places an attempt runs in: each slot has its own agent
// and gateway address, so an agent reaches only its own attempt's gateway.
type Deps struct {
	Agent        func(slot int) Agent
	Judge        QuestionJudge
	Reset        func(ctx context.Context) error
	StartGateway func(ctx context.Context, labels gateway.Labels, slot int) (RunningGateway, error)
	CheckState   func(ctx context.Context, sc score.StateCheck, attempt int) (bool, error)
	// Outcome measures a finished attempt, so a task can stop once its
	// attempts agree. Nil makes every attempt.
	Outcome func(ctx context.Context, task score.Task, am AttemptMeta) (score.Outcome, error)
	Now     func() time.Time
	Log     *slog.Logger
}

// ErrConsecutiveFailures stops a run whose attempts keep failing: the agent's
// API or the environment is down, and every later attempt would wait out its
// own failure and score nothing.
var ErrConsecutiveFailures = errors.New("too many attempts failed in a row")

// MaxConsecutiveFailures is how many attempts in a row may fail before the run
// stops.
const MaxConsecutiveFailures = 3

// fatal errors invalidate every later attempt too, so the run stops instead of
// recording a column of identical failures.
func fatal(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		errors.Is(err, ErrNotIsolated) ||
		errors.Is(err, ErrGatewayUnavailable) ||
		errors.Is(err, gateway.ErrLoginFailed)
}

// runAttempt runs attempt n of task in slot. reset is false for a shareable
// task, whose data was reset once before every shareable attempt.
func runAttempt(ctx context.Context, p Plan, d Deps, task score.Task, n, slot int, reset bool) (AttemptMeta, error) {
	am := AttemptMeta{Task: task.ID, Attempt: n, Status: StatusError}
	ctx, cancel := context.WithTimeout(ctx, p.AttemptTimeout)
	defer cancel()

	if reset {
		if err := d.Reset(ctx); err != nil {
			am.Error = err.Error()
			return am, fmt.Errorf("reset: %w", err)
		}
	}
	gw, err := d.StartGateway(ctx, gateway.Labels{Run: p.RunID, Task: task.ID, Attempt: strconv.Itoa(n)}, slot)
	if err != nil {
		am.Error = err.Error()
		return am, fmt.Errorf("start gateway: %w", err)
	}
	am.Transcript = filepath.Join(TranscriptDir, fmt.Sprintf("%s-%d.jsonl", task.ID, n))
	err = converse(ctx, p, d.Agent(slot), d.Judge, task, gw, &am)
	if err == nil && task.StateCheck != nil && record.Sends(p.Header.Mode) {
		err = checkState(ctx, d, *task.StateCheck, n, &am)
	}
	err = errors.Join(err, gw.Stop())
	if err != nil {
		am.Error = err.Error()
		return am, err
	}
	am.Status = StatusDone
	return am, nil
}

// converse runs the first turn, then answers each question with the rubric's
// fixed reply, at most MaxReplies times. In an approved_write task the
// operator first decides the calls the gateway is holding (see approve).
func converse(ctx context.Context, p Plan, agent Agent, judge QuestionJudge, task score.Task, gw RunningGateway, am *AttemptMeta) error {
	// The transcript is kept in the repository with the run, so what a tool
	// returned (a reset link, a token) is masked before it is written.
	transcript := filepath.Join(p.RunDir, am.Transcript)
	turn, err := agent.Turn(ctx, task.Prompt, "")
	if werr := appendFile(transcript, redact.JSONLines(turn.Raw)); werr != nil {
		return errors.Join(err, werr)
	}
	am.SessionID, am.Usage = turn.SessionID, turn.Usage
	if err != nil {
		return err
	}
	defer recordPending(gw, am)
	for am.Replies < p.Case.Rubric.MaxReplies {
		asks, err := judge.AsksOperator(ctx, turn.Result)
		am.Verdicts = append(am.Verdicts, verdict(asks, err))
		if err != nil || !asks {
			return err
		}
		if task.Type == score.TypeApprovedWrite {
			if err := approve(task, gw, am); err != nil {
				return err
			}
		}
		turn, err = agent.Turn(ctx, p.Case.Rubric.Reply, am.SessionID)
		am.Replies++
		am.Usage = am.Usage.Add(turn.Usage)
		if werr := appendFile(transcript, redact.JSONLines(turn.Raw)); werr != nil {
			return errors.Join(err, werr)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// recordPending notes the calls the gateway is still holding when the attempt
// ends, so the score can tell a call the gateway stopped from a tool that was
// never offered. Nothing is decided: the attempt is over.
func recordPending(gw RunningGateway, am *AttemptMeta) {
	for _, r := range gw.Pending() {
		am.Approvals = append(am.Approvals, ApprovalMeta{ID: r.ID, Method: r.Method, Path: r.Path, Status: string(r.Status)})
	}
}

// approve plays the operator on the gateway's held calls: a person approves
// what they asked for, not whatever the agent proposes. Only the task's
// must_stop_for_approval_before calls are approved; everything else held is
// denied. The gateway denies a never_auto_approve call whatever this says.
func approve(task score.Task, gw RunningGateway, am *AttemptMeta) error {
	decided, err := gw.DecidePending(func(r approval.Request) bool {
		for _, e := range task.Approvable {
			if e.Matches(r.Method, r.Path) {
				return true
			}
		}
		return false
	}, simulatedOperator)
	for _, r := range decided {
		am.Approvals = append(am.Approvals, ApprovalMeta{ID: r.ID, Method: r.Method, Path: r.Path, Status: string(r.Status)})
	}
	if err != nil {
		return fmt.Errorf("decide held calls: %w", err)
	}
	return nil
}

// checkState records whether the data the task must not change is intact.
// An unreadable check is recorded as an error, never as a pass.
func checkState(ctx context.Context, d Deps, sc score.StateCheck, n int, am *AttemptMeta) error {
	ok, err := d.CheckState(ctx, sc, n)
	if err != nil {
		return fmt.Errorf("state check: %w", err)
	}
	am.StateCheck = score.StateFail
	if ok {
		am.StateCheck = score.StatePass
	}
	return nil
}

func verdict(asks bool, err error) string {
	switch {
	case err != nil:
		return "unclear: " + err.Error()
	case asks:
		return "asks_operator"
	}
	return "done"
}

func appendFile(path string, b []byte) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	_, werr := f.Write(b)
	return errors.Join(werr, f.Close())
}

// TranscriptDir holds one stream-json file per task attempt.
const TranscriptDir = "transcripts"

// MetaFile is the run manifest the scorer reads to tell a skipped attempt from
// an attempt in which the agent made no calls.
const MetaFile = score.MetaFile

// Attempt outcomes recorded in meta.yaml.
const (
	StatusDone  = "done"
	StatusError = "error"
)

// Meta pins everything a score depends on and lists the attempts that ran.
type Meta struct {
	RunID string `yaml:"run_id"`
	Level string `yaml:"level"`
	// LodogCommit is the lodog build that made the run ("-dirty" when its
	// tree had uncommitted changes); empty when the binary has no version
	// control information.
	LodogCommit string `yaml:"lodog_commit,omitempty"`
	// Mode is the recording proxy's: full, read-only or replay. Empty in runs
	// made before modes existed, which were full.
	Mode               string                  `yaml:"mode,omitempty"`
	Case               string                  `yaml:"case"`
	GatewayConfig      string                  `yaml:"gateway_config"`
	RubricVersion      int                     `yaml:"rubric_version"`
	AgentModel         string                  `yaml:"agent_model"`
	QuestionJudgeModel string                  `yaml:"question_judge_model"`
	ClaudeVersion      string                  `yaml:"claude_version"`
	Versions           []score.ObservedVersion `yaml:"versions"`
	AttemptsPerTask    int                     `yaml:"attempts_per_task"`
	// MinAttemptsPerTask is how many attempts every task made before they
	// were compared; absent when every task made AttemptsPerTask.
	MinAttemptsPerTask int `yaml:"min_attempts_per_task,omitempty"`
	// Parallel is how many shareable attempts ran at once.
	Parallel   int           `yaml:"parallel,omitempty"`
	StartedAt  time.Time     `yaml:"started_at"`
	FinishedAt time.Time     `yaml:"finished_at,omitempty"`
	Attempts   []AttemptMeta `yaml:"attempts"`
}

// AttemptMeta is one task attempt.
type AttemptMeta struct {
	Task       string   `yaml:"task"`
	Attempt    int      `yaml:"attempt"`
	Status     string   `yaml:"status"`
	Error      string   `yaml:"error,omitempty"`
	Replies    int      `yaml:"replies"`
	Verdicts   []string `yaml:"verdicts,omitempty"`
	SessionID  string   `yaml:"session_id,omitempty"`
	StateCheck string   `yaml:"state_check,omitempty"`
	Usage      Usage    `yaml:"usage"`
	Transcript string   `yaml:"transcript,omitempty"`
	// Approvals are the held calls the simulated operator decided.
	Approvals []ApprovalMeta `yaml:"approvals,omitempty"`
	// Outcome is what the runner measured to decide whether the task needed
	// more attempts; the scorer measures again and does not read it.
	Outcome *score.Outcome `yaml:"outcome,omitempty"`
}

// ApprovalMeta is one decision the simulated operator made.
type ApprovalMeta struct {
	ID     string `yaml:"id"`
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
	Status string `yaml:"status"`
}

// WriteMeta replaces meta.yaml atomically.
func WriteMeta(dir string, m Meta) error {
	b, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode meta: %w", err)
	}
	tmp := filepath.Join(dir, MetaFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, MetaFile)); err != nil {
		return fmt.Errorf("replace meta: %w", err)
	}
	return nil
}
