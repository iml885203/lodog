package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/bench/proxy"
	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
)

// mixedPlan has two lookup tasks, which may share data, and two write tasks,
// which may not.
func mixedPlan(t *testing.T, attempts, parallel int) Plan {
	t.Helper()
	tasks := map[string]score.Task{
		"R1": {ID: "R1", Type: score.TypeLookup, Prompt: "R1"},
		"R2": {ID: "R2", Type: score.TypeList, Prompt: "R2"},
		"W1": {ID: "W1", Type: score.TypeGatedWrite, Prompt: "W1", StateCheck: &score.StateCheck{Call: "GET /x"}},
		"W2": {ID: "W2", Type: score.TypeDisguisedWrite, Prompt: "W2"},
	}
	c := score.Case{Tasks: tasks, Rubric: score.Rubric{RunsPerTask: attempts, MaxReplies: 1}}
	return Plan{
		RunID: "r1", RunDir: t.TempDir(), Case: c, TaskIDs: []string{"R1", "R2", "W1", "W2"},
		Attempts: attempts, Parallel: parallel, AttemptTimeout: time.Minute,
	}
}

// eventLog records what happened in what order, and how many turns ran at
// once. A turn holds until want turns run together or a short wait passes,
// so attempts that may overlap do; a prompt in alone never holds, since
// nothing may run beside it.
type eventLog struct {
	mu       sync.Mutex
	events   []string
	running  []string
	overlaps [][]string
	gateways map[int]gateway.Labels
	want     int
	alone    map[string]bool
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) turn(slot int, prompt string) error {
	l.mu.Lock()
	labels := l.gateways[slot]
	if labels.Task != prompt {
		l.mu.Unlock()
		return fmt.Errorf("slot %d: the agent for %s reached the gateway labelled %+v", slot, prompt, labels)
	}
	l.events = append(l.events, "turn "+prompt)
	l.running = append(l.running, prompt)
	l.overlaps = append(l.overlaps, slices.Clone(l.running))
	l.mu.Unlock()

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && !l.alone[prompt] {
		l.mu.Lock()
		n := len(l.running)
		l.mu.Unlock()
		if n >= l.want {
			break
		}
		time.Sleep(time.Millisecond)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.Index(l.running, prompt)
	l.running = slices.Delete(l.running, i, i+1)
	return nil
}

type slotAgent struct {
	slot int
	log  *eventLog
}

func (a slotAgent) Turn(_ context.Context, prompt, _ string) (Turn, error) {
	if err := a.log.turn(a.slot, prompt); err != nil {
		return Turn{}, err
	}
	return Turn{SessionID: prompt, Result: "done", Raw: []byte(prompt + "\n")}, nil
}

func loggedDeps(l *eventLog) Deps {
	return Deps{
		Agent: func(slot int) Agent { return slotAgent{slot: slot, log: l} },
		Judge: &scriptedJudge{},
		Reset: func(context.Context) error { l.add("reset"); return nil },
		StartGateway: func(_ context.Context, labels gateway.Labels, slot int) (RunningGateway, error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.gateways[slot] = labels
			return &fakeGateway{}, nil
		},
		CheckState: func(context.Context, score.StateCheck, int) (bool, error) { return true, nil },
		Now:        time.Now, Log: slog.New(slog.DiscardHandler),
	}
}

func TestReadOnlyTasksShareOneResetAndWriteTasksRunAloneAfterTheirOwn(t *testing.T) {
	p := mixedPlan(t, 2, 4)
	l := &eventLog{gateways: map[int]gateway.Labels{}, want: 4, alone: map[string]bool{"W1": true, "W2": true}}
	if err := Run(t.Context(), p, loggedDeps(l)); err != nil {
		t.Fatal(err)
	}
	if l.events[0] != "reset" {
		t.Fatalf("events = %q: the shared tasks start on reset data", l.events)
	}
	shared := l.events[1:5]
	for _, e := range shared {
		if e != "turn R1" && e != "turn R2" {
			t.Fatalf("events = %q: the four read attempts run first with no reset between them", l.events)
		}
	}
	want := []string{"reset", "turn W1", "reset", "turn W1", "reset", "turn W2", "reset", "turn W2"}
	if !slices.Equal(l.events[5:], want) {
		t.Fatalf("write attempts = %q, want %q", l.events[5:], want)
	}
	most := 0
	for _, running := range l.overlaps {
		most = max(most, len(running))
		if len(running) > 1 && (slices.Contains(running, "W1") || slices.Contains(running, "W2")) {
			t.Fatalf("a write task ran together with %q", running)
		}
	}
	if most != 4 {
		t.Fatalf("at most %d attempts ran at once, want the 4 read attempts together", most)
	}
	if m := readMeta(t, p.RunDir); len(m.Attempts) != 8 || m.Parallel != 4 {
		t.Fatalf("meta: %d attempts, parallel %d", len(m.Attempts), m.Parallel)
	}
}

func TestNoMoreAttemptsRunAtOnceThanTheSlots(t *testing.T) {
	p := mixedPlan(t, 3, 2)
	l := &eventLog{gateways: map[int]gateway.Labels{}, want: 2, alone: map[string]bool{"W1": true, "W2": true}}
	if err := Run(t.Context(), p, loggedDeps(l)); err != nil {
		t.Fatal(err)
	}
	for _, running := range l.overlaps {
		if len(running) > 2 {
			t.Fatalf("%q ran at once with 2 slots", running)
		}
	}
}

// outcomes scripts what each attempt measures as; an attempt with no script
// measures as correct and safe.
func outcomes(script map[string]score.Outcome, fail map[string]bool) func(context.Context, score.Task, AttemptMeta) (score.Outcome, error) {
	var mu sync.Mutex
	return func(_ context.Context, task score.Task, am AttemptMeta) (score.Outcome, error) {
		mu.Lock()
		defer mu.Unlock()
		key := fmt.Sprintf("%s-%d", task.ID, am.Attempt)
		if fail[key] {
			return score.Outcome{}, errors.New("judge unavailable")
		}
		if o, ok := script[key]; ok {
			return o, nil
		}
		return score.Outcome{Correctness: 1, Honest: true, Safe: true}, nil
	}
}

func attemptsByTask(m Meta) map[string]int {
	got := map[string]int{}
	for _, a := range m.Attempts {
		got[a.Task]++
	}
	return got
}

func TestATaskStopsWhenItsFirstAttemptsAgreeAndRunsToTheCapWhenTheyDoNot(t *testing.T) {
	p := mixedPlan(t, 5, 4)
	p.MinAttempts = 3
	wrong := score.Outcome{Correctness: 0, Honest: true, Safe: true}
	unsafe := score.Outcome{Correctness: 1, Honest: true, Safe: false}
	d := loggedDeps(&eventLog{gateways: map[int]gateway.Labels{}, want: 1})
	d.Outcome = outcomes(map[string]score.Outcome{
		"R2-2": wrong,
		"W1-1": unsafe, "W1-2": unsafe, "W1-3": unsafe,
		"W2-3": unsafe,
	}, nil)
	if err := Run(t.Context(), p, d); err != nil {
		t.Fatal(err)
	}
	m := readMeta(t, p.RunDir)
	want := map[string]int{"R1": 3, "R2": 5, "W1": 3, "W2": 5}
	if got := attemptsByTask(m); !maps.Equal(got, want) {
		t.Fatalf("attempts per task = %v, want %v", got, want)
	}
	if m.MinAttemptsPerTask != 3 {
		t.Fatalf("min_attempts_per_task = %d", m.MinAttemptsPerTask)
	}
	for _, a := range m.Attempts {
		if a.Outcome == nil {
			t.Fatalf("%s attempt %d has no outcome in meta.yaml", a.Task, a.Attempt)
		}
	}
}

func TestAnUnmeasuredOrFailedAttemptNeverStopsATaskEarly(t *testing.T) {
	p := mixedPlan(t, 4, 1)
	p.MinAttempts = 2
	p.TaskIDs = []string{"R1", "W1"}
	d := loggedDeps(&eventLog{gateways: map[int]gateway.Labels{}, want: 1})
	d.Outcome = outcomes(nil, map[string]bool{"R1-2": true})
	resets := 0
	d.Reset = func(context.Context) error {
		resets++
		if resets == 2 {
			return errors.New("environment tool down")
		}
		return nil
	}
	if err := Run(t.Context(), p, d); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"R1": 4, "W1": 4}
	if got := attemptsByTask(readMeta(t, p.RunDir)); !maps.Equal(got, want) {
		t.Fatalf("attempts per task = %v, want %v: R1's second attempt was not measured, W1's first failed on reset", got, want)
	}
}

func TestWithoutAMinimumEveryAttemptRuns(t *testing.T) {
	p := mixedPlan(t, 3, 4)
	d := loggedDeps(&eventLog{gateways: map[int]gateway.Labels{}, want: 1})
	d.Outcome = outcomes(nil, nil)
	if err := Run(t.Context(), p, d); err != nil {
		t.Fatal(err)
	}
	m := readMeta(t, p.RunDir)
	if got := attemptsByTask(m); !maps.Equal(got, map[string]int{"R1": 3, "R2": 3, "W1": 3, "W2": 3}) || m.MinAttemptsPerTask != 0 {
		t.Fatalf("attempts per task = %v, min %d", got, m.MinAttemptsPerTask)
	}
}

func TestAFatalErrorInAParallelAttemptStopsTheRun(t *testing.T) {
	p := mixedPlan(t, 3, 2)
	l := &eventLog{gateways: map[int]gateway.Labels{}, want: 1}
	d := loggedDeps(l)
	d.Agent = func(int) Agent { return &scriptedAgent{err: ErrNotIsolated} }
	err := Run(t.Context(), p, d)
	if !errors.Is(err, ErrNotIsolated) {
		t.Fatalf("err = %v, want ErrNotIsolated", err)
	}
	for _, e := range l.events[1:] {
		if e == "reset" {
			t.Fatalf("events = %q: no write task may start after a fatal error", l.events)
		}
	}
}

func TestEverySlotGetsItsOwnGatewayAddresses(t *testing.T) {
	cfg := &casecfg.Config{Listen: "127.0.0.1:7400", Approvals: casecfg.ApprovalsConfig{Listen: "127.0.0.1:7402"}}
	configs, err := slotConfigs(t.Context(), cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, c := range configs {
		for _, addr := range []string{c.Listen, c.Approvals.Listen} {
			if seen[addr] || !strings.HasPrefix(addr, "127.0.0.1:") {
				t.Fatalf("slot %d address %q is shared or on another host", i, addr)
			}
			seen[addr] = true
		}
	}
	if configs[0] != cfg || cfg.Listen != "127.0.0.1:7400" {
		t.Fatal("slot 0 serves at the config's own address, and the config is not changed")
	}
	if mcpConfigName(0) == mcpConfigName(1) {
		t.Fatal("slots share an MCP config")
	}
}

const itemsDoc = `{"openapi":"3.0.4","paths":{"/api/items":{"get":{"operationId":"listItems","parameters":[
	{"name":"q","in":"query","schema":{"type":"string"}}]}}}}`

// mcpAgent is a real MCP client: in each turn it calls the gateway of its
// slot once, with a marker the test traces from the transcript to the
// proxy's record of the call.
type mcpAgent struct {
	url string
}

// markerSeq makes every marker unique. A clock reading is not: two attempts
// of the same task can read the same nanosecond on a coarse clock, and then
// their markers collapse into one.
var markerSeq atomic.Int64

func (a mcpAgent) Turn(ctx context.Context, prompt, _ string) (Turn, error) {
	marker := fmt.Sprintf("%s-%d", prompt, markerSeq.Add(1))
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: a.url}, nil)
	if err != nil {
		return Turn{}, err
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 {
		return Turn{}, fmt.Errorf("tools: %v, %w", tools, err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tools.Tools[0].Name, Arguments: map[string]any{"q": marker}}); err != nil {
		return Turn{}, err
	}
	return Turn{SessionID: marker, Result: "done", Raw: []byte(marker + "\n")}, nil
}

// recordingProxy serves a legacy system that answers every call behind a
// real recording proxy writing under runs, and returns the proxy's URL.
func recordingProxy(t *testing.T, runs string) string {
	t.Helper()
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(legacy.Close)
	target, err := url.Parse(legacy.URL)
	if err != nil {
		t.Fatal(err)
	}
	writer := record.NewWriter(runs)
	t.Cleanup(func() { _ = writer.Close() })
	prx := httptest.NewServer(proxy.New(target, http.DefaultTransport, writer, slog.New(slog.DiscardHandler)))
	t.Cleanup(prx.Close)
	return prx.URL
}

// itemsGateway is an L0 gateway config over itemsDoc, sending to upstream.
func itemsGateway(t *testing.T, upstream string) *casecfg.Config {
	t.Helper()
	caseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseDir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "spec", "openapi.json"), []byte(itemsDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LODOG_TEST_KEY", "k")
	listen, err := freeAddr(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &casecfg.Config{
		Case: caseDir, Level: casecfg.L0, Listen: listen, Upstream: upstream,
		Auth:     casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "LODOG_TEST_KEY", Header: "X-Key"},
		Identity: casecfg.IdentityConfig{Actor: "agent:test", OnBehalfOf: "user:alice"},
	}
}

// markers maps the marker each attempt's agent wrote to its transcript to
// that attempt.
func markers(t *testing.T, runDir string) map[string]score.Ref {
	t.Helper()
	madeBy := map[string]score.Ref{}
	for _, a := range readMeta(t, runDir).Attempts {
		if a.Status != StatusDone {
			t.Fatalf("%s attempt %d: %s", a.Task, a.Attempt, a.Error)
		}
		b, err := os.ReadFile(filepath.Clean(filepath.Join(runDir, a.Transcript)))
		if err != nil {
			t.Fatal(err)
		}
		madeBy[strings.TrimSpace(string(b))] = score.Ref{Task: a.Task, Attempt: a.Attempt}
	}
	return madeBy
}

// The agents of parallel attempts reach real gateways through a real
// recording proxy, and every call the proxy records carries the task and
// attempt of the agent that made it.
func TestParallelAttemptsKeepTheirCallsApartInTheProxyRecords(t *testing.T) {
	runs := t.TempDir()
	configs, err := slotConfigs(t.Context(), itemsGateway(t, recordingProxy(t, runs)), 3)
	if err != nil {
		t.Fatal(err)
	}
	p := mixedPlan(t, 3, 3)
	p.RunDir = filepath.Join(runs, p.RunID)
	p.TaskIDs = []string{"R1", "R2"}
	log := slog.New(slog.DiscardHandler)
	d := loggedDeps(&eventLog{gateways: map[int]gateway.Labels{}})
	d.Agent = func(slot int) Agent { return mcpAgent{url: "http://" + configs[slot].Listen + gateway.MCPPath} }
	d.StartGateway = func(ctx context.Context, labels gateway.Labels, slot int) (RunningGateway, error) {
		return StartGateway(ctx, configs[slot], labels, "test", log)
	}
	if err := Run(t.Context(), p, d); err != nil {
		t.Fatal(err)
	}

	madeBy := markers(t, p.RunDir)
	calls, err := record.ReadFile(filepath.Join(p.RunDir, record.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 6 || len(madeBy) != 6 {
		t.Fatalf("%d calls recorded, %d attempts; want 6 of each", len(calls), len(madeBy))
	}
	for _, c := range calls {
		want, ok := madeBy[c.Query["q"][0]]
		if !ok || c.TaskID != want.Task || c.Attempt != want.Attempt {
			t.Errorf("call %s is filed under %s attempt %d, but %+v made it", c.Query["q"], c.TaskID, c.Attempt, want)
		}
	}
}

func TestThePlanCarriesTheAttemptsAndSlotsAndRefusesNegativeOnes(t *testing.T) {
	c, err := score.LoadCase("../../../cases/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{Level: casecfg.L0, Listen: "127.0.0.1:7400"}
	p, err := newPlan(Options{RunsDir: t.TempDir(), RunID: "r1", MinAttempts: 3, Parallel: 4}, c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Attempts != c.Rubric.RunsPerTask || p.MinAttempts != 3 || p.Parallel != 4 || p.Header.AttemptsPerTask != p.Attempts {
		t.Fatalf("plan: attempts %d, min %d, parallel %d", p.Attempts, p.MinAttempts, p.Parallel)
	}
	shared, alone := p.split()
	if len(shared) == 0 || len(alone) == 0 || p.slotsNeeded() != 4 {
		t.Fatalf("backoffice splits into %q and %q", shared, alone)
	}
	for _, o := range []Options{{MinAttempts: -1}, {Parallel: -1}, {Attempts: -2}} {
		o.RunsDir, o.RunID = t.TempDir(), "r1"
		if _, err := newPlan(o, c, cfg); !errors.Is(err, ErrPreflight) {
			t.Errorf("%+v: err = %v, want ErrPreflight", o, err)
		}
	}
}
