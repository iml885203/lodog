package record

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendedCallsReadBackPerRun(t *testing.T) {
	root := t.TempDir()
	w := NewWriter(root)
	approval := "apr_1"
	calls := []Call{
		{RunID: "r1", TaskID: "T02", Seq: 1, Method: "GET", Path: "/api/x", TS: time.Unix(0, 0).UTC()},
		{RunID: "r2", TaskID: "T07", Seq: 1, Method: "POST", Path: "/api/y", ApprovalID: &approval},
		{RunID: "r1", TaskID: "T02", Seq: 2, Method: "GET", Path: "/api/z"},
	}
	for _, c := range calls {
		if err := w.Append(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r1, err := ReadFile(filepath.Join(root, "r1", FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(r1) != 2 || r1[0].Path != "/api/x" || r1[1].Path != "/api/z" {
		t.Fatalf("run r1 = %+v", r1)
	}
	r2, err := ReadFile(filepath.Join(root, "r2", FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(r2) != 1 || r2[0].ApprovalID == nil || *r2[0].ApprovalID != "apr_1" {
		t.Fatalf("run r2 = %+v", r2)
	}
}

func TestARunIDCannotEscapeTheRunsDirectory(t *testing.T) {
	w := NewWriter(t.TempDir())
	defer func() { _ = w.Close() }()
	for _, id := range []string{"../evil", "a/b", "", ".hidden", "..", `a\b`} {
		if err := w.Append(Call{RunID: id}); !errors.Is(err, ErrInvalidRunID) {
			t.Errorf("run id %q: got %v, want ErrInvalidRunID", id, err)
		}
	}
}

func TestUnlabeledRunIsAccepted(t *testing.T) {
	w := NewWriter(t.TempDir())
	defer func() { _ = w.Close() }()
	if err := w.Append(Call{RunID: UnlabeledRun}); err != nil {
		t.Fatal(err)
	}
}

func TestStartedAndDoneLinesOfOneCallReadBackAsOneCall(t *testing.T) {
	root := t.TempDir()
	w := NewWriter(root)
	lines := []Call{
		{ID: "c1", Phase: PhaseStarted, RunID: "r1", Seq: 1, Path: "/a"},
		{ID: "c2", Phase: PhaseStarted, RunID: "r1", Seq: 2, Path: "/b"},
		{ID: "c1", Phase: PhaseDone, RunID: "r1", Seq: 1, Path: "/a", Status: 200, DurationMS: 7},
		{RunID: "r1", Seq: 3, Path: "/old", Status: 204},
	}
	for _, c := range lines {
		if err := w.Append(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	calls, err := ReadFile(filepath.Join(root, "r1", FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("got %d calls, want 3 (a call is counted once, and one with no done line is kept): %+v", len(calls), calls)
	}
	if calls[0].Status != 200 || calls[0].DurationMS != 7 {
		t.Errorf("c1 not completed by its done line: %+v", calls[0])
	}
	if calls[1].ID != "c2" || calls[1].Status != 0 {
		t.Errorf("c2 never finished and must stay with status 0: %+v", calls[1])
	}
	if calls[2].Path != "/old" {
		t.Errorf("a line without an id is a call on its own: %+v", calls[2])
	}
}

// The proxy can be killed mid-write; the calls before the torn line must
// survive, otherwise one dead process erases the whole run's record.
func TestAHalfWrittenLastLineIsSkippedButEarlierCallsSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := `{"id":"c1","phase":"started","run_id":"r1","path":"/a"}` + "\n" +
		`{"id":"c2","phase":"started","run_id":"r1","path":"/b"}` + "\n" +
		`{"id":"c3","phase":"star`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].ID != "c1" || calls[1].ID != "c2" {
		t.Fatalf("calls = %+v, want c1 and c2 kept", calls)
	}
}

func TestACorruptLineInTheMiddleOrAFinishedBadLineStillFails(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"middle":   `{"id":"c1"}` + "\n" + `not json` + "\n" + `{"id":"c2"}` + "\n",
		"complete": `{"id":"c1"}` + "\n" + `not json` + "\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFile(path); err == nil {
			t.Errorf("%s: want an error for a corrupt line", name)
		}
	}
}
