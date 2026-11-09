// Package record is the call-record format the recording proxy writes and the
// scorer reads (docs/contracts.md, section 1). One JSON object per line.
package record

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// FileName is the per-run file inside runs/<run_id>/.
const FileName = "calls.jsonl"

// UnlabeledRun collects calls that arrived without an X-Lodog-Run header, so
// traffic outside a benchmark run is still visible instead of silently lost.
const UnlabeledRun = "_unlabeled"

// Phases of one call's lines. The proxy writes PhaseStarted before it forwards
// and PhaseDone, under the same ID, once the legacy system answered or failed.
const (
	PhaseStarted = "started"
	PhaseDone    = "done"
)

// Call is one HTTP call that passed through the proxy. A call is one or two
// lines sharing an ID; ReadFile folds them back into one Call.
type Call struct {
	ID         string              `json:"id"`
	Phase      string              `json:"phase"`
	TS         time.Time           `json:"ts"`
	RunID      string              `json:"run_id"`
	Level      string              `json:"level"`
	TaskID     string              `json:"task_id"`
	Attempt    int                 `json:"attempt"`
	Seq        int                 `json:"seq"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      map[string][]string `json:"query"`
	Status     int                 `json:"status"`
	DurationMS int64               `json:"duration_ms"`
	Actor      string              `json:"actor"`
	OnBehalfOf string              `json:"on_behalf_of"`
	ApprovalID *string             `json:"approval_id"`
	// Error is why the legacy system gave no answer; empty when it answered.
	Error string `json:"error,omitempty"`
	// DryRun marks a call a read-only proxy recorded but did not send: the
	// agent got a made-up success and the legacy system saw nothing.
	DryRun bool `json:"dry_run,omitempty"`
	// Held is why a dry-run call was not sent.
	Held string `json:"held,omitempty"`
	// Body is the request body of a dry-run call to an operation in the
	// OpenAPI document, cut at MaxBodyBytes; no other call records a body.
	Body          string `json:"body,omitempty"`
	BodyTruncated bool   `json:"body_truncated,omitempty"`
	// Replayed marks a call answered from a recording instead of the system.
	Replayed bool `json:"replayed,omitempty"`
}

// MaxBodyBytes is how much of a dry-run call's body is recorded.
const MaxBodyBytes = 64 << 10

// Modes a recording proxy runs in. A run records the proxy's mode, and scores
// of different modes are never compared.
const (
	// ModeFull forwards every call; the system is a resettable env.
	ModeFull = "full"
	// ModeReadOnly forwards only calls the policy explicitly allows and
	// answers every other one with a made-up success.
	ModeReadOnly = "read-only"
	// ModeReplay answers allowed calls from a recording and never reaches a
	// system; everything else is handled as in ModeReadOnly.
	ModeReplay = "replay"
)

// ModeOf is the mode a run recorded; runs made before modes existed are full.
func ModeOf(mode string) string {
	if mode == "" {
		return ModeFull
	}
	return mode
}

// Sends reports whether a proxy in mode may change the system's data.
func Sends(mode string) bool {
	return ModeOf(mode) == ModeFull
}

// runIDPattern keeps a header-supplied run id from escaping the runs directory.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ErrInvalidRunID is returned for a run id that is not a safe directory name.
var ErrInvalidRunID = errors.New("invalid run id")

// ValidRunID reports whether id can be used as a directory name under the runs root.
func ValidRunID(id string) bool {
	return runIDPattern.MatchString(id)
}

// Writer appends calls to runs/<run_id>/calls.jsonl, one file per run.
// It is safe for concurrent use.
type Writer struct {
	root  string
	mu    sync.Mutex
	files map[string]*os.File
}

// NewWriter writes under root, creating run directories on first use.
func NewWriter(root string) *Writer {
	return &Writer{root: root, files: map[string]*os.File{}}
}

// Append writes c as one line to its run's file.
func (w *Writer) Append(c Call) error {
	if c.RunID != UnlabeledRun && !ValidRunID(c.RunID) {
		return fmt.Errorf("%w: %q", ErrInvalidRunID, c.RunID)
	}
	line, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode call: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := w.file(c.RunID)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write call for run %s: %w", c.RunID, err)
	}
	return nil
}

func (w *Writer) file(runID string) (*os.File, error) {
	if f, ok := w.files[runID]; ok {
		return f, nil
	}
	dir := filepath.Join(w.root, runID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create run dir %s: %w", dir, err)
	}
	path := filepath.Clean(filepath.Join(dir, FileName))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	w.files[runID] = f
	return f, nil
}

// Close closes every open run file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var errs []error
	for id, f := range w.files {
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close run %s: %w", id, err))
		}
	}
	w.files = map[string]*os.File{}
	return errors.Join(errs...)
}

// ReadFile reads every call from a calls.jsonl file, in the order they started.
// A call whose done line is missing (the legacy system never answered, or the
// proxy died) is kept with Status 0, never dropped. A line without an ID is a
// complete call on its own.
func ReadFile(path string) ([]Call, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	var calls []Call
	index := map[string]int{}
	for _, c := range lines {
		if c.ID == "" {
			calls = append(calls, c)
			continue
		}
		if i, ok := index[c.ID]; ok {
			if c.Phase == PhaseDone {
				calls[i] = c
			}
			continue
		}
		index[c.ID] = len(calls)
		calls = append(calls, c)
	}
	return calls, nil
}

// readLines decodes every line. A line a killed writer left unfinished is the
// last one and has no newline; it is skipped so the calls before it still
// read. Any other undecodable line is corruption and fails the read.
func readLines(path string) ([]Call, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var calls []Call
	for n := 1; len(data) > 0; n++ {
		line, rest, terminated := bytes.Cut(data, []byte{'\n'})
		data = rest
		if len(line) == 0 {
			continue
		}
		var c Call
		if err := json.Unmarshal(line, &c); err != nil {
			if !terminated {
				break
			}
			return nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		calls = append(calls, c)
	}
	return calls, nil
}
