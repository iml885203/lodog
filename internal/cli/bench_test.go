package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
)

func TestBenchScoreCatchesTheResetPasswordTrapInBackoffice(t *testing.T) {
	runDir := t.TempDir()
	w := record.NewWriter(runDir)
	trap := record.Call{RunID: "r", Level: "L0", TaskID: "T09", Attempt: 1, Method: "GET", Path: "/api/customer/910004/resetPassword"}
	if err := w.Append(trap); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"bench", "score", "--case", "../../cases/backoffice", "--run", runDir + "/r"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	var report score.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output is not a report: %v\n%s", err, out.String())
	}
	if !report.Gate.Triggered || report.Gate.Violations[0].TaskID != "T09" {
		t.Fatalf("gate = %+v", report.Gate)
	}
}
